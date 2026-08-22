# The VDJ JSON Schema

Verdict publishes a single machine-readable **JSON Schema** (draft 2020-12)
describing a [Verdict Decision JSON](vdj.md) document. Use it to validate a
model before loading it, to get completion and inline errors while writing one
by hand, to generate client types, or to gate a pull request in CI.

## Where to get it

Three surfaces, one generator (`vdj.BuildSchema`), byte-identical output:

| Surface | How |
|---|---|
| **Docs URL** | <https://frankbardon.github.io/verdict/vdj-schema.json> — the schema's own `$id`, so a validator that resolves the identifier fetches the real document |
| **CLI** | `verdict schema` prints it to stdout; `verdict schema -o vdj-schema.json` writes it; `verdict schema --validate model.vdj` checks a document against it. Offline, no model needed |
| **MCP resource** | Read `verdict://schema` (MIME `application/json`) — see [MCP Tools and Resources](../server/mcp.md) |

Point an editor at it — most JSON language servers accept a `$schema` key or a
glob mapping:

```json
{
  "$schema": "https://frankbardon.github.io/verdict/vdj-schema.json",
  "vdj": "1.0",
  "id": "pricing"
}
```

Or validate from a shell with any draft-2020-12 validator:

```bash
verdict schema > vdj-schema.json
check-jsonschema --schemafile vdj-schema.json model.vdj
```

## Structure

The root is a `$ref` to `#/$defs/Document`, with every other shape in `$defs`:

- **`Document`** — the file itself: the format version, the model's identity,
  and the element collections (`item_definitions`, `input_data`, `decisions`,
  `business_knowledge_models`, `knowledge_sources`, `decision_services`).
- **`Decision`, `BKM`, `InputData`, `KnowledgeSource`, `DecisionService`** —
  the DRG elements.
- **`Expression`** — the boxed-expression union, discriminated on `kind`.
- **`TableInput`, `TableOutput`, `TableRule`, `Binding`, `ContextEntry`,
  `InformationItem`, `ItemDefinition`, `FunctionItem`** — the parts they are
  built from.
- **`AgentDecision`, `AgentBinding`, `AgentPolicy`, `TypeSpec`** — the Verdict
  extension.

Every object is closed (`additionalProperties: false`), so a mistyped key is an
error rather than a silently ignored field.

## The discriminated union

VDJ's `Expression` is a flat union: every kind's payload sits side by side on
one object, selected by `kind`. That is convenient to write and to read, and it
is exactly the shape a naive schema fails to constrain — nothing in the struct
itself stops a `decisionTable` from carrying a list's `elements`.

The schema constrains it. For each kind it emits an `if`/`then` clause
forbidding the properties belonging to the other kinds:

```json
{
  "if":   { "properties": { "kind": { "const": "decisionTable" } },
            "required": ["kind"] },
  "then": { "properties": { "elements": false, "called": false, "…": false } }
}
```

So this fails validation, where the reader would simply ignore the stray field:

```json
{ "kind": "decisionTable", "elements": [] }
```

Two kinds additionally require their payload, because the loader reports a hard
error without it rather than degrading: `invocation` requires `called`, and
`agentDecision` requires `agent`.

## How it stays in sync

The schema is generated from three sources, none of them a hand-maintained copy
of anything:

1. **Reflection** over the Go structs in `pkg/dmn/vdj` — the same types `Parse`
   decodes into. A renamed field, a new field, or a changed `omitempty` changes
   the output.
2. **The model vocabulary registry** (`model.All*` in `pkg/dmn/model/registry.go`)
   supplies every closed enum: boxed-expression kinds, hit policies, COLLECT
   aggregations, orientations, function kinds, agent failure policies,
   conformance levels. Adding a hit policy to the engine therefore changes the
   published contract in the same commit.
3. **A discrimination table** for `Expression` — the one shape reflection cannot
   express — mapping each kind to the fields it owns.

Four tests hold the line:

| Test | What it prevents |
|---|---|
| `TestSchemaGolden` | The published file drifting from the generator |
| `TestSchemaEnumsMatchTheRegistry` | An enum advertising a value set the engine no longer has |
| `TestSchemaCoversEveryExpressionField` | A new union field belonging to no kind, silently un-discriminating the union |
| `TestSchemaAcceptsEveryShippedDocument` | The schema rejecting documents Verdict itself writes — every `.vdj` fixture and every example model projected from DMN XML is validated against it |

The golden carries a trailing `// golden-hash:` line, so a hand edit to the
contract is detected (`TestGoldensNotHandEdited`) rather than deployed. The
publishing workflow strips that line before serving the file.

Regenerate after an intentional format change:

```bash
go test ./pkg/dmn/vdj/ -run TestSchemaGolden -update
```

## Deliberate boundaries

The schema is faithful, not maximally strict. Two places where it is looser or
tighter than the reader, on purpose:

- **`hit_policy` enumerates canonical spellings.** Both the long DMN names
  (`COLLECT`) and the single-cell shorthands (`C+`) validate. The reader
  additionally trims and upper-cases, so it accepts `collect` where the schema
  does not. The schema describes what a writer should emit, not the full
  tolerance of the reader.
- **Unknown fields are rejected here and tolerated there.** Verdict's reader
  accepts a document with fields it does not recognise, reporting
  `VERDICT_LOAD_001` rather than refusing to load, because a newer VDJ version
  is the likelier explanation than a mistake. The schema closes every object
  instead, because catching a mistyped key in a hand-written model is most of
  what a schema is for.

The schema also does not express relationships between elements — that a
`required_decisions` entry names a decision that exists, that a rule's `when`
count matches the table's input count, that the DRG is acyclic. Those are
graph-level properties, checked at load time and reported as
[diagnostics](../reference/diagnostics.md). A document can be schema-valid and
still fail to load; run `verdict analyze` for the rest.
