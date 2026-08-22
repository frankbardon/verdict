# Verdict Documentation

This directory holds the mdBook source for the Verdict manual, published to
GitHub Pages at <https://frankbardon.github.io/verdict/>.

The site also serves the machine-readable **VDJ JSON Schema** at
<https://frankbardon.github.io/verdict/vdj-schema.json>, which is the `$id`
inside the schema itself. That file is not written by hand and is not committed
under `docs/`: the deployment workflow copies it from the generator's golden
(`pkg/dmn/vdj/testdata/vdj-schema.json`), which `make test` regenerates and
verifies on every run. See `src/formats/schema.md`.

## Local preview

```
$ make docs-serve
```

(Equivalent to `mdbook serve docs --open`.)

## One-shot build

```
$ make docs
```

(Equivalent to `mdbook build docs`, plus staging the schema so local links to
`/vdj-schema.json` resolve the same way they do in production. Build output
lands in `docs/book/`, which is gitignored; `make docs-clean` removes it.)

## Audience

This site documents the model formats, the CLI, embedding the library in Go,
the server surfaces and operations, for human readers. The LLM-facing guidance
is the embedded skill pack in `skill/SKILL.md`, served over MCP at runtime as
`verdict://skill`.
