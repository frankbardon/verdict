# Agent decisions

An `agentDecision` is Verdict's one extension to DMN: a node in the decision
graph whose value comes from an agent rather than from a rule.

It exists because the alternative is worse. Systems that need both rules and
language models usually end up with the boundary between them scattered across
application code — a prompt here, a threshold there, a `if resp == "yes"` in a
handler — where nobody can see it and nothing enforces it. Making the boundary a
node type puts it in the model, where it is reviewable.

## The shape

An agent decision lives in its decision's `<extensionElements>`:

```xml
<decision id="risk_tier" name="Risk Tier">
  <extensionElements>
    <verdict:agentDecision id="risk_tier_agent" typeRef="tRiskTier">
  <verdict:promptTemplate>
    Classify this application's risk tier as one of: low, medium, high.

    Credit rating: {{ .creditRating }}
    Affordability: {{ .affordability }}
    Underwriter notes: {{ .notes }}
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
  </extensionElements>

  <question>How would an underwriter read this applicant's story?</question>
  <variable name="Risk Tier" typeRef="tRiskTier"/>
  <!-- information requirements... -->
</decision>
```

(Indentation compressed for the page; the real element nests one level deeper.)

**Why `extensionElements` and not the decision-logic slot.** DMN's `tDecision`
ends with `<xsd:element ref="expression"/>` — the decision-logic slot accepts
only elements in DMN's own `expression` substitution group. A foreign element
there makes the *whole document* fail schema validation, so a modeller opening
it in Camunda Modeler gets an error rather than a diagram. `extensionElements`
is `<xsd:any namespace="##other">`: the one place the schema invites a foreign
element, and therefore the only placement that actually degrades gracefully.

Verdict's reader accepts both placements, because early Verdict models used the
other one. Its writer only ever emits this one.

## The contract

### 1. The agent sees only its bindings

Each `inputBinding` is a FEEL expression evaluated against the decision's own
context. The agent receives the *results* — never the context itself.

This is the same encapsulation DMN applies to a business knowledge model's
parameters, and it is a security property, not a style preference. An agent that
can see the whole context can see the applicant's identifier, and a model that
can see an identifier can learn to key on it.

Bind the minimum. If a decision needs the applicant's tenure, bind the tenure,
not the applicant.

### 2. The output type is declared and enforced

The declared type is used **twice**: it is projected into a provider-side schema
before the call, so the model is constrained rather than corrected; and it is
checked on the way back, because a schema is a strong hint and not a guarantee.

Coercion is generous about *representation* and strict about *meaning*. A model
that answers `"42"` for a number is accepted, because the text is unambiguous. A
model that answers `"high risk"` for an enumeration of `low`/`medium`/`high` is
rejected, because guessing would be inventing a decision.

### 3. The validator is the escape hatch

`validator` is a FEEL expression evaluated with the coerced answer bound to
`value`. Use it for anything the type system cannot express:

```xml
<verdict:validator feel='value.confidence >= 0.6 and value.tier != null'/>
```

A non-true result is a failure, and the failure policy takes over.

### 4. Failure is a typed policy

| `onFailure` | Behaviour |
|---|---|
| `error` (default) | Propagate; the evaluation fails |
| `null` | Bind null and continue |
| `fallback` | Evaluate `fallbackDecision` and use its result |

Pick the failure mode you can live with. For a moderation model, `null` plus a
catch-all "send to a human" rule means an outage routes posts to review and
never publishes them. For a loan model, `fallback` to a deterministic heuristic
means the product keeps working while the model is down.

**Write the fallback as a real decision.** It should be the heuristic you would
have shipped without an LLM, not a stub. In the loan example, `Risk Tier
Heuristic` is a six-rule `PRIORITY` table that is perfectly serviceable on its
own — the agent is an improvement on it, not a replacement for it.

### 5. Latency and retry belong to the engine

`maxLatency` bounds the whole attempt sequence and `maxRetries` counts
*additional* attempts. Both are enforced by the engine through context
cancellation, not by the bridge, so every bridge gets identical guarantees and a
bridge author cannot accidentally opt out of them.

## Bridges

```go
type Bridge interface {
    Invoke(ctx context.Context, req Request) (Response, error)
}
```

That is the whole interface. The bridge receives a rendered prompt, the bound
inputs, and the declared output type; it returns a value. It does not know it is
inside a decision graph, and it does not implement retry, timeouts or validation.

Three ship in the box:

- `pkg/agent/mock` — deterministic answers for tests and CI, with a recorded
  call log.
- `pkg/agent/http` — POSTs a documented JSON envelope to any endpoint.
- `nexus/` (separate module) — a Nexus session, with a workspace, tools and an
  event-bus record. See [Nexus integration](../nexus.md).

Writing one is a function:

```go
bridge := agent.BridgeFunc(func(ctx context.Context, req agent.Request) (agent.Response, error) {
    // req.Prompt, req.Inputs, req.OutputType
    return agent.Response{Value: "low", SessionRef: "run-42"}, nil
})
```

`SessionRef` is recorded in the trace. Set it to whatever identifies the run in
your world — a request ID, a session, a log URL — so an auditor reading the
trace six months later can find the conversation.

## Where to draw the line

| Signal | Rule | Agent decision |
|---|---|---|
| Must be identical on re-run | ✓ | |
| Someone signs off on the logic | ✓ | |
| Input is a number, code, date or enum | ✓ | |
| Input is free text needing interpretation | | ✓ |
| A rule would need dozens of cases for context | | ✓ |
| Being wrong is expensive and unrecoverable | ✓ | only with a fallback |

A healthy hybrid model is mostly tables. The loan example has five decision
tables and one agent node; the moderation example has three tables, a literal
expression acting as a cost gate, and one agent node that most traffic never
reaches. If more than one or two nodes in a graph are agent decisions, the
boundary is probably in the wrong place.
