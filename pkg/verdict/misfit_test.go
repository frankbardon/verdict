package verdict_test

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/frankbardon/verdict/pkg/diag"
	"github.com/frankbardon/verdict/pkg/trace"
	"github.com/frankbardon/verdict/pkg/verdict"
)

// misfitModel builds a model with a BKM, Scale(n, factor), a decision service,
// Echo(X), a BKM whose body overruns a small iteration budget, Count Up(n), and
// one decision per check, each a literal expression invoking one of them.
func misfitModel(checks map[string]string) string {
	var ds []string
	for id, text := range checks {
		ds = append(ds, fmt.Sprintf(`{"id":%q,"name":%q,
		  "required_knowledge":["scale","count_up"],
		  "variable":{"name":%q,"type_ref":"Any"},
		  "logic":{"kind":"literalExpression","text":%q}}`, id, id, id, text))
	}
	return `{"vdj":"1.0","id":"misfits","conformance_level":"feel",
	  "input_data":[{"id":"x","name":"X","variable":{"name":"X","type_ref":"Any"}}],
	  "business_knowledge_models":[
	    {"id":"scale","name":"Scale","encapsulated_logic":{"kind":"functionDefinition",
	      "parameters":[{"name":"n","type_ref":"number"},{"name":"factor","type_ref":"number"}],
	      "body":{"kind":"literalExpression","text":"n * factor"}}},
	    {"id":"count_up","name":"Count Up","encapsulated_logic":{"kind":"functionDefinition",
	      "parameters":[{"name":"n","type_ref":"number"}],
	      "body":{"kind":"literalExpression","text":"count(for i in 1..n return i)"}}}],
	  "decisions":[
	    {"id":"echoed","name":"Echoed","required_inputs":["x"],
	     "variable":{"name":"Echoed","type_ref":"Any"},
	     "logic":{"kind":"literalExpression","text":"X"}},` + strings.Join(ds, ",") + `],
	  "decision_services":[
	    {"id":"echo","name":"Echo","output_decisions":["echoed"],"input_data":["x"]}]}`
}

// TestMisfittingInvocationsAreNull pins DMN 1.5 §10.3.2.13.2: a BKM or
// decision service invoked from FEEL is "equivalent to a FEEL function", so an
// invocation whose arguments do not conform to its parameters — too many, too
// few, an unknown or a missing name — is null rather than an evaluation error.
// An error raised inside the invoked body still propagates.
func TestMisfittingInvocationsAreNull(t *testing.T) {
	checks := map[string]string{
		"bkm_fits":           `Scale(2, 3)`,
		"bkm_named_fits":     `Scale(factor: 3, n: 2)`,
		"bkm_too_many":       `Scale(2, 3, 4)`,
		"bkm_too_few":        `Scale(2)`,
		"bkm_none":           `Scale()`,
		"bkm_unknown_name":   `Scale(n: 2, factor: 3, extra: 4)`,
		"bkm_missing_name":   `Scale(n: 2)`,
		"bkm_null_composes":  `Scale(2) = null`,
		"svc_fits":           `Echo(5)`,
		"svc_too_many":       `Echo(5, 6)`,
		"svc_too_few":        `Echo()`,
		"svc_unknown_name":   `Echo(Y: 5)`,
		"svc_null_composes":  `Echo(5, 6) = null`,
		"bkm_misfit_no_body": `Count Up(1000, 1)`,
	}
	want := map[string]any{
		"bkm_fits":          6.0,
		"bkm_named_fits":    6.0,
		"bkm_null_composes": true,
		"svc_fits":          5.0,
		"svc_null_composes": true,
	}

	// The budget is too small for Count Up(1000), so a misfitting call to it
	// returning null shows its body never ran.
	e, err := verdict.NewEngine(verdict.WithMaxIterations(100))
	if err != nil {
		t.Fatal(err)
	}
	m, err := e.LoadModel(verdict.FromJSON([]byte(misfitModel(checks))))
	if err != nil {
		t.Fatalf("LoadModel: %v", err)
	}
	for id := range checks {
		t.Run(id, func(t *testing.T) {
			res, err := e.EvaluateDecision(context.Background(), m.ID, id, verdict.Inputs{})
			if err != nil {
				t.Fatalf("%s: evaluation error %v, want %#v", checks[id], err, want[id])
			}
			got, ok := res.Outputs[id]
			if !ok || got != want[id] {
				t.Errorf("%s = %#v (present %v), want %#v", checks[id], got, ok, want[id])
			}
			for _, d := range res.Diagnostics {
				if d.Code == diag.CodeExpressionError {
					t.Errorf("%s reported an expression error: %s", checks[id], d)
				}
			}
		})
	}
}

// TestMisfittingServiceCallLeavesNoServiceNode records what the trace shows:
// a misfitting call is null before the service runs, so the calling decision's
// node is there with a null output and no decisionService child is recorded,
// where a fitting call records one.
func TestMisfittingServiceCallLeavesNoServiceNode(t *testing.T) {
	checks := map[string]string{"fits": `Echo(5)`, "misfits": `Echo(5, 6)`}
	e, err := verdict.NewEngine()
	if err != nil {
		t.Fatal(err)
	}
	m, err := e.LoadModel(verdict.FromJSON([]byte(misfitModel(checks))))
	if err != nil {
		t.Fatalf("LoadModel: %v", err)
	}
	services := func(id string) (int, bool) {
		t.Helper()
		res, err := e.EvaluateDecision(context.Background(), m.ID, id, verdict.Inputs{})
		if err != nil {
			t.Fatalf("EvaluateDecision %s: %v", id, err)
		}
		n, decided := 0, false
		res.Trace.Walk(func(node *trace.Node) {
			if node.NodeKind == "decisionService" {
				n++
			}
			if node.DecisionID == id && node.NodeKind != "evaluation" {
				decided = true
			}
		})
		return n, decided
	}
	if n, decided := services("fits"); n != 1 || !decided {
		t.Errorf("fitting call: %d service nodes (decision node %v), want 1 and a decision node", n, decided)
	}
	if n, decided := services("misfits"); n != 0 || !decided {
		t.Errorf("misfitting call: %d service nodes (decision node %v), want 0 and a decision node", n, decided)
	}
}

// TestErrorInsideAnInvokedBodyStillPropagates is the other half of
// §10.3.2.13.2's null rule: only argument misfits become null. A BKM whose
// body overruns the iteration budget still fails the evaluation.
func TestErrorInsideAnInvokedBodyStillPropagates(t *testing.T) {
	checks := map[string]string{"overrun": `Count Up(1000)`}
	e, err := verdict.NewEngine(verdict.WithMaxIterations(100))
	if err != nil {
		t.Fatal(err)
	}
	m, err := e.LoadModel(verdict.FromJSON([]byte(misfitModel(checks))))
	if err != nil {
		t.Fatalf("LoadModel: %v", err)
	}
	res, err := e.EvaluateDecision(context.Background(), m.ID, "overrun", verdict.Inputs{})
	failed := err != nil
	if res != nil {
		for _, d := range res.Diagnostics {
			if d.Severity == diag.SeverityError {
				failed = true
			}
		}
	}
	if !failed {
		t.Errorf("Count Up(1000) under a budget of 100: no error, outputs %#v", res.Outputs)
	}
}
