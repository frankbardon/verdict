package dmn_test

import (
	"os"
	"reflect"
	"testing"

	"github.com/frankbardon/verdict/pkg/dmn/model"
	"github.com/frankbardon/verdict/pkg/dmn/vdj"
	dmnxml "github.com/frankbardon/verdict/pkg/dmn/xml"
)

const loanModel = "../../examples/loan_approval/loan_approval.dmn"

func readLoan(t *testing.T) *model.Definitions {
	t.Helper()
	raw, err := os.ReadFile(loanModel)
	if err != nil {
		t.Fatalf("reading fixture: %v", err)
	}
	defs, diags, err := dmnxml.Parse(raw)
	if err != nil {
		t.Fatalf("parsing: %v", err)
	}
	for _, d := range diags {
		if d.Severity == "error" {
			t.Errorf("parse error: %s", d)
		}
	}
	return defs
}

// TestXMLRoundTrip writes a parsed model back out and re-reads it, asserting
// that nothing the evaluator depends on was lost. Round-tripping is what makes
// Verdict safe to put between a modelling tool and production.
func TestXMLRoundTrip(t *testing.T) {
	original := readLoan(t)

	out, err := dmnxml.Marshal(original)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	reparsed, diags, err := dmnxml.Parse(out)
	if err != nil {
		t.Fatalf("re-parsing written XML: %v\n%s", err, out)
	}
	for _, d := range diags {
		if d.Severity == "error" {
			t.Errorf("re-parse error: %s\n%s", d, out)
		}
	}
	assertSameModel(t, original, reparsed)

	// Writing is deterministic: the same model must produce identical bytes.
	again, err := dmnxml.Marshal(original)
	if err != nil {
		t.Fatalf("second Marshal: %v", err)
	}
	if string(out) != string(again) {
		t.Error("XML output is not deterministic across two writes of the same model")
	}
}

// TestVDJRoundTrip checks the JSON projection is lossless in the same sense.
func TestVDJRoundTrip(t *testing.T) {
	original := readLoan(t)

	out, err := vdj.Marshal(original)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	reparsed, diags, err := vdj.Parse(out)
	if err != nil {
		t.Fatalf("re-parsing written VDJ: %v\n%s", err, out)
	}
	for _, d := range diags {
		if d.Severity == "error" {
			t.Errorf("re-parse error: %s", d)
		}
	}
	assertSameModel(t, original, reparsed)
}

// TestCrossFormatRoundTrip goes XML to VDJ to XML, which is the path a JSON
// toolchain puts a Camunda-authored model through.
func TestCrossFormatRoundTrip(t *testing.T) {
	original := readLoan(t)

	asJSON, err := vdj.Marshal(original)
	if err != nil {
		t.Fatalf("to VDJ: %v", err)
	}
	viaJSON, _, err := vdj.Parse(asJSON)
	if err != nil {
		t.Fatalf("from VDJ: %v", err)
	}
	asXML, err := dmnxml.Marshal(viaJSON)
	if err != nil {
		t.Fatalf("to XML: %v", err)
	}
	final, _, err := dmnxml.Parse(asXML)
	if err != nil {
		t.Fatalf("from XML: %v\n%s", err, asXML)
	}
	assertSameModel(t, original, final)
}

// assertSameModel compares the parts of a model the evaluator reads. The
// content hash and the exporter fields are expected to differ, since the
// round-trip produces new bytes.
func assertSameModel(t *testing.T, want, got *model.Definitions) {
	t.Helper()

	if got.ID != want.ID || got.Name != want.Name || got.Version != want.Version {
		t.Errorf("identity = %q/%q/%q, want %q/%q/%q",
			got.ID, got.Name, got.Version, want.ID, want.Name, want.Version)
	}
	if got.Level != want.Level {
		t.Errorf("conformance level = %v, want %v", got.Level, want.Level)
	}
	if len(got.Decisions) != len(want.Decisions) {
		t.Fatalf("decisions = %d, want %d", len(got.Decisions), len(want.Decisions))
	}
	if len(got.ItemDefinitions) != len(want.ItemDefinitions) {
		t.Errorf("item definitions = %d, want %d", len(got.ItemDefinitions), len(want.ItemDefinitions))
	}
	if len(got.DecisionServices) != len(want.DecisionServices) {
		t.Errorf("decision services = %d, want %d", len(got.DecisionServices), len(want.DecisionServices))
	}

	for i, wd := range want.Decisions {
		gd := got.Decisions[i]
		if gd.ID != wd.ID || gd.Name != wd.Name {
			t.Errorf("decision %d = %q/%q, want %q/%q", i, gd.ID, gd.Name, wd.ID, wd.Name)
			continue
		}
		if !reflect.DeepEqual(gd.RequiredInputs, wd.RequiredInputs) {
			t.Errorf("%s required inputs = %v, want %v", wd.ID, gd.RequiredInputs, wd.RequiredInputs)
		}
		if !reflect.DeepEqual(gd.RequiredDecisions, wd.RequiredDecisions) {
			t.Errorf("%s required decisions = %v, want %v", wd.ID, gd.RequiredDecisions, wd.RequiredDecisions)
		}
		assertSameExpression(t, wd.ID, wd.Logic, gd.Logic)
	}

	for i, wb := range want.BKMs {
		gb := got.BKMs[i]
		if gb.Name != wb.Name {
			t.Errorf("BKM %d = %q, want %q", i, gb.Name, wb.Name)
			continue
		}
		if (gb.Encapsulated == nil) != (wb.Encapsulated == nil) {
			t.Errorf("BKM %s encapsulated logic presence changed", wb.Name)
			continue
		}
		if wb.Encapsulated != nil {
			assertSameExpression(t, wb.ID, wb.Encapsulated, gb.Encapsulated)
		}
	}

	for i, ws := range want.DecisionServices {
		gs := got.DecisionServices[i]
		if !reflect.DeepEqual(gs.OutputDecisions, ws.OutputDecisions) {
			t.Errorf("service %s outputs = %v, want %v", ws.Name, gs.OutputDecisions, ws.OutputDecisions)
		}
		if !reflect.DeepEqual(gs.EncapsulatedDecisions, ws.EncapsulatedDecisions) {
			t.Errorf("service %s encapsulated = %v, want %v",
				ws.Name, gs.EncapsulatedDecisions, ws.EncapsulatedDecisions)
		}
	}
}

func assertSameExpression(t *testing.T, owner string, want, got model.Expression) {
	t.Helper()
	if want == nil || got == nil {
		if want != got {
			t.Errorf("%s: logic presence changed (want %v, got %v)", owner, want, got)
		}
		return
	}
	if want.Kind() != got.Kind() {
		t.Errorf("%s: kind = %s, want %s", owner, got.Kind(), want.Kind())
		return
	}
	switch w := want.(type) {
	case *model.LiteralExpression:
		g := got.(*model.LiteralExpression)
		if g.Text != w.Text {
			t.Errorf("%s: literal text = %q, want %q", owner, g.Text, w.Text)
		}

	case *model.DecisionTable:
		g := got.(*model.DecisionTable)
		if g.HitPolicy != w.HitPolicy || g.Aggregation != w.Aggregation {
			t.Errorf("%s: hit policy = %s/%s, want %s/%s",
				owner, g.HitPolicy, g.Aggregation, w.HitPolicy, w.Aggregation)
		}
		if len(g.Rules) != len(w.Rules) {
			t.Errorf("%s: rules = %d, want %d", owner, len(g.Rules), len(w.Rules))
			return
		}
		for i := range w.Rules {
			if !reflect.DeepEqual(g.Rules[i].InputEntries, w.Rules[i].InputEntries) {
				t.Errorf("%s rule %d inputs = %v, want %v",
					owner, i, g.Rules[i].InputEntries, w.Rules[i].InputEntries)
			}
			if !reflect.DeepEqual(g.Rules[i].OutputEntries, w.Rules[i].OutputEntries) {
				t.Errorf("%s rule %d outputs = %v, want %v",
					owner, i, g.Rules[i].OutputEntries, w.Rules[i].OutputEntries)
			}
			if !reflect.DeepEqual(g.Rules[i].Annotations, w.Rules[i].Annotations) {
				t.Errorf("%s rule %d annotations = %v, want %v",
					owner, i, g.Rules[i].Annotations, w.Rules[i].Annotations)
			}
		}
		for i := range w.Inputs {
			if g.Inputs[i].Expression != w.Inputs[i].Expression {
				t.Errorf("%s input %d = %q, want %q", owner, i, g.Inputs[i].Expression, w.Inputs[i].Expression)
			}
			if g.Inputs[i].Values != w.Inputs[i].Values {
				t.Errorf("%s input %d values = %q, want %q", owner, i, g.Inputs[i].Values, w.Inputs[i].Values)
			}
		}
		for i := range w.Outputs {
			if g.Outputs[i].Values != w.Outputs[i].Values ||
				g.Outputs[i].DefaultValue != w.Outputs[i].DefaultValue {
				t.Errorf("%s output %d = %q/%q, want %q/%q", owner, i,
					g.Outputs[i].Values, g.Outputs[i].DefaultValue,
					w.Outputs[i].Values, w.Outputs[i].DefaultValue)
			}
		}

	case *model.Invocation:
		g := got.(*model.Invocation)
		if g.Called != w.Called {
			t.Errorf("%s: invocation calls %q, want %q", owner, g.Called, w.Called)
		}
		if len(g.Bindings) != len(w.Bindings) {
			t.Errorf("%s: bindings = %d, want %d", owner, len(g.Bindings), len(w.Bindings))
			return
		}
		for i := range w.Bindings {
			if g.Bindings[i].Parameter.Name != w.Bindings[i].Parameter.Name {
				t.Errorf("%s binding %d = %q, want %q", owner, i,
					g.Bindings[i].Parameter.Name, w.Bindings[i].Parameter.Name)
			}
			assertSameExpression(t, owner, w.Bindings[i].Value, g.Bindings[i].Value)
		}

	case *model.ContextExpression:
		g := got.(*model.ContextExpression)
		if len(g.Entries) != len(w.Entries) {
			t.Errorf("%s: context entries = %d, want %d", owner, len(g.Entries), len(w.Entries))
			return
		}
		for i := range w.Entries {
			assertSameExpression(t, owner, w.Entries[i].Value, g.Entries[i].Value)
		}

	case *model.FunctionDefinition:
		g := got.(*model.FunctionDefinition)
		if len(g.Parameters) != len(w.Parameters) {
			t.Errorf("%s: parameters = %d, want %d", owner, len(g.Parameters), len(w.Parameters))
			return
		}
		for i := range w.Parameters {
			if g.Parameters[i].Name != w.Parameters[i].Name {
				t.Errorf("%s parameter %d = %q, want %q", owner, i,
					g.Parameters[i].Name, w.Parameters[i].Name)
			}
		}
		assertSameExpression(t, owner, w.Body, g.Body)

	case *model.AgentDecision:
		g := got.(*model.AgentDecision)
		if len(g.Bindings) != len(w.Bindings) {
			t.Errorf("%s: agent bindings = %d, want %d", owner, len(g.Bindings), len(w.Bindings))
		}
		for i := range w.Bindings {
			if i >= len(g.Bindings) {
				break
			}
			if *g.Bindings[i] != *w.Bindings[i] {
				t.Errorf("%s agent binding %d = %+v, want %+v", owner, i, *g.Bindings[i], *w.Bindings[i])
			}
		}
		if !reflect.DeepEqual(g.OutputType, w.OutputType) {
			t.Errorf("%s: agent output type = %+v, want %+v", owner, g.OutputType, w.OutputType)
		}
		if g.Validator != w.Validator {
			t.Errorf("%s: validator = %q, want %q", owner, g.Validator, w.Validator)
		}
		if g.Policy != w.Policy {
			t.Errorf("%s: agent policy = %+v, want %+v", owner, g.Policy, w.Policy)
		}
	}
}

func TestDurationRoundTrip(t *testing.T) {
	for _, s := range []string{"PT5S", "PT1M30S", "PT2H", "P1DT2H3M4S", "PT0S", "-PT30S"} {
		d, err := model.ParseDuration(s)
		if err != nil {
			t.Errorf("ParseDuration(%q): %v", s, err)
			continue
		}
		if got := model.FormatDuration(d); got != s {
			t.Errorf("round trip of %q produced %q", s, got)
		}
	}
	// Calendar units have no fixed length and are refused rather than guessed at.
	for _, s := range []string{"P1Y", "P2M", "1H", ""} {
		if _, err := model.ParseDuration(s); err == nil {
			t.Errorf("ParseDuration(%q) should have failed", s)
		}
	}
}
