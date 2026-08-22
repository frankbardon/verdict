---
name: verdict
description: Author, evaluate and debug DMN decision models with the Verdict engine. Use when the task involves decision tables, DMN, FEEL expressions, business rules that must be auditable, or wiring an LLM judgement into an otherwise deterministic decision — including "why did this decision come out this way", reading a Verdict trace, or deciding what belongs in a rule versus a prompt.
---

# Verdict

Verdict is a DMN 1.5 decision engine for Go. It evaluates decision models —
decision tables, FEEL expressions, reusable functions — and returns an
**auditable trace** of how it reached its answer. One node kind, `agentDecision`,
delegates to an LLM; everything else is deterministic.

The point of the design: **rules where rules belong, an LLM where an LLM
belongs, with an explicit typed boundary between them.**

## When to reach for this

Use a decision model when the answer must be the same for the same inputs,
explainable line by line, and reviewable by someone who does not read Go:
eligibility, pricing, routing, tiering, limits, policy application.

Do *not* model something as rules when the input is unstructured language and
the judgement is genuinely contextual. Model the *structure* as rules and
delegate the *judgement* to an `agentDecision` — with a declared output type, a
validator and a fallback, so the graph still works when the model is wrong or
unavailable.

## Decide first: rule or prompt?

| Signal | Put it in a rule | Put it in an agentDecision |
|---|---|---|
| The answer must be identical on re-run | Yes | No |
| Someone must sign off on the logic | Yes | No |
| The input is a number, code, date or enum | Yes | — |
| The input is free text needing interpretation | No | Yes |
| The rule would need dozens of cases to cover context | — | Yes |
| Being wrong is expensive and unrecoverable | Yes | Only with a fallback |

A good hybrid model is mostly tables. If more than one or two nodes are agent
decisions, the boundary is probably in the wrong place.

## Evaluating a model

```bash
verdict eval model.dmn -d '{"Applicant":{"credit_score":780}}'
verdict eval model.dmn --decision credit_rating -i inputs.json --trace
verdict eval model.dmn --service FastTrackDecisionService -i inputs.json
```

From Go:

```go
engine, err := verdict.NewEngine(verdict.WithAgentBridge(bridge))
model, err := engine.LoadModel(verdict.FromFile("loan_approval.dmn"))
res, err := engine.Evaluate(ctx, model.ID, verdict.Inputs{"Applicant": app})
// res.Outputs, res.Trace, res.Diagnostics
```

## Understanding a model you did not write

```bash
verdict explain model.dmn                  # index: decisions, services, which are outputs
verdict explain model.dmn credit_rating    # one decision, in full
verdict analyze model.dmn                  # gaps, overlaps, unreachable rules
```

`explain` on a decision prints the inputs a caller must supply, the decisions it
builds on, and its full logic — every rule of a table, or the bindings, output
type and failure policy of an agent decision. Run it before writing inputs;
guessing input names is the most common way to get a null answer.

## Reading a trace

```bash
verdict eval model.dmn -i inputs.json --trace --quiet | verdict trace --values
```

```
loan_approval  [evaluation]  630µs
├── Credit Rating  [decisionTable]  143µs
│       hit_policy: "U"
│       matched_rules: ["credit_rating_r1"]
│       output: "excellent"
├── Risk Tier  [agentDecision]  106µs
│       agent_session_ref: "nexus://sessions/abc/verdict/risk_tier"
│       agent_attempts: 1
└── Routing  [decisionTable]  25µs
        matched_rules: ["routing_r3"]
        output: "auto-approve"
```

When an answer is wrong, the trace tells you *which node* is wrong before you
read any rules. `matched_rules` is usually the whole story: a rule you did not
expect fired, or none did and the table defaulted.

## Authoring: decision tables

A decision table has **input clauses** (expressions evaluated once per
evaluation), **output clauses**, and **rules**. Each rule's input entries are
*unary tests* against the corresponding input value; `-` means "any".

```
  RULE  Credit score   → credit rating
  r1    >= 740         "excellent"
  r2    [670 .. 740)   "good"
  r3    [580 .. 670)   "fair"
  r4    < 580          "poor"
```

Unary test forms: `< 10`, `>= 740`, `[1..10]` (closed), `[1..10)` (open at the
top), `"a", "b"` (any of), `not("a")`, `-` (any). A bare value is an equality
test.

### Hit policies

Pick the policy that says what you mean; do not encode it in the rules.

| Policy | Meaning | Use when |
|---|---|---|
| `U` UNIQUE | Exactly one rule may match; overlap is a runtime error | The rules partition the input space. **The default, and the right default** |
| `A` ANY | Several may match but must agree | Rules overlap by construction and all agree |
| `P` PRIORITY | Most-preferred output wins, ranked by `outputValues` | Precedence is about *outcomes*, not row order |
| `F` FIRST | First matching rule in row order | Precedence is genuinely positional, e.g. a catch-all last row |
| `C` COLLECT | List of every match | Collecting reasons, flags, applicable rules |
| `C+` `C<` `C>` `C#` | Collect and sum / min / max / count | Stacking discounts, worst-case limits, counting hits |
| `R` RULE ORDER | Every match, in row order | The order rules were written is meaningful |
| `O` OUTPUT ORDER | Every match, ranked by `outputValues` | A ranked list of outcomes |

`PRIORITY` and `OUTPUT ORDER` **require** `outputValues` on an output clause: it
is the priority order, most-preferred first. Getting this order backwards is the
single most common DMN modelling bug — it silently produces the *least*
preferred answer.

Under `FIRST`, a catch-all last row (`-` in every input) closes the table. Under
any other single-hit policy, use `defaultOutputEntry` instead.

### Gaps and overlaps

`verdict analyze` probes every table's input space and reports:

- **gaps** — input combinations no rule covers. Fine if the table has a
  `defaultOutputEntry` or a collecting policy; a bug otherwise.
- **overlaps** — combinations several rules match. An *error* under `U`, and
  under `A` when the rules disagree. Informational elsewhere, since the policy
  resolves them.
- **unreachable rules** — under `FIRST`, a rule an earlier row already covers.

Run it before shipping. `--strict` (or `WithStrictMode(true)`) makes the engine
refuse to load a model with error-severity findings.

## Authoring: FEEL

FEEL is the expression language for every cell, literal expression and binding.

```feel
Applicant.credit_score >= 740                     // comparison
(Repayment + Applicant.existing_debt) / income    // arithmetic, decimal not integer
if x then "a" else "b"                            // conditional
value in ["low", "medium", "high"]                // membership
decimal(amount * 1.2, 2)                          // round to 2 places
min([a, b])   max([a, b])   sum([1, 2, 3])        // list functions
date("2024-03-01")   date and time("2024-03-01T09:00:00")
[1..10]  [1..10)  [1..10[  (1..10]  ]1..10]           // both bracket spellings
substring before(s, "@")   matches(s, "^ab")   split(s, ",")
```

Names may contain spaces (`Credit Rating`) but **not reserved words** — `and`,
`or`, `in`, `if`, `then`, `else`, `for`, `some`, `every`, `return`, `satisfies`.
`Rent and Rates` will not resolve; rename it.

Two dialects, selected per model with `conformanceLevel`:

- `s-feel` (Conformance Level 2) — literals, arithmetic, comparisons, ranges,
  lists, `and`/`or`, qualified names. Rejects `if`, `for`, `some`, `every` and
  inline functions **at parse time**. Use it for decision-table-only models that
  must interoperate with Level 2 tooling.
- `feel` (Conformance Level 3, the default) — the full language.

## Authoring: agent decisions

```xml
<!-- Inside the decision's <extensionElements>, before <question>. The
     decision-logic slot accepts only DMN's own expression elements, so an
     agent decision placed there fails schema validation. -->
<verdict:agentDecision id="risk_tier_agent" typeRef="tRiskTier">
  <verdict:promptTemplate>
    Classify this application's risk tier as one of: low, medium, high.

    Credit rating: {{ .creditRating }}
    Affordability: {{ .affordability }}
    Notes: {{ .notes }}
  </verdict:promptTemplate>

  <verdict:inputBinding name="creditRating"  feel="Credit Rating"/>
  <verdict:inputBinding name="affordability" feel="Affordability"/>
  <verdict:inputBinding name="notes"         feel="Applicant.notes"/>

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

Five rules that make this safe:

1. **The agent sees only its bindings.** Never the model context. Bind the
   minimum — an agent that can see the applicant's ID can key on it.
2. **The output type is declared and enforced.** An enumeration becomes a
   provider-side schema *and* a post-hoc check. A non-conforming answer is a
   failure, not a value.
3. **The validator is the escape hatch** for anything the type cannot express.
   It runs with the coerced answer bound to `value`.
4. **`onFailure` is a policy, not code**: `error` (propagate, the default),
   `null` (bind null and continue), `fallback` (evaluate a sibling decision).
   Choose the one whose failure mode you can live with — for a moderation
   model, `null` plus a catch-all "review" rule means an outage sends posts to a
   human, never publishes them.
5. **The fallback is a real decision**, not a stub. Write the heuristic you
   would have shipped without an LLM.

`maxLatency` and `maxRetries` are enforced by the engine, not the bridge, so
every bridge behaves identically.

## Interoperability

Verdict reads DMN 1.3, 1.4 and 1.5, and **writes 1.3 by default** — Camunda
Modeler and dmn-js only read 1.3, and a model no editor opens is not
interoperable whatever its version number says.

Three rules when hand-authoring a model that must load in an editor:

1. **Verdict's own attributes are namespaced.** `verdict:version` and
   `verdict:conformanceLevel`, never bare — DMN's `tDefinitions` declares
   neither and only admits `##other`-namespaced foreign attributes.
2. **`agentDecision` goes in `<extensionElements>`**, positioned right after
   `<description>` and before `<question>`.
3. **Element order is fixed by the schema.** `description` comes first in every
   element; in a decision the order is description, extensionElements, question,
   allowedAnswers, variable, then the requirements.

`verdict convert model.vdj --to xml` writes all of this correctly, including
auto-laid-out diagram interchange — a model without DMNDI is schema-valid but
opens as an empty canvas.

## Traps

- **Single-output tables return the value, not a record.** A table with one
  output clause binds `"excellent"`; two or more bind `{credit rating: ..., fee: ...}`.
- **`PRIORITY` ranks by `outputValues` order**, not row order. First in the list
  wins.
- **A COLLECT SUM table that matches nothing** yields null, not zero. Give it
  `<defaultOutputEntry>0</defaultOutputEntry>` unless null is what you mean.
- **`/` is decimal division.** `1 / 3` is `0.333…`, not `0`. Dividing by zero is
  null, not an error.
- **Input names come from the input-data variable**, not the file. `verdict
  explain` lists them.
- **A decision service returns only its output decisions.** Encapsulated
  decisions run but do not appear in the result.
- **An empty cell means `-`** (any), not "equals empty string".
- **`itemDefinition` is an `xsd:choice`.** A type is *either* a constrained
  simple type (`typeRef` + `allowedValues`) *or* a structure (`itemComponent`)
  *or* a function signature — never a combination.

## Diagnostic codes

Codes are stable and greppable. `VERDICT_LOAD_*` are parse and preparation
problems, `VERDICT_ANALYZE_*` are static-analysis findings, `VERDICT_EVAL_*` are
runtime. The ones you will actually meet:

| Code | Meaning |
|---|---|
| `VERDICT_ANALYZE_001` | Table gap with no default output |
| `VERDICT_ANALYZE_002` | Overlapping rules (an error under `U`, or under `A` when they disagree) |
| `VERDICT_ANALYZE_004` | Unreachable rule under `FIRST` |
| `VERDICT_EVAL_001` | No rule matched and there is no default |
| `VERDICT_EVAL_002` | More than one rule matched under `UNIQUE` |
| `VERDICT_EVAL_003` | `ANY` rules disagreed |
| `VERDICT_EVAL_004` | An input was not supplied; it evaluated as null |
| `VERDICT_EVAL_007/8/9` | Agent failed / timed out / was rejected by its validator |
| `VERDICT_EVAL_010` | A fallback decision was used |

## MCP tools

When Verdict is served over MCP — `verdict mcp` on stdio, or `verdict serve` at
`/mcp` over HTTP; the surface is identical — these are available:

- `verdict_list_models` — what is loaded, and what inputs each model needs.
  **Start here.**
- `verdict_explain` — a decision's inputs, dependencies and full logic.
- `verdict_evaluate` / `verdict_evaluate_decision` — evaluate, with a per-decision
  summary of which rules fired.
- `verdict_analyze` — gaps and overlaps.

The right order for an unfamiliar model is always: list → explain → evaluate.
Evaluating first and guessing at input names produces nulls that look like
answers.

Two resources are served alongside the tools:

- `verdict://schema` — the JSON Schema (draft 2020-12) for a Verdict Decision
  JSON model file. **Read it before writing a model.** It is the same document
  published at <https://frankbardon.github.io/verdict/vdj-schema.json> and
  printed by `verdict schema`, and it is generated from the engine's own types
  and vocabulary, so it is never out of date with the engine serving it.
- `verdict://skill` — this document.

The schema is strict where the reader is forgiving: it closes every object, so a
mistyped key fails validation rather than being silently ignored, and it
discriminates the boxed-expression union on `kind`, so a `decisionTable`
carrying a `list`'s `elements` is an error. Validate a model you generated
against it before handing it over — `verdict schema --validate model.vdj` needs
no other tooling and exits 2 if the document does not conform.
