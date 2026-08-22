package model

// This file is the single enumeration registry for the model vocabulary.
//
// Every closed set of values the model admits — boxed-expression kinds, hit
// policies, aggregations, orientations, function kinds, agent failure policies,
// conformance levels — is listed here exactly once, and every other surface
// that needs to enumerate them reads these functions rather than repeating the
// list. The published VDJ JSON Schema draws its enums from here, so adding a
// value to the engine changes the contract the schema advertises in the same
// commit; TestSchemaEnumsMatchTheRegistry in pkg/dmn/vdj fails otherwise.
//
// Where a list can be derived from the parser or the formatter rather than
// written out again, it is: AllHitPolicyShorthands composes Shorthand over the
// policy and aggregation lists, so a new COLLECT aggregation appears in the
// shorthand set without anyone remembering to add it.

// AllKinds returns every boxed-expression kind a VDJ or DMN document can carry,
// including KindUnknown, which stands in for content Verdict preserved but
// could not interpret.
func AllKinds() []Kind {
	return []Kind{
		KindLiteral,
		KindDecisionTable,
		KindInvocation,
		KindContext,
		KindList,
		KindRelation,
		KindFunction,
		KindAgent,
		KindUnknown,
	}
}

// AllHitPolicies returns every DMN hit policy in its long spelling.
func AllHitPolicies() []HitPolicy {
	return []HitPolicy{
		HitUnique,
		HitAny,
		HitPriority,
		HitFirst,
		HitCollect,
		HitRuleOrder,
		HitOutputOrder,
	}
}

// AllAggregations returns the COLLECT aggregators. AggNone is excluded: it is
// the absence of an aggregator, spelled as the empty string, not a member of
// the set.
func AllAggregations() []Aggregation {
	return []Aggregation{AggSum, AggMin, AggMax, AggCount}
}

// AllHitPolicyShorthands returns every single-cell hit-policy marker a modeller
// can write — U, A, P, F, C, C+, C<, C>, C#, R, O.
//
// The list is composed from AllHitPolicies and AllAggregations through
// Shorthand rather than written out, so it cannot disagree with the formatter
// it is supposed to describe.
func AllHitPolicyShorthands() []string {
	var out []string
	for _, h := range AllHitPolicies() {
		if h != HitCollect {
			out = append(out, h.Shorthand(AggNone))
			continue
		}
		for _, agg := range append([]Aggregation{AggNone}, AllAggregations()...) {
			out = append(out, h.Shorthand(agg))
		}
	}
	return out
}

// AllOrientations returns the decision-table presentation orientations.
func AllOrientations() []Orientation {
	return []Orientation{RulesAsRows, RulesAsColumns, CrossTable}
}

// AllFunctionKinds returns the three DMN function-definition bodies. Java and
// PMML are parsed and preserved but not executed.
func AllFunctionKinds() []FunctionKind {
	return []FunctionKind{FunctionFEEL, FunctionJava, FunctionPMML}
}

// AllFailurePolicies returns what an agentDecision can do when it fails to
// produce a conforming value within its policy budget.
func AllFailurePolicies() []FailurePolicy {
	return []FailurePolicy{FailError, FailNull, FailFallback}
}

// AllConformanceLevels returns the canonical spelling of every conformance
// level a document can declare. LevelUnspecified is excluded: a document that
// declares nothing omits the field rather than naming a level.
func AllConformanceLevels() []string {
	return []string{Level2.String(), Level3.String()}
}
