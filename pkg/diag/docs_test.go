package diag

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"strconv"
	"strings"
	"testing"
)

// docsCodeTable is the published reference page. Every code the package
// declares must appear in it.
const docsCodeTable = "../../docs/src/reference/diagnostics.md"

// TestEveryCodeIsDocumented holds the reference page complete.
//
// A diagnostic code is a contract with whoever greps the logs at three in the
// morning; an undocumented one sends them into the source. The code list is
// read out of this package's own source rather than from a hand-maintained
// registry, so there is no third list to keep in step.
func TestEveryCodeIsDocumented(t *testing.T) {
	page, err := os.ReadFile(docsCodeTable)
	if err != nil {
		t.Fatalf("reading the reference page: %v", err)
	}
	doc := string(page)

	codes := declaredCodes(t)
	if len(codes) == 0 {
		t.Fatal("no diagnostic codes found in the package source; the test is not checking anything")
	}
	for name, code := range codes {
		if !strings.Contains(doc, "`"+code+"`") {
			t.Errorf("%s (%s) is not documented in %s", code, name, docsCodeTable)
		}
	}
}

// TestNoCodeIsDocumentedTwice catches a copy-and-paste in the page that would
// give one code two meanings — the exact failure the stable-code promise is
// meant to rule out.
func TestNoCodeIsDocumentedTwice(t *testing.T) {
	page, err := os.ReadFile(docsCodeTable)
	if err != nil {
		t.Fatalf("reading the reference page: %v", err)
	}
	doc := string(page)
	for name, code := range declaredCodes(t) {
		// Count only table rows: a row begins the line, so this ignores the
		// prose references further down the page.
		rows := strings.Count(doc, "| `"+code+"` |")
		if rows > 1 {
			t.Errorf("%s (%s) has %d rows in %s", code, name, rows, docsCodeTable)
		}
	}
}

// declaredCodes reads every `Code<X> Code = "..."` constant out of the
// package's own source.
func declaredCodes(t *testing.T) map[string]string {
	t.Helper()
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("reading the package directory: %v", err)
	}

	fset := token.NewFileSet()
	out := map[string]string{}
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatalf("parsing %s: %v", name, err)
		}
		for _, decl := range file.Decls {
			gen, ok := decl.(*ast.GenDecl)
			if !ok || gen.Tok != token.CONST {
				continue
			}
			for _, spec := range gen.Specs {
				vs, ok := spec.(*ast.ValueSpec)
				if !ok || len(vs.Names) != 1 || len(vs.Values) != 1 {
					continue
				}
				ident, ok := vs.Type.(*ast.Ident)
				if !ok || ident.Name != "Code" {
					continue
				}
				lit, ok := vs.Values[0].(*ast.BasicLit)
				if !ok || lit.Kind != token.STRING {
					continue
				}
				value, err := strconv.Unquote(lit.Value)
				if err != nil {
					continue
				}
				out[vs.Names[0].Name] = value
			}
		}
	}
	return out
}
