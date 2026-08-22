package model

import "time"

// FailurePolicy names what the engine does when an agentDecision fails to
// produce a conforming value within its policy budget.
type FailurePolicy string

const (
	// FailError propagates the failure and aborts the evaluation. DMN's own
	// error semantics; the Verdict default.
	FailError FailurePolicy = "error"
	// FailNull binds null for the decision and continues.
	FailNull FailurePolicy = "null"
	// FailFallback evaluates FallbackDecision and uses its result.
	FailFallback FailurePolicy = "fallback"
)

// ParseFailurePolicy maps the XML attribute value onto a FailurePolicy.
func ParseFailurePolicy(s string) (FailurePolicy, bool) {
	switch FailurePolicy(s) {
	case "":
		return FailError, true
	case FailError, FailNull, FailFallback:
		return FailurePolicy(s), true
	default:
		return "", false
	}
}

// AgentPolicy bounds an agent invocation. Latency and retry are enforced by the
// engine, not by the bridge, so every AgentBridge gets identical guarantees.
type AgentPolicy struct {
	// MaxLatency bounds one invocation including its retries. Zero means the
	// engine default from configuration.
	MaxLatency time.Duration
	// MaxRetries is the number of *additional* attempts after the first.
	MaxRetries int
	// OnFailure selects the recovery behaviour.
	OnFailure FailurePolicy
	// FallbackDecision is the ID or name of a sibling decision evaluated when
	// OnFailure is FailFallback.
	FallbackDecision string
	// SessionHint is passed through to the bridge; a bridge that supports
	// session reuse may key on it.
	SessionHint string
}

// AgentInputBinding binds one prompt/input name to a FEEL expression evaluated
// against the decision's own context. The agent never sees the raw context —
// only the values these bindings produce.
type AgentInputBinding struct {
	Name string
	FEEL string
}

// AgentDecision delegates evaluation to an agent while keeping the call site
// typed and traced. It is Verdict's sole non-DMN-native boxed expression and is
// serialised under ExtensionNamespace.
type AgentDecision struct {
	base

	// PromptTemplate is a Go text/template rendered against the bound inputs.
	// Template errors are load-time diagnostics, not runtime surprises.
	PromptTemplate string

	Bindings []*AgentInputBinding

	// OutputType declares the shape the response must conform to.
	OutputType TypeSpec

	// Validator is an optional FEEL expression evaluated with the coerced
	// response bound to `value`. A non-true result is a failure.
	Validator string

	Policy AgentPolicy
}

func (*AgentDecision) Kind() Kind { return KindAgent }

// TypeSpec describes the expected return shape of an agent decision. It is a
// deliberately small subset of DMN's type system: enough to coerce and validate
// a model response without asking bridge authors to understand item definitions.
type TypeSpec struct {
	// TypeRef names a builtin FEEL type or an item definition. Empty means Any.
	TypeRef string
	// Enumeration, when non-empty, restricts the value to this set of strings.
	Enumeration []string
	// Collection marks the value as a list of TypeRef.
	Collection bool
	// Components type a structured response, keyed by field name.
	Components map[string]TypeSpec
}

// IsAny reports whether the spec imposes no constraint at all.
func (t TypeSpec) IsAny() bool {
	return t.TypeRef == "" && len(t.Enumeration) == 0 && !t.Collection && len(t.Components) == 0
}
