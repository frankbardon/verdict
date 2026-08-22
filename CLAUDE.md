# CLAUDE.md

## Project Overview

Verdict is a DMN 1.5 decision engine. It ships as a Go library
(`github.com/frankbardon/verdict`), **one binary** (`cmd/verdict/`) that is both
the CLI and the server, and a **separate module** for Nexus integration
(`github.com/frankbardon/verdict/nexus`).

**Design principles:**

- **Library-first.** `pkg/verdict` is the source of truth. `cmd/` and `server/`
  are adapters: they parse flags, decode envelopes, call the library, format
  output. No decision logic lives in either. Nothing under `pkg/` imports
  anything under `cmd/` or `server/`.
- **DMN is the spec, not the inspiration.** The graph model, boxed expressions,
  hit policies and FEEL come from DMN 1.5. Do not invent a name for something
  DMN already names, and do not "improve" a behaviour the spec fixes — if the
  spec's behaviour is wrong for a use case, that is a documented deviation, not
  a silent one.
- **The trace is output, not logging.** Every evaluation produces a
  `*trace.Trace` describing which nodes fired, which rules matched, and what an
  agent was asked. Its JSON shape is a stable contract that dashboards and
  auditors read.
- **Nexus is a separate module, and that is the point.** The core must never
  import Nexus. `go build ./...` at the repo root does not build `nexus/`; that
  separation is the enforcement mechanism, so do not add a `go.work` that
  collapses them.
- **Errors are diagnostics.** Loader, analyser and evaluator all report through
  `pkg/diag` with stable `VERDICT_*` codes. A code never changes meaning.

## The Update Demand

Any change to Verdict's behaviour, configuration, model vocabulary or public
surface MUST update the corresponding documentation in the same commit.

| If you change... | You MUST also update... |
|---|---|
| A hit policy's semantics, or add one | `pkg/eval/table.go` (`evalTable` dispatch + the per-policy helper) + `pkg/dmn/model/table.go` (`ParseHitPolicy`, `Shorthand`, `SingleHit`) + the hit-policy table in `skill/SKILL.md` + the hit-policy section of `docs/src/concepts/decision-tables.md` + the fixture in `pkg/verdict/testdata/hit_policies.vdj` + a case in `TestHitPolicies` |
| A boxed expression kind | `pkg/dmn/model/expression.go` (the type + `Kind` const + `SetExpressionBase`) + `pkg/eval/boxed.go` (dispatch + evaluator) + `pkg/dmn/xml/expr.go` (decode) + `pkg/dmn/xml/writer.go` (encode) + `pkg/dmn/vdj/expr.go` and `writer.go` (both directions) + `pkg/explain/explain.go` (`describe`) + a round-trip assertion in `pkg/dmn/roundtrip_test.go` |
| Anything the DMN XML **writer** emits | Re-run `make validate` — the output must still pass the DMN 1.3 XSD. `pkg/dmn/xml/schema_test.go` enforces it, but skips when xmllint is absent, so run the target. A new element also needs a position consistent with its type's `xsd:sequence`; the schemas are vendored under `pkg/dmn/xml/testdata/schema/` |
| A model's DRG shape in `examples/` | `make diagrams` to regenerate its DMNDI, then `make validate`. Diagram coordinates are generated from the graph, never hand-maintained |
| A `VERDICT_*` diagnostic code (added, removed or re-meaninged) | `pkg/diag/diag.go` (the const block) + the code table in `skill/SKILL.md` if a user will meet it + a row in `docs/src/reference/diagnostics.md` (always). **Never** change what an existing code means — retire it and add a new one |
| Anything about `agentDecision` — bindings, output type, validator, policy | `pkg/dmn/model/agent.go` + `pkg/eval/agent.go` + both readers (`pkg/dmn/xml/reader.go`, `pkg/dmn/vdj/expr.go`) + both writers + `pkg/explain/explain.go` + the agent-decision section of `skill/SKILL.md` + `docs/src/concepts/agent-decisions.md` + `README.md`'s contract list + `nexus/schema.go` if the change affects what a provider schema can express |
| The `AgentBridge` interface | `pkg/agent/bridge.go` + `pkg/agent/mock/` + `pkg/agent/http/` + `nexus/bridge.go` (a separate module — it will not fail to compile in the root module's build, so check it explicitly with `make test-all`) |
| The trace shape — a `Node` field, an annotation key | `pkg/trace/trace.go` (the type + the `Ann*` const + `summaryAnnotation` if it should survive summary mode) + `cmd/verdict/cmd_trace.go` (`annotationOrder`) + `server/mcp/handlers.go` (`summarise`) + the trace section of `skill/SKILL.md` + `docs/src/concepts/traces.md` |
| A configuration key | `pkg/config/config.go` (the struct field + `Default()` + `Validate()` if it can be wrong + `Options()` if it maps to an engine option) + `configs/verdict.yaml` (shown at its default, with the reasoning) + `nexus/config.go` if the plugin accepts it too |
| An engine `Option` | `pkg/verdict/options.go` (the option + the `options` field + `evalConfig`) + `pkg/eval/program.go` (`Config`) if it reaches the evaluator + `pkg/config/config.go` (`Options()`) if it is configurable |
| A Twirp method or message | `server/twirp/service.proto` → `make proto` → `server/twirp/server.go` (the handler) + a case in `server/server_test.go` |
| An MCP tool | `server/mcp/handlers.go` (typed In/Out structs with `jsonschema` description tags on **every** field, plus the handler) + `server/mcp/tools.go` (reflected schemas + a `ToolDescriptor` in `Tools`) + the MCP section of `skill/SKILL.md` + the tool table in `docs/src/server/mcp.md` + a case in `server/mcp/tools_test.go`. Schemas are reflected from struct tags, never hand-written |
| A FEEL built-in, or the dialect gate | `pkg/feel/stdlib.go` (additive only — never shadow an upstream built-in) or `pkg/feel/dialect.go` + the FEEL cheat-sheet in `skill/SKILL.md` + a case in `pkg/feel/feel_test.go` |
| Anything under `pkg/feel/internal/dialect/` | `pkg/feel/internal/dialect/NOTICE.md` (the divergence table) + a pinning case in `pkg/feel/dialect_fork_test.go`. This directory is a **fork**: every intentional change is a row in that table, or the next re-sync silently reverts it |
| The VDJ document shape — a field on a struct in `pkg/dmn/vdj`, a new boxed-expression payload field | `pkg/dmn/vdj/vdj.go` (the struct) + both directions in `expr.go`/`writer.go` + `expressionFields` in `pkg/dmn/vdj/schema.go` if it belongs to a kind + regenerate the schema golden (`go test ./pkg/dmn/vdj/ -run TestSchemaGolden -update`) + the field table in `docs/src/formats/vdj.md`. `TestSchemaCoversEveryExpressionField` fails if a union field belongs to no kind |
| A closed set of model values — a hit policy, an aggregation, a boxed-expression kind, a failure policy, a conformance level | `pkg/dmn/model/registry.go` (the `All*` function) **as well as** the places the row above for that construct names. The registry is what the published JSON Schema enumerates; `TestSchemaEnumsMatchTheRegistry` fails if the two disagree |
| Anything the published JSON Schema describes | Regenerate the golden — never hand-edit `pkg/dmn/vdj/testdata/vdj-schema.json`. It carries a `// golden-hash:` line and `TestGoldensNotHandEdited` catches an edit. The site publishes that exact file at the URL in the schema's `$id`; `TestSchemaIDIsWhatTheSitePublishes` holds `.github/workflows/docs.yml` to it |
| A `VERDICT_*` code, additionally | A row in `docs/src/reference/diagnostics.md`. `pkg/diag/docs_test.go` reads the code list out of the package source and fails on an undocumented one |
| An MCP **resource** (as opposed to a tool) | `server/mcp/resources.go` (the `ResourceDescriptor` in `Resources`) + the resource table in `docs/src/server/mcp.md` + the MCP section of `skill/SKILL.md` + a case in `server/mcp/resources_test.go` |
| Any user-facing behaviour documented on the site | The corresponding page under `docs/src/`. `docs/src/SUMMARY.md` is the table of contents; a page not listed there is not built |
| A CLI subcommand or flag | `cmd/verdict/cmd_<name>.go` + the usage section of `README.md` + `docs/src/cli/index.md` + `skill/SKILL.md` if an agent would use it |
| An example model | The model + a case in `examples/examples_test.go` + a row in `README.md`'s example table. Every example must carry a deliberate gap or overlap for the analyser to find — `TestEveryExampleReportsItsGapsAndOverlaps` enforces it |

If you want to defer a doc update to "a follow-up", stop. The follow-up does not
happen, and the next session reads stale guidance and writes wrong code.

## Architecture

```
verdict/
├── pkg/verdict/        Public API: Engine, Model, Result, Inputs, Options, ModelSource
├── pkg/dmn/
│   ├── model/          Version-neutral DRG + boxed expressions. No XML or JSON tags —
│   │                   the wire formats project onto these, never the reverse
│   ├── xml/            DMN 1.3/1.4/1.5 reader (namespace-tolerant, matches on local
│   │                   name) and 1.5 writer
│   └── vdj/            Verdict Decision JSON: a lossless projection, not a second
│                       source of truth
├── pkg/feel/           FEEL façade: compile cache, unary-test semantics, dialect gate,
│   │                   value conversion, namespaced function libraries
│   └── internal/dialect/  Vendored fork of pbinitiative/feel — see its NOTICE.md
├── pkg/eval/           The evaluator
│   ├── program.go      Prepare: compile every expression at load, not first use
│   ├── engine.go       Graph slicing, layer scheduling, input binding, parallelism
│   ├── table.go        Decision tables and every hit policy; Matcher for the analyser
│   ├── boxed.go        Boxed-expression dispatch
│   ├── agent.go        agentDecision: binding, prompt, coercion, retry, failure policy
│   └── context.go      Derived scopes — never mutated in place, which is what makes
│                       concurrent sibling evaluation safe without a lock
├── pkg/analyze/        Gap, overlap and reachability analysis over eval.Matcher
├── pkg/agent/          AgentBridge + mock/ + http/
├── pkg/trace/          Trace types, Recorder, redaction, Writer
├── pkg/diag/           VERDICT_* codes, Diagnostic, Set. No dependencies on anything
├── pkg/explain/        Decision rendering, shared by the CLI and the MCP tool
├── pkg/config/         YAML config → engine options
├── nexus/              SEPARATE MODULE: Nexus bridge (Nexus answers Verdict) and
│                       plugin (Verdict answers Nexus)
├── cmd/verdict/        The binary: eval, analyze, explain, convert, trace, schema,
│                       serve (Twirp + MCP over HTTP) and mcp (MCP over stdio)
├── server/             Twirp + MCP + HTTP assembly, driven by `verdict serve`
├── skill/              Embedded SKILL.md
├── docs/               mdBook source for frankbardon.github.io/verdict, which also
│                       serves the generated VDJ JSON Schema at its own $id
└── examples/           Runnable models, exercised end to end by examples_test.go
```

## Build / Test

```bash
make build         # bin/verdict (one binary: CLI and server)
make test          # core module only
make test-all      # core + the nexus module (needs Nexus resolvable)
make test-race
make examples      # build, then load and analyse every example model
make validate      # every example against the DMN 1.3 XSD (needs libxml2)
make diagrams      # regenerate example DMNDI from the current DRG
make proto         # regenerate the Twirp surface; generated files are committed
make docs          # build the mdBook site into docs/book, schema staged beside it
make docs-serve    # preview it
make lint          # vet + staticcheck if installed
```

`go test ./...` at the root does **not** cover `nexus/`. That is deliberate:
`make test-all` is the command that covers everything.

## Git

- **Conventional Commits**: `type(scope): subject`, imperative, no trailing full
  stop. Scope is the package or surface — `feel`, `eval`, `analyze`, `dmn/xml`,
  `vdj`, `cli`, `server`, `nexus`, `docs`. The body carries the *why*, and cites
  the DMN clause when the change touches spec behaviour.
- **`main` is protected.** Branch, open a PR, let CI run. Never commit or push
  unless asked to.
- **CI mirrors the make targets**, so anything that fails in
  `.github/workflows/ci.yml` fails locally first: `test-all`, `test-race`,
  `fmt-check`, `lint`, a `go mod tidy` diff across both modules, `validate` and
  `examples`.
- **`go.work` is gitignored on purpose.** One spanning the root and `nexus/`
  would collapse the module boundary the design rests on — `go build ./...`
  would start resolving Nexus and nothing would enforce the separation.

## Things that will bite you

- **The FEEL evaluator is a fork.** `pkg/feel/internal/dialect` diverges from
  upstream in two places (decimal division, and keywords ending a name). Both are
  pinned by `TestForkedDefects`. If you re-sync from upstream, re-apply them or
  that test fails — which is what it is for.
- **`Matcher` is the analyser's only door into the evaluator.** Static analysis
  runs the real unary tests through the real evaluator, so the report cannot
  drift from runtime behaviour. Do not give `pkg/analyze` a second path that
  reinterprets table source text.
- **Single-output decision tables return the value, not a record.** DMN 1.5 §8.3.
  Getting this wrong is silent and breaks every downstream comparison.
- **`PRIORITY` ranks by `outputValues` order, not rule order.** The unreachable-rule
  check therefore applies to `FIRST` only; see the comment on `unreachableRules`.
- **Trace annotation keys are a public contract.** Prism dashboards and the Nexus
  bus republish them. Add keys; do not rename them.
- **"It parses in our reader" is not interoperability.** The DMN XSD is stricter
  than Verdict's reader in three ways that bit us once already: `tDecision`'s
  logic slot admits only DMN's own `expression` substitution group (so
  `agentDecision` lives in `extensionElements`); `tDefinitions` admits foreign
  attributes only when namespaced (so `verdict:version`, never `version`); and
  `tItemDefinition` is an `xsd:choice` (so never both `typeRef` and
  `itemComponent`). `make validate` is the check that catches all three.
- **DMN 1.3 is the write target, deliberately.** Camunda Modeler and dmn-js read
  1.3 only. Verdict reads 1.3/1.4/1.5 and writes 1.3 unless asked otherwise;
  changing that default breaks every editor a user has.
- **One binary, and stdout belongs to the protocol.** The server is
  `verdict serve`, not a second artefact; `verdict mcp` is the same MCP surface
  over stdio. Under `verdict mcp`, stdout *is* the transport — a stray
  `fmt.Println` corrupts the session rather than looking untidy. Logs go to
  stderr; every subcommand keeps stdout for its own machine-readable output.
- **The published JSON Schema is generated, and three tests say so.** It is
  reflected from `pkg/dmn/vdj`'s structs, enumerated from `pkg/dmn/model`'s
  `All*` registry, and discriminated by the `expressionFields` table in
  `pkg/dmn/vdj/schema.go`. Hand-editing the golden is caught by its hash;
  forgetting the registry is caught by the enum-parity test; adding a union
  field owned by no kind is caught by the coverage test. Regenerate, never edit.
- **`$id` is a promise nothing in Go can keep.** The schema names
  `https://frankbardon.github.io/verdict/vdj-schema.json` as its identifier, and
  only `.github/workflows/docs.yml` makes that URL resolve. Renaming the golden,
  the workflow step or the site path breaks resolution silently — a validator
  fetches a 404 and either fails confusingly or skips validating altogether.
- **Agent bindings are the encapsulation boundary.** An agent must never see the
  model context — only the values its declared bindings produce. Any change that
  widens what reaches `agent.Request.Inputs` is a security change, not a
  convenience.
