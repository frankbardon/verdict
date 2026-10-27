# Vendored FEEL evaluator

This directory is a fork of [`github.com/pbinitiative/feel`](https://github.com/pbinitiative/feel)
at **v1.0.6**, vendored under `pkg/feel/internal/` and renamed to package
`dialect`. It is licensed under the terms in `LICENSE`, which is reproduced here
unchanged.

Verdict embeds rather than imports it because the upstream evaluator has
defects that a DMN engine cannot paper over from the outside — they are in
unexported methods on upstream types, reached through upstream's own `Eval`
dispatch, so no amount of AST post-processing fixes them.

## Divergences from upstream

| Where | Upstream behaviour | Verdict behaviour | Why |
|---|---|---|---|
| `eval_binop.go` — `divOp`, `numeric.go` — `Div`, `IsZero` | `/` routed through `IntDiv`, so `1 / 3` evaluated to `0` and dividing by zero panicked in `math/big` | `/` is exact decimal division at the working precision; a zero divisor yields `null` | FEEL numbers are decimals (DMN 1.5 §10.3.2.3). Integer division makes every ratio, rate and proportion in a decision model silently wrong, and a panic in a rule engine is not a recoverable failure mode |
| `parser.go` — `parseName`, `keywordNamePrefixes` | A name absorbed *any* reserved keyword, so `x and y` parsed as one variable named `"x and y"` (evaluating to null with no error) and `if x then a else b` was a parse error | A keyword ends a name unless the result is still a prefix of `date and time`, `days and time duration` or `years and months duration` | FEEL names may contain spaces, and three standard built-ins embed `and`. Resolving the ambiguity in favour of names breaks conjunction, `in`, and every `if` with a variable condition — the three constructs Conformance Level 3 is built on |
| `eval_binop.go` — `compareInterfaces`, `instantOf` | Comparison matched only the `HasTime` interface, which `FEELDate` does not satisfy (it exposes `Date()`), so `date(...) < date(...)` raised a type error | Temporal comparison goes through `instantOf`, which handles all three FEEL temporal types and their mixtures | "Before the cutoff" and "older than 18" are the bread and butter of a decision table. A rule engine whose date comparisons raise is not usable for the models people actually write |
| `parser.go` — `singleElement`, `parseRangeOrArray`, `endpointExpression`, `delimited` | Only `(a..b)`, `[a..b]`, `(a..b]` and `[a..b)` parsed. `]a..b]` and `[a..b[` — the same intervals in the spec's other spelling — were syntax errors | All six bracket forms parse, with `]` opening an exclusive interval and `[` closing one | DMN 1.5 §10.3.1.7 admits both spellings, and modellers write both. A syntax error on a legal unary test rejects the whole model, and the message points at a bracket rather than at the choice of notation |
| `eval_binop.go` — `orderingOp`, `compareGTOp`, `compareGEOp`, `compareLTOp`, `compareLEOp` | `<`, `<=`, `>` and `>=` ordered null below every value, so `2 < null`, `null >= null` and `min([2]) < max([])` evaluated to `true` | An ordering comparison with a null operand yields `null`; `=` and `!=` are unchanged (`null = null` is `true`, `2 = null` is `false`) | DMN 1.5 §10.3.2: comparing an absent value is not a boolean question. A `true` there fires rules on missing data. Verdict's unary-test layer (`pkg/feel/unarytest.go`) treats the null outcome of a comparison test as "not satisfied" |
| `eval.go` — `quantify`, `SomeExpr.Eval`, `EveryExpr.Eval` | `some x in L satisfies P` returned the first element satisfying `P` (and leaked an interpreter scope doing so); `every` returned the list of elements satisfying `P` | Both yield a boolean. `some` is `true` if any `P` is `true`, else `null` if any `P` is null or non-boolean, else `false` (`false` over an empty list); `every` is `false` if any `P` is `false`, else `null` if any is null or non-boolean, else `true` (`true` over an empty list) | DMN 1.5 FEEL grammar rule 58: a quantified expression is a boolean. Returning an element makes `some ... and x` and a `true` input entry silently compare a number or list to a boolean, so the rule never fires |
| `eval_binop.go` — `inOp` | `null in [1..10]` ordered null below the start and evaluated to `false`, so `not(null in [1..10])` was `true` | Membership of a null value in a range yields `null`; membership in a list is unchanged (`null in [1, null]` is `true`) | DMN 1.5 §10.3.2: range membership is the conjunction of its endpoint comparisons, each null for a null operand — the same divergence as the row above, reached through `in`. Verdict's unary-test layer (`pkg/feel/unarytest.go`) applies it to a bare range entry (`[1..10]`), so `not([1..10])` does not match a null input |
| `parser.go` — `parseSomeOrEvery` | `some` and `every` accepted one iteration context; `some x in A, y in B satisfies P` was a syntax error at the comma | A comma-separated list of iteration contexts parses and desugars to nested quantifiers of the same kind, so the result is the three-valued fold of `quantify` over the cartesian product and a later context can reference an earlier name (`some x in A, y in x.items satisfies …`). Any empty context makes `some` false and `every` true | DMN 1.5 FEEL grammar rule 58 admits several iteration contexts, as rule 46 does for `for`, which the fork already parsed. A legal expression rejected at load forces modellers to rewrite it as nested quantifiers |
| `parser.go` — `UnexpectedToken.Error` | The message appended ten frames of the parser's own Go call stack | The message is the position and the expected tokens; the frames stay available through `Callers()` | The text surfaces to a modeller as a `VERDICT_LOAD_013` diagnostic about one cell of their decision table. Parser internals there bury the one line they can act on |

The trade-off in the second row is that a model variable whose name contains a
reserved word (`Rent and Rates`) cannot be referenced unqualified. Every other
FEEL implementation makes the same trade.

The trade-off in the interval row is that `[` cannot be a list index in an
interval's upper endpoint, because there it is genuinely ambiguous with the
closing bracket: `[1..xs[1]]` does not parse. Parenthesise the endpoint —
`[1..(xs[1])]` — which is what `delimited` restores indexing for. Every other
position, including `xs[1]` on its own and `[1, 2, 3][1]`, is unaffected.

## Keeping the fork honest

The divergences above are the *only* intentional ones. When upstream fixes one
of these defects, the fix should be taken and this table shortened rather than
the fork growing. Everything Verdict adds on top of the standard library lives in
`pkg/feel/stdlib.go`, outside this directory, precisely so that the fork's diff
against upstream stays small enough to re-apply.
