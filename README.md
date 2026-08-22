# Verdict

**A DMN-aligned decision engine for Go, with first-class agent integration.**

Verdict takes the OMG [Decision Model and Notation](https://www.omg.org/dmn/)
standard seriously — its Decision Requirements Graph, its FEEL expression
language, its decision-table hit policies, its conformance levels — and extends
it with exactly one new evaluator type: an **agent decision**, so a portion of
any decision graph can be delegated to an LLM while the rest stays
deterministic.

```
verdict eval loan_approval.dmn -i application.json --trace | verdict trace
```

```
loan_approval — loan_approval
2025-03-01T09:14:22Z  630µs

loan_approval  [evaluation]  630µs
├── Repayment  [invocation]  279µs
│   └── Monthly Repayment  [invocation]  176µs
├── Credit Rating  [decisionTable]  143µs
│       hit_policy: "U"
│       matched_rules: ["credit_rating_r1"]
├── Affordability  [decisionTable]  20µs
│       matched_rules: ["affordability_r1"]
├── Risk Tier  [agentDecision]  106µs
│       agent_session_ref: "nexus://sessions/9f2c/verdict/risk_tier"
│       agent_attempts: 1
└── Routing  [decisionTable]  25µs
        matched_rules: ["routing_r3"]
```

## Why

Business rules and language models fail in opposite directions. Rules are exact
and brittle; models are flexible and unrepeatable. Most systems that need both
end up with the boundary between them scattered across application code, where
nobody can see it and nothing enforces it.

Verdict makes that boundary a **type in a graph**. An agent decision declares
what it is asked, what it may see, what shape its answer must take, and what
happens when it fails — in the model, not in the code around it. Everything else
in the graph is a decision table or a FEEL expression, and produces the same
answer every time.

The result is a system where "why did this come out this way?" has an answer
you can read.

## Design

- **DMN is the spec, not the inspiration.** The graph model, the boxed
  expressions, the hit policies (U, A, P, F, C, C+, C&lt;, C&gt;, C#, R, O) and
  FEEL are taken from DMN 1.5. Nothing DMN already names is renamed.
- **Interoperability is non-negotiable.** Models authored in Camunda Modeler,
  Trisotech or dmn-js load and run — and models Verdict writes open in them.
  DMN 1.3, 1.4 and 1.5 XML are all read; **1.3 is written by default**, because
  that is what the mainstream editors actually accept. Output is validated
  against the DMN XSD in CI, diagram interchange is generated so a model opens
  as a diagram rather than an empty canvas, and a JSON projection (VDJ) is
  available for tooling that prefers it.
- **The LLM is a node kind, not the engine.** The evaluator does not know which
  kind it is calling: it supplies inputs, awaits a value, binds the result.
- **Determinism by default, explainability always.** Every evaluation produces a
  full trace — which nodes fired, which rules matched, what an agent was asked
  and answered. The trace is output, not debug logging.
- **Library first, server optional.** `pkg/verdict` is the source of truth.
  `verdict serve` wraps it in Twirp and MCP. Nothing in the library depends on the
  server.

## Install

```bash
go get github.com/frankbardon/verdict
go install github.com/frankbardon/verdict/cmd/verdict@latest
```

## Use it as a library

```go
engine, err := verdict.NewEngine(
    verdict.WithAgentBridge(bridge),      // optional: only for agentDecision nodes
    verdict.WithStrictMode(true),         // refuse models with gaps or overlaps
)

model, err := engine.LoadModel(verdict.FromFile("loan_approval.dmn"))

res, err := engine.Evaluate(ctx, model.ID, verdict.Inputs{
    "Applicant": applicant,
    "Loan":      loan,
})

res.Outputs["Routing"]   // "auto-approve"
res.Trace                // the full execution record
res.Diagnostics          // warnings, ambiguity reports, fallbacks taken
```

Engines are safe to share across goroutines and hold a registry of models, so a
service loads its models once at startup and evaluates concurrently.

## Use it from the command line

```bash
verdict explain model.dmn                    # what does this model decide?
verdict explain model.dmn credit_rating      # one decision, in full
verdict analyze model.dmn                    # gaps, overlaps, unreachable rules
verdict eval model.dmn -i inputs.json        # evaluate
verdict eval model.dmn --trace | verdict trace --values
verdict convert model.dmn --to json          # DMN XML ⇄ Verdict Decision JSON
verdict schema --validate model.vdj          # check a model against the JSON Schema
verdict serve -m ./models                    # Twirp + MCP over HTTP
verdict mcp -m ./models                      # MCP over stdio, for an MCP client
```

`verdict analyze` exits non-zero on an error-severity finding, so it works as a
CI gate on a decision model the same way a linter works on code.

## Static analysis

Verdict probes every decision table's input space at load time and reports:

- **gaps** — input combinations no rule covers
- **overlaps** — combinations several rules match, flagged as errors under
  `UNIQUE` and under `ANY` when the rules disagree
- **unreachable rules** — under `FIRST`, rows an earlier row already covers

```
DECISION         POLICY  RULES  GAPS  OVERLAPS  UNREACHABLE
Base Rate        U       4      0     0         0
Volume Tier      U       4      1     0         0
Discount Points  C+      6      1     98        0
Discount Cap     P       3      0     2         0
```

The analysis runs the *same* unary tests through the *same* evaluator that
evaluation uses, so it cannot drift from what the engine will actually do.

## Agent decisions

```xml
<verdict:agentDecision id="risk_tier_agent" typeRef="tRiskTier">
  <verdict:promptTemplate>
    Classify this application's risk tier as one of: low, medium, high.
    Credit rating: {{ .creditRating }}
    Notes: {{ .notes }}
  </verdict:promptTemplate>

  <verdict:inputBinding name="creditRating" feel="Credit Rating"/>
  <verdict:inputBinding name="notes"        feel="Applicant.notes"/>

  <verdict:outputType typeRef="string">
    <verdict:enumeration>
      <verdict:value>low</verdict:value>
      <verdict:value>medium</verdict:value>
      <verdict:value>high</verdict:value>
    </verdict:enumeration>
  </verdict:outputType>

  <verdict:validator feel='value in ["low", "medium", "high"]'/>
  <verdict:policy maxLatency="PT5S" maxRetries="2"
                  onFailure="fallback" fallbackDecision="risk_tier_heuristic"/>
</verdict:agentDecision>
```

It lives inside the decision's `<extensionElements>`, which is the one place
DMN's schema admits a foreign element — so a model with an agent node still
validates, still opens in Camunda Modeler, and still round-trips through it.

The contract:

1. **The agent sees only its bindings**, FEEL-evaluated — never the model
   context. The same encapsulation DMN applies to a business knowledge model.
2. **The output type is declared and enforced.** An enumeration becomes a
   provider-side schema *and* a check on the way back. A non-conforming answer
   is a failure, not a value.
3. **Failure is a typed policy**: `error`, `null` or `fallback` to a sibling
   decision. Graphs with agents in them stay deployable.
4. **Latency and retry are enforced by the engine**, not the bridge, so every
   bridge gets identical guarantees.
5. **The agent does not know it is in a graph.** It gets a prompt and inputs,
   and returns a value. That keeps the bridge thin — and swappable.

`AgentBridge` is a one-method interface. A deterministic mock bridge and a
generic HTTP/JSON bridge ship in the box.

## Nexus integration

Verdict is the deterministic counterpart to [Nexus](https://github.com/frankbardon/nexus).
Nexus reasons in natural language and emits events; Verdict evaluates structured
decisions and returns auditable outcomes.

The integration is a **separate Go module** — `github.com/frankbardon/verdict/nexus` —
so importing the engine can never drag in Nexus, its provider SDKs or its plugin
surface. A system that wants both imports both, explicitly.

It supplies both directions:

- **Bridge**: Nexus answers Verdict. An `agentDecision` becomes a real agent
  run — workspace, tools, event bus, journal — and the trace records the session
  reference, so the whole conversation stays available after the fact.
- **Plugin**: Verdict answers Nexus. `nexus.decision.verdict` loads models at
  boot, listens for `decision.requested`, and emits `decision.completed` with
  outputs and trace. A Nexus agent gets deterministic decision-making without
  leaving the process.

Wired together, an agent can ask Verdict for a classification, one of Verdict's
nodes can ask a fresh Nexus session for a judgement, and the answer flows back —
with the recursion bounded by the graph's acyclic structure.

```go
bridge, _ := vnexus.NewBridge(
    vnexus.WithBus(ctx.Bus),
    vnexus.WithSession(ctx.Session),
    vnexus.WithSessionStrategy(vnexus.PerDecision),
)
engine, _ := verdict.NewEngine(verdict.WithAgentBridge(bridge))
```

## Server

```bash
verdict serve -m ./models --listen :7430
```

- **Twirp** at `/twirp/verdict.v1.Engine/*` — `Evaluate`, `EvaluateDecision`,
  `EvaluateService`, `LoadModel`, `ListModels`, `GetTrace`, `Analyze`, `Explain`.
- **MCP** at `/mcp` — `verdict_list_models`, `verdict_explain`,
  `verdict_evaluate`, `verdict_evaluate_decision`, `verdict_analyze`. Point
  Claude at it and it can evaluate a decision by name, and read the rules that
  produced the answer.

The server is a wrapper. Everything it can do, a Go program can do in-process.

## Examples

| Example | What it shows |
|---|---|
| [`examples/loan_approval`](examples/loan_approval) | The classic DMN model plus one agent-classified risk tier, with a deterministic heuristic as its fallback |
| [`examples/content_moderation`](examples/content_moderation) | Rules as the guardrail: bright-line blocks upstream of the agent, a cost gate that only consults it on ambiguity, and a fail-safe that routes to a human |
| [`examples/pricing`](examples/pricing) | Six tables, four hit policies, stacked discounts under a cap — and no agent at all, because pricing is the case where an LLM has nothing to offer |

## Conformance

| | Status |
|---|---|
| DMN XML read (1.3 / 1.4 / 1.5) | Yes |
| DMN XML write | Yes — 1.3 by default, 1.4/1.5 on request |
| Output validates against the DMN XSD | Yes, enforced by `make validate` |
| Diagram interchange (DMNDI) generated | Yes, auto-laid-out from the DRG |
| Decision tables, all hit policies | Yes |
| Literal, context, list, relation, invocation, function definition | Yes |
| Business knowledge models, decision services | Yes |
| FEEL Conformance Level 3 | Yes |
| S-FEEL Conformance Level 2 (enforced at parse) | Yes |
| Java-bound BKM | Parsed and preserved; not executed (returns null with a diagnostic) |
| PMML function reference | Parsed and preserved; not executed |

## Repository

```
pkg/verdict/     public API — Engine, Model, Result, options
pkg/dmn/         model types, DMN XML reader/writer, VDJ projection
pkg/feel/        FEEL adapter, dialect gate, standard-library supplement
pkg/eval/        evaluator: graph scheduling, tables, boxed expressions, agents
pkg/analyze/     gap, overlap and reachability analysis
pkg/agent/       AgentBridge interface, mock and HTTP bridges
pkg/trace/       trace types, recording, redaction
pkg/explain/     human-readable decision rendering
pkg/config/      YAML configuration
nexus/           separate module: Nexus bridge + plugin
cmd/verdict/     the binary: CLI, `serve` and `mcp`
server/          Twirp and MCP surfaces
skill/           embedded agent skill pack
examples/        runnable models, exercised by examples_test.go
docs/            mdBook source for the documentation site
```

## Documentation

The manual is at **<https://frankbardon.github.io/verdict/>** — model formats,
the CLI, embedding, the server surfaces and operations. `docs/` holds its
mdBook source; `make docs-serve` previews it locally.

The site also serves the machine-readable contract for a model file:

**<https://frankbardon.github.io/verdict/vdj-schema.json>**

That URL is the schema's own `$id`, so a validator that resolves the identifier
fetches the document that names it. `verdict schema` prints the identical bytes
offline, and an MCP client can read it as `verdict://schema`. The schema is
generated from the same Go types the reader decodes into and the same
vocabulary registry the engine evaluates against — adding a hit policy changes
the published contract in the same commit — and every shipped model is
validated against it by the test suite.

## Prior art

| System | Approach | Why Verdict differs |
|---|---|---|
| Drools | JVM production rules + DMN, very mature | JVM-only; heavy; no agent integration |
| GoRules / Zen | Rust core with Go bindings, JSON Decision Model | Not DMN; vendor format; closed node types |
| Grule | Drools-inspired DSL for Go | Custom DSL, no standard backing, no DMN |
| Camunda DMN | JVM DMN engine, Conformance Level 3 | JVM-only; no embeddable Go library |
| **Verdict** | DMN 1.5 + FEEL in pure Go, agent-aware | Standards-aligned, Go-native, hybrid by design |

## License

See [LICENSE](LICENSE). The FEEL evaluator under `pkg/feel/internal/dialect` is
a fork of [pbinitiative/feel](https://github.com/pbinitiative/feel) and retains
its own licence and a [NOTICE](pkg/feel/internal/dialect/NOTICE.md) recording
every divergence.
