package vdj

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	jsonschema "github.com/santhosh-tekuri/jsonschema/v6"

	"github.com/frankbardon/verdict/pkg/dmn/model"
	dmnxml "github.com/frankbardon/verdict/pkg/dmn/xml"
)

// TestSchemaGolden pins the published contract. A changed VDJ struct field, a
// new hit policy, a new boxed-expression kind or an edited description all
// change this output; regenerate with:
//
//	go test ./pkg/dmn/vdj/ -run TestSchemaGolden -update
func TestSchemaGolden(t *testing.T) {
	compareGolden(t, "vdj-schema.json", BuildSchema())
}

// TestSchemaIsItselfValid checks the document against the draft it declares.
// A schema with a malformed keyword still marshals; it just silently stops
// constraining anything, which is worse than not publishing one.
func TestSchemaIsItselfValid(t *testing.T) {
	if _, err := compileSchema(t); err != nil {
		t.Fatalf("the generated schema is not a valid draft 2020-12 schema: %v", err)
	}
}

// TestSchemaEnumsMatchTheRegistry is the anti-drift hinge. Every enum in the
// published schema must carry exactly the value set the engine executes
// against, so adding a hit policy or a failure mode cannot leave the contract
// advertising the old set.
func TestSchemaEnumsMatchTheRegistry(t *testing.T) {
	var doc struct {
		Defs map[string]struct {
			Properties map[string]struct {
				Enum []string `json:"enum"`
			} `json:"properties"`
		} `json:"$defs"`
	}
	if err := json.Unmarshal(BuildSchema(), &doc); err != nil {
		t.Fatalf("unmarshalling the schema: %v", err)
	}

	for key, want := range enumOverrides() {
		defName, prop, _ := strings.Cut(key, ".")
		def, ok := doc.Defs[defName]
		if !ok {
			t.Errorf("$defs.%s is missing from the schema", defName)
			continue
		}
		got := def.Properties[prop].Enum
		if !reflect.DeepEqual(got, want) {
			t.Errorf("%s enum drifted from the registry:\n  schema:   %v\n  registry: %v", key, got, want)
		}
	}
}

// TestSchemaCoversEveryExpressionField guards the one hand-written table in the
// generator. Every property of the flat Expression union belongs either to the
// shared header or to exactly one kind; a field added to the struct and to no
// row would be permitted for every kind, silently un-discriminating the union.
func TestSchemaCoversEveryExpressionField(t *testing.T) {
	owner := map[string]model.Kind{}
	for kind, fields := range expressionFields {
		for _, f := range fields {
			if prev, dup := owner[f]; dup {
				t.Errorf("field %q is claimed by both %s and %s", f, prev, kind)
			}
			owner[f] = kind
		}
	}
	for _, f := range expressionHeaderFields {
		if kind, ok := owner[f]; ok {
			t.Errorf("header field %q is also claimed by kind %s", f, kind)
		}
	}

	et := reflect.TypeFor[Expression]()
	for i := 0; i < et.NumField(); i++ {
		name, _, skip := jsonFieldName(et.Field(i))
		if skip {
			continue
		}
		if _, ok := owner[name]; ok {
			continue
		}
		if slices.Contains(expressionHeaderFields, name) {
			continue
		}
		t.Errorf("Expression field %q belongs to no kind and is not a header field; "+
			"add it to expressionFields in schema.go", name)
	}

	// And every kind the engine knows must have a row, or documents using it
	// would be rejected outright.
	for _, kind := range model.AllKinds() {
		if _, ok := expressionFields[kind]; !ok {
			t.Errorf("boxed-expression kind %s has no row in expressionFields", kind)
		}
	}
}

// TestSchemaAcceptsEveryShippedDocument is the claim that matters: the schema
// describes the documents Verdict actually writes. Every VDJ fixture, and every
// example model projected from DMN XML into VDJ, must validate.
func TestSchemaAcceptsEveryShippedDocument(t *testing.T) {
	schema, err := compileSchema(t)
	if err != nil {
		t.Fatalf("compiling the schema: %v", err)
	}

	fixtures, err := filepath.Glob("../../verdict/testdata/*.vdj")
	if err != nil {
		t.Fatal(err)
	}
	if len(fixtures) == 0 {
		t.Fatal("no .vdj fixtures found; the test is not checking anything")
	}
	for _, path := range fixtures {
		t.Run(filepath.Base(path), func(t *testing.T) {
			raw, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			validateDocument(t, schema, raw)
		})
	}

	models, err := filepath.Glob("../../../examples/*/*.dmn")
	if err != nil {
		t.Fatal(err)
	}
	if len(models) == 0 {
		t.Fatal("no example models found; the test is not checking anything")
	}
	for _, path := range models {
		t.Run("projected/"+filepath.Base(path), func(t *testing.T) {
			raw, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			defs, _, err := dmnxml.Parse(raw)
			if err != nil {
				t.Fatalf("parsing: %v", err)
			}
			out, err := Marshal(defs)
			if err != nil {
				t.Fatalf("projecting to VDJ: %v", err)
			}
			validateDocument(t, schema, out)
		})
	}
}

// TestSchemaRejectsAMisplacedPayload checks the discrimination actually bites.
// A decision table carrying a list's `elements` is the mistake the flat union
// invites, and the reader ignores it silently — the schema is the only place it
// gets caught.
func TestSchemaRejectsAMisplacedPayload(t *testing.T) {
	schema, err := compileSchema(t)
	if err != nil {
		t.Fatal(err)
	}

	cases := map[string]string{
		"table with list elements": `{"vdj":"1.0","id":"m","decisions":[
			{"name":"d","logic":{"kind":"decisionTable","elements":[]}}]}`,
		"invocation with no callee": `{"vdj":"1.0","id":"m","decisions":[
			{"name":"d","logic":{"kind":"invocation"}}]}`,
		"agent decision with no agent": `{"vdj":"1.0","id":"m","decisions":[
			{"name":"d","logic":{"kind":"agentDecision"}}]}`,
		"unknown hit policy": `{"vdj":"1.0","id":"m","decisions":[
			{"name":"d","logic":{"kind":"decisionTable","hit_policy":"MAJORITY"}}]}`,
		"mistyped field": `{"vdj":"1.0","id":"m","decisions":[
			{"name":"d","logick":{"kind":"context"}}]}`,
		"unknown boxed expression kind": `{"vdj":"1.0","id":"m","decisions":[
			{"name":"d","logic":{"kind":"spreadsheet"}}]}`,
	}
	for name, doc := range cases {
		t.Run(name, func(t *testing.T) {
			var v any
			if err := json.Unmarshal([]byte(doc), &v); err != nil {
				t.Fatalf("the test document is not valid JSON: %v", err)
			}
			if err := schema.Validate(v); err == nil {
				t.Error("the schema accepted a document it should have rejected")
			}
		})
	}
}

// TestSchemaVersionMatchesTheFormat holds the version the schema advertises
// equal to the one the writer stamps. Drift here publishes a contract for a
// format version nobody emits.
func TestSchemaVersionMatchesTheFormat(t *testing.T) {
	var doc struct {
		Description string `json:"description"`
		Defs        map[string]struct {
			Properties map[string]struct {
				Description string `json:"description"`
			} `json:"properties"`
		} `json:"$defs"`
	}
	if err := json.Unmarshal(BuildSchema(), &doc); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(doc.Description, Version) {
		t.Errorf("the schema description does not name format version %s: %q", Version, doc.Description)
	}
	if got := doc.Defs["Document"].Properties["vdj"].Description; !strings.Contains(got, Version) {
		t.Errorf("the `vdj` property description does not name format version %s: %q", Version, got)
	}
}

// compileSchema compiles the generated schema under its own $id, so a document
// that references the published URL resolves against the same bytes.
func compileSchema(t *testing.T) (*jsonschema.Schema, error) {
	t.Helper()
	var doc any
	if err := json.Unmarshal(BuildSchema(), &doc); err != nil {
		t.Fatalf("the generated schema is not valid JSON: %v", err)
	}
	c := jsonschema.NewCompiler()
	if err := c.AddResource(SchemaID, doc); err != nil {
		return nil, err
	}
	return c.Compile(SchemaID)
}

func validateDocument(t *testing.T, schema *jsonschema.Schema, raw []byte) {
	t.Helper()
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatalf("the document is not valid JSON: %v", err)
	}
	if err := schema.Validate(v); err != nil {
		t.Errorf("the document does not validate against the published schema:\n%v", err)
	}
}

// TestSchemaIDIsWhatTheSitePublishes closes the loop on the `$id`. The
// identifier is a promise that fetching it returns this document; nothing in
// the generator can keep that promise, only the workflow that copies the golden
// into the site. A rename on either side breaks resolution silently — a
// validator would simply fetch a 404 and, depending on its configuration,
// either fail confusingly or skip validation altogether.
func TestSchemaIDIsWhatTheSitePublishes(t *testing.T) {
	const (
		workflow = "../../../.github/workflows/docs.yml"
		sitePath = "https://frankbardon.github.io/verdict/"
	)
	if !strings.HasPrefix(SchemaID, sitePath) {
		t.Fatalf("SchemaID %q is not under the published site root %q", SchemaID, sitePath)
	}
	published := "docs/book/" + strings.TrimPrefix(SchemaID, sitePath)

	raw, err := os.ReadFile(workflow)
	if err != nil {
		t.Fatalf("reading the publishing workflow: %v", err)
	}
	doc := string(raw)
	if !strings.Contains(doc, published) {
		t.Errorf("the workflow does not publish the schema to %s, so %s will not resolve", published, SchemaID)
	}
	if !strings.Contains(doc, "pkg/dmn/vdj/testdata/vdj-schema.json") {
		t.Error("the workflow does not copy the golden; the published schema would be whatever is in docs/")
	}
}
