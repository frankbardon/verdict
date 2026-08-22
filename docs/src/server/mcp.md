# MCP Tools and Resources

Verdict speaks [MCP](https://modelcontextprotocol.io), so an agent can
evaluate decisions, read a decision's rules and check a model for gaps without a
human in the loop.

The catalogue is SDK-agnostic. A tool carries a name, reflected input and output
schemas, and a type-erased handler; the go-sdk adapter mounts them, and anything
else could. Schemas are **reflected from Go struct tags**, never hand-written —
a hand-written schema drifts from the struct on the first field added.

## Tools

| Tool | What it does |
|---|---|
| `verdict_list_models` | What is loaded: versions, the decisions and services each model offers, and which decisions are top-level outputs. **Start here.** |
| `verdict_explain` | A decision's inputs, dependencies and full logic — every rule of a table, or the bindings, output type and failure policy of an agent decision |
| `verdict_evaluate` | Evaluate a model's top-level decisions and return the outputs with a trace |
| `verdict_evaluate_decision` | Evaluate one decision or service and its dependencies. Cheaper, and the trace shows exactly which rules fired |
| `verdict_analyze` | Gaps, overlaps and unreachable rules |
| `verdict_load_model` | Register a model sent by the caller. **Off by default** — see below |

The right order against an unfamiliar model is always **list → explain →
evaluate**. Evaluating first and guessing at input names produces nulls that
look like answers.

`verdict_load_model` is opt-in (`AllowLoad`) because an agent that can load
arbitrary models into a shared engine can also shadow the ones an operator
deployed.

A tool that fails reports the error as tool *output* rather than as a protocol
failure, so the model can read what went wrong and correct its next call instead
of losing the turn.

## Resources

Two static documents, addressable under the `verdict://` scheme. They describe
the format and the engine, not any loaded model, so they are safe to expose
whatever the server has been given to evaluate.

| URI | MIME | What |
|---|---|---|
| `verdict://schema` | `application/json` | The [VDJ JSON Schema](../formats/schema.md) — the contract for a model document |
| `verdict://skill` | `text/markdown` | The embedded skill pack: how to write, evaluate and analyse a model, written for a model rather than for a reader of this manual |

Resources rather than tools, deliberately: these are documents an agent reads,
not actions it takes, and a client listing its options should not have to weigh
them against the tools it might call.

`verdict://schema` serves the same bytes as `verdict schema` and as the copy
published at <https://frankbardon.github.io/verdict/vdj-schema.json>. That
matters when an agent is *writing* a model: it can fetch the contract in-session
and validate its own output against the same document CI will use.

The skill pack is embedded in the module (`skill/SKILL.md`), so the guidance an
agent is given always matches the version of the library it is talking to. A
skill pack in a separate repository drifts: the engine gains a hit policy, the
guidance keeps describing the old set, and an agent confidently writes a model
the engine rejects.

## Two transports, one surface

```bash
verdict mcp   -m ./models     # stdio: for a client that launches Verdict itself
verdict serve -m ./models     # HTTP:  MCP at /mcp, alongside Twirp and health
```

Most MCP clients launch their servers as a subprocess and talk over stdin and
stdout — that is `verdict mcp`. Configure one with:

```json
{
  "command": "verdict",
  "args": ["mcp", "--model", "/path/to/models"]
}
```

`verdict serve` additionally mounts the same surface at `/mcp` over streamable
HTTP, for a client that connects to a shared deployment rather than spawning
one. Both build their MCP server through the same constructor, so the tools and
resources are identical either way — `TestMCPOverStdioServesTheSameSurface`
holds them to it.

Under `verdict mcp`, **stdout is the transport**: logs and diagnostics go to
stderr, and the command is not one to pipe into anything but an MCP client.

MCP is served over HTTP unless `--no-mcp` is passed. `verdict_load_model`
appears only with `--mcp-allow-load`, on either transport.

See [Deployment](deployment.md) for transports, the Twirp surface, health
checks and what to think about before exposing any of it.
