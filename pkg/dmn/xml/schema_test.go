package xml_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/frankbardon/verdict/pkg/dmn/model"
	dmnxml "github.com/frankbardon/verdict/pkg/dmn/xml"
)

// schemaPath is the vendored DMN 1.3 schema. See testdata/schema/README.md for
// why it is vendored rather than fetched.
const schemaPath = "testdata/schema/DMN13.xsd"

// validate runs a document through xmllint against the DMN 1.3 schema.
//
// DMN 1.3 is the version validated against because it is what the mainstream
// tooling reads. A document Verdict writes is worth little if Camunda Modeler
// will not open it, and "it parses in our own reader" is not the same claim.
func validate(t *testing.T, name string, doc []byte) {
	t.Helper()
	requireXMLLint(t)

	path := filepath.Join(t.TempDir(), "model.dmn")
	if err := os.WriteFile(path, doc, 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command("xmllint", "--noout", "--schema", schemaPath, path).CombinedOutput()
	if err != nil {
		t.Errorf("%s does not validate against DMN 1.3:\n%s", name, indent(string(out)))
	}
}

func requireXMLLint(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("xmllint"); err != nil {
		t.Skip("xmllint not on PATH; install libxml2 to run schema validation")
	}
}

func indent(s string) string {
	var b strings.Builder
	for _, line := range strings.Split(strings.TrimSpace(s), "\n") {
		b.WriteString("    " + line + "\n")
	}
	return b.String()
}

func examplePaths() []string {
	return []string{
		"../../../examples/loan_approval/loan_approval.dmn",
		"../../../examples/content_moderation/content_moderation.dmn",
		"../../../examples/pricing/pricing.dmn",
	}
}

// TestExamplesValidateAgainstTheDMNSchema is the interoperability guarantee the
// examples make. They are what a user opens in a modeller first, and a shipped
// example that will not load teaches the wrong thing about the whole project.
func TestExamplesValidateAgainstTheDMNSchema(t *testing.T) {
	for _, path := range examplePaths() {
		t.Run(filepath.Base(path), func(t *testing.T) {
			doc, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			validate(t, path, doc)
		})
	}
}

// TestWrittenModelsValidateAgainstTheDMNSchema covers the other direction: a
// model that came in through the reader and went out through the writer must
// still be loadable by a third-party tool.
func TestWrittenModelsValidateAgainstTheDMNSchema(t *testing.T) {
	for _, path := range append(examplePaths(), "../../verdict/testdata/camunda_dish.dmn") {
		t.Run(filepath.Base(path), func(t *testing.T) {
			raw, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			defs, _, err := dmnxml.Parse(raw)
			if err != nil {
				t.Fatalf("parsing: %v", err)
			}
			written, err := dmnxml.Marshal(defs)
			if err != nil {
				t.Fatalf("writing: %v", err)
			}
			validate(t, "written "+filepath.Base(path), written)
		})
	}
}

// TestWriterDefaultsToDMN13 pins the interoperability choice. Emitting the
// newest namespace Verdict can read would be defensible and wrong: Camunda
// Modeler and dmn-js reject anything but 1.3.
func TestWriterDefaultsToDMN13(t *testing.T) {
	defs := &model.Definitions{ID: "m", Name: "M", Namespace: "urn:test"}
	out, err := dmnxml.Marshal(defs)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), dmnxml.NS13) {
		t.Errorf("default output does not use the DMN 1.3 namespace:\n%s", out)
	}

	newer, err := dmnxml.MarshalWith(defs, dmnxml.Options{Namespace: dmnxml.NS15})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(newer), dmnxml.NS15) {
		t.Errorf("an explicit 1.5 target was not honoured:\n%s", newer)
	}
}

// TestVerdictMetadataIsNamespaced checks that Verdict's own attributes travel
// where the schema allows a foreign attribute, and nowhere else. Bare `version`
// and `conformanceLevel` on <definitions> are not DMN attributes and make the
// whole document fail validation.
func TestVerdictMetadataIsNamespaced(t *testing.T) {
	defs := &model.Definitions{
		ID: "m", Name: "M", Namespace: "urn:test",
		Version: "2.1.0", Level: model.Level3,
	}
	out, err := dmnxml.Marshal(defs)
	if err != nil {
		t.Fatal(err)
	}
	doc := string(out)
	for _, want := range []string{`verdict:version="2.1.0"`, `verdict:conformanceLevel="feel"`} {
		if !strings.Contains(doc, want) {
			t.Errorf("output is missing %s:\n%s", want, doc)
		}
	}
	// Scope the negative check to the <definitions> element: the XML
	// declaration legitimately carries a bare `version="1.0"`.
	root := doc[strings.Index(doc, "<definitions"):]
	root = root[:strings.Index(root, ">")]
	if strings.Contains(root, ` version="`) || strings.Contains(root, ` conformanceLevel="`) {
		t.Errorf("<definitions> carries an unqualified Verdict attribute:\n%s", root)
	}
	validate(t, "metadata", out)

	// And it must survive the round trip, or namespacing it would have traded
	// one bug for another.
	back, _, err := dmnxml.Parse(out)
	if err != nil {
		t.Fatal(err)
	}
	if back.Version != "2.1.0" || back.Level != model.Level3 {
		t.Errorf("round trip lost the metadata: version=%q level=%v", back.Version, back.Level)
	}
}

// TestAgentDecisionTravelsInExtensionElements is the placement rule. The
// decision-logic slot accepts only DMN's expression substitution group, so a
// foreign element there fails validation — which is the opposite of the
// graceful degradation the extension is supposed to provide.
func TestAgentDecisionTravelsInExtensionElements(t *testing.T) {
	raw, err := os.ReadFile("../../../examples/loan_approval/loan_approval.dmn")
	if err != nil {
		t.Fatal(err)
	}
	defs, _, err := dmnxml.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	out, err := dmnxml.Marshal(defs)
	if err != nil {
		t.Fatal(err)
	}
	doc := string(out)

	idx := strings.Index(doc, "<verdict:agentDecision")
	if idx < 0 {
		t.Fatal("the agent decision was not written at all")
	}
	ext := strings.LastIndex(doc[:idx], "<extensionElements>")
	closeExt := strings.LastIndex(doc[:idx], "</extensionElements>")
	if ext < 0 || ext < closeExt {
		t.Error("the agent decision was not written inside extensionElements")
	}
	validate(t, "agent decision", out)

	// It must still be read back as decision logic, not merely preserved.
	back, _, err := dmnxml.Parse(out)
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range back.Decisions {
		if d.ID != "risk_tier" {
			continue
		}
		if _, ok := d.Logic.(*model.AgentDecision); !ok {
			t.Errorf("risk_tier logic came back as %T, want *model.AgentDecision", d.Logic)
		}
		return
	}
	t.Error("risk_tier was lost on the round trip")
}

// TestWrittenModelCarriesADiagram checks the other half of "opens in a
// modeller": a schema-valid document with no DMNDI renders as an empty canvas
// in every editor built on dmn-js.
func TestWrittenModelCarriesADiagram(t *testing.T) {
	raw, err := os.ReadFile("../../../examples/pricing/pricing.dmn")
	if err != nil {
		t.Fatal(err)
	}
	defs, _, err := dmnxml.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}

	out, err := dmnxml.Marshal(defs)
	if err != nil {
		t.Fatal(err)
	}
	doc := string(out)
	if !strings.Contains(doc, "<dmndi:DMNDI>") {
		t.Fatal("no diagram interchange was written")
	}
	// Every drawable DRG element needs a shape, or it is invisible in the editor.
	for _, d := range defs.Decisions {
		if !strings.Contains(doc, `dmnElementRef="`+d.ID+`"`) {
			t.Errorf("decision %s has no shape", d.ID)
		}
	}
	for _, in := range defs.InputData {
		if !strings.Contains(doc, `dmnElementRef="`+in.ID+`"`) {
			t.Errorf("input data %s has no shape", in.ID)
		}
	}
	// Coordinates must be on-canvas: a diagram laid out at negative offsets
	// opens scrolled away from its own content and looks broken.
	if strings.Contains(doc, `x="-`) || strings.Contains(doc, `y="-`) {
		t.Error("the layout placed shapes at negative coordinates")
	}

	// Turning the diagram off must still produce a valid document.
	plain, err := dmnxml.MarshalWith(defs, dmnxml.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(plain), "DMNDI") {
		t.Error("a diagram was written despite Options.Diagram being false")
	}
	validate(t, "no diagram", plain)
}

// TestItemDefinitionHonoursTheSchemaChoice covers a trap that only shows up in
// validation: tItemDefinition is an xsd:choice, so a definition is either a
// constrained simple type or a structure — never both.
func TestItemDefinitionHonoursTheSchemaChoice(t *testing.T) {
	defs := &model.Definitions{
		ID: "m", Name: "M", Namespace: "urn:test",
		ItemDefinitions: []*model.ItemDefinition{
			{
				ID: "t1", Name: "tStruct",
				// A structure that also carries a typeRef: the reader tolerates
				// it, and the writer must not emit both branches.
				TypeRef: "string",
				Components: []*model.ItemDefinition{
					{ID: "t1a", Name: "field", TypeRef: "number"},
				},
			},
			{ID: "t2", Name: "tSimple", TypeRef: "string", AllowedValues: `"a","b"`},
			{ID: "t3", Name: "tBare"},
		},
	}
	out, err := dmnxml.Marshal(defs)
	if err != nil {
		t.Fatal(err)
	}
	validate(t, "item definitions", out)
}
