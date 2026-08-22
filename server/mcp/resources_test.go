package mcp

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/frankbardon/verdict/pkg/dmn/vdj"
)

// TestResourceCatalogue checks every resource is addressable, readable and
// non-empty, and that the URIs are unique — a duplicate would shadow one
// resource with another depending on registration order.
func TestResourceCatalogue(t *testing.T) {
	seen := map[string]bool{}
	for _, r := range Resources() {
		if !strings.HasPrefix(r.URI, URIScheme) {
			t.Errorf("resource %s is not under the %s scheme", r.URI, URIScheme)
		}
		if seen[r.URI] {
			t.Errorf("duplicate resource URI %s", r.URI)
		}
		seen[r.URI] = true
		if r.Name == "" || r.Description == "" || r.MIMEType == "" {
			t.Errorf("resource %s is missing a name, description or MIME type", r.URI)
		}

		body, err := r.Read(context.Background())
		if err != nil {
			t.Errorf("reading %s: %v", r.URI, err)
			continue
		}
		if len(body) == 0 {
			t.Errorf("resource %s is empty", r.URI)
		}
		if r.MIMEType == "application/json" {
			var v any
			if err := json.Unmarshal(body, &v); err != nil {
				t.Errorf("resource %s claims application/json but does not parse: %v", r.URI, err)
			}
		}

		got, err := Resource(r.URI)
		if err != nil {
			t.Errorf("Resource(%q): %v", r.URI, err)
			continue
		}
		if got.Name != r.Name {
			t.Errorf("Resource(%q) returned %q", r.URI, got.Name)
		}
	}

	if _, err := Resource("verdict://nothing"); err == nil {
		t.Error("Resource returned a descriptor for a URI that does not exist")
	}
}

// TestSchemaResourceMatchesTheLibrary holds the MCP surface equal to the
// generator. The published file, `verdict schema` and this resource are three
// deliveries of one contract; if they can differ, none of them is the contract.
func TestSchemaResourceMatchesTheLibrary(t *testing.T) {
	r, err := Resource(SchemaResourceURI)
	if err != nil {
		t.Fatal(err)
	}
	body, err := r.Read(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != string(vdj.BuildSchema()) {
		t.Error("the MCP schema resource is not vdj.BuildSchema() verbatim")
	}
}
