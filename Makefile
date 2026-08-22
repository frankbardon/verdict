.PHONY: all build test test-race test-all cover fmt fmt-check vet lint proto clean examples validate diagrams docs docs-serve docs-clean install tidy

BINARY=verdict
BUILD_DIR=bin
GO=go
VERSION?=$(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS=-s -w -X main.version=$(VERSION)
BUILD_FLAGS=-trimpath -ldflags="$(LDFLAGS)"

# Verdict is pure Go — no CGO in the build graph. Disabling it globally makes
# that a contract rather than an accident: any future import that pulls in a C
# toolchain fails the build instead of silently appearing in the dependency
# tree. Override on the command line if a consumer genuinely needs it.
export CGO_ENABLED=0

all: fmt-check vet test build

# One binary. The server is `verdict serve`, not a second artefact: everything
# it does is already in the library, and a separate daemon duplicates the flag
# parsing, the config loading and the bridge construction that cmd/verdict
# already owns.
build:
	@mkdir -p $(BUILD_DIR)
	$(GO) build $(BUILD_FLAGS) -o $(BUILD_DIR)/$(BINARY) ./cmd/verdict
	@ls -lh $(BUILD_DIR)/

install:
	$(GO) install $(BUILD_FLAGS) ./cmd/verdict

# test covers the core module. The Nexus integration is a separate module and
# is not built by a bare `go test ./...` — see test-all.
test:
	$(GO) test ./...

# test-all adds the nexus module, which pulls in Nexus and its dependency tree.
# Keeping it out of the default target is the point of the module split: the
# core's test run must not depend on Nexus being resolvable.
test-all: test
	cd nexus && $(GO) test ./...

test-race:
	CGO_ENABLED=1 $(GO) test -race ./...
	cd nexus && CGO_ENABLED=1 $(GO) test -race ./...

cover:
	$(GO) test -coverprofile=coverage.out -covermode=atomic ./...
	$(GO) tool cover -func=coverage.out | tail -1

fmt:
	gofmt -w $(shell find . -name '*.go' -not -path './bin/*')

fmt-check:
	@unformatted=$$(gofmt -l $$(find . -name '*.go' -not -path './bin/*')); \
	if [ -n "$$unformatted" ]; then \
		echo "gofmt: these files need formatting:"; echo "$$unformatted"; exit 1; \
	fi

vet:
	$(GO) vet ./...
	cd nexus && $(GO) vet ./...

# LINT_PKGS excludes the vendored FEEL fork. Its style is upstream's, and
# reformatting it to satisfy our linter would bloat the diff we have to re-apply
# on every re-sync. Its correctness divergences are pinned by tests instead.
LINT_PKGS=$(shell $(GO) list ./... | grep -v '/pkg/feel/internal/dialect')

lint: vet
	@if command -v staticcheck >/dev/null 2>&1; then \
		staticcheck $(LINT_PKGS) && (cd nexus && staticcheck ./...); \
	else \
		echo "lint: staticcheck not installed (go install honnef.co/go/tools/cmd/staticcheck@latest); ran vet only"; \
	fi

tidy:
	$(GO) mod tidy
	cd nexus && $(GO) mod tidy

# proto regenerates the Twirp surface. The generated files are committed, so a
# normal build never needs protoc.
proto:
	@if ! command -v protoc >/dev/null 2>&1; then \
		echo "proto: protoc not installed (brew install protobuf)."; \
		echo "proto: generated files are committed; the build does not require protoc."; \
		exit 0; \
	fi
	@if ! command -v protoc-gen-go >/dev/null 2>&1; then \
		echo "proto: go install google.golang.org/protobuf/cmd/protoc-gen-go@latest"; exit 0; \
	fi
	@if ! command -v protoc-gen-twirp >/dev/null 2>&1; then \
		echo "proto: go install github.com/twitchtv/twirp/protoc-gen-twirp@latest"; exit 0; \
	fi
	protoc --go_out=paths=source_relative:. --twirp_out=paths=source_relative:. server/twirp/service.proto

# validate checks every example against the DMN 1.3 XML schema. The Go test
# suite does this too, but skips when xmllint is absent; this target fails
# instead, so CI cannot pass by quietly skipping the interoperability check.
validate:
	@command -v xmllint >/dev/null 2>&1 || { \
		echo "validate: xmllint not found (install libxml2)"; exit 1; }
	@fail=0; for m in examples/*/*.dmn; do \
		if xmllint --noout --schema pkg/dmn/xml/testdata/schema/DMN13.xsd "$$m" 2>&1 \
			| grep -q "validates"; then echo "OK   $$m"; \
		else echo "FAIL $$m"; \
			xmllint --noout --schema pkg/dmn/xml/testdata/schema/DMN13.xsd "$$m" 2>&1 | head -5; \
			fail=1; fi; \
	done; exit $$fail

# examples runs every shipped model through the CLI, which is both a smoke test
# of the binary and a check that the documentation still works.
examples: build
	@for m in examples/*/*.dmn; do \
		echo "--- $$m"; \
		$(BUILD_DIR)/$(BINARY) analyze "$$m" >/dev/null || exit 1; \
	done
	@echo "all example models load and analyse"

# diagrams regenerates the DMNDI in every example from its current DRG. Run it
# after changing a model's shape; the coordinates are generated, not maintained.
diagrams: build
	python3 scripts/add-diagram.py examples/*/*.dmn
	@$(MAKE) validate

# SCHEMA_GOLDEN is the generated VDJ JSON Schema. It is a test golden rather
# than a checked-in build artefact: `make test` regenerates and verifies it, so
# it cannot drift from the engine. The documentation site publishes this exact
# file at the URL the schema names as its own $$id.
SCHEMA_GOLDEN=pkg/dmn/vdj/testdata/vdj-schema.json

# docs builds the mdBook site and stages the schema beside it, so a local build
# serves /vdj-schema.json the same way production does. The staging step runs
# after mdbook, which empties its build directory first.
#
# The golden carries a trailing `// golden-hash:` line for tamper detection;
# strip it so the served file is valid JSON.
docs:
	@command -v mdbook >/dev/null 2>&1 || { \
		echo "docs: mdbook not found (cargo install mdbook, or brew install mdbook)"; exit 1; }
	mdbook build docs
	@sed '/^\/\/ golden-hash:/d' $(SCHEMA_GOLDEN) > docs/book/vdj-schema.json
	@echo "docs: built docs/book (schema staged at docs/book/vdj-schema.json)"

docs-serve:
	mdbook serve docs --open

docs-clean:
	rm -rf docs/book

clean: docs-clean
	rm -rf $(BUILD_DIR) coverage.out
