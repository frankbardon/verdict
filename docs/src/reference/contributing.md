# Contributing

The full guide is [`CONTRIBUTING.md`](https://github.com/frankbardon/verdict/blob/main/CONTRIBUTING.md)
in the repository. This page covers the two things that surprise people.

## The update demand

Any change to Verdict's behaviour, configuration, model vocabulary or public
surface **must update the corresponding documentation in the same commit**.

`CLAUDE.md` carries the table: change a hit policy and it names the six places
that must change with it; add a boxed expression kind and it names eight. This
is not bureaucracy, it is the only thing that stops the next reader — human or
model — from acting on guidance that stopped being true two releases ago.

If you want to defer a documentation update to a follow-up: the follow-up does
not happen.

## Commits and branches

Commit subjects follow [Conventional Commits](https://www.conventionalcommits.org)
— `type(scope): subject`, imperative, no trailing full stop — with the scope
naming the package or surface: `feel`, `eval`, `analyze`, `dmn/xml`, `vdj`,
`cli`, `server`, `nexus`, `docs`.

```
fix(analyze): clip gap probes to a clause's declared input domain
```

The body carries the *why*, including the DMN clause when the change touches
spec behaviour. `main` is protected — branch, open a PR, and let CI run.

## The gates

```bash
make test        # the core module
make test-all    # core + the nexus module — the one that covers everything
make test-race
make lint        # vet, plus staticcheck if installed
make validate    # every example against the DMN 1.3 XSD (needs libxml2)
make examples    # load and analyse every example through the built binary
make docs        # build this site
```

`go test ./...` at the root does **not** cover `nexus/`. That is deliberate:
Nexus lives in a separate module so the core cannot depend on it, and the
core's test run must not require Nexus to be resolvable. `make test-all` is the
command that covers everything.

`make validate` fails when `xmllint` is missing rather than skipping. The Go
test does skip, which is why the make target exists — an interoperability check
that quietly skips quietly rots.

## Regenerating what is generated

Nothing in this list is hand-maintained, and editing any of it by hand is a
mistake the test suite will catch:

| Artefact | Regenerate with |
|---|---|
| [The VDJ JSON Schema](../formats/schema.md) | `go test ./pkg/dmn/vdj/ -run TestSchemaGolden -update` |
| Example diagram interchange | `make diagrams` |
| The Twirp surface | `make proto` (generated files are committed) |

Everything in that table is verified by `make test`: the schema golden by its
hash, the diagrams by `make validate`, the Twirp surface by the build. Editing
one of them by hand produces a failure, not a change.

The schema golden carries a trailing `// golden-hash:` line so a hand edit is
detected rather than published.
