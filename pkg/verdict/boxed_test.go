package verdict_test

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
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

// TestArithmeticOnAMissingInputIsNull checks DMN 1.5 §10.3.2.15 at the engine
// level: `A + B` with B not supplied is null — an absent answer flagged by
// VERDICT_EVAL_004 — not a VERDICT_EVAL_005 expression failure.
func TestArithmeticOnAMissingInputIsNull(t *testing.T) {
	doc := `{"vdj":"1.0","id":"sum","conformance_level":"feel",
	  "input_data":[
	    {"id":"a","name":"A","variable":{"name":"A","type_ref":"number"}},
	    {"id":"b","name":"B","variable":{"name":"B","type_ref":"number"}}],
	  "decisions":[{"id":"total","name":"Total","required_inputs":["a","b"],
	    "variable":{"name":"Total","type_ref":"number"},
	    "logic":{"kind":"literalExpression","text":"A + B"}}]}`

	e, err := verdict.NewEngine()
	if err != nil {
		t.Fatal(err)
	}
	m, err := e.LoadModel(verdict.FromBytes([]byte(doc)))
	if err != nil {
		t.Fatalf("LoadModel: %v", err)
	}
	res, err := e.Evaluate(context.Background(), m.ID, verdict.Inputs{"A": 2})
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if got, ok := res.Outputs["Total"]; !ok || got != nil {
		t.Errorf("Total = %#v (present %v), want null", got, ok)
	}
	missing := false
	for _, d := range res.Diagnostics {
		switch d.Code {
		case diag.CodeExpressionError:
			t.Errorf("arithmetic on a missing input failed the expression: %s", d)
		case diag.CodeMissingInput:
			missing = true
		}
	}
	if !missing {
		t.Errorf("no VERDICT_EVAL_004 diagnostic for the missing input: %v", res.Diagnostics)
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

// TestIterationBudgetBoundsAnEvaluation checks that one expression evaluation
// cannot iterate without bound: its iteration contexts together — nested and
// multi-context products counted in full — fail with VERDICT_EVAL_005 once they
// exceed the budget, an expression within it evaluates, the configured budget
// rather than the default is the one enforced, and sibling decisions evaluating
// in parallel draw on one budget for the request.
func TestIterationBudgetBoundsAnEvaluation(t *testing.T) {
	model := func(texts ...string) string {
		var ds []string
		for k, text := range texts {
			ds = append(ds, fmt.Sprintf(`{"id":"d%d","name":"D%d","required_inputs":["n"],
			  "variable":{"name":"D%d","type_ref":"number"},
			  "logic":{"kind":"literalExpression","text":%q}}`, k, k, k, text))
		}
		return `{"vdj":"1.0","id":"iterations","conformance_level":"feel",
		  "input_data":[{"id":"n","name":"N","variable":{"name":"N","type_ref":"number"}}],
		  "decisions":[` + strings.Join(ds, ",") + `]}`
	}
	// run evaluates every decision of doc and returns their outputs, or the
	// first VERDICT_EVAL_005 diagnostic.
	run := func(t *testing.T, e *verdict.Engine, doc string, n int) (map[string]any, error) {
		t.Helper()
		m, err := e.LoadModel(verdict.FromBytes([]byte(doc)))
		if err != nil {
			t.Fatalf("LoadModel: %v", err)
		}
		res, err := e.Evaluate(context.Background(), m.ID, verdict.Inputs{"N": n})
		if err != nil {
			return nil, err
		}
		for _, d := range res.Diagnostics {
			if d.Code == diag.CodeExpressionError {
				return nil, d
			}
		}
		return res.Outputs, nil
	}
	isBudgetFailure := func(err error) bool {
		var d diag.Diagnostic
		return errors.As(err, &d) && d.Code == diag.CodeExpressionError &&
			strings.Contains(d.Message, "exceeds the limit")
	}
	flat := model("count(for i in 1..N return i)")
	nested := model("count(for i in 1..N, j in 1..N return j)")

	t.Run("default budget", func(t *testing.T) {
		e := verdict.MustNewEngine()
		if got, err := run(t, e, flat, 1000); err != nil || got["D0"] != 1000.0 {
			t.Errorf("1..1000 = %#v, %v; want 1000", got, err)
		}
		if _, err := run(t, e, flat, 1_000_000_000); !isBudgetFailure(err) {
			t.Errorf("1..1e9 under the default budget: err = %v, want a VERDICT_EVAL_005 budget failure", err)
		}
		// 1..1000 twice is 1000 + 1000*1000 elements: each range alone is
		// well inside the budget, their product is not.
		if _, err := run(t, e, nested, 1000); !isBudgetFailure(err) {
			t.Errorf("1..1000 × 1..1000 under the default budget: err = %v, want a VERDICT_EVAL_005 budget failure", err)
		}
	})

	t.Run("configured budget", func(t *testing.T) {
		e := verdict.MustNewEngine(verdict.WithMaxIterations(12))
		if got, err := run(t, e, flat, 12); err != nil || got["D0"] != 12.0 {
			t.Errorf("1..12 under a budget of 12 = %#v, %v; want 12", got, err)
		}
		if _, err := run(t, e, flat, 13); !isBudgetFailure(err) {
			t.Errorf("1..13 under a budget of 12: err = %v, want a VERDICT_EVAL_005 budget failure", err)
		}
		// 3 + 3*3 = 12 fits; 4 + 4*4 = 20 does not.
		if got, err := run(t, e, nested, 3); err != nil || got["D0"] != 9.0 {
			t.Errorf("1..3 × 1..3 under a budget of 12 = %#v, %v; want 9", got, err)
		}
		if _, err := run(t, e, nested, 4); !isBudgetFailure(err) {
			t.Errorf("1..4 × 1..4 under a budget of 12: err = %v, want a VERDICT_EVAL_005 budget failure", err)
		}
	})

	t.Run("parallel siblings share the request's budget", func(t *testing.T) {
		// Eight independent decisions in one parallel layer, each spending
		// 3 + 3*3 = 12. One of them fits a budget of 12; the request does
		// not, because every decision draws on one budget.
		texts := make([]string, 8)
		for k := range texts {
			texts[k] = "count(for i in 1..N, j in 1..N return j)"
		}
		e := verdict.MustNewEngine(verdict.WithMaxIterations(12), verdict.WithParallelDecisions(true))
		if got, err := run(t, e, model(texts[0]), 3); err != nil || got["D0"] != 9.0 {
			t.Fatalf("one decision within budget = %#v, %v; want 9", got, err)
		}
		if _, err := run(t, e, model(texts...), 3); !isBudgetFailure(err) {
			t.Errorf("eight siblings of 12 under a request budget of 12: err = %v, want a VERDICT_EVAL_005 budget failure", err)
		}
		// 8 × 12 = 96 fits a budget of 96, run in parallel: the shared
		// counter is exact under concurrent charges.
		e = verdict.MustNewEngine(verdict.WithMaxIterations(96), verdict.WithParallelDecisions(true))
		got, err := run(t, e, model(texts...), 3)
		if err != nil {
			t.Fatalf("parallel siblings within the request budget: %v", err)
		}
		for k := range texts {
			if v := got[fmt.Sprintf("D%d", k)]; v != 9.0 {
				t.Errorf("D%d = %#v, want 9", k, v)
			}
		}
	})

	t.Run("each request starts with the whole budget", func(t *testing.T) {
		e := verdict.MustNewEngine(verdict.WithMaxIterations(12))
		for n := 0; n < 3; n++ {
			if got, err := run(t, e, flat, 12); err != nil || got["D0"] != 12.0 {
				t.Errorf("request %d: 1..12 under a budget of 12 = %#v, %v; want 12", n+1, got, err)
			}
		}
	})

	t.Run("negative budget is rejected", func(t *testing.T) {
		if _, err := verdict.NewEngine(verdict.WithMaxIterations(-1)); err == nil {
			t.Error("NewEngine accepted a negative iteration budget")
		}
	})
}

// TestIterationBudgetIsPerRequest checks that function invocations do not
// multiply the budget: a business knowledge model, a boxed function and a
// decision service called from a `for` all draw on the request's one budget,
// so a loop whose every call fits the budget still fails once the calls
// together exceed it.
func TestIterationBudgetIsPerRequest(t *testing.T) {
	// Heavy costs 10 per call; Svc's output decision costs 10 per call.
	const doc = `{"vdj":"1.0","id":"per_request","conformance_level":"feel",
	  "input_data":[{"id":"n","name":"N","variable":{"name":"N","type_ref":"number"}}],
	  "business_knowledge_models":[{"id":"heavy","name":"Heavy",
	    "encapsulated_logic":{"kind":"functionDefinition",
	      "parameters":[{"name":"x","type_ref":"number"}],
	      "body":{"kind":"literalExpression","text":"x + count(for i in 1..10 return i)"}}}],
	  "decisions":[
	    {"id":"via_bkm","name":"Via BKM","required_inputs":["n"],"required_knowledge":["heavy"],
	     "variable":{"name":"Via BKM","type_ref":"number"},
	     "logic":{"kind":"literalExpression","text":"count(for k in 1..N return Heavy(k))"}},
	    {"id":"via_boxed","name":"Via Boxed","required_inputs":["n"],
	     "variable":{"name":"Via Boxed","type_ref":"number"},
	     "logic":{"kind":"context","entries":[
	       {"variable":{"name":"f"},"value":{"kind":"functionDefinition",
	         "parameters":[{"name":"x","type_ref":"number"}],
	         "body":{"kind":"literalExpression","text":"x + count(for i in 1..10 return i)"}}},
	       {"value":{"kind":"literalExpression","text":"count(for k in 1..N return f(k))"}}]}},
	    {"id":"work","name":"Work","required_inputs":["n"],
	     "variable":{"name":"Work","type_ref":"number"},
	     "logic":{"kind":"literalExpression","text":"N + count(for i in 1..10 return i)"}},
	    {"id":"via_service","name":"Via Service","required_inputs":["n"],
	     "variable":{"name":"Via Service","type_ref":"number"},
	     "logic":{"kind":"literalExpression","text":"count(for k in 1..N return Svc(k))"}}],
	  "decision_services":[{"id":"svc","name":"Svc","output_decisions":["work"],"input_data":["n"]}]}`

	// Each call fits a budget of 100. At N = 5 a decision costs 5 + 5*10 = 55
	// and fits; at N = 10 it costs 10 + 10*10 = 110 and does not, although no
	// single call comes near the budget.
	for id, decision := range map[string]string{"via_bkm": "Via BKM", "via_boxed": "Via Boxed", "via_service": "Via Service"} {
		t.Run(decision, func(t *testing.T) {
			e := verdict.MustNewEngine(verdict.WithMaxIterations(100))
			m, err := e.LoadModel(verdict.FromBytes([]byte(doc)))
			if err != nil {
				t.Fatalf("LoadModel: %v", err)
			}
			eval := func(n int) (any, error) {
				res, err := e.EvaluateDecision(context.Background(), m.ID, id, verdict.Inputs{"N": n})
				if err != nil {
					return nil, err
				}
				for _, d := range res.Diagnostics {
					if d.Code == diag.CodeExpressionError {
						return nil, d
					}
				}
				return res.Outputs[decision], nil
			}
			if got, err := eval(5); err != nil || got != 5.0 {
				t.Errorf("N = 5 under a request budget of 100 = %#v, %v; want 5", got, err)
			}
			_, err = eval(10)
			var d diag.Diagnostic
			if !errors.As(err, &d) || d.Code != diag.CodeExpressionError || !strings.Contains(d.Message, "exceeds the limit") {
				t.Errorf("N = 10 under a request budget of 100: err = %v, want a VERDICT_EVAL_005 budget failure", err)
			}
		})
	}
}
