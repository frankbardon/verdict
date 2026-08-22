package nexus

import (
	"time"

	"github.com/frankbardon/verdict/pkg/trace"
)

// Event types this module emits and listens for. They are dotted names in the
// Nexus convention, so a subscriber filters them exactly as it would any other
// event family.
const (
	// EventDecisionRequested asks the Verdict plugin to evaluate a model.
	EventDecisionRequested = "decision.requested"
	// EventDecisionCompleted carries an evaluation's outputs and trace.
	EventDecisionCompleted = "decision.completed"
	// EventDecisionFailed carries an evaluation that could not complete.
	EventDecisionFailed = "decision.failed"

	// EventAgentDecisionRequested is emitted by the bridge before it asks a
	// session to answer an agentDecision node.
	EventAgentDecisionRequested = "verdict.decision.requested"
	// EventAgentDecisionCompleted is emitted when the session has answered.
	EventAgentDecisionCompleted = "verdict.decision.completed"

	// EventTraceNode republishes one node of a Verdict trace as it completes,
	// so a dashboard or terminal UI can watch a decision graph evolve.
	EventTraceNode = "verdict.trace.node"
)

// DecisionRequest asks the plugin to evaluate a decision model.
type DecisionRequest struct {
	// RequestID correlates the request with its completion event. Generated
	// when empty.
	RequestID string `json:"request_id,omitempty"`

	// ModelID names a loaded model. Empty uses the plugin's default model,
	// which is the only loaded one when exactly one is configured.
	ModelID string `json:"model_id,omitempty"`
	// Version pins a model version. Empty means the most recently loaded.
	Version string `json:"version,omitempty"`

	// Decision evaluates a single decision by id or name.
	Decision string `json:"decision,omitempty"`
	// Service evaluates a decision service by id or name.
	Service string `json:"service,omitempty"`

	// Inputs are the model's input-data values.
	Inputs map[string]any `json:"inputs,omitempty"`
}

// DecisionCompleted carries the result of an evaluation.
type DecisionCompleted struct {
	RequestID string         `json:"request_id,omitempty"`
	ModelID   string         `json:"model_id"`
	Version   string         `json:"version,omitempty"`
	Outputs   map[string]any `json:"outputs"`
	// Trace is the execution record, omitted when the engine's tracing is off.
	Trace *trace.Trace `json:"trace,omitempty"`
	// Diagnostics are rendered as strings so a subscriber needs no Verdict
	// types to log or display them.
	Diagnostics []string      `json:"diagnostics,omitempty"`
	Duration    time.Duration `json:"duration_ns"`
}

// DecisionFailed carries an evaluation that could not complete. A failed
// evaluation still carries whatever trace was recorded before the failure,
// which is usually the fastest route to the cause.
type DecisionFailed struct {
	RequestID   string       `json:"request_id,omitempty"`
	ModelID     string       `json:"model_id,omitempty"`
	Error       string       `json:"error"`
	Trace       *trace.Trace `json:"trace,omitempty"`
	Diagnostics []string     `json:"diagnostics,omitempty"`
}

// AgentDecisionRequested announces that a decision graph is about to ask a
// session a question.
type AgentDecisionRequested struct {
	CallID       string         `json:"call_id"`
	DecisionID   string         `json:"decision_id"`
	DecisionName string         `json:"decision_name,omitempty"`
	Prompt       string         `json:"prompt"`
	Inputs       map[string]any `json:"inputs,omitempty"`
	OutputType   string         `json:"output_type,omitempty"`
	Attempt      int            `json:"attempt"`
	SessionRef   string         `json:"session_ref,omitempty"`
}

// AgentDecisionCompleted announces the session's answer.
type AgentDecisionCompleted struct {
	CallID       string `json:"call_id"`
	DecisionID   string `json:"decision_id"`
	DecisionName string `json:"decision_name,omitempty"`
	Value        any    `json:"value,omitempty"`
	SessionRef   string `json:"session_ref,omitempty"`
	InputTokens  int    `json:"input_tokens,omitempty"`
	OutputTokens int    `json:"output_tokens,omitempty"`
	Model        string `json:"model,omitempty"`
	// Error is set when the session could not answer. The engine's failure
	// policy decides what happens next; this event only reports.
	Error string `json:"error,omitempty"`
}

// TraceNode republishes a completed trace node.
type TraceNode struct {
	RequestID string      `json:"request_id,omitempty"`
	ModelID   string      `json:"model_id"`
	Node      *trace.Node `json:"node"`
	// Depth is the node's distance from the trace root, so a renderer can
	// indent without walking the tree itself.
	Depth int `json:"depth"`
}
