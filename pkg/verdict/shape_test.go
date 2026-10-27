package verdict_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/frankbardon/verdict/pkg/agent/mock"
	"github.com/frankbardon/verdict/pkg/trace"
	"github.com/frankbardon/verdict/pkg/verdict"
)

// shapeValues are the FEEL values every boundary must show in lexical form
// (issue #46), keyed by the decision that returns each, with the JSON the
// outside world sees for it. Numbers stay float64.
var shapeValues = []struct {
	id, feel, json string
}{
	{"date", `date("2024-03-05")`, `"2024-03-05"`},
	{"time", `time("10:00:00")`, `"10:00:00"`},
	{"time_z", `time("10:00:00Z")`, `"10:00:00Z"`},
	{"time_off", `time("10:00:00+02:00")`, `"10:00:00+02:00"`},
	{"dt", `date and time("2024-03-05T10:00:00")`, `"2024-03-05T10:00:00"`},
	{"dt_z", `date and time("2024-03-05T10:00:00Z")`, `"2024-03-05T10:00:00Z"`},
	{"dt_off", `date and time("2024-03-05T10:00:00+02:00")`, `"2024-03-05T10:00:00+02:00"`},
	{"dt_zone", `date and time("2024-03-05T10:00:00@Europe/Paris")`, `"2024-03-05T10:00:00@Europe/Paris"`},
	{"dt_frac", `date and time("2024-03-05T10:00:00.5")`, `"2024-03-05T10:00:00.5"`},
	{"dtd", `duration("P1DT2H")`, `"P1DT2H"`},
	{"ymd", `duration("P1Y2M")`, `"P1Y2M"`},
	{"num", `1000000`, `1000000`},
	{"frac", `0.1`, `0.1`},
	// A context literal evaluates to the fork's own map type; 0.1 inside it
	// pins that it is converted too (unconverted, it marshalled as "0.1").
	{"nested", `{d: date("2024-03-05"), f: 0.1, l: [duration("PT1H"), 2.5]}`, `{"d":"2024-03-05","f":0.1,"l":["PT1H",2.5]}`},
}

// shapeModel returns one literal decision per shape value, plus an agent
// decision bound to a handful of the same values, so a single evaluation crosses every
// boundary: Result.Outputs, the trace, the agent request and the prompt.
func shapeModel() string {
	var ds []string
	for _, v := range shapeValues {
		b, _ := json.Marshal(v.feel)
		ds = append(ds, `{"id":"`+v.id+`","name":"`+v.id+`",
		  "variable":{"name":"`+v.id+`","type_ref":"Any"},
		  "logic":{"kind":"literalExpression","text":`+string(b)+`}}`)
	}
	// The agent's bindings are literals rather than references, so every
	// shape decision stays a root and appears in Result.Outputs.
	ds = append(ds, `{"id":"ask","name":"ask",
	  "variable":{"name":"ask","type_ref":"string"},
	  "logic":{"kind":"agentDecision","agent":{
	    "prompt_template":"date={{.d}} dt={{.t}} dur={{.p}} num={{.n}} none={{.z}} ctx={{.c}}",
	    "input_bindings":[
	      {"name":"d","feel":"date(\"2024-03-05\")"},
	      {"name":"t","feel":"date and time(\"2024-03-05T10:00:00\")"},
	      {"name":"p","feel":"duration(\"P1DT2H\")"},
	      {"name":"n","feel":"1000000"},
	      {"name":"z","feel":"null"},
	      {"name":"c","feel":"{d: date(\"2024-03-05\"), l: [duration(\"PT1H\"), 2.5]}"}],
	    "output_type":{"type_ref":"string"}}}}`)
	return `{"vdj":"1.0","id":"shapes","conformance_level":"feel","decisions":[` +
		strings.Join(ds, ",") + `]}`
}

// TestBoundaryShapesAreFEELLexical pins FR-15–FR-17 (issue #46) end to end:
// one evaluation, every boundary, the same lexical form.
func TestBoundaryShapesAreFEELLexical(t *testing.T) {
	bridge := mock.New(mock.WithAnswer("ask", "ok"))
	e, err := verdict.NewEngine(verdict.WithAgentBridge(bridge), verdict.WithTracing(verdict.TracingFull))
	if err != nil {
		t.Fatal(err)
	}
	m, err := e.LoadModel(verdict.FromJSON([]byte(shapeModel())))
	if err != nil {
		t.Fatalf("LoadModel: %v", err)
	}
	res, err := e.Evaluate(context.Background(), m.ID, verdict.Inputs{})
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}

	asJSON := func(v any) string {
		t.Helper()
		b, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}

	// Result.Outputs, and the decision's own trace node output.
	traced := map[string]any{}
	res.Trace.Walk(func(n *trace.Node) {
		if n.NodeKind != "evaluation" {
			traced[n.DecisionID] = n.Output
		}
	})
	for _, v := range shapeValues {
		if got := asJSON(res.Outputs[v.id]); got != v.json {
			t.Errorf("Outputs[%s] (%s) = %s, want %s", v.id, v.feel, got, v.json)
		}
		if got := asJSON(traced[v.id]); got != v.json {
			t.Errorf("trace output %s (%s) = %s, want %s", v.id, v.feel, got, v.json)
		}
	}
	// Numbers are float64 in Go, not strings, and a context is a plain map.
	if _, ok := res.Outputs["num"].(float64); !ok {
		t.Errorf("Outputs[num] is %T, want float64", res.Outputs["num"])
	}
	if _, ok := res.Outputs["nested"].(map[string]any); !ok {
		t.Errorf("Outputs[nested] is %T, want map[string]any", res.Outputs["nested"])
	}

	// The whole trace document carries the same forms.
	doc := asJSON(res.Trace)
	for _, want := range []string{`"2024-03-05T10:00:00"`, `"10:00:00"`, `"P1DT2H"`, `"P1Y2M"`} {
		if !strings.Contains(doc, want) {
			t.Errorf("trace JSON lacks %s", want)
		}
	}
	for _, bad := range []string{`"2024-03-05T00:00:00Z"`, `"0000-01-01T10:00:00Z"`, `93600000000000`} {
		if strings.Contains(doc, bad) {
			t.Errorf("trace JSON still carries the old shape %s", bad)
		}
	}

	// The agent request sees the same shapes, and only its bindings.
	calls := bridge.Calls()
	if len(calls) != 1 {
		t.Fatalf("bridge calls = %d, want 1", len(calls))
	}
	req := calls[0]
	if got, want := asJSON(req.Inputs),
		`{"c":{"d":"2024-03-05","l":["PT1H",2.5]},"d":"2024-03-05","n":1000000,"p":"P1DT2H","t":"2024-03-05T10:00:00","z":null}`; got != want {
		t.Errorf("agent Request.Inputs = %s, want %s", got, want)
	}

	// The prompt prints scalars as FEEL does: no Go time, 26h0m0s or 1e+06.
	wantPrompt := "date=2024-03-05 dt=2024-03-05T10:00:00 dur=P1DT2H num=1000000 none=null ctx={\n" +
		"  \"d\": \"2024-03-05\",\n  \"l\": [\n    \"PT1H\",\n    2.5\n  ]\n}"
	if req.Prompt != wantPrompt {
		t.Errorf("prompt =\n%s\nwant\n%s", req.Prompt, wantPrompt)
	}

	// The agent node's traced inputs match the request.
	var agentInputs map[string]any
	res.Trace.Walk(func(n *trace.Node) {
		if n.DecisionID == "ask" && n.NodeKind != "evaluation" && n.Inputs != nil {
			agentInputs = n.Inputs
		}
	})
	if got, want := asJSON(agentInputs), asJSON(req.Inputs); got != want {
		t.Errorf("agent trace inputs = %s, want %s", got, want)
	}
}
