package verdict_test

import (
	"context"
	"testing"

	"github.com/frankbardon/verdict/pkg/diag"
	"github.com/frankbardon/verdict/pkg/verdict"
)

// TestCamundaAuthoredModelLoadsAndEvaluates is the interoperability claim: a
// DMN 1.3 document exported by Camunda Modeler — older namespace, vendor
// namespaces on the root, diagram-interchange elements Verdict has no use for,
// no declared conformance level — loads and evaluates untouched.
//
// The expected answers are the published Camunda "Dish" example's own test
// cases, so a divergence here is a divergence from a reference implementation
// rather than from our own opinion.
func TestCamundaAuthoredModelLoadsAndEvaluates(t *testing.T) {
	e, err := verdict.NewEngine()
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}
	m, err := e.LoadModel(verdict.FromFile("testdata/camunda_dish.dmn"))
	if err != nil {
		t.Fatalf("LoadModel: %v", err)
	}
	for _, d := range m.Diagnostics() {
		if d.Severity == diag.SeverityError {
			t.Errorf("unexpected error loading a Camunda export: %s", d)
		}
	}
	if m.ID != "dish" {
		t.Errorf("model id = %q, want dish", m.ID)
	}
	if got := len(m.Decisions()); got != 3 {
		t.Fatalf("decisions = %d, want 3", got)
	}

	cases := []struct {
		temperature int
		dayType     string
		wantSeason  string
		wantGuests  float64
		wantDish    string
	}{
		{temperature: 35, dayType: "Weekend", wantSeason: "Summer", wantGuests: 15,
			wantDish: "Light Salad and a nice Steak"},
		{temperature: 5, dayType: "Weekday", wantSeason: "Winter", wantGuests: 4,
			wantDish: "Roastbeef"},
		{temperature: 20, dayType: "Weekday", wantSeason: "Spring", wantGuests: 4,
			wantDish: "Dry Aged Gourmet Steak"},
		{temperature: 20, dayType: "Holiday", wantSeason: "Spring", wantGuests: 10,
			wantDish: "Stew"},
		{temperature: 15, dayType: "Weekend", wantSeason: "Spring", wantGuests: 15,
			wantDish: "Stew"},
	}

	for _, c := range cases {
		inputs := verdict.Inputs{"temperature": c.temperature, "dayType": c.dayType}
		res, err := e.Evaluate(context.Background(), m.ID, inputs)
		if err != nil {
			t.Fatalf("Evaluate(%d, %s): %v", c.temperature, c.dayType, err)
		}
		// The decision declares no `variable`, so its result is keyed by the
		// output clause name — which is exactly how the model refers to it
		// downstream and what Camunda itself returns.
		if got := res.Outputs["desiredDish"]; got != c.wantDish {
			t.Errorf("(%d°, %s) dish = %v, want %v", c.temperature, c.dayType, got, c.wantDish)
		}

		season, err := e.EvaluateDecision(context.Background(), m.ID, "season", inputs)
		if err != nil {
			t.Fatalf("EvaluateDecision(season): %v", err)
		}
		if got := season.Outputs["season"]; got != c.wantSeason {
			t.Errorf("(%d°) season = %v, want %v", c.temperature, got, c.wantSeason)
		}

		guests, err := e.EvaluateDecision(context.Background(), m.ID, "guestCount", inputs)
		if err != nil {
			t.Fatalf("EvaluateDecision(guestCount): %v", err)
		}
		if got := guests.Outputs["guestCount"]; got != c.wantGuests {
			t.Errorf("(%s) guests = %v, want %v", c.dayType, got, c.wantGuests)
		}
	}
}

// TestCamundaModelBindsInputsByPlainName checks the binding rule a third-party
// export depends on: an inputData element with no `variable` child binds under
// its own name.
func TestCamundaModelBindsInputsByPlainName(t *testing.T) {
	e, _ := mustLoadDish(t)
	res, err := e.EvaluateDecision(context.Background(), "dish", "season",
		verdict.Inputs{"temperature": 35})
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if res.Outputs["season"] != "Summer" {
		t.Errorf("season = %v, want Summer", res.Outputs["season"])
	}
}

// TestMissingInputIsADiagnosticNotACrash checks the failure mode a caller will
// actually hit: an input that was not supplied evaluates as null, with a
// diagnostic saying so, rather than aborting the evaluation.
func TestMissingInputIsADiagnosticNotACrash(t *testing.T) {
	e, _ := mustLoadDish(t)
	res, err := e.EvaluateDecision(context.Background(), "dish", "guestCount", verdict.Inputs{})
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	found := false
	for _, d := range res.Diagnostics {
		if d.Code == diag.CodeMissingInput {
			found = true
		}
	}
	if !found {
		t.Errorf("no VERDICT_EVAL_004 diagnostic for the missing input: %v", res.Diagnostics)
	}
}

func mustLoadDish(t *testing.T) (*verdict.Engine, *verdict.Model) {
	t.Helper()
	e, err := verdict.NewEngine()
	if err != nil {
		t.Fatal(err)
	}
	m, err := e.LoadModel(verdict.FromFile("testdata/camunda_dish.dmn"))
	if err != nil {
		t.Fatal(err)
	}
	return e, m
}
