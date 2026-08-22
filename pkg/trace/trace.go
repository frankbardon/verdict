// Package trace holds Verdict's execution trace: the record of which decisions
// fired, what they were given, what they produced, and how they decided.
//
// The trace is a deliverable, not a debug artifact. It is what dashboards
// visualise, what auditors read, and what the Nexus event bus republishes for
// live observability, so its shape is a stable, documented JSON contract rather
// than an internal representation.
package trace

import (
	"encoding/json"
	"sync"
	"time"
)

// Mode selects how much of an evaluation is recorded.
type Mode string

const (
	// Off records nothing. Result.Trace is nil.
	Off Mode = "off"
	// Summary records the node graph, timings and outputs, but not the inputs
	// each node received nor per-node annotations beyond the essentials.
	Summary Mode = "summary"
	// Full records everything, including node inputs and hit-policy detail.
	Full Mode = "full"
)

// ParseMode maps a configuration string onto a Mode, defaulting to Full.
func ParseMode(s string) (Mode, bool) {
	switch Mode(s) {
	case "":
		return Full, true
	case Off, Summary, Full:
		return Mode(s), true
	default:
		return Full, false
	}
}

// Annotation keys used by the engine. They are exported because consumers key
// off them: a dashboard reads AnnHitPolicy to render a table's decision, and
// the Nexus bridge reads AnnSessionRef to link a trace node to a session.
const (
	AnnHitPolicy    = "hit_policy"
	AnnAggregation  = "aggregation"
	AnnMatchedRules = "matched_rules"
	AnnRuleCount    = "rule_count"
	AnnInputValues  = "input_values"
	AnnDefaulted    = "defaulted"
	AnnSessionRef   = "agent_session_ref"
	AnnPrompt       = "agent_prompt"
	AnnAttempts     = "agent_attempts"
	AnnTokens       = "agent_tokens"
	AnnFallbackUsed = "fallback_used"
	AnnFallbackFrom = "fallback_from"
	AnnCacheHit     = "cache_hit"
	AnnError        = "error"
	AnnRedacted     = "redacted"
	AnnBKM          = "invoked_bkm"
	AnnParameters   = "parameters"
)

// Node is one step of an evaluation. Nodes nest: a decision's node has children
// for the decisions it required and for any BKM it invoked.
type Node struct {
	DecisionID   string         `json:"decision_id"`
	DecisionName string         `json:"decision_name,omitempty"`
	NodeKind     string         `json:"node_kind"`
	Inputs       map[string]any `json:"inputs,omitempty"`
	Output       any            `json:"output,omitempty"`
	Children     []*Node        `json:"children,omitempty"`
	Annotations  map[string]any `json:"annotations,omitempty"`
	StartedAt    time.Time      `json:"started_at"`
	Duration     time.Duration  `json:"duration_ns"`

	// Error is set when the node failed. A failed node is still recorded: the
	// trace of a failed evaluation is the most valuable one there is.
	Error string `json:"error,omitempty"`
}

// Trace is the root of an evaluation record.
type Trace struct {
	ModelID   string        `json:"model_id"`
	ModelHash string        `json:"model_hash,omitempty"`
	Entry     string        `json:"entry,omitempty"`
	Root      *Node         `json:"root"`
	StartedAt time.Time     `json:"started_at"`
	Duration  time.Duration `json:"duration_ns"`
}

// Walk visits every node depth-first, parents before children.
func (t *Trace) Walk(fn func(*Node)) {
	if t == nil || t.Root == nil {
		return
	}
	walk(t.Root, fn)
}

func walk(n *Node, fn func(*Node)) {
	fn(n)
	for _, c := range n.Children {
		walk(c, fn)
	}
}

// Find returns the first node for a decision ID, or nil.
func (t *Trace) Find(decisionID string) *Node {
	var found *Node
	t.Walk(func(n *Node) {
		if found == nil && n.DecisionID == decisionID {
			found = n
		}
	})
	return found
}

// MarshalJSON renders the trace as the documented wire shape.
func (t *Trace) MarshalJSON() ([]byte, error) {
	type alias Trace
	return json.Marshal((*alias)(t))
}

// Writer is the interface a node evaluator uses to contribute to the trace. It
// is handed to agent bridges so an agent can record its own reasoning steps
// without knowing anything about Verdict's internals.
type Writer interface {
	// Annotate attaches a key to the node currently being recorded.
	Annotate(key string, value any)
	// Child opens a nested node and returns a writer for it. Call the returned
	// finish function when the child completes.
	Child(decisionID, nodeKind string) (Writer, func(output any, err error))
	// Enabled reports whether anything is being recorded, so an expensive
	// annotation can be skipped when tracing is off.
	Enabled() bool
}

// Recorder builds a Trace. It is safe for concurrent use so that independent
// decisions evaluated in parallel can record into the same trace.
type Recorder struct {
	mode Mode

	mu   sync.Mutex
	root *Node
	// redact holds the binding names whose values are replaced with a marker.
	redact map[string]bool
	clock  func() time.Time
}

// NewRecorder builds a recorder. redactPaths name bindings whose values must
// not appear in the trace; clock supplies timestamps so tests are deterministic.
func NewRecorder(mode Mode, redactPaths []string, clock func() time.Time) *Recorder {
	if clock == nil {
		clock = time.Now
	}
	r := &Recorder{mode: mode, clock: clock}
	if len(redactPaths) > 0 {
		r.redact = make(map[string]bool, len(redactPaths))
		for _, p := range redactPaths {
			r.redact[p] = true
		}
	}
	return r
}

// Mode reports the recorder's mode.
func (r *Recorder) Mode() Mode { return r.mode }

// Begin opens the root node and returns its writer plus a finish function.
func (r *Recorder) Begin(decisionID, decisionName, nodeKind string) (Writer, func(any, error)) {
	if r == nil || r.mode == Off {
		return nopWriter{}, func(any, error) {}
	}
	n := &Node{
		DecisionID:   decisionID,
		DecisionName: decisionName,
		NodeKind:     nodeKind,
		StartedAt:    r.clock(),
	}
	r.mu.Lock()
	r.root = n
	r.mu.Unlock()
	return &nodeWriter{rec: r, node: n}, r.finisher(n)
}

func (r *Recorder) finisher(n *Node) func(any, error) {
	return func(output any, err error) {
		r.mu.Lock()
		defer r.mu.Unlock()
		n.Duration = r.clock().Sub(n.StartedAt)
		n.Output = output
		if err != nil {
			n.Error = err.Error()
		}
	}
}

// Trace materialises the recorded trace.
func (r *Recorder) Trace(modelID, modelHash, entry string, startedAt time.Time) *Trace {
	if r == nil || r.mode == Off {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return &Trace{
		ModelID:   modelID,
		ModelHash: modelHash,
		Entry:     entry,
		Root:      r.root,
		StartedAt: startedAt,
		Duration:  r.clock().Sub(startedAt),
	}
}

// nodeWriter is the Writer bound to one node.
type nodeWriter struct {
	rec  *Recorder
	node *Node
}

func (w *nodeWriter) Enabled() bool { return w.rec.mode != Off }

func (w *nodeWriter) Annotate(key string, value any) {
	if w.rec.mode == Off {
		return
	}
	if w.rec.mode == Summary && !summaryAnnotation(key) {
		return
	}
	w.rec.mu.Lock()
	defer w.rec.mu.Unlock()
	if w.node.Annotations == nil {
		w.node.Annotations = map[string]any{}
	}
	w.node.Annotations[key] = value
}

// SetInputs records the values a node was given. Summary mode omits them
// entirely; full mode applies redaction.
func (w *nodeWriter) SetInputs(inputs map[string]any) {
	if w.rec.mode != Full || len(inputs) == 0 {
		return
	}
	w.rec.mu.Lock()
	defer w.rec.mu.Unlock()
	out := make(map[string]any, len(inputs))
	for k, v := range inputs {
		if w.rec.redact[k] {
			out[k] = redactedMarker
			continue
		}
		out[k] = v
	}
	w.node.Inputs = out
}

func (w *nodeWriter) Child(decisionID, nodeKind string) (Writer, func(any, error)) {
	if w.rec.mode == Off {
		return nopWriter{}, func(any, error) {}
	}
	child := &Node{DecisionID: decisionID, NodeKind: nodeKind, StartedAt: w.rec.clock()}
	w.rec.mu.Lock()
	w.node.Children = append(w.node.Children, child)
	w.rec.mu.Unlock()
	return &nodeWriter{rec: w.rec, node: child}, w.rec.finisher(child)
}

// SetName labels a node with its decision name.
func (w *nodeWriter) SetName(name string) {
	w.rec.mu.Lock()
	defer w.rec.mu.Unlock()
	w.node.DecisionName = name
}

// redactedMarker replaces a value the configuration says must not be traced.
const redactedMarker = "[redacted]"

// RedactedMarker is the value substituted for a redacted binding.
const RedactedMarker = redactedMarker

// nopWriter satisfies Writer when tracing is off.
type nopWriter struct{}

func (nopWriter) Annotate(string, any) {}
func (nopWriter) Enabled() bool        { return false }
func (nopWriter) Child(string, string) (Writer, func(any, error)) {
	return nopWriter{}, func(any, error) {}
}

// summaryAnnotation reports whether an annotation survives summary mode. The
// selection is what a dashboard needs to render the shape of a decision without
// exposing the data that flowed through it.
func summaryAnnotation(key string) bool {
	switch key {
	case AnnHitPolicy, AnnAggregation, AnnMatchedRules, AnnRuleCount,
		AnnSessionRef, AnnAttempts, AnnTokens, AnnFallbackUsed, AnnFallbackFrom,
		AnnCacheHit, AnnDefaulted, AnnError, AnnBKM:
		return true
	default:
		return false
	}
}

// InputSetter is implemented by writers that can record node inputs. The agent
// bridge interface hands out a plain Writer, so this is a narrowing assertion
// used inside the engine.
type InputSetter interface {
	SetInputs(map[string]any)
	SetName(string)
}
