package verdict_test

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/frankbardon/verdict/pkg/trace"
	"github.com/frankbardon/verdict/pkg/verdict"
)

// serviceModel builds a model with an identity decision service, Echo, a
// two-output service, Pair, and one boolean decision per check, each invoking
// a service from FEEL.
func serviceModel(checks map[string]string) string {
	var ds []string
	for id, text := range checks {
		ds = append(ds, fmt.Sprintf(`{"id":%q,"name":%q,
		  "variable":{"name":%q,"type_ref":"Any"},
		  "logic":{"kind":"literalExpression","text":%q}}`, id, id, id, text))
	}
	extra := ""
	if len(ds) > 0 {
		extra = "," + strings.Join(ds, ",")
	}
	return `{"vdj":"1.0","id":"services","conformance_level":"feel",
	  "input_data":[{"id":"x","name":"X","variable":{"name":"X","type_ref":"Any"}}],
	  "decisions":[
	    {"id":"echoed","name":"Echoed","required_inputs":["x"],
	     "variable":{"name":"Echoed","type_ref":"Any"},
	     "logic":{"kind":"literalExpression","text":"X"}},
	    {"id":"first","name":"First","required_inputs":["x"],
	     "variable":{"name":"First","type_ref":"Any"},
	     "logic":{"kind":"literalExpression","text":"X"}},
	    {"id":"second","name":"Second","required_inputs":["x"],
	     "variable":{"name":"Second","type_ref":"Any"},
	     "logic":{"kind":"literalExpression","text":"X"}}` + extra + `],
	  "decision_services":[
	    {"id":"echo","name":"Echo","output_decisions":["echoed"],"input_data":["x"]},
	    {"id":"pair","name":"Pair","output_decisions":["first","second"],"input_data":["x"]}]}`
}

// TestDecisionServicesPassNativeFEELValues pins issue #45: a value crossing a
// decision service, in either direction, keeps its FEEL type and value (DMN 1.5
// §10.3.2.9). Before the fix every argument and result was narrowed to plain Go
// and back, so a zoneless date and time came back zoned UTC, a date or time
// came back a date and time, a years-and-months duration came back a string
// and a decimal came back a float64 — each visible to FEEL after the call.
func TestDecisionServicesPassNativeFEELValues(t *testing.T) {
	// 0.1000000000000000055511151231257827 is what 0.1 became after a trip
	// through float64; 1/3 and a 30-digit literal lose digits the same way.
	const big = "12345678901234567890.123456789"
	checks := map[string]string{
		// Zoneless date and time: equal to itself, still zoneless, and its
		// lexical form unchanged.
		"dt_equal":    `Echo(date and time("2024-03-05T10:00:00")) = date and time("2024-03-05T10:00:00")`,
		"dt_zoneless": `Echo(date and time("2024-03-05T10:00:00")).timezone = null and Echo(date and time("2024-03-05T10:00:00")).time offset = null`,
		"dt_string":   `string(Echo(date and time("2024-03-05T10:00:00"))) = "2024-03-05T10:00:00"`,
		"dt_zoned":    `Echo(date and time("2024-03-05T10:00:00+02:00")).time offset = duration("PT2H")`,

		// A date stays a date and a time stays a time — not a date and time at
		// midnight UTC or on 0000-01-01: kind identity is checked through
		// instance of, through equality, which is null across kinds, and
		// through string().
		"date_kind":     `Echo(date("2024-03-05")) instance of date`,
		"time_kind":     `Echo(time("10:00:00")) instance of time`,
		"date_equal":    `Echo(date("2024-03-05")) = date("2024-03-05")`,
		"date_string":   `string(Echo(date("2024-03-05"))) = "2024-03-05"`,
		"date_arith":    `Echo(date("2024-03-05")) + duration("P1D") = date("2024-03-06")`,
		"time_equal":    `Echo(time("10:00:00")) = time("10:00:00")`,
		"time_string":   `string(Echo(time("10:00:00"))) = "10:00:00"`,
		"time_zoneless": `Echo(time("10:00:00")).time offset = null`,

		// A years-and-months duration stays a duration, not its ISO string.
		"ym_equal":    `Echo(duration("P1Y2M")) = duration("P1Y2M")`,
		"ym_property": `Echo(duration("P1Y2M")).months = 2`,
		"ym_arith":    `Echo(duration("P1Y2M")) + duration("P1M") = duration("P1Y3M")`,
		"dt_duration": `Echo(duration("P1DT2H")) = duration("P1DT2H")`,

		// A decimal keeps every digit, through equality and arithmetic.
		"decimal_tenth": `Echo(0.1) = 0.1 and Echo(0.1) + 0.2 = 0.3 and string(Echo(0.1)) = "0.1"`,
		"decimal_third": `Echo(1/3) = 1/3`,
		"decimal_big":   `Echo(` + big + `) = ` + big,

		// A multi-output service returns a context of native values.
		"pair_date":    `Pair(date("2024-03-05")).First = date("2024-03-05")`,
		"pair_decimal": `Pair(0.1).Second + 0.2 = 0.3`,

		// Nested values inside a context and a list cross intact too.
		"nested": `Echo({d: date("2024-03-05"), l: [duration("P1Y")]}).d = date("2024-03-05") and Echo({d: date("2024-03-05"), l: [duration("P1Y")]}).l[1] = duration("P1Y")`,
	}

	e, err := verdict.NewEngine()
	if err != nil {
		t.Fatal(err)
	}
	m, err := e.LoadModel(verdict.FromJSON([]byte(serviceModel(checks))))
	if err != nil {
		t.Fatalf("LoadModel: %v", err)
	}
	for id := range checks {
		t.Run(id, func(t *testing.T) {
			res, err := e.EvaluateDecision(context.Background(), m.ID, id, verdict.Inputs{})
			if err != nil {
				t.Fatalf("EvaluateDecision: %v", err)
			}
			if got := res.Outputs[id]; got != true {
				t.Errorf("%s = %#v, want true", checks[id], got)
			}
		})
	}
}

// TestDecisionServiceOutputsAreUnchangedAtTheBoundary checks the other half of
// #45: keeping values native inside the engine changes nothing a caller sees.
// A value returned through a service is narrowed for Result.Outputs and the
// trace exactly as the same value returned directly is.
func TestDecisionServiceOutputsAreUnchangedAtTheBoundary(t *testing.T) {
	values := []string{
		`date and time("2024-03-05T10:00:00")`,
		`date and time("2024-03-05T10:00:00+02:00")`,
		`date("2024-03-05")`,
		`time("10:00:00")`,
		`duration("P1DT2H")`,
		`duration("P1Y2M")`,
		`0.1000000000000000055511151231257827`,
		`{a: date("2024-03-05"), b: [1.5, "x"]}`,
	}
	checks := map[string]string{}
	for i, v := range values {
		checks[fmt.Sprintf("direct%d", i)] = v
		checks[fmt.Sprintf("service%d", i)] = "Echo(" + v + ")"
	}

	e, err := verdict.NewEngine()
	if err != nil {
		t.Fatal(err)
	}
	m, err := e.LoadModel(verdict.FromJSON([]byte(serviceModel(checks))))
	if err != nil {
		t.Fatalf("LoadModel: %v", err)
	}
	eval := func(id string) (string, string) {
		t.Helper()
		res, err := e.EvaluateDecision(context.Background(), m.ID, id, verdict.Inputs{})
		if err != nil {
			t.Fatalf("EvaluateDecision %s: %v", id, err)
		}
		out, err := json.Marshal(res.Outputs[id])
		if err != nil {
			t.Fatal(err)
		}
		// The decision's own node, not the evaluation root that shares its ID.
		var traced any
		res.Trace.Walk(func(n *trace.Node) {
			if n.DecisionID == id && n.NodeKind != "evaluation" {
				traced = n.Output
			}
		})
		tr, err := json.Marshal(traced)
		if err != nil {
			t.Fatal(err)
		}
		return string(out), string(tr)
	}
	for i, v := range values {
		direct, directTrace := eval(fmt.Sprintf("direct%d", i))
		via, viaTrace := eval(fmt.Sprintf("service%d", i))
		if direct != via {
			t.Errorf("%s: direct output %s, through a service %s", v, direct, via)
		}
		if directTrace != viaTrace {
			t.Errorf("%s: direct trace output %s, through a service %s", v, directTrace, viaTrace)
		}
	}
}

// TestEvaluateServiceAcceptsPlainGoInputs checks the public entry still
// converts caller-supplied Go values to FEEL once, now that the conversion
// lives in Evaluate rather than in input binding.
func TestEvaluateServiceAcceptsPlainGoInputs(t *testing.T) {
	e, err := verdict.NewEngine()
	if err != nil {
		t.Fatal(err)
	}
	m, err := e.LoadModel(verdict.FromJSON([]byte(serviceModel(map[string]string{}))))
	if err != nil {
		t.Fatalf("LoadModel: %v", err)
	}
	res, err := e.EvaluateService(context.Background(), m.ID, "Pair", verdict.Inputs{"X": 3})
	if err != nil {
		t.Fatalf("EvaluateService: %v", err)
	}
	if res.Outputs["First"] != 3.0 || res.Outputs["Second"] != 3.0 {
		t.Errorf("outputs = %#v, want First and Second 3", res.Outputs)
	}
}
