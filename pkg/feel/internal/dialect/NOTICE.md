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
