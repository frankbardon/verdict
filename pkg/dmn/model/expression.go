package model

// Kind names a boxed-expression type. Kind strings are stable: they appear in
// traces, in the node-evaluator registry, and in the VDJ projection.
type Kind string

const (
	KindLiteral       Kind = "literalExpression"
	KindDecisionTable Kind = "decisionTable"
	KindInvocation    Kind = "invocation"
	KindContext       Kind = "context"
	KindList          Kind = "list"
	KindRelation      Kind = "relation"
	KindFunction      Kind = "functionDefinition"
	// KindAgent is the Verdict extension: a decision delegated to an agent.
	KindAgent Kind = "agentDecision"
	// KindUnknown is the placeholder kind carried by an UnknownExpression: logic
	// Verdict preserved but could not interpret.
	KindUnknown Kind = "unknown"
)

// ExtensionNamespace is the XML namespace Verdict extension elements live
// under. A standard DMN tool that round-trips a model sees a typed extension
// element here and either preserves or warns about it.
const ExtensionNamespace = "https://github.com/frankbardon/verdict/schema/1.0"

// Expression is any boxed expression that can serve as decision logic.
type Expression interface {
	// Kind reports the boxed-expression type.
	Kind() Kind
	// ExprID is the DMN element ID, used to anchor diagnostics and trace nodes.
	ExprID() string
	// TypeRef is the declared result type, empty when the model omits it.
	ResultType() string
}

// base carries the fields every boxed expression shares.
type base struct {
	ID      string
	TypeRef string
}

func (b base) ExprID() string     { return b.ID }
func (b base) ResultType() string { return b.TypeRef }

// LiteralExpression evaluates a single FEEL expression.
type LiteralExpression struct {
	base
	Text string
	// ExpressionLanguage overrides the document default; a non-FEEL language is
	// reported as a diagnostic and evaluates to null.
	ExpressionLanguage string
}

func NewLiteral(id, typeRef, text string) *LiteralExpression {
	return &LiteralExpression{base: base{ID: id, TypeRef: typeRef}, Text: text}
}

func (*LiteralExpression) Kind() Kind { return KindLiteral }

// Invocation calls a BusinessKnowledgeModel with named parameters.
type Invocation struct {
	base
	// Called names the invoked BKM. DMN encodes this as a nested literal
	// expression whose text is the BKM's name.
	Called   string
	Bindings []*Binding
}

// Binding is one named parameter of an Invocation.
type Binding struct {
	Parameter *InformationItem
	Value     Expression
}

func (*Invocation) Kind() Kind { return KindInvocation }

// ContextExpression is a record of named entries. Per DMN, if the final entry
// has no variable name it is the result of the whole context; otherwise the
// result is a context value of all entries.
type ContextExpression struct {
	base
	Entries []*ContextEntry
}

// ContextEntry is one named entry of a ContextExpression.
type ContextEntry struct {
	Variable *InformationItem
	Value    Expression
}

func (*ContextExpression) Kind() Kind { return KindContext }

// ListExpression is an ordered list of boxed expressions.
type ListExpression struct {
	base
	Elements []Expression
}

func (*ListExpression) Kind() Kind { return KindList }

// Relation is DMN's row-oriented data literal: a table of contexts sharing one
// column vocabulary.
type Relation struct {
	base
	Columns []*InformationItem
	// Rows are parallel to Columns; a row with fewer cells than columns binds
	// the missing entries to null.
	Rows [][]Expression
}

func (*Relation) Kind() Kind { return KindRelation }

// FunctionKind distinguishes the three DMN function definition bodies.
type FunctionKind string

const (
	FunctionFEEL FunctionKind = "FEEL"
	FunctionJava FunctionKind = "Java"
	FunctionPMML FunctionKind = "PMML"
)

// FunctionDefinition is a callable body with formal parameters.
type FunctionDefinition struct {
	base
	Parameters []*InformationItem
	Body       Expression
	FnKind     FunctionKind
}

func (*FunctionDefinition) Kind() Kind { return KindFunction }

// UnknownExpression stands in for boxed-expression content Verdict could not
// interpret. It evaluates to null and carries a load-time diagnostic, so a
// partially understood model still loads and every other decision still runs.
type UnknownExpression struct {
	base
	Detail string
}

func (*UnknownExpression) Kind() Kind { return KindUnknown }

// SetExpressionBase writes the shared ID and result type of a boxed expression.
// The base struct is unexported so that only the DMN readers — which construct
// expressions from a wire format — can set it; evaluation treats expressions as
// immutable.
func SetExpressionBase(e Expression, id, typeRef string) {
	switch v := e.(type) {
	case *LiteralExpression:
		v.ID, v.TypeRef = id, typeRef
	case *DecisionTable:
		v.ID, v.TypeRef = id, typeRef
	case *Invocation:
		v.ID, v.TypeRef = id, typeRef
	case *ContextExpression:
		v.ID, v.TypeRef = id, typeRef
	case *ListExpression:
		v.ID, v.TypeRef = id, typeRef
	case *Relation:
		v.ID, v.TypeRef = id, typeRef
	case *FunctionDefinition:
		v.ID, v.TypeRef = id, typeRef
	case *AgentDecision:
		v.ID, v.TypeRef = id, typeRef
	case *UnknownExpression:
		v.ID, v.TypeRef = id, typeRef
	}
}
