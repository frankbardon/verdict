# Nexus integration

Verdict is the deterministic counterpart to [Nexus](https://github.com/frankbardon/nexus).
Nexus reasons in natural language and emits events; Verdict evaluates structured
decisions and returns auditable outcomes. Together they cover the hybrid space
with an explicit boundary between the halves.

## A separate module, on purpose

The integration lives in `github.com/frankbardon/verdict/nexus` — a **distinct
Go module** in the same repository.

This is enforcement, not tidiness. A service that imports
`github.com/frankbardon/verdict` cannot transitively acquire Nexus, its provider
SDKs, or its plugin surface, because the module boundary makes that impossible
rather than merely discouraged. `go build ./...` at the repository root does not
build `nexus/` at all.

The dependency runs one way: this module imports Verdict and Nexus, and neither
imports it.

```go
import (
    "github.com/frankbardon/verdict/pkg/verdict"
    vnexus "github.com/frankbardon/verdict/nexus"
)
```

## Direction 1 — Nexus answers Verdict

The bridge routes `agentDecision` nodes into Nexus.

```go
bridge, err := vnexus.NewBridge(
    vnexus.WithBus(ctx.Bus),
    vnexus.WithSession(ctx.Session),
    vnexus.WithSessionStrategy(vnexus.PerDecision),
)

engine, err := verdict.NewEngine(verdict.WithAgentBridge(bridge))
```

Each invocation:

1. announces `verdict.decision.requested` on the bus, with the prompt, the bound
   inputs and the required answer shape;
2. puts the question to the configured runner;
3. parses the answer against the declared type;
4. announces `verdict.decision.completed` with the value and the session
   reference;
5. writes a transcript into the session workspace, so the call is readable long
   after the process exits.

The session reference lands in the decision trace, which is what makes an agent
decision *forensically* rather than merely *statistically* available: given a
trace, you can open the exact conversation that produced the value.

### Runners

The bridge's bookkeeping is fixed; how the question is actually answered is not.

**`LLMRunner`** (the default) puts one structured-output request to the engine's
LLM plugin. The declared output type becomes a provider-side JSON Schema, so an
enumerated tier is constrained at the provider rather than corrected afterwards.
Cheap, synchronous, and enough for the classification-shaped questions most
agent decisions ask.

**`DelegateRunner`** turns each question into a full sub-agent run: a posture, a
workspace, tool access, a budget and a journal.

```go
bridge, _ := vnexus.NewBridge(
    vnexus.WithBus(ctx.Bus),
    vnexus.WithSession(ctx.Session),
    vnexus.WithRunner(&vnexus.DelegateRunner{
        Runtime:   delegateRuntime,
        Posture:   "underwriter",
        Overrides: delegate.Overrides{MaxTokens: 4000},
    }),
)
```

This is what "a Nexus session as evaluator" buys over a bare model call. The
node is unchanged — same declared type, same validator, same failure policy —
but the thing answering it can read files, call tools and take several turns.
Swapping runners is a wiring change, not a model change.

### Session strategies

| Strategy | Behaviour | When |
|---|---|---|
| `per-decision` (default) | Each agent node gets its own session reference | Almost always. One node's conversation cannot colour another's |
| `per-evaluation` | One session per evaluation | Later nodes benefit from seeing what earlier ones were asked. The useful middle ground |
| `shared` | One long-lived session across evaluations | Cheapest and best-informed, and the **only** strategy where one request's data can reach another's prompt. Use it only where that is acceptable |

A model may override the strategy for one node with
`<verdict:policy sessionHint="..."/>`; an explicit hint always wins, because the
modeller asked for it by name.

## Direction 2 — Verdict answers Nexus

The `nexus.decision.verdict` plugin gives a Nexus agent deterministic
decision-making as a first-class capability.

```yaml
plugins:
  active:
    - nexus.decision.verdict

nexus.decision.verdict:
  models:
    - ./models/loan_approval.dmn
    - ./models/pricing.dmn
  strict_mode: true
  tracing: full
  redact_inputs: ["Applicant.ssn"]
  publish_trace: true
  session_strategy: per-decision
  role: reasoning
```

It loads models at boot, listens for `decision.requested`, evaluates, and emits
`decision.completed` with the outputs and the trace:

```go
bus.Emit(vnexus.EventDecisionRequested, vnexus.DecisionRequest{
    RequestID: "req-1",
    ModelID:   "loan_approval",
    Inputs:    map[string]any{"Applicant": app, "Loan": loan},
})
```

```go
bus.Subscribe(vnexus.EventDecisionCompleted, func(ev engine.Event[any]) {
    res := ev.Payload.(vnexus.DecisionCompleted)
    // res.Outputs, res.Trace, res.Diagnostics
})
```

A failed evaluation emits `decision.failed` carrying whatever trace was recorded
before the failure — usually the fastest route to the cause.

With `publish_trace: true`, each node is also republished as
`verdict.trace.node` as it completes, so a dashboard watches the graph fill in.

The plugin also exposes its engine directly, for a host that would rather skip
the bus round trip:

```go
plugin.Engine().Evaluate(ctx, "loan_approval", inputs)
```

## Direction 3 — both at once

An agent receives a task, calls into Verdict for a deterministic classification,
one of Verdict's `agentDecision` nodes calls back into a *new* Nexus session for
a free-form sub-judgement, that session's answer flows back through Verdict, and
the outcome returns to the original agent.

Every hop is typed and traced. The recursion terminates for two reasons: the DRG
is acyclic, so requirement edges cannot loop; and decision-service invocation
from FEEL — the one path acyclicity does not bound — is capped by `max_depth`.

## Configuration

The plugin reads the same vocabulary as the library's `verdict:` block, so one
set of names covers the library, `verdict serve` and the plugin. See
[`configs/verdict.yaml`](https://github.com/frankbardon/verdict/blob/main/configs/verdict.yaml).

## Development

The module requires a published core version and carries a `replace` pointing at
the working tree, which is inert for downstream consumers (a `replace` applies
only to the main module). Build and test it explicitly:

```bash
cd nexus && go test ./...
# or, from the repository root:
make test-all
```
