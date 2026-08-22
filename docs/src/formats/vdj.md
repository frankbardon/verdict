# Verdict Decision JSON

VDJ is a **lossless JSON projection of a DMN model**, for tooling that would
rather not touch XML. Every construct maps one-to-one onto the model the
evaluator sees, and a document round-trips through DMN XML and back unchanged.

It is a projection, not a second source of truth. There is no behaviour VDJ can
express that DMN cannot, and nothing Verdict evaluates differently because a
model arrived as JSON. The reader produces the same in-memory model either way,
and the same diagnostics.

```bash
verdict convert model.dmn --to json > model.vdj   # XML  → VDJ
verdict convert model.vdj --to xml  > model.dmn   # VDJ  → XML
```

Use VDJ when a model is **generated** — by an agent, a rules editor, a config
pipeline. Use DMN XML when a model is **edited by a person in a modeller**. Both
load; `verdict eval` does not care which you hand it.

## A whole model

```json
{
  "vdj": "1.0",
  "id": "pricing",
  "name": "Pricing",
  "namespace": "https://example.com/pricing",
  "conformance_level": "feel",

  "input_data": [
    { "name": "Order", "variable": { "name": "Order", "type_ref": "Order" } }
  ],

  "item_definitions": [
    { "name": "Order", "components": [
        { "name": "total",  "type_ref": "number" },
        { "name": "tier",   "type_ref": "string" }
    ] }
  ],

  "decisions": [
    {
      "id": "discount",
      "name": "Discount",
      "question": "What discount applies to this order?",
      "required_inputs": ["Order"],
      "variable": { "name": "Discount", "type_ref": "number" },
      "logic": {
        "kind": "decisionTable",
        "hit_policy": "UNIQUE",
        "inputs":  [{ "label": "Tier",  "expression": "Order.tier",  "type_ref": "string" },
                    { "label": "Total", "expression": "Order.total", "type_ref": "number" }],
        "outputs": [{ "name": "discount", "type_ref": "number", "default_value": "0" }],
        "rules": [
          { "when": ["\"gold\"",   ">= 1000"], "then": ["0.15"] },
          { "when": ["\"gold\"",   "< 1000"],  "then": ["0.10"] },
          { "when": ["\"silver\"", "-"],       "then": ["0.05"] }
        ]
      }
    }
  ]
}
```

Everything except `vdj` and `id` is optional. Omitted collections are absent
rather than empty, and an element without an `id` takes its `name` as one.

## The document

| Field | Meaning |
|---|---|
| `vdj` | Format version. Currently `"1.0"` |
| `id` | Stable identifier for the model |
| `name`, `description` | Human labels |
| `namespace` | The DMN namespace the elements belong to |
| `version` | The *model's* version — yours, not the format's |
| `conformance_level` | `"s-feel"` or `"feel"`. Omit to take the engine default |
| `expression_language` | Default language URI for literal expressions |
| `exporter`, `exporter_version` | What wrote the file |

Then the element collections: `item_definitions`, `input_data`, `decisions`,
`business_knowledge_models`, `knowledge_sources`, `decision_services`.

Requirements are named, not nested: a decision lists `required_inputs`,
`required_decisions`, `required_knowledge` and `authority_requirements` as
arrays of IDs. The graph is reconstructed from those names at load, and a name
that matches nothing is `VERDICT_LOAD_004`, not a silent orphan.

## Boxed expressions

A decision's `logic` is a discriminated union on `kind`. The payload fields for
each kind:

| `kind` | Fields |
|---|---|
| `literalExpression` | `text`, `expression_language` |
| `decisionTable` | `hit_policy`, `aggregation`, `orientation`, `inputs`, `outputs`, `rules`, `annotations` |
| `invocation` | `called`, `bindings` |
| `context` | `entries` |
| `list` | `elements` |
| `relation` | `columns`, `rows` |
| `functionDefinition` | `parameters`, `body`, `function_kind` |
| `agentDecision` | `agent` |
| `unknown` | `detail` |

Every kind may also carry `id` and `type_ref`. Carrying another kind's field is
an error — see [the JSON Schema](schema.md), which is the only thing that
catches it; the reader ignores it.

`unknown` is what a document round-tripped through Verdict carries when the
source contained logic Verdict could not interpret. It evaluates to null, is
reported as `VERDICT_LOAD_001`, and survives the round trip so converting a
model does not delete the part you were about to fix.

## Decision tables

```json
{
  "kind": "decisionTable",
  "hit_policy": "COLLECT",
  "aggregation": "SUM",
  "inputs":  [{ "label": "Amount", "expression": "Claim.amount", "values": "[0..1000000]" }],
  "outputs": [{ "name": "fee", "type_ref": "number", "default_value": "0" }],
  "annotations": ["Reason"],
  "rules": [
    { "when": ["> 500"], "then": ["25"], "annotations": ["large claim surcharge"] }
  ]
}
```

- `hit_policy` accepts the long DMN name (`COLLECT`) or the single-cell
  shorthand (`C+`). A shorthand that names an aggregation sets `aggregation`
  too; an explicit `aggregation` field wins over whatever the shorthand said.
- `when` holds one unary test per input clause, positionally aligned. `-` (or
  an empty string) matches anything.
- `then` holds one FEEL expression per output clause. A count that does not
  match the clause count is `VERDICT_LOAD_011`.
- `annotations` on the table are the column headers; `annotations` on a rule are
  its cells, aligned to them.
- A **single-output table returns the value, not a record** (DMN 1.5 §8.3).
  Two outputs return a record keyed by `name`, which is why `name` is required
  as soon as there is more than one.

`values` on an input clause and on an output clause carry very different weight:

- Input `values` bound the input's domain, and the analyser uses them to decide
  what counts as a gap. Without them, a `string` input has an infinite domain
  and no gap can be proven.
- Output `values` are the permitted results — and under `PRIORITY` and
  `OUTPUT ORDER` they are also **the ranking, most preferred first**. Reversing
  that list silently reverses the decision.

See [Decision Tables](../concepts/decision-tables.md) for the semantics behind
all of this.

## Agent decisions

```json
{
  "kind": "agentDecision",
  "agent": {
    "prompt_template": "Classify the risk of this applicant.\n\nNotes: {{.notes}}\nScore: {{.score}}",
    "input_bindings": [
      { "name": "notes", "feel": "Applicant.notes" },
      { "name": "score", "feel": "Applicant.credit_score" }
    ],
    "output_type": { "type_ref": "string", "enumeration": ["low", "medium", "high"] },
    "validator": "value in [\"low\", \"medium\", \"high\"]",
    "policy": {
      "max_latency": "PT2S",
      "max_retries": 1,
      "on_failure": "fallback",
      "fallback_decision": "conservative_risk_tier"
    }
  }
}
```

`input_bindings` is the encapsulation boundary: the agent sees the values those
bindings produce and nothing else — never the model context. `max_latency` is an
ISO-8601 duration and bounds the whole invocation including retries.
[Agent Decisions](../concepts/agent-decisions.md) covers the contract in full.

## Reading a document Verdict wrote

The writer emits every field it has and omits every field it does not, indents
with two spaces, and orders keys as the struct declares them. It does not sort
keys or normalise whitespace inside FEEL text — a `when` entry comes back out
exactly as it went in, because the source text of a rule is what a person reads
in a review.

## Tolerance

The reader accepts a document containing fields it does not recognise: it
reports `VERDICT_LOAD_001` and loads everything else, on the grounds that a
newer VDJ version is a likelier explanation than a typo. A UTF-8 byte-order mark
is stripped rather than choked on.

If you want the strict reading — where a mistyped key is an error — validate
against [the JSON Schema](schema.md) first. That is the division of labour: the
reader is forgiving so a model written for a newer Verdict still runs, and the
schema is strict so a model written wrongly is caught before it does.
