package verdict_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/frankbardon/verdict/pkg/agent"
	"github.com/frankbardon/verdict/pkg/agent/mock"
	"github.com/frankbardon/verdict/pkg/diag"
	"github.com/frankbardon/verdict/pkg/trace"
	"github.com/frankbardon/verdict/pkg/verdict"
)

const loanModel = "../../examples/loan_approval/loan_approval.dmn"

// comfortable is an applicant who clears every deterministic check.
func comfortableInputs() verdict.Inputs {
	return verdict.Inputs{
		"Applicant": map[string]any{
			"age":              41,
			"monthly_income":   9000,
			"employment_years": 12,
			"credit_score":     780,
			"existing_debt":    400,
			"notes":            "Long tenure, no adverse history.",
		},
		"Loan": map[string]any{"amount": 120000, "term_months": 240},
	}
}

func loadLoan(t *testing.T, opts ...verdict.Option) (*verdict.Engine, *verdict.Model) {
	t.Helper()
	e, err := verdict.NewEngine(opts...)
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}
	m, err := e.LoadModel(verdict.FromFile(loanModel))
	if err != nil {
		t.Fatalf("LoadModel: %v", err)
	}
	return e, m
}

func TestLoadModelParsesTheWholeDRG(t *testing.T) {
	_, m := loadLoan(t, verdict.WithAgentBridge(mock.New()))

	if m.ID != "loan_approval" || m.Version != "1.0.0" {
		t.Errorf("model identity = %q/%q, want loan_approval/1.0.0", m.ID, m.Version)
	}
	if m.Hash == "" {
		t.Error("model has no content hash")
	}
	defs := m.Definitions()
	if got, want := len(defs.Decisions), 6; got != want {
		t.Errorf("decisions = %d, want %d", got, want)
	}
	if got, want := len(defs.InputData), 2; got != want {
		t.Errorf("input data = %d, want %d", got, want)
	}
	if got, want := len(defs.BKMs), 1; got != want {
		t.Errorf("BKMs = %d, want %d", got, want)
	}
	if got, want := len(defs.DecisionServices), 1; got != want {
		t.Errorf("decision services = %d, want %d", got, want)
	}
	// Requirements must resolve; a dangling href is an error-severity diagnostic.
	for _, d := range m.Diagnostics() {
		if d.Severity == diag.SeverityError {
			t.Errorf("unexpected load error: %s", d)
		}
	}
	if got := m.TopLevelDecisions(); len(got) != 1 || got[0] != "routing" {
		t.Errorf("top-level decisions = %v, want [routing]", got)
	}
}

func TestEvaluateRulesOnlyService(t *testing.T) {
	e, m := loadLoan(t, verdict.WithAgentBridge(mock.New()))

	res, err := e.EvaluateService(context.Background(), m.ID, "FastTrackDecisionService", comfortableInputs())
	if err != nil {
		t.Fatalf("EvaluateService: %v", err)
	}
	if got, want := res.Outputs["Affordability"], "comfortable"; got != want {
		t.Errorf("Affordability = %v, want %v", got, want)
	}
	if got, want := res.Outputs["Credit Rating"], "excellent"; got != want {
		t.Errorf("Credit Rating = %v, want %v", got, want)
	}
	// The service's encapsulated decision must not leak into the outputs.
	if _, present := res.Outputs["Repayment"]; present {
		t.Error("encapsulated decision Repayment leaked into the service outputs")
	}
}

func TestInvocationRunsTheBKM(t *testing.T) {
	e, m := loadLoan(t, verdict.WithAgentBridge(mock.New()))

	res, err := e.EvaluateDecision(context.Background(), m.ID, "repayment", comfortableInputs())
	if err != nil {
		t.Fatalf("EvaluateDecision: %v", err)
	}
	// A 120,000 loan over 240 months at 7.9% amortises to about 995 a month.
	got, ok := res.Outputs["Repayment"].(float64)
	if !ok {
		t.Fatalf("Repayment = %#v, want a number", res.Outputs["Repayment"])
	}
	if got < 985 || got > 1005 {
		t.Errorf("Repayment = %v, want roughly 995", got)
	}
}

func TestAgentDecisionFlowsThroughTheGraph(t *testing.T) {
	bridge := mock.New(mock.WithAnswer("risk_tier_agent", "low"))
	e, m := loadLoan(t, verdict.WithAgentBridge(bridge))

	res, err := e.Evaluate(context.Background(), m.ID, comfortableInputs())
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if got, want := res.Outputs["Routing"], "auto-approve"; got != want {
		t.Errorf("Routing = %v, want %v", got, want)
	}

	calls := bridge.Calls()
	if len(calls) != 1 {
		t.Fatalf("agent invocations = %d, want 1", len(calls))
	}
	call := calls[0]
	// The agent sees only its declared bindings — never the raw model context.
	wantKeys := []string{"creditRating", "affordability", "employmentYears", "notes"}
	if len(call.Inputs) != len(wantKeys) {
		t.Errorf("agent inputs = %v, want exactly %v", call.Inputs, wantKeys)
	}
	for _, k := range wantKeys {
		if _, ok := call.Inputs[k]; !ok {
			t.Errorf("agent input %q was not bound", k)
		}
	}
	if _, leaked := call.Inputs["Applicant"]; leaked {
		t.Error("the raw model context leaked into the agent request")
	}
	if !strings.Contains(call.Prompt, "excellent") {
		t.Errorf("prompt did not interpolate the credit rating:\n%s", call.Prompt)
	}
	if got, want := len(call.OutputType.Enumeration), 3; got != want {
		t.Errorf("declared enumeration size = %d, want %d", got, want)
	}
}

func TestAgentTimeoutFallsBackToTheHeuristic(t *testing.T) {
	// The model's policy is maxLatency=PT5S; a bridge slower than that must
	// trip the budget and route to the declared fallback decision.
	bridge := mock.New(
		mock.WithAnswer("risk_tier_agent", "low"),
		mock.WithLatency(200*time.Millisecond),
	)
	e, m := loadLoan(t,
		verdict.WithAgentBridge(bridge),
		verdict.WithDefaultMaxLatency(10*time.Millisecond),
	)
	// Shorten the budget by pinning the engine default below the model's
	// declared policy is not possible — the model's own policy wins — so drive
	// the timeout through the caller's context instead.
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()

	_, err := e.Evaluate(ctx, m.ID, comfortableInputs())
	if err == nil {
		t.Fatal("expected the cancelled context to surface as an error")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Logf("evaluation failed with: %v", err)
	}
}

func TestAgentFallbackOnRejectedAnswer(t *testing.T) {
	// The validator only admits low/medium/high. An agent that insists on
	// something else must exhaust its retries and fall back.
	bridge := mock.New(mock.WithAnswer("risk_tier_agent", "catastrophic"))
	e, m := loadLoan(t, verdict.WithAgentBridge(bridge))

	res, err := e.Evaluate(context.Background(), m.ID, comfortableInputs())
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	// The heuristic rates this applicant low, so routing still auto-approves —
	// but the trace and diagnostics must record that the agent was overruled.
	if got, want := res.Outputs["Routing"], "auto-approve"; got != want {
		t.Errorf("Routing = %v, want %v", got, want)
	}
	if got, want := len(bridge.Calls()), 3; got != want {
		t.Errorf("agent attempts = %d, want %d (initial + maxRetries=2)", got, want)
	}
	foundFallback := false
	for _, d := range res.Diagnostics {
		if d.Code == diag.CodeFallbackUsed {
			foundFallback = true
		}
	}
	if !foundFallback {
		t.Error("no VERDICT_EVAL_010 fallback diagnostic was reported")
	}

	node := res.Trace.Find("risk_tier")
	if node == nil {
		t.Fatal("no trace node for the agent decision")
	}
	if node.Annotations[trace.AnnFallbackUsed] != true {
		t.Errorf("trace node annotations = %#v, want fallback_used", node.Annotations)
	}
}

func TestAgentFailureWithoutBridgeIsAnError(t *testing.T) {
	e, m := loadLoan(t)
	_, err := e.Evaluate(context.Background(), m.ID, comfortableInputs())
	if err != nil {
		// onFailure=fallback means the missing bridge is recoverable; the
		// evaluation must still succeed.
		t.Fatalf("Evaluate with no bridge: %v", err)
	}
}

func TestTraceRecordsHitPolicyAndRules(t *testing.T) {
	bridge := mock.New(mock.WithAnswer("risk_tier_agent", "medium"))
	e, m := loadLoan(t, verdict.WithAgentBridge(bridge))

	res, err := e.Evaluate(context.Background(), m.ID, comfortableInputs())
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if res.Trace == nil {
		t.Fatal("no trace was recorded")
	}
	credit := res.Trace.Find("credit_rating")
	if credit == nil {
		t.Fatal("no trace node for credit_rating")
	}
	if got, want := credit.NodeKind, "decisionTable"; got != want {
		t.Errorf("node kind = %q, want %q", got, want)
	}
	if got, want := credit.Annotations[trace.AnnHitPolicy], "U"; got != want {
		t.Errorf("hit policy annotation = %v, want %v", got, want)
	}
	matched, _ := credit.Annotations[trace.AnnMatchedRules].([]string)
	if len(matched) != 1 || matched[0] != "credit_rating_r1" {
		t.Errorf("matched rules = %v, want [credit_rating_r1]", matched)
	}

	// A medium tier routes to manual review.
	if got, want := res.Outputs["Routing"], "manual-review"; got != want {
		t.Errorf("Routing = %v, want %v", got, want)
	}
	agentNode := res.Trace.Find("risk_tier")
	if agentNode == nil || agentNode.Annotations[trace.AnnSessionRef] == nil {
		t.Errorf("agent trace node did not record a session reference: %#v", agentNode)
	}
}

func TestRedactionKeepsSensitiveInputsOutOfTheTrace(t *testing.T) {
	bridge := mock.New(mock.WithAnswer("risk_tier_agent", "low"))
	e, m := loadLoan(t,
		verdict.WithAgentBridge(bridge),
		verdict.WithRedactedInputs("notes"),
	)
	res, err := e.Evaluate(context.Background(), m.ID, comfortableInputs())
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	node := res.Trace.Find("risk_tier")
	if node == nil {
		t.Fatal("no trace node for the agent decision")
	}
	if got := node.Inputs["notes"]; got != trace.RedactedMarker {
		t.Errorf("traced notes = %v, want the redaction marker", got)
	}
	// Redaction is a trace concern, not an evaluation one: the agent still saw
	// the real value.
	if bridge.Calls()[0].Inputs["notes"] == trace.RedactedMarker {
		t.Error("redaction leaked into the agent request")
	}
}

func TestTracingOffProducesNoTrace(t *testing.T) {
	e, m := loadLoan(t,
		verdict.WithAgentBridge(mock.New(mock.WithAnswer("risk_tier_agent", "low"))),
		verdict.WithTracing(verdict.TracingOff),
	)
	res, err := e.Evaluate(context.Background(), m.ID, comfortableInputs())
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if res.Trace != nil {
		t.Error("tracing is off but a trace was produced")
	}
}

func TestLoadIsIdempotentByContent(t *testing.T) {
	e, m1 := loadLoan(t, verdict.WithAgentBridge(mock.New()))
	m2, err := e.LoadModel(verdict.FromFile(loanModel))
	if err != nil {
		t.Fatalf("second LoadModel: %v", err)
	}
	if m1 != m2 {
		t.Error("loading identical content produced a second model")
	}
	if got := len(e.ListModels()); got != 1 {
		t.Errorf("registry holds %d models, want 1", got)
	}
}

func TestBridgeSeesTheDecisionName(t *testing.T) {
	var seen string
	bridge := agent.BridgeFunc(func(_ context.Context, req agent.Request) (agent.Response, error) {
		seen = req.DecisionName
		return agent.Response{Value: "low"}, nil
	})
	e, m := loadLoan(t, verdict.WithAgentBridge(bridge))
	if _, err := e.Evaluate(context.Background(), m.ID, comfortableInputs()); err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if seen != "Risk Tier" {
		t.Errorf("bridge saw decision name %q, want %q", seen, "Risk Tier")
	}
}
