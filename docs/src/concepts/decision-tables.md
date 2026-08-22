# Decision tables

A decision table is DMN's central construct, and the reason DMN exists: a way of
writing rules that a domain expert can read and a machine can execute without a
translation step in between.

## Anatomy

```
                    ┌─ input clauses ─────────┐  ┌─ output clause ─┐
  RULE   HIT: U     Credit score   Region        → credit rating
  r1                >= 740         -             "excellent"
  r2                [670 .. 740)   -             "good"
  r3                < 670          "EU"          "fair"
  r4                < 670          not("EU")     "poor"
```

- **Input clauses** carry a FEEL expression evaluated **once per table
  evaluation**, not once per rule. That matters when the expression is expensive
  or invokes a business knowledge model.
- **Rules** carry one *unary test* per input clause. A test is not an
  expression: the value being tested is implicit.
- **Output clauses** carry a name and, optionally, an `outputValues` list and a
  `defaultOutputEntry`.

A table with **one** output clause produces that clause's value directly. A
table with **two or more** produces a context keyed by output name. This catches
people out; it is DMN 1.5 §8.3.

## Unary tests

| Form | Meaning |
|---|---|
| `-` or empty | any value |
| `42`, `"eu"`, `true` | equals |
| `< 10`, `>= 740` | comparison against the input |
| `[1..10]` | closed range |
| `[1..10)` or `[1..10[` | closed below, open above |
| `(1..10]` or `]1..10]` | open below, closed above |
| `"a", "b"` | any of |
| `not("a", "b")` | none of |
| `list contains([1,2], ?)` | an arbitrary predicate, with `?` as the input |

`?` names the value being tested. It is only needed when the test is not one of
the shorthand forms.

DMN spells an exclusive bound two ways — `(1..10]` and `]1..10]` are the same
interval — and Verdict accepts both, because modellers and exporters both use
both. The one place the bracket spelling costs something is a list index inside
an interval's upper endpoint: write `[1..(xs[1])]`, since a bare `[1..xs[1]]`
is genuinely ambiguous with the closing bracket.

## Hit policies

The hit policy says what happens when zero, one or several rules match. Choosing
the right one is the difference between a table that documents its own intent
and a table whose intent lives in a comment.

### Single-hit policies

| Policy | Behaviour |
|---|---|
| `U` UNIQUE | Exactly one rule may match. Two matches is a **runtime error**. The default, and the right default: it makes the table's totality a checkable claim |
| `A` ANY | Several may match, but they must agree. Disagreement is an error |
| `P` PRIORITY | Several may match; the one whose output ranks highest in `outputValues` wins |
| `F` FIRST | Several may match; the first in row order wins |

`UNIQUE` and `ANY` fail loudly on ambiguity. `PRIORITY` and `FIRST` resolve it —
which means they can also hide it, so `verdict analyze` reports their overlaps
as information rather than staying silent.

### Multiple-hit policies

| Policy | Behaviour |
|---|---|
| `C` COLLECT | A list of every matching rule's output, in rule order |
| `C+` COLLECT SUM | The sum |
| `C<` COLLECT MIN | The minimum |
| `C>` COLLECT MAX | The maximum |
| `C#` COLLECT COUNT | The number of matches |
| `R` RULE ORDER | Every match, in rule order |
| `O` OUTPUT ORDER | Every match, sorted by `outputValues` |

A collecting policy that matches nothing yields the **empty list**, not null —
except the aggregators, which yield null (`C#` yields `0`). If zero is what you
mean, say so with a `defaultOutputEntry`.

### The `outputValues` trap

`PRIORITY` and `OUTPUT ORDER` rank by the output clause's `outputValues` list,
**most-preferred first**. This is the most common DMN modelling bug, because
getting it backwards is silent: the table returns the *least* preferred answer
and nothing complains.

```xml
<!-- A cap: the most generous cap must rank highest, or an education
     customer silently gets the commercial cap. -->
<outputValues><text>40,30,20</text></outputValues>
```

## Closing a table

A table is *total* when every possible input combination matches something.
Three ways to get there:

1. Write rules that partition the input space (best — the totality is visible).
2. Add a `defaultOutputEntry` to each output clause.
3. Under `FIRST`, add a catch-all last row with `-` in every input.

Under `UNIQUE`, option 3 is not available: a catch-all row overlaps everything.

## Gaps and overlaps

`verdict analyze` probes each table's input space and reports what it finds.

**How it works.** Exhaustively checking a table is impossible in general — an
input of type `number` has an infinite domain. What makes it tractable is that a
rule set built from unary tests partitions its inputs at a *finite* number of
boundaries. Verdict collects every literal and range endpoint the rules mention,
probes each one plus the values immediately either side, plus one value outside
everything the table mentions, and runs the real rules against each probe.

That last detail matters: the analyser calls the **same** evaluator the runtime
does, so its report cannot drift from what the engine will actually do.

**Declare `inputValues` when you can.** A clause that declares its domain —
`[0..50]`, or `"domestic","eu","world"` — is closed: probes outside it are
dropped rather than reported, because a value the model says cannot occur is not
a hole. Without a declared domain, a `number` input is infinite and the "one
value outside everything" probe will always be uncovered, so the table can never
be proved complete. Declaring the domain is what makes `--strict` a gate a
correct model passes.

**What it reports:**

- **gaps** — combinations nothing covers. Harmless with a default or a
  collecting policy; a hole otherwise.
- **overlaps** — combinations several rules match. An **error** under `U`, and
  under `A` when the rules disagree; informational elsewhere.
- **unreachable rules** — under `FIRST`, a row an earlier row already covers.
  Not reported for `PRIORITY`, where row order is not precedence.

```
DECISION         POLICY  RULES  GAPS  OVERLAPS  UNREACHABLE
Discount Points  C+      6      1     98        0
Discount Cap     P       3      0     2         0
```

Ninety-eight overlaps in a `C+` table is not a problem — stacking is the point.
Two overlaps in a `P` table is exactly what `PRIORITY` is for. The number to
look at is the one under a policy that forbids it.

**As a CI gate.** `verdict analyze` exits `2` when it reports an
error-severity finding, and `--strict` raises gaps to errors:

```bash
verdict analyze model.dmn --strict   # exit 2 if any table has a hole
```

The report is printed either way — a gate that fails without saying what it
found just sends the reader back to run the command again.

## Orientation

DMN allows rules as rows or as columns. Verdict evaluates both identically and
preserves the `preferredOrientation` through a round-trip, so an editor that
renders your table sideways gets it back sideways.
