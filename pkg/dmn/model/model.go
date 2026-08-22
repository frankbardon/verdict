// Package model holds the parsed, in-memory representation of a DMN
// definitions document: the Decision Requirements Graph (DRG), its elements,
// and the boxed expressions that carry each decision's logic.
//
// The types here are deliberately free of any XML or JSON annotations. The
// dmn/xml and dmn/vdj packages own the wire formats and project onto these
// types, so a model loaded from DMN 1.5 XML and one loaded from Verdict
// Decision JSON are indistinguishable to the evaluator.
package model

import "fmt"

// ConformanceLevel names the DMN conformance level a model is evaluated at.
type ConformanceLevel int

const (
	// LevelUnspecified means the loader should fall back to the engine default.
	LevelUnspecified ConformanceLevel = iota
	// Level2 restricts expressions to S-FEEL: decision tables, simple unary
	// tests, arithmetic, comparisons and literals. FEEL-only constructs
	// (for/some/every/if, function definitions, filters) are rejected at parse.
	Level2
	// Level3 admits the full FEEL dialect and the whole boxed-expression family.
	Level3
)

func (c ConformanceLevel) String() string {
	switch c {
	case Level2:
		return "s-feel"
	case Level3:
		return "feel"
	default:
		return "unspecified"
	}
}

// ParseConformanceLevel maps a configuration string onto a ConformanceLevel.
func ParseConformanceLevel(s string) (ConformanceLevel, error) {
	switch s {
	case "s-feel", "S-FEEL", "level2", "2":
		return Level2, nil
	case "feel", "FEEL", "level3", "3":
		return Level3, nil
	case "":
		return LevelUnspecified, nil
	default:
		return LevelUnspecified, fmt.Errorf("unknown conformance level %q (want \"feel\" or \"s-feel\")", s)
	}
}

// Definitions is a whole DMN document: a namespace, a set of DRG elements and
// the item definitions that type them.
type Definitions struct {
	ID          string
	Name        string
	Namespace   string
	Description string

	// Version is free-form (semver recommended). Empty means unversioned; the
	// registry treats unversioned models as a single mutable slot.
	Version string

	// ExpressionLanguage is the document-level default expression language URI.
	ExpressionLanguage string

	// Level is the conformance level declared by the document, if any.
	Level ConformanceLevel

	Exporter        string
	ExporterVersion string

	ItemDefinitions  []*ItemDefinition
	Decisions        []*Decision
	InputData        []*InputData
	BKMs             []*BusinessKnowledgeModel
	KnowledgeSources []*KnowledgeSource
	DecisionServices []*DecisionService

	// Hash is the content address of the canonical source document. Set by the
	// loader; empty for models built programmatically.
	Hash string
}

// DRGElement is any node that can appear in the Decision Requirements Graph.
type DRGElement interface {
	ElementID() string
	ElementName() string
	// Requires lists the IDs of DRG elements this element depends on.
	Requires() []string
}

// Decision is a DRG node that produces a value by evaluating its decision logic.
type Decision struct {
	ID          string
	Name        string
	Description string

	// Question and AllowedAnswers are DMN's documentation fields; Verdict
	// surfaces them through `verdict explain`.
	Question       string
	AllowedAnswers string

	// Variable is the output variable this decision binds into the context.
	// Its Name defaults to the decision Name when the document omits it.
	Variable *InformationItem

	// Logic is the boxed expression evaluated to produce the decision's value.
	// Nil means the decision is a documentation-only stub; evaluating it yields
	// null plus a diagnostic.
	Logic Expression

	// RequiredDecisions, RequiredInputs and RequiredKnowledge are the three
	// DMN information/knowledge requirement kinds, stored as element IDs.
	RequiredDecisions []string
	RequiredInputs    []string
	RequiredKnowledge []string

	// AuthorityRequirements point at knowledge sources; non-executable.
	AuthorityRequirements []string
}

func (d *Decision) ElementID() string   { return d.ID }
func (d *Decision) ElementName() string { return d.Name }
func (d *Decision) Requires() []string {
	out := make([]string, 0, len(d.RequiredDecisions)+len(d.RequiredInputs)+len(d.RequiredKnowledge))
	out = append(out, d.RequiredDecisions...)
	out = append(out, d.RequiredInputs...)
	out = append(out, d.RequiredKnowledge...)
	return out
}

// OutputName is the name this decision binds into the evaluation context.
func (d *Decision) OutputName() string {
	if d.Variable != nil && d.Variable.Name != "" {
		return d.Variable.Name
	}
	return d.Name
}

// InputData is a named external input supplied by the caller.
type InputData struct {
	ID          string
	Name        string
	Description string
	Variable    *InformationItem
}

func (i *InputData) ElementID() string   { return i.ID }
func (i *InputData) ElementName() string { return i.Name }
func (i *InputData) Requires() []string  { return nil }

// InputName is the key the caller supplies this input under.
func (i *InputData) InputName() string {
	if i.Variable != nil && i.Variable.Name != "" {
		return i.Variable.Name
	}
	return i.Name
}

// BusinessKnowledgeModel is a reusable function in the DRG.
type BusinessKnowledgeModel struct {
	ID          string
	Name        string
	Description string
	Variable    *InformationItem

	// Encapsulated is the function definition that carries the BKM's body.
	Encapsulated *FunctionDefinition

	RequiredKnowledge []string
	RequiredInputs    []string
	RequiredDecisions []string
}

func (b *BusinessKnowledgeModel) ElementID() string   { return b.ID }
func (b *BusinessKnowledgeModel) ElementName() string { return b.Name }
func (b *BusinessKnowledgeModel) Requires() []string {
	out := make([]string, 0, len(b.RequiredKnowledge)+len(b.RequiredInputs)+len(b.RequiredDecisions))
	out = append(out, b.RequiredKnowledge...)
	out = append(out, b.RequiredInputs...)
	out = append(out, b.RequiredDecisions...)
	return out
}

// KnowledgeSource is a documentation pointer to an external authority. It is
// never executed; Verdict preserves it for round-tripping and `explain`.
type KnowledgeSource struct {
	ID          string
	Name        string
	Description string
	Type        string
	LocationURI string
	Owner       string
}

func (k *KnowledgeSource) ElementID() string   { return k.ID }
func (k *KnowledgeSource) ElementName() string { return k.Name }
func (k *KnowledgeSource) Requires() []string  { return nil }

// DecisionService is a named, callable subset of the DRG with declared inputs
// and outputs.
type DecisionService struct {
	ID          string
	Name        string
	Description string
	Variable    *InformationItem

	// OutputDecisions are the decisions whose values the service returns.
	OutputDecisions []string
	// EncapsulatedDecisions are evaluated as part of the service but not returned.
	EncapsulatedDecisions []string
	// InputDecisions are decisions the caller must supply values for rather than
	// the service evaluating them.
	InputDecisions []string
	// InputData are the input-data elements the service declares.
	InputData []string
}

func (s *DecisionService) ElementID() string   { return s.ID }
func (s *DecisionService) ElementName() string { return s.Name }
func (s *DecisionService) Requires() []string {
	out := make([]string, 0, len(s.OutputDecisions)+len(s.EncapsulatedDecisions))
	out = append(out, s.OutputDecisions...)
	out = append(out, s.EncapsulatedDecisions...)
	return out
}

// InformationItem is a typed name: a decision's output variable, a BKM
// parameter, or a context entry's binding.
type InformationItem struct {
	ID       string
	Name     string
	TypeRef  string
	Optional bool
}
