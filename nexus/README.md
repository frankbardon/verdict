# verdict/nexus

The Nexus integration for [Verdict](https://github.com/frankbardon/verdict),
shipped as a **separate Go module** so that importing the decision engine never
drags in Nexus.

```
github.com/frankbardon/verdict         ← the engine. No Nexus anywhere in it.
github.com/frankbardon/verdict/nexus   ← this module. Imports both.
```

That separation is the whole point of the directory: it is enforced by the
module boundary rather than by convention, and `go build ./...` at the
repository root does not build this package at all.

## What it provides

**A bridge** — Nexus answers Verdict. An `agentDecision` node in a decision
graph becomes a real agent run: a session, a workspace, tool access, an
event-bus record and a transcript. The session reference lands in the decision
trace, so the conversation that produced a value stays findable.

**A plugin** — Verdict answers Nexus. `nexus.decision.verdict` loads decision
models at boot, listens for `decision.requested` on the bus, and emits
`decision.completed` with outputs and trace. A Nexus agent gets deterministic
decision-making without leaving the process, and without being trusted to apply
the rules itself.

## Install

```bash
go get github.com/frankbardon/verdict/nexus
```

## Bridge

```go
bridge, err := vnexus.NewBridge(
    vnexus.WithBus(ctx.Bus),
    vnexus.WithSession(ctx.Session),
    vnexus.WithSessionStrategy(vnexus.PerDecision),
)

engine, err := verdict.NewEngine(verdict.WithAgentBridge(bridge))
```

The default runner puts one structured-output request to the engine's LLM
plugin, with the decision's declared output type projected into a provider-side
JSON Schema. Swap in `DelegateRunner` to make each question a full sub-agent run
under a posture instead — same node, same declared type, same failure policy,
different sophistication.

## Plugin

```yaml
plugins:
  active:
    - nexus.decision.verdict

nexus.decision.verdict:
  models:
    - ./models/loan_approval.dmn
  strict_mode: true
  publish_trace: true
  session_strategy: per-decision
  role: reasoning
```

```go
bus.Emit(vnexus.EventDecisionRequested, vnexus.DecisionRequest{
    ModelID: "loan_approval",
    Inputs:  map[string]any{"Applicant": app, "Loan": loan},
})
```

## Events

| Event | Direction | Payload |
|---|---|---|
| `decision.requested` | in | `DecisionRequest` |
| `decision.completed` | out | `DecisionCompleted` — outputs, trace, diagnostics |
| `decision.failed` | out | `DecisionFailed` — the error plus whatever trace was recorded |
| `verdict.decision.requested` | out | `AgentDecisionRequested` — an agent node is about to ask |
| `verdict.decision.completed` | out | `AgentDecisionCompleted` — and what it answered |
| `verdict.trace.node` | out | `TraceNode` — one node, as it completes |

## Development

This module requires a published core version and carries a `replace` pointing
at the working tree. A `replace` applies only to the main module, so it is inert
for downstream consumers while making local development work.

```bash
go test ./...              # from this directory
make test-all              # from the repository root
```

Full documentation: [docs/nexus.md](../docs/nexus.md).
