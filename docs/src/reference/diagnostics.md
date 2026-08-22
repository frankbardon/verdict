# Diagnostic Codes

Every problem the loader, the analyser or the evaluator reports carries a
stable, greppable code.

**A code never changes meaning.** A code that turns out to be wrong is retired
and a new one added, never redefined — because logs, dashboards and alert rules
outlive the release that emitted them, and a code that silently changes meaning
turns a year of history into a lie.

The three families say *when* the problem was found:

| Prefix | Found during |
|---|---|
| `VERDICT_LOAD_*` | Parsing and preparation. The model may still load |
| `VERDICT_ANALYZE_*` | Static analysis. Found without running anything |
| `VERDICT_EVAL_*` | Evaluation. Found by running this particular input |

Severity is separate from the code: `error`, `warning` or `info`. Errors block
loading in strict mode and always surface; `verdict eval` exits **2** when an
evaluation succeeds but reported one.

## Loading

| Code | Meaning |
|---|---|
| `VERDICT_LOAD_001` | A boxed expression kind Verdict cannot evaluate. It is preserved and evaluates to null |
| `VERDICT_LOAD_002` | Unknown hit policy; the table falls back to `UNIQUE` |
| `VERDICT_LOAD_003` | A decision has no decision logic; it evaluates to null |
| `VERDICT_LOAD_004` | A requirement points at an element that does not exist |
| `VERDICT_LOAD_005` | The DRG is not acyclic |
| `VERDICT_LOAD_006` | Duplicate element ID |
| `VERDICT_LOAD_007` | A Java-bound function was parsed but is not executable; calls return null |
| `VERDICT_LOAD_008` | A PMML function reference was preserved but is not executed |
| `VERDICT_LOAD_009` | `expressionLanguage` is not FEEL |
| `VERDICT_LOAD_010` | A FEEL-only construct in a model that declared S-FEEL |
| `VERDICT_LOAD_011` | A rule's entry count differs from the table's clause count |
| `VERDICT_LOAD_012` | An `agentDecision` prompt template does not parse |
| `VERDICT_LOAD_013` | FEEL text does not parse |
| `VERDICT_LOAD_014` | A decision service that does not exist, or one declaring no output decisions |

## Static analysis

| Code | Meaning |
|---|---|
| `VERDICT_ANALYZE_001` | A table gap: an input combination no rule covers, and no default output |
| `VERDICT_ANALYZE_002` | Overlapping rules — an error under `U`, or under `A` when the outputs disagree |
| `VERDICT_ANALYZE_003` | `PRIORITY` or `OUTPUT ORDER` without `outputValues`, so there is no ranking to apply |
| `VERDICT_ANALYZE_004` | A rule an earlier rule makes unreachable. `FIRST` only — see below |
| `VERDICT_ANALYZE_005` | Reserved for an input clause no rule tests. Allocated, not currently emitted |

`VERDICT_ANALYZE_004` applies to `FIRST` alone, and that is not an oversight:
under `PRIORITY` precedence comes from the `outputValues` order, not from where
the rule sits in the table, so an earlier rule shadowing a later one is not a
defect there.

## Evaluation

| Code | Meaning |
|---|---|
| `VERDICT_EVAL_001` | No rule matched and the table has no default output |
| `VERDICT_EVAL_002` | More than one rule matched under `UNIQUE` |
| `VERDICT_EVAL_003` | Rules matched under `ANY` and their outputs disagreed |
| `VERDICT_EVAL_004` | A required input was not supplied; it evaluated as null |
| `VERDICT_EVAL_005` | An expression failed to evaluate |
| `VERDICT_EVAL_006` | A value violated its declared type |
| `VERDICT_EVAL_007` | An agent decision failed |
| `VERDICT_EVAL_008` | An agent decision exceeded its latency budget |
| `VERDICT_EVAL_009` | An agent's answer was rejected by its validator |
| `VERDICT_EVAL_010` | A fallback decision was used in place of a failed agent |
| `VERDICT_EVAL_011` | A Java BKM or an uninterpretable expression yielded null |
| `VERDICT_EVAL_012` | Recursion depth exceeded |

## The ones worth alerting on

Most codes are advisory. These four mean an answer came back that you should
not treat as an answer:

- **`VERDICT_EVAL_001`** — the model was asked something it does not cover. The
  fix is a rule or a default, not a retry.
- **`VERDICT_EVAL_004`** — an input was missing, so a rule tested null and
  quietly did not match. This is the one that most often looks like a wrong
  decision rather than a broken call.
- **`VERDICT_EVAL_002` / `003`** — the table is ambiguous and the model has been
  telling you so since `verdict analyze` first ran.

`VERDICT_EVAL_010` is not a failure — it is the failure policy working — but a
sustained rate of it means the agent path is effectively down and every answer
is coming from the fallback.
