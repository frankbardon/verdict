package model

import (
	"slices"
	"testing"
)

// TestAllKindsCoversEveryExpressionType holds the kind registry equal to the
// set of types that actually implement Expression. A kind missing here is a
// kind the published JSON Schema would reject documents for.
func TestAllKindsCoversEveryExpressionType(t *testing.T) {
	implementations := []Expression{
		&LiteralExpression{},
		&DecisionTable{},
		&Invocation{},
		&ContextExpression{},
		&ListExpression{},
		&Relation{},
		&FunctionDefinition{},
		&AgentDecision{},
		&UnknownExpression{},
	}
	all := AllKinds()
	if len(all) != len(implementations) {
		t.Errorf("AllKinds has %d entries but %d types implement Expression", len(all), len(implementations))
	}
	for _, e := range implementations {
		if !slices.Contains(all, e.Kind()) {
			t.Errorf("%T reports kind %q, which AllKinds does not list", e, e.Kind())
		}
	}
}

// TestEveryHitPolicyShorthandParses checks the shorthand set against the parser
// it describes: every marker a modeller may write in a table's corner must
// parse, and must format back to the same marker.
func TestEveryHitPolicyShorthandParses(t *testing.T) {
	for _, sh := range AllHitPolicyShorthands() {
		policy, agg, err := ParseHitPolicy(sh)
		if err != nil {
			t.Errorf("shorthand %q does not parse: %v", sh, err)
			continue
		}
		if back := policy.Shorthand(agg); back != sh {
			t.Errorf("shorthand %q round-tripped to %q", sh, back)
		}
	}
	// The set is composed from the two registries, so its size is a check on
	// both: one marker per non-COLLECT policy, plus COLLECT with and without
	// each aggregator.
	want := len(AllHitPolicies()) - 1 + 1 + len(AllAggregations())
	if got := len(AllHitPolicyShorthands()); got != want {
		t.Errorf("AllHitPolicyShorthands has %d markers, want %d", got, want)
	}
}

// TestEveryHitPolicyParsesFromItsLongName covers the other spelling: the long
// DMN names the VDJ writer emits.
func TestEveryHitPolicyParsesFromItsLongName(t *testing.T) {
	for _, h := range AllHitPolicies() {
		got, agg, err := ParseHitPolicy(string(h))
		if err != nil {
			t.Errorf("hit policy %q does not parse: %v", h, err)
			continue
		}
		if got != h || agg != AggNone {
			t.Errorf("hit policy %q parsed as (%q, %q)", h, got, agg)
		}
	}
}

// TestEveryFailurePolicyParses pins the agent failure modes to their parser.
func TestEveryFailurePolicyParses(t *testing.T) {
	for _, p := range AllFailurePolicies() {
		got, ok := ParseFailurePolicy(string(p))
		if !ok || got != p {
			t.Errorf("failure policy %q parsed as (%q, %v)", p, got, ok)
		}
	}
}

// TestEveryConformanceLevelRoundTrips pins the level names to their parser and
// formatter. The VDJ document carries the formatted name and the reader parses
// it back, so a mismatch loses the level on a round trip.
func TestEveryConformanceLevelRoundTrips(t *testing.T) {
	for _, name := range AllConformanceLevels() {
		lvl, err := ParseConformanceLevel(name)
		if err != nil {
			t.Errorf("conformance level %q does not parse: %v", name, err)
			continue
		}
		if lvl == LevelUnspecified {
			t.Errorf("conformance level %q parsed as unspecified", name)
			continue
		}
		if back := lvl.String(); back != name {
			t.Errorf("conformance level %q round-tripped to %q", name, back)
		}
	}
}
