// Package http provides a generic HTTP/JSON agent bridge, so an agentDecision
// can be answered by any service that speaks a small, documented envelope —
// an internal classifier, a model gateway, or a colleague's Python service.
package http

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/frankbardon/verdict/pkg/agent"
	"github.com/frankbardon/verdict/pkg/dmn/model"
)

// RequestBody is what the bridge POSTs.
type RequestBody struct {
	DecisionID   string         `json:"decision_id"`
	DecisionName string         `json:"decision_name,omitempty"`
	Prompt       string         `json:"prompt"`
	Inputs       map[string]any `json:"inputs,omitempty"`
	OutputType   TypeSpec       `json:"output_type"`
	SessionHint  string         `json:"session_hint,omitempty"`
	Attempt      int            `json:"attempt"`
}

// ResponseBody is what the bridge expects back. `value` is the only required
// field; everything else enriches the trace.
type ResponseBody struct {
	Value        any    `json:"value"`
	SessionRef   string `json:"session_ref,omitempty"`
	InputTokens  int    `json:"input_tokens,omitempty"`
	OutputTokens int    `json:"output_tokens,omitempty"`
	Model        string `json:"model,omitempty"`
	// Error lets a service report a semantic failure with a 200, which some
	// gateways prefer; a non-empty Error is treated exactly like a transport
	// failure and therefore participates in retry and the failure policy.
	Error string `json:"error,omitempty"`
}

// TypeSpec is the JSON projection of the declared output type, so the remote
// service can build a matching structured-output schema.
type TypeSpec struct {
	TypeRef     string              `json:"type_ref,omitempty"`
	Enumeration []string            `json:"enumeration,omitempty"`
	Collection  bool                `json:"collection,omitempty"`
	Components  map[string]TypeSpec `json:"components,omitempty"`
}

func typeSpec(t model.TypeSpec) TypeSpec {
	out := TypeSpec{TypeRef: t.TypeRef, Enumeration: t.Enumeration, Collection: t.Collection}
	if len(t.Components) > 0 {
		out.Components = make(map[string]TypeSpec, len(t.Components))
		for k, v := range t.Components {
			out.Components[k] = typeSpec(v)
		}
	}
	return out
}

// Bridge posts agent decisions to an HTTP endpoint.
type Bridge struct {
	endpoint string
	client   *http.Client
	headers  map[string]string
}

// Option configures a Bridge.
type Option func(*Bridge)

// WithClient supplies the HTTP client. The engine enforces the per-decision
// latency budget through context cancellation, so a client timeout here is a
// belt-and-braces upper bound rather than the primary control.
func WithClient(c *http.Client) Option {
	return func(b *Bridge) { b.client = c }
}

// WithHeader sets a header on every request — an API key, a tenant ID.
func WithHeader(k, v string) Option {
	return func(b *Bridge) {
		if b.headers == nil {
			b.headers = map[string]string{}
		}
		b.headers[k] = v
	}
}

// New builds an HTTP bridge pointed at endpoint.
func New(endpoint string, opts ...Option) *Bridge {
	b := &Bridge{
		endpoint: endpoint,
		client:   &http.Client{Timeout: 60 * time.Second},
	}
	for _, o := range opts {
		o(b)
	}
	return b
}

// Invoke implements agent.Bridge.
func (b *Bridge) Invoke(ctx context.Context, req agent.Request) (agent.Response, error) {
	body, err := json.Marshal(RequestBody{
		DecisionID:   req.DecisionID,
		DecisionName: req.DecisionName,
		Prompt:       req.Prompt,
		Inputs:       req.Inputs,
		OutputType:   typeSpec(req.OutputType),
		SessionHint:  req.SessionHint,
		Attempt:      req.Attempt,
	})
	if err != nil {
		return agent.Response{}, fmt.Errorf("agent/http: encoding request: %w", err)
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, b.endpoint, bytes.NewReader(body))
	if err != nil {
		return agent.Response{}, fmt.Errorf("agent/http: building request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "application/json")
	for k, v := range b.headers {
		httpReq.Header.Set(k, v)
	}

	resp, err := b.client.Do(httpReq)
	if err != nil {
		return agent.Response{}, fmt.Errorf("agent/http: calling %s: %w", b.endpoint, err)
	}
	defer resp.Body.Close()

	// Bound the response so a misbehaving endpoint cannot exhaust memory.
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return agent.Response{}, fmt.Errorf("agent/http: reading response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return agent.Response{}, fmt.Errorf("agent/http: %s returned %s: %s",
			b.endpoint, resp.Status, truncate(string(raw), 512))
	}
	var out ResponseBody
	if err := json.Unmarshal(raw, &out); err != nil {
		return agent.Response{}, fmt.Errorf("agent/http: decoding response: %w", err)
	}
	if out.Error != "" {
		return agent.Response{}, fmt.Errorf("agent/http: endpoint reported: %s", out.Error)
	}
	total := out.InputTokens + out.OutputTokens
	return agent.Response{
		Value:      out.Value,
		SessionRef: out.SessionRef,
		Tokens: agent.TokenAccounting{
			Input: out.InputTokens, Output: out.OutputTokens, Total: total, Model: out.Model,
		},
	}, nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
