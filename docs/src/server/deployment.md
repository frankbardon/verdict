# Deployment

## Embedded (recommended)

Import the library. A `*verdict.Engine` is safe across goroutines, holds a
registry of models, and evaluates a full model with a trace in a few hundred
microseconds.

```go
engine, err := verdict.NewEngine(
    verdict.WithStrictMode(true),
    verdict.WithTracing(verdict.TracingFull),
    verdict.WithAgentBridge(bridge),
)
for _, path := range modelPaths {
    if _, err := engine.LoadModel(verdict.FromFile(path)); err != nil {
        return err   // fail startup: a service that is up but missing a model
    }                // returns 404s the caller may not check
}
```

Load at startup and fail on error. A decision service that starts without its
models is worse than one that does not start.

## verdict serve

Run the server when the callers are not Go, or when several services should
share one versioned decision surface.

```bash
verdict serve -m ./models --listen :7430 --strict
verdict serve -c /etc/verdict/verdict.yaml
```

There is one binary. `verdict serve` is a subcommand of the same `verdict` you
use to evaluate and analyse models locally — same flags, same configuration
file, same bridge construction. A separate daemon would have to duplicate all
three, and the day they drift is the day a model behaves differently in
production than it did in CI.

Models are loaded at startup and the process **refuses to start** if any of
them fails to load, or if there are none to load. A decision server that is up
with an empty registry answers every call with a 404 the caller may not check.

For an MCP client that launches Verdict as a subprocess, use `verdict mcp`
instead — the same MCP surface over stdio, with no listener. See
[MCP Tools and Resources](mcp.md).

### Twirp

`POST /twirp/verdict.v1.Engine/<Method>` with a JSON body.

| Method | Purpose |
|---|---|
| `Evaluate` | The model's top-level decisions |
| `EvaluateDecision` | One decision and its dependencies |
| `EvaluateService` | A named decision service |
| `LoadModel` | Register a model from a document, or from a path under the model root |
| `ListModels` | What is registered |
| `GetTrace` | A retained trace by id |
| `Analyze` | The gap and overlap report |
| `Explain` | A decision's inputs, dependencies and logic |

```bash
curl -s localhost:7430/twirp/verdict.v1.Engine/Evaluate \
  -H 'Content-Type: application/json' \
  -d '{"model_id":"loan_approval","inputs_json":"{\"Applicant\":{...}}"}'
```

Models, inputs, outputs and traces cross the wire as JSON-encoded strings rather
than being re-modelled in protobuf. There is one source of truth for each of
those shapes, and re-modelling them would guarantee drift on the first schema
change.

A failed evaluation returns a `failed_precondition` error carrying its
diagnostics and a `trace_id` in the error metadata, so the reason survives the
failure.

### MCP

`/mcp` speaks streamable HTTP. Tools:

- `verdict_list_models` — what is loaded, and what inputs each model needs
- `verdict_explain` — a decision's inputs, dependencies and full logic
- `verdict_evaluate` / `verdict_evaluate_decision` — evaluate, with a
  per-decision summary of which rules fired
- `verdict_analyze` — gaps and overlaps

`verdict_load_model` is **off by default** and enabled with `--mcp-allow-load`:
an agent that can load models into a shared engine can also shadow the ones you
deployed.

The right order for an unfamiliar model is list → explain → evaluate. Evaluating
first and guessing at input names produces nulls that look like answers.

### Health

`/healthz` and `/readyz` return the server version and the loaded model IDs. A
server with no models is live but not useful, and the payload says so.

### Security

- **`LoadModel` by path is refused unless `--model-root` is set**, and confined
  to that directory when it is. Without it, a server that reads any path a
  caller names is a file-disclosure primitive, not a decision engine.
- **Request bodies are not logged.** A decision request is the caller's data;
  logging it by default would quietly turn the server into a data store. Set
  `--log-level debug` for path-and-status request logging.
- **Traces are retained in a bounded ring** (256 by default). An aged-out trace
  is a 404, not a leak.
- **The server has no authentication of its own.** Put it behind whatever your
  environment already uses; it is designed to sit inside a trust boundary.

## Configuration

One YAML vocabulary covers the library, the server and the Nexus plugin. Every
key has a documented default and a file only needs the keys it changes. See
[`configs/verdict.yaml`](https://github.com/frankbardon/verdict/blob/main/configs/verdict.yaml).

## Operating a model

- **Version your models.** `version` on `<definitions>` is free-form; semver is
  recommended. An engine holds several versions of one ID at once, and callers
  can pin one — which is how you roll a change out gradually.
- **Content addressing is automatic.** Loading identical bytes twice is
  idempotent and returns the same model, so a reload watcher is cheap.
- **Run `verdict analyze` in CI.** It exits non-zero on an error-severity
  finding, so a table that develops a hole fails the build rather than a request.
- **Turn on `strict_mode` in production**, and leave it off while modelling.
- **Watch `fallback_used` in your traces.** A rising rate is an agent
  availability problem showing up before your users report it.
