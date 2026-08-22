# Getting started

## Install

```bash
go get github.com/frankbardon/verdict
go install github.com/frankbardon/verdict/cmd/verdict@latest
```

## A model in ten lines

Verdict reads DMN XML and Verdict Decision JSON. JSON is the shorter way to see
the shape:

```json
{
  "vdj": "1.0",
  "id": "shipping",
  "input_data": [
    { "id": "order", "name": "Order", "variable": { "name": "Order" } }
  ],
  "decisions": [
    {
      "id": "shipping_band", "name": "Shipping Band",
      "required_inputs": ["order"],
      "variable": { "name": "Shipping Band", "type_ref": "string" },
      "logic": {
        "kind": "decisionTable",
        "hit_policy": "UNIQUE",
        "inputs":  [{ "label": "Order value", "expression": "Order.value", "type_ref": "number" }],
        "outputs": [{ "name": "band", "type_ref": "string",
                      "default_value": "\"standard\"" }],
        "rules": [
          { "id": "r1", "when": ["< 25"],       "then": ["\"economy\""] },
          { "id": "r2", "when": ["[25..100)"],  "then": ["\"standard\""] },
          { "id": "r3", "when": [">= 100"],     "then": ["\"free\""] }
        ]
      }
    }
  ]
}
```

```bash
$ verdict eval shipping.vdj -d '{"Order":{"value":150}}'
{
  "duration_ms": 0,
  "outputs": { "Shipping Band": "free" }
}
```

## From Go

```go
package main

import (
    "context"
    "fmt"

    "github.com/frankbardon/verdict/pkg/verdict"
)

func main() {
    engine, err := verdict.NewEngine()
    if err != nil {
        panic(err)
    }

    model, err := engine.LoadModel(verdict.FromFile("shipping.vdj"))
    if err != nil {
        panic(err)
    }

    res, err := engine.Evaluate(context.Background(), model.ID, verdict.Inputs{
        "Order": map[string]any{"value": 150},
    })
    if err != nil {
        panic(err)
    }

    fmt.Println(res.Outputs["Shipping Band"]) // free
}
```

A `*verdict.Engine` is safe to share across goroutines and holds a registry of
loaded models, so a service loads its models once at startup.

## Check the model before you trust it

```bash
$ verdict analyze shipping.vdj
Model shipping

DECISION       POLICY  RULES  GAPS  OVERLAPS  UNREACHABLE  NOTE
Shipping Band  U       3      0     0         0

0 error(s), 0 warning(s)
```

`verdict analyze` exits non-zero on an error-severity finding, so it belongs in
CI next to your linter. `verdict.WithStrictMode(true)` makes the engine refuse
such a model at load instead.

## Read what a decision does

```bash
verdict explain shipping.vdj                 # index the model
verdict explain shipping.vdj shipping_band   # one decision, in full
```

## Read what happened

```bash
verdict eval shipping.vdj -d '{"Order":{"value":150}}' --trace --quiet \
  | verdict trace --values
```

```
shipping — shipping
shipping  [evaluation]  95µs
└── Shipping Band  [decisionTable]  61µs
        hit_policy: "U"
        matched_rules: ["r3"]
        inputs: {"Order":{"value":150}}
        output: "free"
```

## Open it in a modeller

```bash
verdict convert shipping.vdj --to xml --out shipping.dmn
```

The output is DMN 1.3 — the version Camunda Modeler and dmn-js read — with
auto-laid-out diagram interchange, so it opens as a diagram rather than an empty
canvas. Edit it there, and `verdict eval shipping.dmn` picks up the changes.

## Validate a model before you load it

If you are generating models rather than drawing them, point a validator at the
published JSON Schema:

```bash
verdict schema > vdj-schema.json
check-jsonschema --schemafile vdj-schema.json shipping.vdj
```

Or put its URL in the file and let your editor do it:

```json
{ "$schema": "https://frankbardon.github.io/verdict/vdj-schema.json", "vdj": "1.0", "…": "…" }
```

## Next

- [The command line](../cli/index.md) — every subcommand and flag
- [Decision tables](../concepts/decision-tables.md) — hit policies, gaps and overlaps
- [Agent decisions](../concepts/agent-decisions.md) — putting an LLM inside a decision graph
- [Traces](../concepts/traces.md) — the execution record and what reads it
- [Verdict Decision JSON](../formats/vdj.md) and [its JSON Schema](../formats/schema.md)
- [DMN XML and interoperability](../formats/dmn-xml.md) — opening a model in a modeller
- [Nexus integration](../nexus.md) — both directions
- [Deployment](../server/deployment.md) — `verdict serve`, Twirp and MCP
