// Package mock provides a deterministic in-memory agent bridge for tests and
// for running agent-bearing models in CI without an LLM.
package mock

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/frankbardon/verdict/pkg/agent"
)

// Bridge answers agent decisions from a static routing table. It records every
// invocation so a test can assert on what the graph asked.
type Bridge struct {
	mu sync.Mutex

	// byDecision maps a decision ID or name to a canned answer.
	byDecision map[string]any
	// handlers are consulted when no canned answer matches.
	handlers []handler
	// fallback answers anything unmatched; when nil, an unmatched decision is
	// an error, which is what makes a test fail loudly rather than silently
	// taking a default path.
	fallback func(agent.Request) (agent.Response, error)

	latency time.Duration
	calls   []agent.Request
}

type handler struct {
	match func(agent.Request) bool
	fn    func(agent.Request) (agent.Response, error)
}

// Option configures a Bridge.
type Option func(*Bridge)

// WithAnswer routes a decision (by ID or name) to a fixed value.
func WithAnswer(decision string, value any) Option {
	return func(b *Bridge) { b.byDecision[decision] = value }
}

// WithHandler registers a predicate-driven handler, consulted in registration
// order after the fixed answers.
func WithHandler(match func(agent.Request) bool, fn func(agent.Request) (agent.Response, error)) Option {
	return func(b *Bridge) { b.handlers = append(b.handlers, handler{match: match, fn: fn}) }
}

// WithFallback answers any decision no other rule matched.
func WithFallback(fn func(agent.Request) (agent.Response, error)) Option {
	return func(b *Bridge) { b.fallback = fn }
}

// WithLatency makes every invocation take at least d, so failure-policy tests
// can drive the engine's latency budget without a real network.
func WithLatency(d time.Duration) Option {
	return func(b *Bridge) { b.latency = d }
}

// New builds a mock bridge.
func New(opts ...Option) *Bridge {
	b := &Bridge{byDecision: map[string]any{}}
	for _, o := range opts {
		o(b)
	}
	return b
}

// Invoke implements agent.Bridge.
func (b *Bridge) Invoke(ctx context.Context, req agent.Request) (agent.Response, error) {
	b.mu.Lock()
	b.calls = append(b.calls, req)
	latency := b.latency
	value, hasValue := b.byDecision[req.DecisionID]
	if !hasValue {
		value, hasValue = b.byDecision[req.DecisionName]
	}
	handlers := append([]handler(nil), b.handlers...)
	fallback := b.fallback
	b.mu.Unlock()

	if latency > 0 {
		select {
		case <-time.After(latency):
		case <-ctx.Done():
			return agent.Response{}, ctx.Err()
		}
	}
	if err := ctx.Err(); err != nil {
		return agent.Response{}, err
	}

	if hasValue {
		if fn, ok := value.(func(agent.Request) (agent.Response, error)); ok {
			return fn(req)
		}
		return agent.Response{Value: value, SessionRef: "mock:" + req.DecisionID}, nil
	}
	for _, h := range handlers {
		if h.match(req) {
			return h.fn(req)
		}
	}
	if fallback != nil {
		return fallback(req)
	}
	return agent.Response{}, fmt.Errorf("mock bridge: no answer configured for decision %q (%s); known: %s",
		req.DecisionName, req.DecisionID, strings.Join(b.known(), ", "))
}

func (b *Bridge) known() []string {
	out := make([]string, 0, len(b.byDecision))
	for k := range b.byDecision {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// Calls returns every request the bridge received, in order.
func (b *Bridge) Calls() []agent.Request {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]agent.Request(nil), b.calls...)
}

// Reset clears the recorded calls.
func (b *Bridge) Reset() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.calls = nil
}
