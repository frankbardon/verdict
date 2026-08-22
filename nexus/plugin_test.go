package nexus

import (
	"context"
	"testing"
	"time"

	nexusengine "github.com/frankbardon/nexus/pkg/engine"
)

const loanModel = "../examples/loan_approval/loan_approval.dmn"

func comfortableInputs() map[string]any {
	return map[string]any{
		"Applicant": map[string]any{
			"age": 41, "monthly_income": 9000, "employment_years": 12,
			"credit_score": 780, "existing_debt": 400,
			"notes": "Long tenure, no adverse history.",
		},
		"Loan": map[string]any{"amount": 120000, "term_months": 240},
	}
}

// bootPlugin wires the plugin against a real bus with a stub runner standing in
// for the LLM plugin, which is what makes the round trip testable without a
// provider or an API key.
func bootPlugin(t *testing.T, cfg map[string]any, answer string) (*Plugin, nexusengine.EventBus) {
	t.Helper()
	bus := nexusengine.NewEventBus()
	p := NewPlugin()

	if err := p.Init(nexusengine.PluginContext{
		Config: cfg,
		Bus:    bus,
	}); err != nil {
		t.Fatalf("Init: %v", err)
	}
	// Replace the LLM-backed runner with a stub so the test exercises the
	// plugin and the graph, not a provider.
	p.Bridge().runner = &stubRunner{answer: answer}

	if err := p.Ready(); err != nil {
		t.Fatalf("Ready: %v", err)
	}
	t.Cleanup(func() { _ = p.Shutdown(context.Background()) })
	return p, bus
}

func TestPluginEvaluatesOnTheBus(t *testing.T) {
	_, bus := bootPlugin(t, map[string]any{
		"models":        []any{loanModel},
		"publish_trace": true,
	}, "low")

	var completed []DecisionCompleted
	var failed []DecisionFailed
	var nodes []TraceNode
	bus.Subscribe(EventDecisionCompleted, func(ev nexusengine.Event[any]) {
		if p, ok := ev.Payload.(DecisionCompleted); ok {
			completed = append(completed, p)
		}
	})
	bus.Subscribe(EventDecisionFailed, func(ev nexusengine.Event[any]) {
		if p, ok := ev.Payload.(DecisionFailed); ok {
			failed = append(failed, p)
		}
	})
	bus.Subscribe(EventTraceNode, func(ev nexusengine.Event[any]) {
		if p, ok := ev.Payload.(TraceNode); ok {
			nodes = append(nodes, p)
		}
	})

	if err := bus.Emit(EventDecisionRequested, DecisionRequest{
		RequestID: "req-1",
		Inputs:    comfortableInputs(),
	}); err != nil {
		t.Fatalf("Emit: %v", err)
	}

	if len(failed) > 0 {
		t.Fatalf("evaluation failed: %s", failed[0].Error)
	}
	if len(completed) != 1 {
		t.Fatalf("completion events = %d, want 1", len(completed))
	}
	got := completed[0]
	if got.RequestID != "req-1" {
		t.Errorf("request id = %q, want req-1", got.RequestID)
	}
	if got.ModelID != "loan_approval" {
		t.Errorf("model id = %q, want loan_approval", got.ModelID)
	}
	if got.Outputs["Routing"] != "auto-approve" {
		t.Errorf("Routing = %#v, want auto-approve", got.Outputs["Routing"])
	}
	if got.Trace == nil {
		t.Error("no trace was returned")
	}
	// The republished nodes are what a live dashboard renders.
	if len(nodes) < 6 {
		t.Errorf("republished %d trace nodes, want one per decision plus the root", len(nodes))
	}
}

func TestPluginEvaluatesASingleDecision(t *testing.T) {
	_, bus := bootPlugin(t, map[string]any{"models": []any{loanModel}}, "low")

	var completed []DecisionCompleted
	bus.Subscribe(EventDecisionCompleted, func(ev nexusengine.Event[any]) {
		if p, ok := ev.Payload.(DecisionCompleted); ok {
			completed = append(completed, p)
		}
	})

	if err := bus.Emit(EventDecisionRequested, DecisionRequest{
		Decision: "credit_rating",
		Inputs:   comfortableInputs(),
	}); err != nil {
		t.Fatalf("Emit: %v", err)
	}
	if len(completed) != 1 {
		t.Fatalf("completion events = %d, want 1", len(completed))
	}
	if got := completed[0].Outputs["Credit Rating"]; got != "excellent" {
		t.Errorf("Credit Rating = %#v, want excellent", got)
	}
}

func TestPluginReportsAnUnknownModel(t *testing.T) {
	_, bus := bootPlugin(t, map[string]any{"models": []any{loanModel}}, "low")

	var failed []DecisionFailed
	bus.Subscribe(EventDecisionFailed, func(ev nexusengine.Event[any]) {
		if p, ok := ev.Payload.(DecisionFailed); ok {
			failed = append(failed, p)
		}
	})
	if err := bus.Emit(EventDecisionRequested, DecisionRequest{ModelID: "nope"}); err != nil {
		t.Fatalf("Emit: %v", err)
	}
	if len(failed) != 1 {
		t.Fatalf("failure events = %d, want 1", len(failed))
	}
}

func TestPluginContractDeclarationsMatch(t *testing.T) {
	p := NewPlugin()
	if p.ID() != PluginID {
		t.Errorf("ID = %q, want %q", p.ID(), PluginID)
	}
	subs := p.Subscriptions()
	if len(subs) != 1 || subs[0].EventType != EventDecisionRequested {
		t.Errorf("subscriptions = %+v, want exactly decision.requested", subs)
	}
	emissions := map[string]bool{}
	for _, e := range p.Emissions() {
		emissions[e] = true
	}
	for _, want := range []string{
		EventDecisionCompleted, EventDecisionFailed,
		EventAgentDecisionRequested, EventAgentDecisionCompleted, EventTraceNode,
	} {
		if !emissions[want] {
			t.Errorf("Emissions() does not declare %q", want)
		}
	}
	if len(p.Capabilities()) == 0 {
		t.Error("the plugin advertises no capability")
	}
}

func TestConfigParsing(t *testing.T) {
	c := parseConfig(map[string]any{
		"models":              []any{"a.dmn", "b.vdj"},
		"strict_mode":         true,
		"tracing":             "summary",
		"redact_inputs":       []any{"Applicant.ssn"},
		"session_strategy":    "per-evaluation",
		"channel":             "decisions",
		"role":                "reasoning",
		"default_max_latency": "12s",
		"publish_trace":       false,
	})
	if len(c.Paths) != 2 || c.Paths[0] != "a.dmn" {
		t.Errorf("paths = %v", c.Paths)
	}
	if !c.Strict || c.Tracing != "summary" || c.Channel != "decisions" || c.Role != "reasoning" {
		t.Errorf("config = %+v", c)
	}
	if c.SessionStrategy != PerEvaluation {
		t.Errorf("session strategy = %q, want per-evaluation", c.SessionStrategy)
	}
	if c.DefaultMaxLatency != 12*time.Second {
		t.Errorf("default max latency = %v, want 12s", c.DefaultMaxLatency)
	}
	if c.PublishTrace {
		t.Error("publish_trace: false was ignored")
	}

	// An unparseable value must fall back rather than fail: a decision plugin
	// that refuses to boot takes the whole agent down with it.
	fallback := parseConfig(map[string]any{"session_strategy": "telepathic", "default_max_latency": "soon"})
	if fallback.SessionStrategy != PerDecision {
		t.Errorf("bad session strategy = %q, want the per-decision default", fallback.SessionStrategy)
	}
	if fallback.DefaultMaxLatency != 0 {
		t.Errorf("bad duration = %v, want zero", fallback.DefaultMaxLatency)
	}
}
