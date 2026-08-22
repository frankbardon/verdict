package nexus

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	nexusengine "github.com/frankbardon/nexus/pkg/engine"

	"github.com/frankbardon/verdict/pkg/agent"
	"github.com/frankbardon/verdict/pkg/dmn/model"
)

// stubRunner answers with a fixed text and records what it was asked.
type stubRunner struct {
	answer string
	calls  []Call
}

func (r *stubRunner) Run(_ context.Context, call Call) (Answer, error) {
	r.calls = append(r.calls, call)
	return Answer{Text: r.answer, Model: "stub", InputTokens: 10, OutputTokens: 2}, nil
}

func newTestBridge(t *testing.T, runner Runner, opts ...Option) (*Bridge, nexusengine.EventBus) {
	t.Helper()
	bus := nexusengine.NewEventBus()
	all := append([]Option{
		WithBus(bus),
		WithRunner(runner),
		WithTranscripts(t.TempDir()),
	}, opts...)
	b, err := NewBridge(all...)
	if err != nil {
		t.Fatalf("NewBridge: %v", err)
	}
	return b, bus
}

func TestBridgeAnswersAnEnumeratedDecision(t *testing.T) {
	runner := &stubRunner{answer: `{"value":"medium"}`}
	b, _ := newTestBridge(t, runner)

	resp, err := b.Invoke(context.Background(), agent.Request{
		DecisionID:   "risk_tier",
		DecisionName: "Risk Tier",
		Prompt:       "Classify the risk.",
		Inputs:       map[string]any{"score": 700},
		OutputType:   model.TypeSpec{TypeRef: "string", Enumeration: []string{"low", "medium", "high"}},
	})
	if err != nil {
		t.Fatalf("Invoke: %v", err)
	}
	if resp.Value != "medium" {
		t.Errorf("value = %#v, want \"medium\"", resp.Value)
	}
	if resp.Tokens.Total != 12 {
		t.Errorf("token total = %d, want 12", resp.Tokens.Total)
	}
	if !strings.Contains(resp.SessionRef, "risk_tier") {
		t.Errorf("session ref = %q, want it to name the decision under per-decision strategy", resp.SessionRef)
	}

	if len(runner.calls) != 1 {
		t.Fatalf("runner calls = %d, want 1", len(runner.calls))
	}
	call := runner.calls[0]
	// The declared shape must reach the runner both as a schema, for structured
	// output, and in words, so a model that ignores the schema still complies.
	if !strings.Contains(call.Prompt, "exactly one of: low, medium, high") {
		t.Errorf("prompt does not state the required shape:\n%s", call.Prompt)
	}
	props, _ := call.Schema["properties"].(map[string]any)
	value, _ := props["value"].(map[string]any)
	enum, _ := value["enum"].([]string)
	if len(enum) != 3 {
		t.Errorf("schema enum = %#v, want three values", value["enum"])
	}
}

func TestBridgeAcceptsABareAnswer(t *testing.T) {
	// Structured output is requested, but a model that answers `low` in plain
	// text is unambiguous and must not cost a retry.
	runner := &stubRunner{answer: "  low\n"}
	b, _ := newTestBridge(t, runner)

	resp, err := b.Invoke(context.Background(), agent.Request{
		DecisionID: "tier",
		OutputType: model.TypeSpec{Enumeration: []string{"low", "high"}},
	})
	if err != nil {
		t.Fatalf("Invoke: %v", err)
	}
	if resp.Value != "low" {
		t.Errorf("value = %#v, want \"low\"", resp.Value)
	}
}

func TestBridgeUnwrapsAFencedObject(t *testing.T) {
	runner := &stubRunner{answer: "```json\n{\"value\": {\"tier\": \"high\", \"score\": 3}}\n```"}
	b, _ := newTestBridge(t, runner)

	resp, err := b.Invoke(context.Background(), agent.Request{
		DecisionID: "structured",
		OutputType: model.TypeSpec{Components: map[string]model.TypeSpec{
			"tier":  {Enumeration: []string{"low", "high"}},
			"score": {TypeRef: "number"},
		}},
	})
	if err != nil {
		t.Fatalf("Invoke: %v", err)
	}
	got, ok := resp.Value.(map[string]any)
	if !ok {
		t.Fatalf("value = %#v, want an object", resp.Value)
	}
	if got["tier"] != "high" {
		t.Errorf("tier = %#v, want \"high\"", got["tier"])
	}
}

func TestBridgeRejectsProseWhereAnObjectWasRequired(t *testing.T) {
	runner := &stubRunner{answer: "I think the applicant looks fine, honestly."}
	b, _ := newTestBridge(t, runner)

	_, err := b.Invoke(context.Background(), agent.Request{
		DecisionID: "structured",
		OutputType: model.TypeSpec{Components: map[string]model.TypeSpec{"tier": {}}},
	})
	if err == nil {
		t.Fatal("prose was accepted where a structured answer was required")
	}
}

func TestSessionStrategies(t *testing.T) {
	req := func(id string) agent.Request {
		return agent.Request{DecisionID: id, OutputType: model.TypeSpec{}}
	}
	session := &nexusengine.SessionWorkspace{ID: "sess-1"}

	t.Run("per-decision isolates each node", func(t *testing.T) {
		b, _ := newTestBridge(t, &stubRunner{answer: "x"},
			WithSession(session), WithSessionStrategy(PerDecision))
		a, _ := b.Invoke(context.Background(), req("alpha"))
		c, _ := b.Invoke(context.Background(), req("beta"))
		if a.SessionRef == c.SessionRef {
			t.Errorf("two decisions shared a session under per-decision: %q", a.SessionRef)
		}
	})

	t.Run("per-evaluation shares until reset", func(t *testing.T) {
		b, _ := newTestBridge(t, &stubRunner{answer: "x"},
			WithSession(session), WithSessionStrategy(PerEvaluation))
		a, _ := b.Invoke(context.Background(), req("alpha"))
		c, _ := b.Invoke(context.Background(), req("beta"))
		if a.SessionRef != c.SessionRef {
			t.Errorf("per-evaluation did not share: %q vs %q", a.SessionRef, c.SessionRef)
		}
		b.Reset()
		d, _ := b.Invoke(context.Background(), req("gamma"))
		if d.SessionRef == a.SessionRef {
			t.Error("Reset did not start a new evaluation session")
		}
	})

	t.Run("shared reuses one session", func(t *testing.T) {
		b, _ := newTestBridge(t, &stubRunner{answer: "x"},
			WithSession(session), WithSessionStrategy(Shared))
		a, _ := b.Invoke(context.Background(), req("alpha"))
		b.Reset()
		c, _ := b.Invoke(context.Background(), req("beta"))
		if a.SessionRef != c.SessionRef {
			t.Errorf("shared strategy produced two sessions: %q vs %q", a.SessionRef, c.SessionRef)
		}
	})

	t.Run("an explicit hint overrides the strategy", func(t *testing.T) {
		b, _ := newTestBridge(t, &stubRunner{answer: "x"},
			WithSession(session), WithSessionStrategy(PerDecision))
		r := req("alpha")
		r.SessionHint = "underwriting"
		a, _ := b.Invoke(context.Background(), r)
		if !strings.HasSuffix(a.SessionRef, "underwriting") {
			t.Errorf("session ref = %q, want the hint honoured", a.SessionRef)
		}
	})
}

func TestBridgeAnnouncesOnTheBus(t *testing.T) {
	b, bus := newTestBridge(t, &stubRunner{answer: "low"})

	var requested []AgentDecisionRequested
	var completed []AgentDecisionCompleted
	bus.Subscribe(EventAgentDecisionRequested, func(ev nexusengine.Event[any]) {
		if p, ok := ev.Payload.(AgentDecisionRequested); ok {
			requested = append(requested, p)
		}
	})
	bus.Subscribe(EventAgentDecisionCompleted, func(ev nexusengine.Event[any]) {
		if p, ok := ev.Payload.(AgentDecisionCompleted); ok {
			completed = append(completed, p)
		}
	})

	if _, err := b.Invoke(context.Background(), agent.Request{
		DecisionID: "tier",
		Prompt:     "Classify.",
		OutputType: model.TypeSpec{Enumeration: []string{"low", "high"}},
	}); err != nil {
		t.Fatalf("Invoke: %v", err)
	}

	if len(requested) != 1 || len(completed) != 1 {
		t.Fatalf("events: %d requested, %d completed; want 1 and 1", len(requested), len(completed))
	}
	if requested[0].CallID != completed[0].CallID {
		t.Error("the request and completion events do not share a call id")
	}
	if completed[0].Value != "low" {
		t.Errorf("completion carried %#v, want \"low\"", completed[0].Value)
	}
}

func TestSchemaProjection(t *testing.T) {
	spec := model.TypeSpec{Components: map[string]model.TypeSpec{
		"tier":    {Enumeration: []string{"low", "high"}},
		"score":   {TypeRef: "number"},
		"reasons": {Collection: true, TypeRef: "string"},
	}}
	raw, err := json.Marshal(jsonSchema(spec))
	if err != nil {
		t.Fatal(err)
	}
	s := string(raw)
	for _, want := range []string{`"enum":["low","high"]`, `"type":"number"`, `"type":"array"`, `"required":["value"]`} {
		if !strings.Contains(s, want) {
			t.Errorf("schema is missing %s:\n%s", want, s)
		}
	}
}
