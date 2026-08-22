package mcp_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/frankbardon/verdict/pkg/agent/mock"
	"github.com/frankbardon/verdict/pkg/verdict"
	vmcp "github.com/frankbardon/verdict/server/mcp"
)

const loanModel = "../../examples/loan_approval/loan_approval.dmn"

func newEngine(t *testing.T) *verdict.Engine {
	t.Helper()
	e, err := verdict.NewEngine(
		verdict.WithAgentBridge(mock.New(mock.WithAnswer("risk_tier_agent", "low"))))
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}
	if _, err := e.LoadModel(verdict.FromFile(loanModel)); err != nil {
		t.Fatalf("LoadModel: %v", err)
	}
	return e
}

// call invokes a tool through the catalogue's type-erased entry point, which is
// exactly the path the SDK adapter takes.
func call(t *testing.T, e *verdict.Engine, name string, args any) any {
	t.Helper()
	raw, err := json.Marshal(args)
	if err != nil {
		t.Fatal(err)
	}
	for _, td := range vmcp.Tools(vmcp.Config{AllowLoad: true}) {
		if td.Name != name {
			continue
		}
		out, err := td.Invoke(context.Background(), e, raw)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		return out
	}
	t.Fatalf("no tool named %q in the catalogue", name)
	return nil
}

func TestCatalogueShape(t *testing.T) {
	tools := vmcp.Tools(vmcp.Config{})
	names := map[string]bool{}
	for _, td := range tools {
		names[td.Name] = true
		if td.Description == "" {
			t.Errorf("%s has no description; an agent picks a tool from its description alone", td.Name)
		}
		if len(td.InputSchema) == 0 || len(td.OutputSchema) == 0 {
			t.Errorf("%s is missing a reflected schema", td.Name)
		}
		if td.Invoke == nil {
			t.Errorf("%s has no handler", td.Name)
		}
	}
	for _, want := range []string{
		"verdict_evaluate", "verdict_evaluate_decision",
		"verdict_explain", "verdict_analyze", "verdict_list_models",
	} {
		if !names[want] {
			t.Errorf("catalogue is missing %s", want)
		}
	}
	// Model loading is opt-in: an agent that can load models can shadow the
	// ones an operator deployed.
	if names["verdict_load_model"] {
		t.Error("verdict_load_model is exposed by default")
	}
	if !hasTool(vmcp.Tools(vmcp.Config{AllowLoad: true}), "verdict_load_model") {
		t.Error("AllowLoad did not expose verdict_load_model")
	}
}

func hasTool(tools []vmcp.ToolDescriptor, name string) bool {
	for _, td := range tools {
		if td.Name == name {
			return true
		}
	}
	return false
}

func TestListModels(t *testing.T) {
	e := newEngine(t)
	out := call(t, e, "verdict_list_models", vmcp.ListInput{}).(vmcp.ListOutput)
	if len(out.Models) != 1 {
		t.Fatalf("models = %d, want 1", len(out.Models))
	}
	m := out.Models[0]
	if m.ID != "loan_approval" {
		t.Errorf("model id = %q", m.ID)
	}
	// The input names are what a caller must supply, so they must be listed.
	if len(m.Inputs) != 2 || m.Inputs[0] != "Applicant" {
		t.Errorf("inputs = %v, want [Applicant Loan]", m.Inputs)
	}
	if len(m.TopLevel) != 1 || m.TopLevel[0] != "routing" {
		t.Errorf("top level = %v, want [routing]", m.TopLevel)
	}
}

func TestEvaluateTools(t *testing.T) {
	e := newEngine(t)
	inputs := map[string]any{
		"Applicant": map[string]any{
			"age": 41, "monthly_income": 9000, "employment_years": 12,
			"credit_score": 780, "existing_debt": 400, "notes": "Long tenure.",
		},
		"Loan": map[string]any{"amount": 120000, "term_months": 240},
	}

	out := call(t, e, "verdict_evaluate", vmcp.EvaluateInput{
		ModelID: "loan_approval", Inputs: inputs,
	}).(vmcp.EvaluateOutput)
	if out.Outputs["Routing"] != "auto-approve" {
		t.Errorf("Routing = %#v, want auto-approve", out.Outputs["Routing"])
	}
	// The summary is what an agent reads instead of the whole trace.
	if len(out.Summary) == 0 {
		t.Error("no per-decision summary was returned")
	}
	joined := strings.Join(out.Summary, "\n")
	if !strings.Contains(joined, "credit_rating_r1") {
		t.Errorf("summary does not name the rules that fired:\n%s", joined)
	}
	if out.Trace != nil {
		t.Error("the full trace was returned without include_trace")
	}

	single := call(t, e, "verdict_evaluate_decision", vmcp.EvaluateDecisionInput{
		ModelID: "loan_approval", DecisionID: "credit_rating", Inputs: inputs, IncludeTrace: true,
	}).(vmcp.EvaluateOutput)
	if single.Outputs["Credit Rating"] != "excellent" {
		t.Errorf("Credit Rating = %#v", single.Outputs["Credit Rating"])
	}
	if single.Trace == nil {
		t.Error("include_trace did not return the trace")
	}
}

func TestEvaluateDecisionRequiresExactlyOneTarget(t *testing.T) {
	e := newEngine(t)
	for _, in := range []vmcp.EvaluateDecisionInput{
		{ModelID: "loan_approval"},
		{ModelID: "loan_approval", DecisionID: "credit_rating", ServiceID: "FastTrackDecisionService"},
	} {
		if _, err := vmcp.EvaluateDecision(context.Background(), e, in); err == nil {
			t.Errorf("accepted an ambiguous target: %+v", in)
		}
	}
}

func TestExplainTool(t *testing.T) {
	e := newEngine(t)
	out := call(t, e, "verdict_explain", vmcp.ExplainInput{
		ModelID: "loan_approval", DecisionID: "risk_tier",
	}).(vmcp.ExplainOutput)
	if !strings.Contains(out.Text, "exactly one of") && !strings.Contains(out.Text, "Must answer") {
		t.Errorf("explain text does not state the agent's answer shape:\n%s", out.Text)
	}
	if out.Slice == nil {
		t.Error("no structured slice was returned")
	}
}

func TestAnalyzeTool(t *testing.T) {
	e := newEngine(t)
	out := call(t, e, "verdict_analyze", vmcp.AnalyzeInput{ModelID: "loan_approval"}).(vmcp.AnalyzeOutput)
	if len(out.Tables) != 4 {
		t.Errorf("tables analysed = %d, want 4", len(out.Tables))
	}
	found := false
	for _, tbl := range out.Tables {
		if tbl.Decision == "Routing" && len(tbl.Overlaps) > 0 {
			found = true
		}
	}
	if !found {
		t.Error("the Routing table's overlaps were not reported")
	}
}

func TestToolErrorsAreLegible(t *testing.T) {
	e := newEngine(t)
	// An agent must be able to correct its next call from the error text, so
	// the message names the tool that lists the valid values.
	_, err := vmcp.Evaluate(context.Background(), e, vmcp.EvaluateInput{})
	if err == nil {
		t.Fatal("a missing model_id was accepted")
	}
	if !strings.Contains(err.Error(), "verdict_list_models") {
		t.Errorf("error does not point at the discovery tool: %v", err)
	}
}
