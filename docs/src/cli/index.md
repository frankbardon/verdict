# The Command Line

`verdict` is a thin adapter over the library: every subcommand parses flags,
calls the library and formats the result. No decision logic lives in it, which
is why the CLI, the server and an embedded engine cannot disagree about what a
model means.

```
verdict eval      evaluate a model against inputs
verdict analyze   report gaps and overlaps
verdict explain   describe what a decision depends on and how it decides
verdict convert   move a model between DMN XML and VDJ
verdict trace     render a saved trace as a readable tree
verdict schema    print the VDJ JSON Schema
verdict serve     serve models over Twirp and MCP
verdict mcp       serve MCP over stdio
```

There is **one binary**. The server is a subcommand, not a second artefact, so
the flags, the configuration file and the agent-bridge construction are shared
with the commands you run locally — there is nothing that can drift between
what CI checks and what production runs.

Every subcommand accepts the global `--config` (or `$VERDICT_CONFIG`) pointing
at a YAML configuration file. Every subcommand reads either format: `.dmn` and
`.vdj` are interchangeable inputs everywhere.

## eval

```bash
verdict eval [options] <model.dmn|model.vdj>
```

Evaluates the model's top-level decisions and writes the outputs as JSON on
stdout. Diagnostics go to **stderr**, so stdout stays machine-readable.

```bash
$ verdict eval examples/pricing/pricing.dmn -d '{
    "Customer": {"segment":"enterprise","seats":250,"tenure_years":3,"region":"emea"},
    "Order":    {"plan":"business","term_months":24,"promo_code":""}
  }'
{
  "duration_ms": 0,
  "outputs": {
    "Quote": {
      "discount percent": 20,
      "list price per seat": 39,
      "monthly total": 7800,
      "plan": "business",
      "price per seat": 31.2,
      "seats": 250,
      "term total": 187200,
      "volume tier": "large"
    }
  }
}
```

| Flag | |
|---|---|
| `--decision <id\|name>` | Evaluate one decision instead of the model's outputs |
| `--service <id\|name>` | Evaluate a decision service |
| `--input, -i <file>` | Input values from a JSON file |
| `--data, -d <json>` | Input values as a literal JSON object |
| `--trace` | Include the execution trace in the output |
| `--quiet` | Suppress diagnostics on stderr |
| `--bridge <mock\|http\|none>` | Agent bridge, overriding the configuration |
| `--agent-endpoint <url>` | Endpoint for the `http` bridge |
| `--agent-answer <DECISION=VALUE>` | Canned answer for the `mock` bridge; repeatable |

**Exit status** is `1` when the evaluation fails and **`2` when it succeeds but
the model reported an error-severity diagnostic** — an evaluation that returned
an answer it is not confident in is not a success you should pipe onward
unexamined.

The mock bridge is what makes agent decisions testable without a model
provider:

```bash
verdict eval examples/loan_approval/loan_approval.dmn \
  --bridge mock --agent-answer risk_tier_agent=low \
  -i applicant.json
```

## analyze

```bash
verdict analyze [options] <model>
```

Probes every decision table's input space and reports the combinations no rule
covers (**gaps**), the combinations several rules cover (**overlaps**), and the
rules an earlier rule makes unreachable.

| Flag | |
|---|---|
| `--json` | Emit the full report as JSON |
| `--strict` | Treat gaps as errors |

**Exit status 2** on any error-severity finding, so this is directly usable as
a CI gate:

```bash
verdict analyze model.dmn --strict || exit 1
```

The analysis runs the real unary tests through the real evaluator — it does not
reinterpret the table's source text — so the report cannot drift from runtime
behaviour. See [Decision Tables](../concepts/decision-tables.md) for what a gap
means and when one is deliberate.

## explain

```bash
verdict explain [options] <model> [decision]
```

Prints the DRG slice for a decision: the inputs a caller must supply, the
decisions it builds on, the knowledge it may invoke, and its logic rendered in
full — the rules of a decision table, or the bindings, output type and failure
policy of an agent decision.

With no decision named, lists the model's decisions and services. That is the
right first command against a model you did not write.

| Flag | |
|---|---|
| `--json` | Emit the slice as JSON |

## convert

```bash
verdict convert [options] <model>
```

Reads either format and writes the other, or the one named by `--to`. The
projection is lossless in both directions.

| Flag | |
|---|---|
| `--to <xml\|json>` | Output format (default: the opposite of the input) |
| `--dmn-version <1.3\|1.4\|1.5>` | DMN namespace to emit (default `1.3`) |
| `--no-diagram` | Omit diagram interchange |
| `--out, -o <file>` | Write to a file instead of stdout |

DMN XML is written at 1.3 with generated diagram interchange, because that is
what editors read and draw. See [DMN XML and
Interoperability](../formats/dmn-xml.md).

## trace

```bash
verdict trace [options] [trace.json]
```

Reads a trace produced by `verdict eval --trace` (or by the server's `GetTrace`
endpoint) and prints it as an indented tree: which decisions fired, what each
was given, what it produced, which rules matched, and what an agent decision was
asked and answered. Reads stdin when given no file.

| Flag | |
|---|---|
| `--values` | Show each node's inputs and output |

```bash
verdict eval model.dmn -i inputs.json --trace | jq .trace | verdict trace --values
```

See [Traces](../concepts/traces.md).

## schema

```bash
verdict schema [--out vdj-schema.json]
verdict schema --validate model.vdj
```

Prints the [VDJ JSON Schema](../formats/schema.md) (draft 2020-12) to stdout.
No model, no network. The bytes are identical to the copy published at
<https://frankbardon.github.io/verdict/vdj-schema.json> and to the MCP resource
`verdict://schema`.

| Flag | |
|---|---|
| `--out, -o <file>` | Write the schema to a file instead of stdout |
| `--validate <file>` | Check a VDJ document against the schema instead of printing it |

`--validate` needs no other tooling, and is worth running before `verdict eval`:

```bash
$ verdict schema --validate model.vdj
model.vdj does not validate against https://frankbardon.github.io/verdict/vdj-schema.json:
- at '/decisions/0/logic': additional properties 'hit_polciy' not allowed
```

Exit `2` when the document does not validate, `1` if the file cannot be read.

That example is the case that motivates it: the loader tolerates fields it does
not recognise — a newer VDJ version is a likelier explanation than a typo — so
`hit_polciy` loads as *no hit policy at all* and the table silently becomes
`UNIQUE`. The schema is the only place that is caught.

Or export it and point a validator or an editor at it:

```bash
verdict schema > vdj-schema.json
check-jsonschema --schemafile vdj-schema.json model.vdj
```

## serve

```bash
verdict serve [options]
```

Loads the models named on the command line or in the configuration and serves
them over Twirp at `--twirp-path`, MCP at `/mcp`, and health at `/healthz` and
`/readyz`.

| Flag | |
|---|---|
| `--model, -m <path>` | Model file or directory to load; repeatable. Directories are scanned non-recursively |
| `--listen, -l <addr>` | Address to bind (default `:7430`, `$VERDICT_LISTEN`) |
| `--twirp-path <prefix>` | Path prefix for the Twirp endpoint (default `/twirp`) |
| `--no-mcp` | Disable the MCP endpoint |
| `--mcp-allow-load` | Expose `verdict_load_model` |
| `--model-root <dir>` | Directory model loading may read paths from; unset forbids loading by path |
| `--strict` | Refuse to load a model with error-severity findings |
| `--log-level <level>` | `debug`, `info`, `warn` or `error` |

Plus the agent flags shared with `eval`: `--bridge`, `--agent-endpoint`,
`--agent-answer`.

The process **refuses to start** with no models, or if any model fails to load.
A decision server that is up with an empty registry answers every call with a
404 the caller may not check — worse than one that is down.

```bash
verdict serve -m ./models --listen :7430 --strict
```

See [Deployment](../server/deployment.md).

## mcp

```bash
verdict mcp [options]
```

Serves the MCP surface over stdin and stdout — the same tools and resources
`verdict serve` exposes at `/mcp`, without a listener. This is what an MCP
client that launches Verdict as a subprocess talks to.

```json
{
  "command": "verdict",
  "args": ["mcp", "--model", "/path/to/models"]
}
```

It takes the model and agent flags from `serve`, and none of the HTTP ones:
`--listen`, `--twirp-path` and `--no-mcp` are rejected rather than silently
ignored.

**Stdout is the transport.** Logs and diagnostics go to stderr; do not pipe
this command into anything but an MCP client.

See [MCP Tools and Resources](../server/mcp.md).
