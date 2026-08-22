# Traces

The trace is Verdict's primary output. Outputs tell you what was decided; the
trace tells you how, and it is what makes a decision engine auditable rather
than merely fast.

## Shape

```go
type Trace struct {
    ModelID   string
    ModelHash string        // content address of the model that ran
    Entry     string        // what was asked for
    Root      *Node
    StartedAt time.Time
    Duration  time.Duration
}

type Node struct {
    DecisionID   string
    DecisionName string
    NodeKind     string         // "decisionTable", "agentDecision", "invocation", …
    Inputs       map[string]any
    Output       any
    Children     []*Node
    Annotations  map[string]any
    StartedAt    time.Time
    Duration     time.Duration
    Error        string
}
```

The JSON encoding is a stable contract. Dashboards, the Nexus event bus and the
`verdict trace` renderer all read it.

## Annotations

Annotation keys are exported constants in `pkg/trace` and are part of the
contract — keys get added, never renamed.

| Key | On | Meaning |
|---|---|---|
| `hit_policy` | decision tables | The policy in shorthand (`U`, `C+`, …) |
| `aggregation` | collecting tables | `SUM`, `MIN`, `MAX`, `COUNT` |
| `matched_rules` | decision tables | The rule IDs that fired |
| `rule_count` | decision tables | How many rules were considered |
| `input_values` | decision tables | Each input clause's evaluated value |
| `defaulted` | decision tables | No rule matched; the default was used |
| `invoked_bkm` | invocations | The business knowledge model called |
| `agent_prompt` | agent decisions | The rendered prompt |
| `agent_session_ref` | agent decisions | The bridge's identifier for the run |
| `agent_attempts` | agent decisions | How many attempts it took |
| `agent_tokens` | agent decisions | Cost telemetry, when the bridge reports it |
| `fallback_used` / `fallback_from` | agent decisions | The agent failed and which decision answered instead |
| `cache_hit` | invocations | A memoised result was reused |
| `error` | any | What went wrong |

## Modes

| Mode | Records |
|---|---|
| `full` (default) | Everything, including every node's inputs and output |
| `summary` | The node graph, timings and outputs, plus structural annotations. Inputs and data-bearing annotations are dropped |
| `off` | Nothing; `Result.Trace` is nil |

`summary` is the mode for a service that wants to see the *shape* of its
decisions in production without persisting the data that flowed through them.

## Redaction

```go
verdict.WithRedactedInputs("Applicant.ssn", "notes")
```

Named bindings are replaced with `[redacted]` in the trace. Redaction is a
**trace concern only**: the decision still sees the real value, so redacting an
input never changes an outcome.

## Reading one

```bash
verdict eval model.dmn -i inputs.json --trace --quiet | verdict trace --values
```

```
loan_approval  [evaluation]  630µs
├── Repayment  [invocation]  279µs
│       inputs: {"Loan":{"amount":120000,"term_months":240}}
│       output: 996.27
│   └── Monthly Repayment  [invocation]  176µs
│           invoked_bkm: "Monthly Repayment"
├── Credit Rating  [decisionTable]  143µs
│       hit_policy: "U"
│       matched_rules: ["credit_rating_r1"]
│       output: "excellent"
└── Risk Tier  [agentDecision]  106µs
        agent_session_ref: "nexus://sessions/9f2c/verdict/risk_tier"
        agent_attempts: 1
        output: "low"
```

When an answer is wrong, the trace tells you *which node* is wrong before you
read a single rule. `matched_rules` is usually the whole story: either a rule
you did not expect fired, or none did and the table defaulted.

## Replay

A trace plus the model and the inputs is enough to reproduce an evaluation
exactly — every deterministic node takes the same path and produces the same
value. `ModelHash` pins *which* model ran, so a replay against a changed model
is detectable rather than silently different.

Agent decisions are the exception, and deliberately so: their answers are not a
function of their inputs. The trace records the answer and the session
reference rather than pretending the call is repeatable. To replay one, feed the
recorded answer back in — which is exactly what a mock bridge configured from a
trace does.

## On the bus

With the Nexus plugin configured with `publish_trace: true`, every node is
republished on the event bus as it completes, so a dashboard or terminal UI sees
the decision graph filling in rather than waiting for a final answer.
