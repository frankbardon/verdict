# Verdict

Verdict is a **DMN 1.5 decision engine for Go**. It loads a decision model,
tells you what is wrong with it before you deploy it, evaluates it, and hands
back a trace of exactly what it did and why.

It also does one thing DMN does not: a decision can be delegated to an **agent**
— an LLM behind a typed, bounded, traced boundary — without the rest of the
graph losing any of its determinism.

```
model.dmn ──▶ load ──▶ analyse ──▶ evaluate ──▶ outputs
                 │         │           │
                 ▼         ▼           ▼
            diagnostics  gaps &      trace
                        overlaps
```

## What is here

| If you want to… | Read |
|---|---|
| Install it and run a model | [Installation and a First Model](getting-started/index.md) |
| Drive it from a shell | [The Command Line](cli/index.md) |
| Write a decision table that holds up | [Decision Tables](concepts/decision-tables.md) |
| Put a model inside a decision safely | [Agent Decisions](concepts/agent-decisions.md) |
| Audit what an evaluation did | [Traces](concepts/traces.md) |
| Open a Verdict model in Camunda Modeler | [DMN XML and Interoperability](formats/dmn-xml.md) |
| Author or generate models as JSON | [Verdict Decision JSON](formats/vdj.md) |
| Validate a model file in CI or an editor | [The VDJ JSON Schema](formats/schema.md) |
| Run it as a service | [Deployment](server/deployment.md) |
| Let an agent drive it | [MCP Tools and Resources](server/mcp.md) |
| Wire it into Nexus | [Nexus Integration](nexus.md) |
| Look up an error code | [Diagnostic Codes](reference/diagnostics.md) |

## Three commitments

**DMN is the spec, not the inspiration.** The graph model, the boxed
expressions, the hit policies and FEEL come from DMN 1.5. Verdict does not
invent a name for something DMN already names, and does not quietly improve a
behaviour the spec fixes. Where it deviates, the deviation is documented.

**The trace is output, not logging.** Every evaluation produces a record of
which nodes fired, which rules matched, and what an agent was asked. Its JSON
shape is a stable contract that dashboards and auditors read — not a debug aid
that might change shape next release.

**Interoperability is verified, not asserted.** Every shipped model is validated
against the OMG DMN 1.3 XSD by `make validate`, and every VDJ document Verdict
writes is validated against the [published JSON Schema](formats/schema.md) by
the test suite. "It parses in our own reader" is not a claim about anyone
else's tool.

## The machine-readable contract

The VDJ JSON Schema is published at its own `$id`:

<https://frankbardon.github.io/verdict/vdj-schema.json>

It is generated from the same Go types the reader decodes into and the same
vocabulary registry the engine evaluates against, so it cannot drift from the
engine that serves it. `verdict schema` prints the identical bytes offline.
