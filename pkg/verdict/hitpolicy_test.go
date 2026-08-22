package verdict_test

import (
	"context"
	"reflect"
	"testing"

	"github.com/frankbardon/verdict/pkg/diag"
	"github.com/frankbardon/verdict/pkg/verdict"
)

func loadHitPolicies(t *testing.T) (*verdict.Engine, *verdict.Model) {
	t.Helper()
	e, err := verdict.NewEngine()
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}
	m, err := e.LoadModel(verdict.FromFile("testdata/hit_policies.vdj"))
	if err != nil {
		t.Fatalf("LoadModel: %v", err)
	}
	return e, m
}

// decide evaluates one decision at a Score and returns its single output value.
func decide(t *testing.T, e *verdict.Engine, m *verdict.Model, decision string, score float64) (any, error) {
	t.Helper()
	res, err := e.EvaluateDecision(context.Background(), m.ID, decision, verdict.Inputs{"Score": score})
	if err != nil {
		return nil, err
	}
	if len(res.Outputs) != 1 {
		t.Fatalf("%s produced %d outputs, want 1: %#v", decision, len(res.Outputs), res.Outputs)
	}
	for _, v := range res.Outputs {
		return v, nil
	}
	return nil, nil
}

func TestHitPolicies(t *testing.T) {
	e, m := loadHitPolicies(t)

	cases := []struct {
		decision string
		score    float64
		want     any
	}{
		// UNIQUE: exactly one rule may match.
		{"unique_table", 5, "low"},
		{"unique_table", 15, "mid"},
		{"unique_table", 25, "high"},

		// FIRST: rule order decides.
		{"first_table", 25, "high"},
		{"first_table", 15, "mid"},
		{"first_table", 1, "low"},

		// ANY: several rules may match provided they agree.
		{"any_table", 9, "pass"},

		// PRIORITY: outputValues order decides, not rule order.
		{"priority_table", 25, "high"},
		{"priority_table", 15, "mid"},
		{"priority_table", 5, "low"},

		// RULE ORDER: every match, in rule order.
		{"rule_order_table", 25, []any{"low", "mid", "high"}},
		{"rule_order_table", 15, []any{"low", "mid"}},

		// OUTPUT ORDER: every match, sorted by outputValues.
		{"output_order_table", 25, []any{"high", "mid", "low"}},
		{"output_order_table", 15, []any{"mid", "low"}},

		// COLLECT and its aggregators.
		{"collect_table", 25, []any{1.0, 2.0, 4.0}},
		{"collect_sum", 25, 7.0},
		{"collect_sum", 15, 3.0},
		{"collect_min", 25, 3.0},
		{"collect_max", 25, 9.0},
		{"collect_count", 25, 3.0},

		// An empty collect is the empty list, not null.
		{"collect_table", -1, []any{}},
		{"collect_count", -1, 0.0},

		// A defaulted table falls back rather than erroring.
		{"defaulted", 5, "low"},
		{"defaulted", 50, "unknown"},
	}

	for _, c := range cases {
		got, err := decide(t, e, m, c.decision, c.score)
		if err != nil {
			t.Errorf("%s at Score=%v: %v", c.decision, c.score, err)
			continue
		}
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s at Score=%v = %#v, want %#v", c.decision, c.score, got, c.want)
		}
	}
}

func TestMultiOutputTableProducesAContext(t *testing.T) {
	e, m := loadHitPolicies(t)
	res, err := e.EvaluateDecision(context.Background(), m.ID, "multi_output", verdict.Inputs{"Score": 42})
	if err != nil {
		t.Fatalf("EvaluateDecision: %v", err)
	}
	got, ok := res.Outputs["MultiOutput"].(map[string]any)
	if !ok {
		t.Fatalf("MultiOutput = %#v, want a context", res.Outputs["MultiOutput"])
	}
	if got["band"] != "high" || got["fee"] != 25.0 {
		t.Errorf("MultiOutput = %#v, want band=high fee=25", got)
	}
}

func TestUniqueOverlapIsARuntimeError(t *testing.T) {
	e, m := loadHitPolicies(t)
	// Score 20 matches both `< 30` and `> 10`.
	_, err := e.EvaluateDecision(context.Background(), m.ID, "overlap_unique", verdict.Inputs{"Score": 20})
	if err == nil {
		t.Fatal("expected a UNIQUE overlap to be a runtime error")
	}
	d, ok := err.(diag.Diagnostic)
	if !ok || d.Code != diag.CodeMultipleHits {
		t.Errorf("error = %v, want a VERDICT_EVAL_002 diagnostic", err)
	}
	// Outside the overlap the table still evaluates.
	if got, err := decide(t, e, m, "overlap_unique", 5); err != nil || got != "a" {
		t.Errorf("non-overlapping evaluation = %v (err %v), want \"a\"", got, err)
	}
}

func TestAnyDisagreementIsARuntimeError(t *testing.T) {
	e, m := loadHitPolicies(t)
	_, err := e.EvaluateDecision(context.Background(), m.ID, "any_conflict", verdict.Inputs{"Score": 9})
	if err == nil {
		t.Fatal("expected disagreeing ANY rules to be a runtime error")
	}
	if d, ok := err.(diag.Diagnostic); !ok || d.Code != diag.CodeInconsistentAny {
		t.Errorf("error = %v, want a VERDICT_EVAL_003 diagnostic", err)
	}
}

func TestStaticAnalysisFindsGapsAndOverlaps(t *testing.T) {
	_, m := loadHitPolicies(t)
	report := m.Analysis()

	gapped, ok := report.Table("gap_table")
	if !ok {
		t.Fatal("no analysis for gap_table")
	}
	if !gapped.Analysable {
		t.Fatalf("gap_table was not analysable: %s", gapped.Reason)
	}
	if len(gapped.Gaps) == 0 {
		t.Error("gap_table covers < 10 and > 20 but no gap was reported for the middle")
	}

	overlapping, ok := report.Table("overlap_unique")
	if !ok {
		t.Fatal("no analysis for overlap_unique")
	}
	if len(overlapping.Overlaps) == 0 {
		t.Error("overlap_unique has rules `< 30` and `> 10` but no overlap was reported")
	}

	// A total table must come back clean, or the analyser is crying wolf.
	unique, ok := report.Table("unique_table")
	if !ok {
		t.Fatal("no analysis for unique_table")
	}
	if len(unique.Gaps) != 0 || len(unique.Overlaps) != 0 {
		t.Errorf("unique_table is total but reported gaps=%v overlaps=%v", unique.Gaps, unique.Overlaps)
	}

	// The findings must reach the diagnostic surface at the right severity.
	var sawGap, sawOverlap, sawAnyConflict bool
	for _, d := range m.Diagnostics() {
		switch d.Code {
		case diag.CodeTableGap:
			if d.ElementID == "gap_table" && d.Severity == diag.SeverityWarning {
				sawGap = true
			}
		case diag.CodeTableOverlap:
			if d.ElementID == "overlap_unique" && d.Severity == diag.SeverityError {
				sawOverlap = true
			}
			if d.ElementID == "any_conflict" && d.Severity == diag.SeverityError {
				sawAnyConflict = true
			}
		}
	}
	if !sawGap {
		t.Error("no VERDICT_ANALYZE_001 warning for gap_table")
	}
	if !sawOverlap {
		t.Error("no VERDICT_ANALYZE_002 error for the UNIQUE overlap")
	}
	if !sawAnyConflict {
		t.Error("no VERDICT_ANALYZE_002 error for the disagreeing ANY table")
	}
}

func TestStrictModeRefusesAModelWithFindings(t *testing.T) {
	e, err := verdict.NewEngine(verdict.WithStrictMode(true))
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}
	if _, err := e.LoadModel(verdict.FromFile("testdata/hit_policies.vdj")); err == nil {
		t.Fatal("strict mode loaded a model with a UNIQUE overlap and an uncovered gap")
	}
}

func TestAgreeingOverlapUnderAnyIsNotAnError(t *testing.T) {
	_, m := loadHitPolicies(t)
	for _, d := range m.Diagnostics() {
		if d.Code == diag.CodeTableOverlap && d.ElementID == "any_table" && d.Severity == diag.SeverityError {
			t.Errorf("agreeing ANY rules were reported as an error: %s", d)
		}
	}
}
