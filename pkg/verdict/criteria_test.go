package verdict_test

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/frankbardon/verdict/pkg/agent"
	"github.com/frankbardon/verdict/pkg/agent/mock"
	"github.com/frankbardon/verdict/pkg/dmn/model"
	"github.com/frankbardon/verdict/pkg/eval"
	"github.com/frankbardon/verdict/pkg/trace"
	"github.com/frankbardon/verdict/pkg/verdict"
)

// TestTraceIsSufficientToReplay is success criterion 7: a trace plus the model
// and inputs must be enough to reproduce the evaluation exactly — modulo the
// agent, whose non-determinism the trace records by session reference rather
// than pretending to replay.
func TestTraceIsSufficientToReplay(t *testing.T) {
	inputs := comfortableInputs()

	// The first run's agent answer is what the trace records. The replay run
	// pins that answer by session reference, which is precisely the contract:
	// deterministic nodes replay exactly, the agent node is reproduced from its
	// recorded answer rather than re-asked.
	first := mock.New(mock.WithAnswer("risk_tier_agent", "medium"))
	e1, m1 := loadLoan(t, verdict.WithAgentBridge(first))
	run1, err := e1.Evaluate(context.Background(), m1.ID, inputs)
	if err != nil {
		t.Fatalf("first run: %v", err)
	}

	agentNode := run1.Trace.Find("risk_tier")
	if agentNode == nil || agentNode.Output == nil {
		t.Fatal("the trace did not record the agent decision's answer")
	}
	if agentNode.Annotations[trace.AnnSessionRef] == nil {
		t.Fatal("the trace did not record a session reference for the agent decision")
	}

	replay := mock.New(mock.WithAnswer("risk_tier_agent", agentNode.Output))
	e2, m2 := loadLoan(t, verdict.WithAgentBridge(replay))
	run2, err := e2.Evaluate(context.Background(), m2.ID, inputs)
	if err != nil {
		t.Fatalf("replay run: %v", err)
	}

	if !reflect.DeepEqual(run1.Outputs, run2.Outputs) {
		t.Errorf("replay produced different outputs:\n first: %#v\nreplay: %#v", run1.Outputs, run2.Outputs)
	}

	// Every deterministic node must have fired identically: same node, same
	// kind, same rules, same value — and in the same order, since a trace that
	// reorders itself between runs cannot be diffed to explain a change.
	shape1 := traceShape(run1.Trace)
	shape2 := traceShape(run2.Trace)
	if !reflect.DeepEqual(shape1, shape2) {
		t.Errorf("replay took a different path:\n first: %v\nreplay: %v", shape1, shape2)
	}

	// The model hash pins which model was evaluated, so a replay against a
	// changed model is detectable rather than silently wrong.
	if run1.Trace.ModelHash == "" || run1.Trace.ModelHash != run2.Trace.ModelHash {
		t.Errorf("model hashes = %q and %q", run1.Trace.ModelHash, run2.Trace.ModelHash)
	}
}

// traceShape reduces a trace to the facts a replay must reproduce, dropping
// timings, which cannot and need not match.
func traceShape(t *trace.Trace) []string {
	var out []string
	t.Walk(func(n *trace.Node) {
		rules, _ := n.Annotations[trace.AnnMatchedRules].([]string)
		out = append(out, fmt.Sprintf("%s|%s|%v|%v", n.DecisionID, n.NodeKind, rules, n.Output))
	})
	return out
}

// TestTraceSerialisesAndSurvivesARoundTrip checks the trace is a wire format,
// not just an in-memory structure: a dashboard receives it as JSON.
func TestTraceSerialisesAndSurvivesARoundTrip(t *testing.T) {
	e, m := loadLoan(t, verdict.WithAgentBridge(mock.New(mock.WithAnswer("risk_tier_agent", "low"))))
	res, err := e.Evaluate(context.Background(), m.ID, comfortableInputs())
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}

	blob, err := json.Marshal(res.Trace)
	if err != nil {
		t.Fatalf("marshalling the trace: %v", err)
	}
	var restored trace.Trace
	if err := json.Unmarshal(blob, &restored); err != nil {
		t.Fatalf("unmarshalling the trace: %v", err)
	}
	if restored.ModelID != res.Trace.ModelID || restored.Root == nil {
		t.Fatalf("round trip lost the trace: %+v", restored)
	}
	if got, want := len(restored.Root.Children), len(res.Trace.Root.Children); got != want {
		t.Errorf("round-tripped trace has %d children, want %d", got, want)
	}
	if restored.Find("credit_rating") == nil {
		t.Error("round-tripped trace cannot find a decision the original recorded")
	}
}

// TestDeterministicEvaluationIsFast is success criterion 4's latency budget: a
// caller must get a typed result with a trace in well under 50ms, excluding any
// agent latency.
func TestDeterministicEvaluationIsFast(t *testing.T) {
	e, m := loadLoan(t, verdict.WithAgentBridge(mock.New(mock.WithAnswer("risk_tier_agent", "low"))))
	inputs := comfortableInputs()

	// Warm the compile caches, which is the state a running service is in.
	if _, err := e.Evaluate(context.Background(), m.ID, inputs); err != nil {
		t.Fatal(err)
	}

	const runs = 50
	start := time.Now()
	for i := 0; i < runs; i++ {
		res, err := e.Evaluate(context.Background(), m.ID, inputs)
		if err != nil {
			t.Fatalf("run %d: %v", i, err)
		}
		if res.Trace == nil {
			t.Fatal("no trace was produced")
		}
	}
	avg := time.Since(start) / runs
	if avg > 50*time.Millisecond {
		t.Errorf("average evaluation took %v, want under 50ms", avg)
	}
	t.Logf("average full-model evaluation with a trace: %v", avg)
}

func BenchmarkEvaluateLoanApproval(b *testing.B) {
	e, err := verdict.NewEngine(
		verdict.WithAgentBridge(mock.New(mock.WithAnswer("risk_tier_agent", "low"))))
	if err != nil {
		b.Fatal(err)
	}
	m, err := e.LoadModel(verdict.FromFile(loanModel))
	if err != nil {
		b.Fatal(err)
	}
	inputs := comfortableInputs()
	ctx := context.Background()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := e.Evaluate(ctx, m.ID, inputs); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkEvaluateTracingOff(b *testing.B) {
	e, err := verdict.NewEngine(
		verdict.WithAgentBridge(mock.New(mock.WithAnswer("risk_tier_agent", "low"))),
		verdict.WithTracing(verdict.TracingOff))
	if err != nil {
		b.Fatal(err)
	}
	m, err := e.LoadModel(verdict.FromFile(loanModel))
	if err != nil {
		b.Fatal(err)
	}
	inputs := comfortableInputs()
	ctx := context.Background()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := e.Evaluate(ctx, m.ID, inputs); err != nil {
			b.Fatal(err)
		}
	}
}

// TestConcurrentEvaluationIsSafe checks the claim in the API docs: one engine,
// many goroutines. Run with -race for it to mean anything.
func TestConcurrentEvaluationIsSafe(t *testing.T) {
	e, m := loadLoan(t, verdict.WithAgentBridge(mock.New(mock.WithAnswer("risk_tier_agent", "low"))))

	const workers = 16
	errs := make(chan error, workers)
	for i := 0; i < workers; i++ {
		go func(i int) {
			inputs := comfortableInputs()
			applicant := inputs["Applicant"].(map[string]any)
			applicant["credit_score"] = 600 + i*20
			res, err := e.Evaluate(context.Background(), m.ID, inputs)
			if err != nil {
				errs <- err
				return
			}
			if res.Outputs["Routing"] == nil {
				errs <- fmt.Errorf("worker %d got no routing", i)
				return
			}
			errs <- nil
		}(i)
	}
	for i := 0; i < workers; i++ {
		if err := <-errs; err != nil {
			t.Error(err)
		}
	}
}

// TestCustomNodeEvaluatorReplacesAKind exercises the extension point: an
// application can replace the handling of a boxed-expression kind without
// forking the engine. Here the agent node is answered by ordinary Go code, so a
// model with an agent decision in it runs with no bridge at all.
func TestCustomNodeEvaluatorReplacesAKind(t *testing.T) {
	calls := 0
	answer := eval.NodeEvaluatorFunc(func(ec *eval.Context, expr model.Expression) (any, error) {
		calls++
		a, ok := expr.(*model.AgentDecision)
		if !ok {
			return nil, fmt.Errorf("expected an agent decision, got %T", expr)
		}
		// The evaluator sees the real expression, so it can honour the model's
		// declared type rather than guessing.
		if len(a.OutputType.Enumeration) == 0 {
			return nil, fmt.Errorf("expected an enumerated output type")
		}
		return a.OutputType.Enumeration[0], nil
	})

	e, m := loadLoan(t, verdict.WithNodeEvaluator("agentDecision", answer))
	res, err := e.Evaluate(context.Background(), m.ID, comfortableInputs())
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if calls != 1 {
		t.Errorf("custom evaluator called %d times, want 1", calls)
	}
	// "low" is the first enumeration value, so routing auto-approves.
	if got := res.Outputs["Routing"]; got != "auto-approve" {
		t.Errorf("Routing = %v, want auto-approve", got)
	}
}

// TestAgentBridgeReceivesTheDeclaredType checks the bridge contract: the
// declared output type travels with the request, so a bridge can constrain the
// model rather than only checking it afterwards.
func TestAgentBridgeReceivesTheDeclaredType(t *testing.T) {
	var seen agent.Request
	bridge := agent.BridgeFunc(func(_ context.Context, req agent.Request) (agent.Response, error) {
		seen = req
		return agent.Response{Value: "low", SessionRef: "test://1"}, nil
	})
	e, m := loadLoan(t, verdict.WithAgentBridge(bridge))
	if _, err := e.Evaluate(context.Background(), m.ID, comfortableInputs()); err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if len(seen.OutputType.Enumeration) != 3 {
		t.Errorf("bridge saw output type %+v, want a three-value enumeration", seen.OutputType)
	}
	if seen.Trace == nil || !seen.Trace.Enabled() {
		t.Error("bridge was not given a usable trace writer")
	}
}

// TestTraceOrderIsStableUnderParallelism pins the ordering guarantee directly.
// Independent decisions are evaluated concurrently, so their trace nodes could
// easily land in completion order; they must land in evaluation order instead.
func TestTraceOrderIsStableUnderParallelism(t *testing.T) {
	e, m := loadLoan(t,
		verdict.WithAgentBridge(mock.New(mock.WithAnswer("risk_tier_agent", "low"))),
		verdict.WithParallelDecisions(true))

	var first []string
	for i := 0; i < 12; i++ {
		res, err := e.Evaluate(context.Background(), m.ID, comfortableInputs())
		if err != nil {
			t.Fatalf("run %d: %v", i, err)
		}
		var order []string
		res.Trace.Walk(func(n *trace.Node) { order = append(order, n.DecisionID) })
		if first == nil {
			first = order
			continue
		}
		if !reflect.DeepEqual(order, first) {
			t.Fatalf("run %d visited nodes in a different order:\n first: %v\n  this: %v", i, first, order)
		}
	}
}
