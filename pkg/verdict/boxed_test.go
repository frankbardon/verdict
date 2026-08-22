package verdict_test

import (
	"context"
	"reflect"
	"testing"

	"github.com/frankbardon/verdict/pkg/diag"
	"github.com/frankbardon/verdict/pkg/verdict"
)

func loadBoxed(t *testing.T) (*verdict.Engine, *verdict.Model) {
	t.Helper()
	e, err := verdict.NewEngine()
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}
	m, err := e.LoadModel(verdict.FromFile("testdata/boxed.vdj"))
	if err != nil {
		t.Fatalf("LoadModel: %v", err)
	}
	return e, m
}

func employee() verdict.Inputs {
	return verdict.Inputs{
		"Employee": map[string]any{"name": "Ada", "dept": "eng", "salary": 72000},
		"Budget":   90000,
	}
}

// TestBoxedExpressions covers the whole DMN boxed-expression family, which is
// what Conformance Level 3 requires beyond decision tables.
func TestBoxedExpressions(t *testing.T) {
	e, m := loadBoxed(t)

	cases := []struct {
		decision string
		key      string
		want     any
	}{
		{"literal_decision", "Doubled Salary", 144000.0},

		// A context whose last entry is named produces the whole record.
		{"context_decision", "Compensation", map[string]any{
			"base": 72000.0, "bonus": 7200.0, "total": 79200.0,
		}},

		// A context whose last entry is unnamed produces that entry's value.
		{"context_result_decision", "Total Compensation", 79200.0},

		{"list_decision", "Thresholds", []any{1000.0, 5000.0, 10000.0}},

		{"relation_decision", "Rate Card", []any{
			map[string]any{"dept": "eng", "rate": 120.0},
			map[string]any{"dept": "sales", "rate": 95.0},
			map[string]any{"dept": "ops", "rate": 80.0},
		}},

		// An invocation and a direct FEEL call of the same BKM must agree: DMN
		// makes a business knowledge model an ordinary function.
		{"invocation_decision", "Band", "senior"},
		{"bkm_from_feel", "Band Via FEEL", "senior"},

		// A relation is a list of contexts, so ordinary FEEL iterates it.
		{"rate_lookup", "Department Rate", 120.0},

		{"affordable", "Affordable", true},

		// A decision service is callable from an expression (DMN 1.5 §7.4).
		{"service_caller", "Service Caller", "senior"},
	}

	for _, c := range cases {
		t.Run(c.decision, func(t *testing.T) {
			res, err := e.EvaluateDecision(context.Background(), m.ID, c.decision, employee())
			if err != nil {
				t.Fatalf("EvaluateDecision: %v", err)
			}
			got := res.Outputs[c.key]
			if !reflect.DeepEqual(got, c.want) {
				t.Errorf("%s = %#v, want %#v", c.key, got, c.want)
			}
		})
	}
}

// TestJavaBoundFunctionIsAStub is the documented non-goal: a Java-bound BKM is
// parsed and preserved so the model round-trips, but calling it returns null
// with a diagnostic rather than failing the evaluation or pretending to work.
func TestJavaBoundFunctionIsAStub(t *testing.T) {
	e, m := loadBoxed(t)

	sawLoadDiagnostic := false
	for _, d := range m.Diagnostics() {
		if d.Code == diag.CodeJavaBinding {
			sawLoadDiagnostic = true
		}
		if d.Severity == diag.SeverityError {
			t.Errorf("unexpected load error: %s", d)
		}
	}
	if !sawLoadDiagnostic {
		t.Error("no VERDICT_LOAD_007 diagnostic for the Java-bound BKM")
	}

	res, err := e.EvaluateDecision(context.Background(), m.ID, "java_call", employee())
	if err != nil {
		t.Fatalf("calling a Java-bound BKM should not fail the evaluation: %v", err)
	}
	if got := res.Outputs["Legacy Score"]; got != nil {
		t.Errorf("Legacy Score = %#v, want null", got)
	}
	sawEvalDiagnostic := false
	for _, d := range res.Diagnostics {
		if d.Code == diag.CodeStubEvaluated {
			sawEvalDiagnostic = true
		}
	}
	if !sawEvalDiagnostic {
		t.Error("no VERDICT_EVAL_011 diagnostic when the stub was called")
	}
}

// TestDecisionServiceReturnsOnlyItsOutputs pins the service contract.
func TestDecisionServiceReturnsOnlyItsOutputs(t *testing.T) {
	e, m := loadBoxed(t)
	res, err := e.EvaluateService(context.Background(), m.ID, "BandService", employee())
	if err != nil {
		t.Fatalf("EvaluateService: %v", err)
	}
	if len(res.Outputs) != 1 || res.Outputs["Band"] != "senior" {
		t.Errorf("service outputs = %#v, want exactly {Band: senior}", res.Outputs)
	}
}

// TestMemoizationDoesNotChangeResults checks the cache is transparent: turning
// it on must change performance and nothing else.
func TestMemoizationDoesNotChangeResults(t *testing.T) {
	plain, m1 := loadBoxed(t)
	cachedEngine, err := verdict.NewEngine(verdict.WithMemoization(true))
	if err != nil {
		t.Fatal(err)
	}
	m2, err := cachedEngine.LoadModel(verdict.FromFile("testdata/boxed.vdj"))
	if err != nil {
		t.Fatal(err)
	}

	a, err := plain.Evaluate(context.Background(), m1.ID, employee())
	if err != nil {
		t.Fatal(err)
	}
	b, err := cachedEngine.Evaluate(context.Background(), m2.ID, employee())
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(a.Outputs, b.Outputs) {
		t.Errorf("memoisation changed the answer:\n plain: %#v\ncached: %#v", a.Outputs, b.Outputs)
	}
}

// TestSFEELModelRejectsLevel3Constructs checks the conformance gate fires at
// load time, which is the point: a Level 2 model that uses a Level 3 construct
// must fail where the modeller can see it, not in production.
func TestSFEELModelRejectsLevel3Constructs(t *testing.T) {
	doc := `{"vdj":"1.0","id":"level2","conformance_level":"s-feel",
	  "decisions":[{"id":"d","name":"D",
	    "logic":{"kind":"literalExpression","text":"for x in [1,2] return x"}}]}`

	e, err := verdict.NewEngine()
	if err != nil {
		t.Fatal(err)
	}
	m, err := e.LoadModel(verdict.FromBytes([]byte(doc)))
	if err != nil {
		// Strict mode is off, so the model loads with an error-severity
		// diagnostic rather than being rejected outright.
		t.Fatalf("LoadModel: %v", err)
	}
	found := false
	for _, d := range m.Diagnostics() {
		if d.Code == diag.CodeDialectViolation && d.Severity == diag.SeverityError {
			found = true
		}
	}
	if !found {
		t.Errorf("no VERDICT_LOAD_010 dialect violation was reported: %v", m.Diagnostics())
	}

	// The same document at Level 3 is fine.
	level3 := `{"vdj":"1.0","id":"level3","conformance_level":"feel",
	  "decisions":[{"id":"d","name":"D",
	    "logic":{"kind":"literalExpression","text":"for x in [1,2] return x"}}]}`
	m3, err := e.LoadModel(verdict.FromBytes([]byte(level3)))
	if err != nil {
		t.Fatalf("LoadModel at level 3: %v", err)
	}
	for _, d := range m3.Diagnostics() {
		if d.Severity == diag.SeverityError {
			t.Errorf("level 3 model reported an error: %s", d)
		}
	}
}

// TestRecursiveServiceTerminates checks the one bound the DRG's acyclicity does
// not supply. Requirement edges cannot loop, but a literal expression invoking
// a decision service can — and does, in this model. Without a depth cap the
// engine would recurse until the stack gave out.
func TestRecursiveServiceTerminates(t *testing.T) {
	e, err := verdict.NewEngine(verdict.WithMaxDepth(8))
	if err != nil {
		t.Fatal(err)
	}
	m, err := e.LoadModel(verdict.FromFile("testdata/recursive.vdj"))
	if err != nil {
		t.Fatalf("LoadModel: %v", err)
	}

	// Within the budget, the recursion is ordinary and correct.
	res, err := e.Evaluate(context.Background(), m.ID, verdict.Inputs{"N": 3})
	if err != nil {
		t.Fatalf("a recursion of depth 3 under a cap of 8 should succeed: %v", err)
	}
	if got := res.Outputs["Countdown"]; got != 3.0 {
		t.Errorf("Countdown = %#v, want 3", got)
	}

	// Beyond it, the engine reports a bounded failure rather than dying.
	_, err = e.Evaluate(context.Background(), m.ID, verdict.Inputs{"N": 500})
	if err == nil {
		t.Fatal("unbounded recursion was not stopped")
	}
	if d, ok := err.(diag.Diagnostic); !ok || d.Code != diag.CodeRecursionExceeded {
		t.Errorf("error = %v, want a VERDICT_EVAL_012 diagnostic", err)
	}
}
