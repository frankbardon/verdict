// Package agent defines the boundary between Verdict's deterministic evaluator
// and whatever answers an agentDecision node.
//
// A bridge is deliberately thin. It receives a rendered prompt, the bound and
// FEEL-evaluated inputs, and the declared output type; it returns a value. It
// does not know it is inside a decision graph, does not see the model context,
// and does not enforce latency or retry — the engine owns all of that, so every
// bridge gets identical guarantees.
package agent

import (
	"context"

	"github.com/frankbardon/verdict/pkg/dmn/model"
	"github.com/frankbardon/verdict/pkg/trace"
)

// Bridge evaluates an agent decision.
type Bridge interface {
	// Invoke answers one agent decision. Implementations should respect
	// ctx cancellation; the engine cancels the context when the decision's
	// latency budget expires.
	Invoke(ctx context.Context, req Request) (Response, error)
}

// Request is one agent decision's call site, fully bound.
type Request struct {
	// DecisionID is the DRG element the request belongs to, for correlation.
	DecisionID string
	// DecisionName is the human-readable decision name.
	DecisionName string
	// Prompt is the rendered prompt template.
	Prompt string
	// Inputs are the bound, FEEL-evaluated inputs, converted to plain Go values.
	Inputs map[string]any
	// OutputType is the shape the response must conform to.
	OutputType model.TypeSpec
	// Trace lets the bridge record its own steps as children of the agent node.
	Trace trace.Writer
	// SessionHint is the model's optional request to reuse a session. A bridge
	// that has no session concept ignores it.
	SessionHint string
	// Attempt is the zero-based retry attempt. A bridge may use it to vary
	// temperature or to widen its instructions on a retry.
	Attempt int
}

// Response is a bridge's answer.
type Response struct {
	// Value must conform to Request.OutputType. Plain Go values are expected;
	// the engine coerces and validates before binding.
	Value any
	// SessionRef identifies the agent run in the bridge's own world — a Nexus
	// session ID, a provider request ID — and is recorded in the trace so the
	// run stays forensically available after the fact.
	SessionRef string
	// Tokens is optional cost telemetry.
	Tokens TokenAccounting
}

// TokenAccounting is optional per-invocation cost telemetry.
type TokenAccounting struct {
	Input  int `json:"input,omitempty"`
	Output int `json:"output,omitempty"`
	Total  int `json:"total,omitempty"`
	// Model names the model that answered, when the bridge knows it.
	Model string `json:"model,omitempty"`
}

// BridgeFunc adapts a function to the Bridge interface.
type BridgeFunc func(ctx context.Context, req Request) (Response, error)

// Invoke implements Bridge.
func (f BridgeFunc) Invoke(ctx context.Context, req Request) (Response, error) { return f(ctx, req) }
