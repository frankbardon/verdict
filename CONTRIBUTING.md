# Contributing

## Getting set up

```bash
git clone https://github.com/frankbardon/verdict
cd verdict
make build
make test-all
```

Contributing from a fork:

1. Fork the repository and clone your fork
2. Branch: `git checkout -b my-feature`
3. Make the change, with tests
4. `make test-all && make lint`
5. Push and open a pull request against `main`

`main` is protected: a review from a code owner and green CI are required, so
work on a branch even when you can push directly.

Go 1.26 or later. No CGO — the Makefile sets `CGO_ENABLED=0` deliberately, so
that an import pulling in a C toolchain fails the build instead of quietly
appearing in the dependency tree.

## Before you open a pull request

```bash
make fmt vet test-all
make examples
make validate      # needs libxml2 for xmllint
```

`make validate` checks every example against the DMN 1.3 XML schema. The Go
suite does this too but skips when xmllint is missing, so run the target — an
interoperability check that silently skips is one that silently rots.

`make test` covers the core module. `make test-all` adds `nexus/`, which is a
separate module and therefore invisible to a bare `go test ./...` — a change to
the `AgentBridge` interface will compile fine at the root and break the bridge,
so run the full target.

If you touched anything under `docs/`, build the site too:

```bash
make docs          # needs mdbook
```

CI runs the same targets on every push and pull request — `make test-all`,
`make test-race`, `make fmt-check`, `make lint`, a `go mod tidy` diff check
across **both** modules, `make validate` and `make examples`. Running them
locally first is the difference between one review round and three.

## Generated artefacts

Some files are generated and verified by the test suite. Editing one by hand
produces a failure, not a change:

| Artefact | Regenerate with |
|---|---|
| `pkg/dmn/vdj/testdata/vdj-schema.json` — the published VDJ JSON Schema | `go test ./pkg/dmn/vdj/ -run TestSchemaGolden -update` |
| Example DMNDI (diagram interchange) | `make diagrams` |
| The Twirp surface | `make proto` |

The schema golden carries a trailing `// golden-hash:` line so that a hand edit
is detected rather than published; the documentation workflow strips it before
serving the file at the URL the schema names as its own `$id`.

## Commits and pull requests

Commit subjects follow [Conventional Commits](https://www.conventionalcommits.org):
`type(scope): subject`, in the imperative, no trailing full stop.

```
feat(feel): accept both exclusive-interval bracket spellings
fix(analyze): clip gap probes to a clause's declared input domain
docs(vdj): document the discriminated boxed-expression union
chore(deps): bump github.com/modelcontextprotocol/go-sdk to v1.7.0
```

Types in use: `feat`, `fix`, `docs`, `test`, `refactor`, `perf`, `chore`. The
scope is the package or surface the change lands in — `feel`, `eval`, `analyze`,
`dmn/xml`, `vdj`, `cli`, `server`, `nexus`, `docs`.

The body is where the *why* goes, and it is the part that earns its keep: a
subject line says what changed, and six months later the question is always why
it was allowed to change. If the change deviates from DMN, say which clause and
why, here.

Pull requests:

- One feature or fix per PR. A green diff nobody can hold in their head gets
  rubber-stamped, which is the same as not being reviewed.
- Tests with the change, not after it.
- Documentation with the change, per the Update Demand — see below.
- Fill in the PR template; its checklist is the set of gates CI runs anyway.

## Reporting bugs and requesting features

Use the [bug report](https://github.com/frankbardon/verdict/issues/new?template=bug_report.yml)
or [feature request](https://github.com/frankbardon/verdict/issues/new?template=feature_request.yml)
template.

For a bug, the single most useful thing you can attach is a trace:

```bash
verdict eval model.dmn -i inputs.json --trace --quiet | verdict trace --values
```

It shows which rules actually fired and what each decision was given, which is
usually the whole story. For a feature, if DMN already names the thing you want,
cite the clause — Verdict implements the spec rather than inventing a parallel
vocabulary, so that is the fastest route to agreement.

## Two rules that are not negotiable

**1. Documentation ships with the change.** `CLAUDE.md` carries a table mapping
every kind of change to the files it must also touch — a hit policy touches the
skill pack and a test fixture, a diagnostic code touches the code catalogue, an
MCP tool touches its schema and its description. Deferring a doc update to a
follow-up means the next person reads guidance that is no longer true.

**2. Diagnostic codes never change meaning.** `VERDICT_EVAL_002` means "more
than one rule matched under UNIQUE" permanently. Retire a code and add a new one
rather than repurposing one; logs and dashboards in the field refer to them.

## Conventions

- **DMN is the spec.** Do not rename something DMN already names, and do not
  "improve" behaviour the spec fixes. If the spec's behaviour is genuinely wrong
  for a case, that is a documented deviation with a comment explaining it — not a
  silent one.
- **`pkg/` never imports `cmd/` or `server/`.** The library is the source of
  truth; everything else adapts to it.
- **The trace is a contract.** Its JSON shape and annotation keys are read by
  dashboards and republished on the Nexus bus. Add keys; do not rename them.
- **Comments explain *why*.** The code already says what it does. A comment
  earns its place by recording a decision, a spec reference, or a trap — see the
  ones on `unreachableRules`, `parseAnswer` and `bindingNames` for the register.
- **Tests assert on behaviour a user could notice.** A test named for the rule
  it protects ("a keyword ends a name", "the service returns only its outputs")
  is worth more than one named after the function it calls.

## The FEEL fork

`pkg/feel/internal/dialect` is a fork of
[pbinitiative/feel](https://github.com/pbinitiative/feel), vendored because two
of its defects are unfixable from outside: `/` was integer division, and a name
absorbed reserved keywords so `x and y` parsed as one variable.

Every intentional divergence is a row in
[`NOTICE.md`](pkg/feel/internal/dialect/NOTICE.md) and is pinned by
`TestForkedDefects`. If you re-sync from upstream, re-apply them — that test
exists to make a silent revert impossible. Additions to the standard library go
in `pkg/feel/stdlib.go`, *outside* the fork, so the diff against upstream stays
small enough to re-apply.

## Adding an example

Examples are documentation, and documentation that does not run stops being
true. A new example needs a case in `examples/examples_test.go` and a row in the
README's table — and it must carry a deliberate gap or overlap for the analyser
to find, which `TestEveryExampleReportsItsGapsAndOverlaps` enforces.
