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

Values in the trace (`inputs`, `output`, `input_values`) — and in
`Result.Outputs`, agent inputs, MCP, Twirp and CLI output — are numbers as
`float64` and every temporal and duration as its FEEL lexical string:
`"2024-03-05"`, `"10:00:00"`, `"2024-03-05T10:00:00"` (zoneless: no `Z`),
`"2024-03-05T10:00:00+02:00"`, `"...@Europe/Paris"`, `"P1DT2H"`, `"P1Y2M"`. Read
a value's kind and zone from its string; feed it back through `date()`,
`time()`, `date and time()` or `duration()`.

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
| `C+` `C<` `C>` `C#` | FEEL `sum` / `min` / `max` / `count` of the matches: null (never an error) outside the built-in's domain — `C+` over a non-number or null output, `C<`/`C>` over booleans, mixed kinds or a null. No hits: the default output, else null (`C#` too) | Stacking discounts, worst-case limits, counting hits |
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
-balance   rate ** years                          // negation, exponentiation
if x then "a" else "b"                            // conditional
value in ["low", "medium", "high"]                // membership
decimal(amount * 1.2, 2)                          // round to 2 places
modulo(-10.1, 4.5) = 3.4   sqrt(-1) = null           // numeric fns: exact decimals; null on null, non-number or out-of-domain args
sqrt(2)   log(10)   exp(1)   odd(9007199254740993)  // 34 significant digits; odd/even exact on any integer
odd(1.5) = null   even(2.5) = null                  // odd/even of a non-integer is null (interpretation; spec silent)
string(1/3) = "0.3333333333333333333333333333333333"   // numbers print at 34 significant digits, plain decimal (never 1E-40), zeros trimmed
min([a, b])   max([a, b])   sum([1, 2, 3])        // list functions
count([]) = 0   mode([]) = []   sum([]) = null   mean([]) = null   // empty list: null for sum/product/mean/median/stddev/min/max
sum([1, null]) = null   count([1, null]) = 2   sum(null) = null   // null/non-number item: arithmetic aggregates null; count counts it
median([6, 1, 2, 3]) = 2.5   stddev([2, 4, 7, 5])   mode([6, 1, 9, 6, 1]) = [1, 6]   // stddev is the sample deviation; mode ascending
date("2024-03-01")   date and time("2024-03-01T09:00:00Z")   @"P30D"
number("12.5")   string(x)   duration("P1Y2M")   // null on a malformed string
date(2026, 2, 28)   time(10, 30, 0)   time(10, 30, 0, duration("PT2H"))   // components; bad ones null
date(dt)   time(dt)   date and time(d, t)   number("1.000,5", ".", ",")   // Table 72 overloads
[1..10]  [1..10)  [1..10[  (1..10]  ]1..10]           // both bracket spellings
overlaps([1..10], [3..4]) = true   overlaps before([3..4], [1..5]) = false   // range relations: DMN Table 78 formulas
overlaps after((5..10], [1..5]) = false   finishes([1..10], [5..10]) = false   // open/closed endpoints count
starts([1..10), [1..10]) = true   finished by([1..10], (1..10]) = true   // starts/started by: same start, ends no later; finishes/finished by mirrored
substring before(s, "@")   matches(s, "^ab")   split(s, ",")
matches(s, "^\\d+$")   "a\tb"   "\u00e9"   "\uD83D\uDC0E"   // escapes \' \" \\ \n \r \t \uXXXX \UXXXXXX decode; others stay verbatim
string length("🐎ab") = 3                         // lengths and positions count characters (code points), not bytes
string(true) = "true"   string([1, "a"]) = "[1, \"a\"]"   // FEEL form; strings nested in lists/contexts quoted
context([{key: "a", value: 1}, {key: "a", value: 2}]) = null   // duplicate key; context put/merge override instead
{c: 1, r: c + 1} = {c: 1, r: 2}   {a: 2, n: {b: a + 1}}.n.b = 3   // an entry sees earlier entries of its literal; shadows outer names
context merge({a: 1})   list contains(5, 5)              // a non-list item is a singleton list
context put(Applicant, "k", 1)   Applicant = {name: "A", age: 30}   // a context input is a context to =, built-ins, string()
get entries({b: 1, a: 2}) = [{key: "a", value: 2}, {key: "b", value: 1}]   get entries(m)[1].key   // a FEEL list, entries in key order
list replace(xs, -1, null)   substring("foobar", -2, 1)   // positions from 1, negative from the end
xs[1]   xs[-1]   [1, 2, 3][4] = null   [1, 2, 3][0] = null   // index from 1, negative from the end; out of range or fractional: null
xs[item > 2]   people[age >= 18]   people[item.age >= 18]   // filter: item bound, a context's entries by name; false/null drops
people.name = ["A", "B"]   get entries(m)[key = "y"].value   // a path on a list of contexts projects; missing entry: null
list replace([2,4,6,8], function(item, newItem) item < newItem, 5) = [5,5,6,8]   // match form: replace each item match(item, newItem) is true for
all([true, null]) = null   all([false, null]) = false   any([true, null]) = true   // three-valued; all([]) true, any([]) false
all(true, false)   all([false, 123]) = null           // variadic; a non-boolean item makes the call null
1 in 1   "a" in "a"                                   // a non-list, non-range right side is `=`
```

String, list and context built-ins return **null**, never an error, outside
their domain (DMN §10.3.4): a null or wrong-typed argument, a position outside
the list or string, an invalid regex, replacement or flags, the wrong number of
arguments, an unknown parameter name. The parameters the spec says take "any
element including null" still do — `append`, `insert before` and `list replace`
items, `index of` match, the `context put` value — and `string join` skips null
elements and treats a null delimiter as none. A non-matching `substring before`
/ `after` is `""`, not null.

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
   minimum — an agent that can see the applicant's ID can key on it. The
   bound values arrive in the trace's boundary shape (temporals and durations
   as FEEL lexical strings, numbers as `float64`), and the prompt template
   prints scalars in FEEL lexical form (`1000000`, `2024-03-05`, `P1DT2H`,
   `null`) and contexts/lists as indented JSON.
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
- **A COLLECT SUM or COUNT table that matches nothing** yields null, not zero
  (DMN 1.5 §10.3.2.10 resolves no-hit before aggregating). Give it
  `<defaultOutputEntry>0</defaultOutputEntry>` unless null is what you mean.
- **`/` is decimal division.** `1 / 3` is `0.333…`, not `0`. Dividing by zero is
  null, not an error.
- **Arithmetic on a null is null.** `a + b` with `b` missing, `null * 2` and
  `-null` all yield null, not an error — so a missing input surfaces as a null
  output (with `VERDICT_EVAL_004`), not a `VERDICT_EVAL_005` failure. Mismatched
  types are null too: `1 + "a"` is null, not `"1a"`; use `string(1) + "a"`.
- **Ordering a null is null.** `2 < null`, `null >= null` and
  `x > 5` with `x` missing all yield null, not true or false — and a `> 5`
  cell does not match a null input. Ranges too: `null in [1..10]` is null, and
  neither `[1..10]` nor `not([1..10])` matches a null input. A null *endpoint*
  is no bound, not "unbounded": `5 in [null..10]` and range built-ins over it
  (`before`, `overlaps`, `includes`, …) are null, and a `[null..10]` cell
  never matches — write `<= 10`. A null point is the same: every range
  built-in given a null `a` or `b` (`before(null, [1..10])`,
  `includes([1..10], null)`, `coincides(null, null)`) is null. Equality is
  different: `null = null` is true. Guard with `x != null and x > 5` or an
  explicit `null` rule.
- **`and`, `or` and `not` are three-valued; booleans have no order.** Only
  `true` and `false` count: null and any other kind (`0`, `""`, `[]`) are
  unknown, never falsy. `false and null` is false, `true and null` null,
  `true or null` true, `false or null` null; `not(null)`, `not(0)` and
  `not(5 in ["a".."z"])` are null. That is why `x != null and x > 5` is a safe
  guard — a missing `x` makes it false, not null. `true < false` and the other
  orderings of two booleans are null; `=` and `!=` still work on them. The same
  goes wherever an order is needed: `true in [false..true]`,
  `before(false, true)` and `max([true, false])` are null.
- **Comparing different kinds is null, not an error.** `1 = "a"`, `1 != "a"`,
  `1 < "a"`, `duration("P1Y") < duration("P1D")`, `5 in ["a".."z"]` and range
  built-ins over mixed kinds (`before(1, "a")`, `coincides(1, "a")`) all yield
  null. In a table every cell form follows suit: a `< 5`, `"gold"`,
  `["a".."z"]` or `[false..true]` cell against an input of another (or, for
  booleans, unordered) kind is null — it does not match, and neither does its
  `not(...)`: `not("gold")` does not match `5`, `not(["a", "b"])` does not
  match `5` but does match `"c"`. Separate kinds into their own rules, or end
  with a `-` rule.
- **Lists and contexts compare element by element; they have no order.**
  `[1, 2] = [1, 2]` and `{a: 1, b: 2} = {b: 2, a: 1}` are true, `[1, 2] =
  [2, 1]` and `{a: 1} = {b: 1}` false; a mixed-kind element pair makes it null
  (`[1, "a"] = [1, 2]`) unless another pair is false. `[1, 2] < [1, 3]` is
  null. `list contains`, `index of`, `union`, `distinct values`,
  `duplicate values` and `in` find list and context elements by value, and a
  `[1, 2]` cell matches a list input equal to it as well as `1` or `2`. Table
  equality (cells, ANY agreement, PRIORITY/OUTPUT ORDER ranking) is this same
  `=`: `1` equals `1.00`, `time("10:00:00+01:00")` equals `time("09:00:00Z")`.
- **Ranges compare by endpoints and closedness; they have no order.**
  `[1..2] = [1..2]` is true, `[1..2] = (1..2]` and `[1..2] = [1..3]` false;
  `[1..2] < [3..4]` is null.
- **`x in [list]` is `x = e1 or x = e2 or …`, three-valued.** `3 in [1, 2]` is
  false and `1 in [1, "a"]` true, but `"a" in [1, 2]` is null (each `=` is
  null), so `not("a" in [1, 2])` is null too. `null in [1, null]` is true.
  A range element is membership, not `=`: `1 in [[2..4], [1..3]]` is true and
  `5 in [[1..3], 7]` false; a range input still matches an equal range element.
- **`min` and `max` need comparable items of one kind.** Numbers, strings,
  temporals or durations of one kind: `max(3, 1, 2)` is `3`. An empty list, a
  null item, a mixed list (`min([1, "a"])`) or items with no order (booleans,
  lists, contexts, ranges) give null — `min([1, null])` is null, not `1`.
- **A malformed conversion is null; a malformed `@"..."` does not load.**
  `date("nope")`, `time("25:00")`, `date and time("x")`, `number("abc")`,
  `duration("P1Y2D")` (mixes the two duration kinds), `duration("P")` (no
  component), `date(null)` and
  `string(null)` are null, not an error — so a bad string input surfaces as a
  null output, and `number("1,000")` is null (use the grouping form,
  `number("1,000", ",", ".")`). An argument of the wrong kind is null too:
  `number(5)` (`from` must be a string), `date(true)`, `date and time(5)`,
  `duration(5)`, `context(5)` or an entry lacking `key`/`value`, and
  `years and months duration(date, date and time)` — both must be dates or both
  date and times. `string(x)` takes any non-null value. The literal `@"P1Y2D"` (or `@"P"`) is a syntax error instead:
  `VERDICT_LOAD_013` when the model loads, with the literal's 1-based
  `line L, column C` in the message. A string the spec allows but Verdict
  cannot hold still fails the evaluation (`VERDICT_EVAL_005`) rather than
  reading as malformed: a zone id the time zone database does not know
  (`"2026-01-01T10:00:00@Mars/Olympus"`), a duration finer than a nanosecond (`PT0.0000000001S`) or longer
  than about 292 years of days and time (`P200000D`), and a `time()` second
  finer than a nanosecond.
- **Every Table 72 conversion overload works, positionally or by name.**
  `date(2026, 2, 28)` = `date(year: 2026, month: 2, day: 28)`;
  `date(<date and time>)` is its calendar date. `time(10, 30, 0)` is
  **zoneless**; `time(10, 30, 0, duration("PT2H"))` is `10:30:00+02:00`
  (`PT0S` gives `Z`, `offset: null` stays zoneless). `second` may be
  fractional. `time(<date and time>)` keeps its offset or zonelessness (a DST
  zone id becomes the offset at that instant). `date and time(date, time)`
  takes the time's zone-ness. Invalid components — month 13, 29 February in
  a common year, hour 24, second 60, a non-integer year, an offset beyond
  ±14h — are null. `number(from, grouping separator, decimal separator)`:
  grouping `" "`, `","`, `"."` or null; decimal `"."`, `","` or null (= `"."`);
  they must differ, else null.
- **Durations are normalised.** `duration("P26M")` is `P2Y2M` and
  `duration("PT36H")` is `P1DT12H` — `string()` and `.days`/`.hours`/... read
  the canonical form, and `PT36H = P1DT12H`. Seconds may be fractional
  (`PT1.5S`, kept to the nanosecond; `.seconds` is `1.5`). Two durations of
  the same kind add and subtract (`PT1.5S + PT0.5S = PT2S`); a years and months
  duration plus a days and time one is null. A zero keeps its kind:
  `duration("P1Y") - duration("P1Y")` is `P0M`, not `PT0S`, so adding `PT1H`
  to it, or comparing it with `PT0S`, is null too. A duration scales by a
  number (`PT1H * 2` and `2 * PT1H` are `PT2H`, `P1Y / 2` is `P6M`, exact
  decimal: `PT1S * 0.1` is `PT0.1S`); a fraction of a month or nanosecond
  truncates toward zero (`P1M / 3` is `P0M`, `P1Y * 1.5` is `P1Y6M`).
  Two durations of the same kind divide to a number (`@"P10Y" / @"P5Y"` is
  `2`, `@"P1D" / @"PT1H"` is `24`); a zero divisor or mixed kinds are null.
  Division by zero, `number / duration` and a non-number operand are null.
- **Temporal arithmetic follows DMN Table 57.** `date - date` is a days and time
  duration (`date("2026-01-02") - date("2026-01-01")` is `P1D`; a date counts as
  UTC midnight against a date and time), as is `time - time`. A duration added
  to a date, date and time or time commutes (`@"P1D" + d`); subtracting one
  *from* a duration is null. `time ± days and time duration` wraps within the
  day (`23:00 + PT2H` is `01:00`); a time with a years and months duration is
  null. Adding months pins the day to the end of a shorter month
  (`date("2026-01-31") + @"P1M"` is `2026-02-28`; time and offset are kept).
  Whole years and months between dates is `years and months duration(a, b)`,
  not `b - a` — and it counts by day of month, so Jan 31 to Feb 28 is `P0M`.
- **Zoneless and zoned temporals are different values.** `time("10:00:00")`
  (no offset: a local time) is not `time("10:00:00Z")`. Two zoned values
  compare by instant (`10:00:00+02:00 = 08:00:00Z` is true); two zoneless ones
  by wall clock. Mixed: `=` and `!=` are **null**; `<`/`>` read the zoneless
  one at the other's offset (`10:00:00 < 11:00:00Z` true, `10:00:00 <
  10:00:00Z` false); `<=`/`>=` are true when `<`/`>` is and otherwise **null**
  (`10:00:00 <= 10:00:00Z` is null), and so is a closed range endpoint;
  `date and time - date and time` and `time - time` are null. The range
  built-ins (`before`, `meets`, `overlaps`, `starts`, `includes`, `during`,
  `coincides`, ...) use the same operators, so a mixed tie or non-strict
  comparison makes them null (`meets([09:00..10:00], [10:00Z..11:00Z])`,
  `coincides(10:00:00, 11:00:00Z)` are null) while a strict one still adopts
  the offset (`before(10:00:00, 11:00:00Z)` is true). A date takes the
  zone-ness of the date and time it meets. A **time never compares with a date
  or date and time**: `=`, `<`, `in`, `max`, the range built-ins over the pair
  are null; a date against a date and time compares as its UTC midnight. `+`/`- duration` keeps the operand's
  zone-ness. `.time offset` (a duration) and `.timezone` are null for a zoneless
  value, and `string()` omits the offset (`"10:00:00"`). In a decision table a
  zoneless input never equals a zoned cell — write inputs and cells the same way.
- **IANA zone ids work on date and times.** `date and time("2026-01-01T10:00:00@Europe/Paris")`
  (and `@"...@Europe/Paris"`) takes the zone's offset at that date, so it equals
  `2026-01-01T09:00:00Z`; ids and offsets compare by instant. `.timezone` is the
  id, and null for a value written with an offset (`.time offset` still gives
  the offset). A **time** takes only a zone id with one fixed offset (`@UTC`,
  `@Etc/GMT-2`): `time("10:00:00@Europe/Paris")` is null (DST makes its offset
  depend on a date it does not have) — write `10:00:00+01:00`.
- **`string()` of a temporal is its lexical form**, and parses back to the same
  value: `"2026-02-28T23:59:59+05:00"`, `"...Z"` for a zero offset,
  `"...@Europe/Paris"` only for a zone id, `"2026-02-28T23:59:59"` with none;
  likewise for `time`. A fraction of a second prints only when it is non-zero,
  with trailing zeros trimmed: `string(time("10:00:00.500"))` is `"10:00:00.5"`,
  `string(time("10:00:00.000"))` is `"10:00:00"`.
- **`today()` is the UTC date**, not the host's: with `feel.timezone` set (the
  engine clock's zone, UTC by default) it is the date in that zone.
- **`now()` and Go `time.Time` inputs keep their IANA zone.** Under
  `feel.timezone: Europe/Paris`, `now()` is `...@Europe/Paris` and
  `now().timezone` is `"Europe/Paris"`; under UTC it is `...Z` and `.timezone`
  is null. A Go time in `time.Local`, a `time.FixedZone`, or the occurrence of a
  DST fall-back hour the id cannot name carries its offset only (`.timezone` null).
- **`some` / `every` are booleans.** `some h in xs satisfies h > 1` is true,
  false or null — null when no element decides it and some condition was null.
  `some` over `[]` is false, `every` over `[]` is true. Several iteration
  contexts range over the cartesian product, and a later one may use an earlier
  name: `some o in orders, l in o.lines satisfies l.qty > 10`.
  A non-list domain is a one-element list: `some x in 5 satisfies x > 1` is
  true, and `some x in null satisfies x > 1` is null (the condition sees null).
  A range *value* is not iterated: `some x in [1..3] …` binds `x` to the range.
- **`for` iterates lists or integer ranges.** `for i in 1..3 return i` is
  `[1,2,3]` and `3..1` counts down; both endpoints are included and must be
  integers, or the whole expression is null. `some`/`every` accept `a..b` too.
  Several contexts yield one flat list: `for x in [1,2], y in 1..x return y` is
  `[1,1,2]`. A non-list domain is a one-element list, as for `some`/`every`:
  `for x in 5 return x * 2` is `[10]`, `for x in null return x` is `[null]`.
  Write `1..3`, not `[1..3]` — the bracketed form is a range value, so
  `for x in [1..3] return x` is `[[1..3]]`, one element, not three.
  One request's iteration contexts may produce at most 1,000,000 elements
  in total — ranges and lists alike, nested contexts multiplying
  (`for i in 1..n, j in 1..n` costs n²), and every decision, BKM/function
  call and unary test of the request drawing on the same budget (so
  `for i in 1..n return bkm(i)` pays for all n calls) — configurable as
  `feel.max_iterations`; exceeding it fails the evaluation with
  `VERDICT_EVAL_005`, an over-long range before building anything.
- **Input names come from the input-data variable**, not the file. `verdict
  explain` lists them.
- **A decision service returns only its output decisions.** Encapsulated
  decisions run but do not appear in the result.
- **Values cross a decision service as FEEL values.** Invoked from FEEL, a
  service receives and returns its values unconverted: a zoneless date and
  time stays zoneless, a `date` or `time` keeps its kind, a years-and-months
  duration stays a duration and a number keeps every decimal digit. Only
  `Result.Outputs` and the trace are narrowed to plain Go/JSON (temporals and
  durations as FEEL lexical strings, numbers as `float64`).
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
