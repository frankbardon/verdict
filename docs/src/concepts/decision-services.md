# Decision Services

A decision service is a named slice of the decision requirements graph with a
contract: the inputs it declares (input data and input decisions), the output
decisions it returns, and the encapsulated decisions it runs to get there
without exposing them (DMN 1.5 §6.3.10, Table 17; the notation is §6.2.5).

## Calling a service

From outside the model, evaluate it by name: `verdict eval --service <name>`,
`Engine.EvaluateService` in Go, or `EvaluateService` over Twirp. The result
holds the output decisions only; encapsulated decisions run but never appear.

From inside the model, a service is a FEEL function named after the service.
Its parameters are the declared inputs, in declaration order:

```
if N <= 0 then 0 else 1 + Countdown Service(N - 1)
```

A service with one output decision returns that value; a service with several
returns a context keyed by their names. The call sees only its arguments,
nothing from the calling scope, which is what makes it a contract rather than a
macro. Recursion through services is bounded by `verdict.evaluation.max_depth`
(`VERDICT_EVAL_012`).

## Calling with the wrong arguments

A decision service or business knowledge model invoked from FEEL is "equivalent
to a FEEL function" (DMN 1.5 §10.3.2.13.2), so an invocation whose arguments do
not conform to its parameters is `null`, not an evaluation error: too many or
too few positional arguments, a named argument that is not a parameter, or a
parameter left out of a named call.

```
Echo(5, 6)                  // null: Echo takes one argument
Scale(2)                    // null: Scale(n, factor) needs two
Scale(n: 2, extra: 1)       // null: extra is not a parameter
Scale(2) = null             // true
```

The invoked body does not run, so a misfitting service call records no
`decisionService` node in the [trace](traces.md); the calling decision's node is
there, with a `null` output. Only argument misfits become `null`: an error
raised inside the body — the iteration budget (`VERDICT_EVAL_005`) or the
service recursion depth (`VERDICT_EVAL_012`) — still fails the evaluation. The
same rule applies to functions from an application function library and to
FEEL's built-ins.

Before this rule, Verdict raised an evaluation error for a misfitting BKM,
service or library-function call; a model that relied on that error to catch
a wrong call now sees `null` instead.

## Values keep their FEEL types

Arguments and results cross a service call from FEEL as FEEL values, without
conversion. A zoneless `date and time` stays zoneless, a `date` or `time` keeps
its kind, a years-and-months duration stays a duration and a number keeps every
decimal digit, so FEEL after the call computes exactly what it would have
computed on the value directly:

```
Echo(date and time("2024-03-05T10:00:00")) = date and time("2024-03-05T10:00:00")   // true
Echo(duration("P1Y2M")) + duration("P1M") = duration("P1Y3M")                      // true
Echo(0.1) + 0.2 = 0.3                                                               // true
```

Values are narrowed to plain Go — and, from there, JSON — only where they leave
the engine: `Result.Outputs` and the [trace](traces.md), in the boundary shape
described under [Values](traces.md#values) (temporals and durations as their
FEEL lexical strings, numbers as `float64`). A value returned through a service
is narrowed at that boundary exactly as the same value returned directly.
