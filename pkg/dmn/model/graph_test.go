package model_test

import (
	"reflect"
	"testing"

	"github.com/frankbardon/verdict/pkg/dmn/model"
)

// build assembles a small DRG: two inputs, three decisions, one of which
// depends on the other two.
func build() *model.Definitions {
	return &model.Definitions{
		ID: "graph",
		InputData: []*model.InputData{
			{ID: "a", Name: "A"},
			{ID: "b", Name: "B"},
		},
		Decisions: []*model.Decision{
			{ID: "d1", Name: "D1", RequiredInputs: []string{"a"}},
			{ID: "d2", Name: "D2", RequiredInputs: []string{"b"}},
			{ID: "d3", Name: "D3", RequiredDecisions: []string{"d1", "d2"}},
		},
	}
}

func TestGraphOrdersDependenciesFirst(t *testing.T) {
	g, problems, err := model.BuildGraph(build())
	if err != nil {
		t.Fatalf("BuildGraph: %v", err)
	}
	if len(problems) != 0 {
		t.Fatalf("unexpected problems: %+v", problems)
	}

	pos := map[string]int{}
	for i, id := range g.Order() {
		pos[id] = i
	}
	for _, pair := range [][2]string{{"a", "d1"}, {"b", "d2"}, {"d1", "d3"}, {"d2", "d3"}} {
		if pos[pair[0]] >= pos[pair[1]] {
			t.Errorf("%s must be ordered before %s", pair[0], pair[1])
		}
	}
}

func TestGraphOrderIsDeterministic(t *testing.T) {
	// A model loaded twice must produce the same evaluation order, or traces of
	// identical evaluations will not be diffable.
	g1, _, _ := model.BuildGraph(build())
	g2, _, _ := model.BuildGraph(build())
	if !reflect.DeepEqual(g1.Order(), g2.Order()) {
		t.Errorf("order differed between builds:\n%v\n%v", g1.Order(), g2.Order())
	}
}

func TestSliceIsTheTransitiveClosure(t *testing.T) {
	g, _, _ := model.BuildGraph(build())

	slice, err := g.Slice("d1")
	if err != nil {
		t.Fatalf("Slice: %v", err)
	}
	if !reflect.DeepEqual(slice, []string{"a", "d1"}) {
		t.Errorf("slice of d1 = %v, want [a d1]", slice)
	}

	full, err := g.Slice("d3")
	if err != nil {
		t.Fatalf("Slice: %v", err)
	}
	if len(full) != 5 {
		t.Errorf("slice of d3 = %v, want all five elements", full)
	}

	if _, err := g.Slice("nope"); err == nil {
		t.Error("slicing an unknown element should fail")
	}
}

func TestLayersGroupIndependentSiblings(t *testing.T) {
	g, _, _ := model.BuildGraph(build())
	slice, _ := g.Slice("d3")
	layers := g.Layers(slice)

	if len(layers) != 3 {
		t.Fatalf("layers = %d, want 3 (inputs, d1+d2, d3)", len(layers))
	}
	if len(layers[0]) != 2 {
		t.Errorf("layer 0 = %v, want both input data", layers[0])
	}
	// d1 and d2 are independent and must share a layer, which is what makes
	// concurrent evaluation possible.
	if len(layers[1]) != 2 {
		t.Errorf("layer 1 = %v, want d1 and d2 together", layers[1])
	}
	if !reflect.DeepEqual(layers[2], []string{"d3"}) {
		t.Errorf("layer 2 = %v, want [d3]", layers[2])
	}
}

func TestCycleIsRejected(t *testing.T) {
	defs := &model.Definitions{
		ID: "cyclic",
		Decisions: []*model.Decision{
			{ID: "x", Name: "X", RequiredDecisions: []string{"y"}},
			{ID: "y", Name: "Y", RequiredDecisions: []string{"x"}},
		},
	}
	if _, _, err := model.BuildGraph(defs); err == nil {
		t.Fatal("a cyclic requirement graph was accepted")
	}
}

func TestDanglingAndDuplicateAreReportedNotFatal(t *testing.T) {
	defs := &model.Definitions{
		ID: "messy",
		Decisions: []*model.Decision{
			{ID: "x", Name: "X", RequiredInputs: []string{"ghost"}},
			{ID: "x", Name: "X duplicate"},
		},
	}
	g, problems, err := model.BuildGraph(defs)
	if err != nil {
		t.Fatalf("a dangling reference should be reported, not fatal: %v", err)
	}
	if g == nil {
		t.Fatal("no graph was returned")
	}

	kinds := map[model.ProblemKind]int{}
	for _, p := range problems {
		kinds[p.Kind]++
	}
	if kinds[model.ProblemDangling] != 1 {
		t.Errorf("dangling problems = %d, want 1", kinds[model.ProblemDangling])
	}
	if kinds[model.ProblemDuplicateID] != 1 {
		t.Errorf("duplicate-id problems = %d, want 1", kinds[model.ProblemDuplicateID])
	}
}

func TestResolveAcceptsIDHrefAndName(t *testing.T) {
	g, _, _ := model.BuildGraph(build())
	for _, ref := range []string{"d1", "#d1", "D1"} {
		e, ok := g.Resolve(ref)
		if !ok || e.ElementID() != "d1" {
			t.Errorf("Resolve(%q) = %v, %v", ref, e, ok)
		}
	}
	if _, ok := g.Resolve("missing"); ok {
		t.Error("Resolve found an element that does not exist")
	}
}

func TestHitPolicyParsingAcceptsBothSpellings(t *testing.T) {
	cases := map[string]struct {
		policy model.HitPolicy
		agg    model.Aggregation
	}{
		"":             {model.HitUnique, model.AggNone},
		"U":            {model.HitUnique, model.AggNone},
		"UNIQUE":       {model.HitUnique, model.AggNone},
		"C+":           {model.HitCollect, model.AggSum},
		"C<":           {model.HitCollect, model.AggMin},
		"C>":           {model.HitCollect, model.AggMax},
		"C#":           {model.HitCollect, model.AggCount},
		"RULE ORDER":   {model.HitRuleOrder, model.AggNone},
		"R":            {model.HitRuleOrder, model.AggNone},
		"OUTPUT ORDER": {model.HitOutputOrder, model.AggNone},
	}
	for in, want := range cases {
		p, a, err := model.ParseHitPolicy(in)
		if err != nil {
			t.Errorf("ParseHitPolicy(%q): %v", in, err)
			continue
		}
		if p != want.policy || a != want.agg {
			t.Errorf("ParseHitPolicy(%q) = %s/%s, want %s/%s", in, p, a, want.policy, want.agg)
		}
		// The shorthand must round-trip, since it is what traces and reports
		// display.
		if in != "" && len(in) <= 2 && p.Shorthand(a) != in {
			t.Errorf("Shorthand of %q = %q", in, p.Shorthand(a))
		}
	}
	if _, _, err := model.ParseHitPolicy("SOMETIMES"); err == nil {
		t.Error("an unknown hit policy was accepted")
	}
}

func TestTypeRegistryResolvesQualifiedAndPrefixedNames(t *testing.T) {
	defs := []*model.ItemDefinition{{
		Name: "tApplicant",
		Components: []*model.ItemDefinition{
			{Name: "age", TypeRef: "number"},
			{Name: "address", Components: []*model.ItemDefinition{{Name: "city", TypeRef: "string"}}},
		},
	}}
	r := model.NewTypeRegistry(defs)

	if r.Lookup("tApplicant") == nil {
		t.Error("top-level type did not resolve")
	}
	if r.Lookup("tns:tApplicant") == nil {
		t.Error("a namespace-prefixed typeRef did not resolve")
	}
	if r.Lookup("tApplicant.address") == nil {
		t.Error("a qualified component name did not resolve")
	}
	if got := r.BaseType("tApplicant.age"); got != "number" {
		t.Errorf("BaseType = %q, want number", got)
	}
	if r.Lookup("nope") != nil {
		t.Error("an unknown type resolved")
	}
}
