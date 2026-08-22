package model

import "fmt"

// HitPolicy is a DMN decision-table hit policy. The zero value is Unique,
// matching the DMN default for an omitted hitPolicy attribute.
type HitPolicy string

const (
	HitUnique      HitPolicy = "UNIQUE"
	HitAny         HitPolicy = "ANY"
	HitPriority    HitPolicy = "PRIORITY"
	HitFirst       HitPolicy = "FIRST"
	HitCollect     HitPolicy = "COLLECT"
	HitRuleOrder   HitPolicy = "RULE ORDER"
	HitOutputOrder HitPolicy = "OUTPUT ORDER"
)

// Aggregation is the COLLECT aggregator suffix (C+, C<, C>, C#). Empty means a
// plain COLLECT that returns the unaggregated list.
type Aggregation string

const (
	AggNone  Aggregation = ""
	AggSum   Aggregation = "SUM"
	AggMin   Aggregation = "MIN"
	AggMax   Aggregation = "MAX"
	AggCount Aggregation = "COUNT"
)

// SingleHit reports whether the policy yields a single value rather than a list.
func (h HitPolicy) SingleHit() bool {
	switch h {
	case HitUnique, HitAny, HitPriority, HitFirst, "":
		return true
	default:
		return false
	}
}

// ParseHitPolicy accepts both the DMN XML spelling ("RULE ORDER") and the
// shorthand notation used in table headers and documentation ("R", "C+").
func ParseHitPolicy(s string) (HitPolicy, Aggregation, error) {
	switch s {
	case "", "U", "UNIQUE":
		return HitUnique, AggNone, nil
	case "A", "ANY":
		return HitAny, AggNone, nil
	case "P", "PRIORITY":
		return HitPriority, AggNone, nil
	case "F", "FIRST":
		return HitFirst, AggNone, nil
	case "C", "COLLECT":
		return HitCollect, AggNone, nil
	case "C+", "COLLECT SUM", "SUM":
		return HitCollect, AggSum, nil
	case "C<", "COLLECT MIN", "MIN":
		return HitCollect, AggMin, nil
	case "C>", "COLLECT MAX", "MAX":
		return HitCollect, AggMax, nil
	case "C#", "COLLECT COUNT", "COUNT":
		return HitCollect, AggCount, nil
	case "R", "RULE ORDER":
		return HitRuleOrder, AggNone, nil
	case "O", "OUTPUT ORDER":
		return HitOutputOrder, AggNone, nil
	default:
		return "", "", fmt.Errorf("unknown hit policy %q", s)
	}
}

// Shorthand renders the policy in the compact notation DMN table headers use.
func (h HitPolicy) Shorthand(agg Aggregation) string {
	base := map[HitPolicy]string{
		HitUnique: "U", HitAny: "A", HitPriority: "P", HitFirst: "F",
		HitCollect: "C", HitRuleOrder: "R", HitOutputOrder: "O",
	}[h]
	if h != HitCollect {
		return base
	}
	switch agg {
	case AggSum:
		return "C+"
	case AggMin:
		return "C<"
	case AggMax:
		return "C>"
	case AggCount:
		return "C#"
	default:
		return "C"
	}
}

// Orientation is a presentation property preserved through round-trips. The
// engine evaluates both orientations identically.
type Orientation string

const (
	RulesAsRows    Orientation = "Rule-as-Row"
	RulesAsColumns Orientation = "Rule-as-Column"
	CrossTable     Orientation = "CrossTable"
)

// DecisionTable is DMN's central boxed expression.
type DecisionTable struct {
	base

	HitPolicy   HitPolicy
	Aggregation Aggregation
	Orientation Orientation

	Inputs  []*TableInput
	Outputs []*TableOutput
	Rules   []*TableRule

	// Annotations are the rule-annotation column labels, parallel to
	// TableRule.Annotations.
	Annotations []string
}

func (*DecisionTable) Kind() Kind { return KindDecisionTable }

// TableInput is one input clause: the expression whose value each rule's
// corresponding input entry is tested against.
type TableInput struct {
	ID    string
	Label string
	// Expression is the FEEL text evaluated once per table evaluation; its value
	// is bound to `?` while the rule's unary tests run.
	Expression string
	TypeRef    string
	// Values is the optional allowed-value list (`inputValues`), used by
	// completeness analysis to enumerate the input domain.
	Values string
}

// TableOutput is one output clause.
type TableOutput struct {
	ID      string
	Label   string
	Name    string
	TypeRef string
	// Values is the `outputValues` list. For PRIORITY and OUTPUT ORDER it is
	// required: it defines the precedence order of output values.
	Values string
	// DefaultValue is the FEEL expression used when no rule matches.
	DefaultValue string
}

// TableRule is one rule: a list of unary tests parallel to the table's inputs
// and a list of result expressions parallel to its outputs.
type TableRule struct {
	ID          string
	Description string
	// Index is the rule's zero-based position in rule order.
	Index int
	// InputEntries are FEEL unary tests; "-" (or empty) means "any".
	InputEntries []string
	// OutputEntries are FEEL expressions producing this rule's output values.
	OutputEntries []string
	Annotations   []string
}
