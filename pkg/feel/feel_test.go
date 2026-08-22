package feel

import (
	"math"
	"testing"
	"time"
)

func mustEval(t *testing.T, e *Evaluator, text string, vars map[string]any) any {
	t.Helper()
	v, err := e.EvalText(text, vars)
	if err != nil {
		t.Fatalf("EvalText(%q): %v", text, err)
	}
	return ToGo(v)
}

func newFEEL(t *testing.T) *Evaluator {
	t.Helper()
	e, err := NewEvaluator(FEEL)
	if err != nil {
		t.Fatalf("NewEvaluator: %v", err)
	}
	return e
}

func TestEvalBasics(t *testing.T) {
	e := newFEEL(t)
	vars := map[string]any{"Applicant": FromGo(map[string]any{"age": 42, "name": "Ada"})}
	if got := mustEval(t, e, "Applicant.age + 1", vars); got != 43.0 {
		t.Errorf("Applicant.age + 1 = %v, want 43", got)
	}
	if got := mustEval(t, e, `Applicant.name = "Ada"`, vars); got != true {
		t.Errorf("name comparison = %v, want true", got)
	}
	if got := mustEval(t, e, "2 + 3 * 4", nil); got != 14.0 {
		t.Errorf("arithmetic = %v, want 14", got)
	}
}

func TestUnaryTests(t *testing.T) {
	e := newFEEL(t)
	cases := []struct {
		entry string
		input any
		want  bool
	}{
		{"", 5, true},
		{"-", 5, true},
		{"< 10", 5, true},
		{"< 10", 15, false},
		{"[1..10]", 10, true},
		{"[1..10)", 10, false},
		{"1, 3, 5", 3, true},
		{"1, 3, 5", 4, false},
		{`"low", "medium"`, "medium", true},
		{`"low", "medium"`, "high", false},
		{"not(1, 2)", 3, true},
	}
	for _, c := range cases {
		x, err := e.CompileUnaryTest(c.entry)
		if err != nil {
			t.Fatalf("CompileUnaryTest(%q): %v", c.entry, err)
		}
		got, err := e.EvalUnaryTest(x, FromGo(c.input), nil)
		if err != nil {
			t.Fatalf("EvalUnaryTest(%q, %v): %v", c.entry, c.input, err)
		}
		if got != c.want {
			t.Errorf("unary test %q against %v = %v, want %v", c.entry, c.input, got, c.want)
		}
	}
}

func TestSFEELRejectsLevel3Constructs(t *testing.T) {
	e, err := NewEvaluator(SFEEL)
	if err != nil {
		t.Fatal(err)
	}
	rejected := []string{
		"for x in [1,2,3] return x * 2",
		"if 1 > 0 then 1 else 2",
		"some x in [1,2] satisfies x > 1",
		"every x in [1,2] satisfies x > 0",
		"function(a) a + 1",
	}
	for _, src := range rejected {
		if _, err := e.Compile(src); err == nil {
			t.Errorf("S-FEEL accepted FEEL-only construct %q", src)
		}
	}
	accepted := []string{"1 + 2", `"a" = "b"`, "[1..10]", "x > 3 and x < 9"}
	for _, src := range accepted {
		if _, err := e.Compile(src); err != nil {
			t.Errorf("S-FEEL rejected valid simple expression %q: %v", src, err)
		}
	}
	// The same constructs must compile under the full dialect.
	full := newFEEL(t)
	for _, src := range rejected {
		if _, err := full.Compile(src); err != nil {
			t.Errorf("FEEL rejected %q: %v", src, err)
		}
	}
}

func TestStandardSupplement(t *testing.T) {
	e := newFEEL(t)
	cases := []struct {
		expr string
		want any
	}{
		{`substring before("verdict", "dict")`, "ver"},
		{`substring after("verdict", "ver")`, "dict"},
		{`replace("abcd", "(ab)|(a)", "[1=$1][2=$2]")`, "[1=ab][2=]cd"},
		{`matches("verdict", "^ver")`, true},
		{`split("a,b,c", ",")`, []any{"a", "b", "c"}},
		{`floor(1.9)`, 1.0},
		{`floor(-1.1)`, -2.0},
		{`ceiling(1.1)`, 2.0},
		{`ceiling(-1.9)`, -1.0},
		{`round up(5.5, 0)`, 6.0},
		{`round down(5.5, 0)`, 5.0},
		{`round half up(5.5, 0)`, 6.0},
		{`round half down(5.5, 0)`, 5.0},
		{`decimal(2.5, 0)`, 2.0},
		{`decimal(3.5, 0)`, 4.0},
		{`decimal(1.234, 2)`, 1.23},
		{`modulo(12, 5)`, 2.0},
		{`modulo(-12, 5)`, 3.0},
		{`sqrt(16)`, 4.0},
		{`exp(0)`, 1.0},
		{`odd(5)`, true},
		{`even(5)`, false},
		{`list replace([1,2,3], 2, 9)`, []any{1.0, 9.0, 3.0}},
		{`duplicate values([1,2,3,2,1])`, []any{1.0, 2.0}},
	}
	for _, c := range cases {
		got := mustEval(t, e, c.expr, nil)
		if !deepEqualNum(got, c.want) {
			t.Errorf("%s = %#v, want %#v", c.expr, got, c.want)
		}
	}
}

func TestYearsAndMonthsDuration(t *testing.T) {
	e := newFEEL(t)
	got := mustEval(t, e, `years and months duration(date("2020-01-15"), date("2023-03-14"))`, nil)
	if got != "P3Y1M" {
		t.Errorf("years and months duration = %v, want P3Y1M", got)
	}
}

func TestFunctionLibraryIsNamespaced(t *testing.T) {
	lib := FunctionLibrary{
		Namespace: "acme",
		Functions: map[string]Function{
			"double": {Params: []string{"n"}, Fn: func(a map[string]any) (any, error) {
				f, err := asFloat(a["n"], "n")
				return f * 2, err
			}},
		},
	}
	e, err := NewEvaluator(FEEL, WithLibraries(lib))
	if err != nil {
		t.Fatal(err)
	}
	if got := mustEval(t, e, "acme.double(21)", nil); got != 42.0 {
		t.Errorf("acme.double(21) = %v, want 42", got)
	}
	// A library may not claim a built-in name as its namespace.
	for _, ns := range []string{"", "floor", "count"} {
		if _, err := NewEvaluator(FEEL, WithLibraries(FunctionLibrary{Namespace: ns})); err == nil {
			t.Errorf("NewEvaluator accepted namespace %q", ns)
		}
	}
}

func TestRoundTripValues(t *testing.T) {
	when := time.Date(2024, 3, 1, 12, 30, 0, 0, time.UTC)
	in := map[string]any{
		"n":    3,
		"f":    2.5,
		"s":    "x",
		"b":    true,
		"t":    when,
		"d":    90 * time.Minute,
		"list": []int{1, 2},
		"nest": map[string]any{"k": 1},
		"nil":  nil,
	}
	out, ok := ToGo(FromGo(in)).(map[string]any)
	if !ok {
		t.Fatalf("ToGo(FromGo(map)) is %T", ToGo(FromGo(in)))
	}
	if out["n"] != 3.0 || out["f"] != 2.5 || out["s"] != "x" || out["b"] != true {
		t.Errorf("scalar round trip lost fidelity: %#v", out)
	}
	// Round-tripping normalises the location to a fixed UTC offset: the
	// embedded library exposes no constructor from a time.Time and its parse
	// grammar carries an offset, not a zone name. The instant is preserved.
	if got, ok := out["t"].(time.Time); !ok || !got.Equal(when) {
		t.Errorf("time round trip = %v, want the same instant as %v", out["t"], when)
	}
	if got, want := out["d"], 90*time.Minute; got != want {
		t.Errorf("duration round trip = %v, want %v", got, want)
	}
	if got := out["nil"]; got != nil {
		t.Errorf("nil round trip = %v, want nil", got)
	}
	if l, ok := out["list"].([]any); !ok || len(l) != 2 || l[0] != 1.0 {
		t.Errorf("list round trip = %#v", out["list"])
	}
}

func TestCompareAndEqual(t *testing.T) {
	if !Equal(FromGo(1), FromGo(1.0)) {
		t.Error("1 and 1.0 should be equal FEEL numbers")
	}
	if c, ok := Compare(FromGo(1), FromGo(2)); !ok || c != -1 {
		t.Errorf("Compare(1,2) = %d,%v", c, ok)
	}
	if _, ok := Compare(FromGo(map[string]any{}), FromGo(map[string]any{})); ok {
		t.Error("contexts should not be orderable")
	}
}

func TestUnaryTestSemantics(t *testing.T) {
	e := newFEEL(t)
	// A bare boolean literal is an equality test against the tested value, not
	// a constant outcome.
	x, err := e.CompileUnaryTest("true")
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := e.EvalUnaryTest(x, FromGo(false), nil); err != nil || ok {
		t.Errorf("unary test `true` against false = %v (err %v), want false", ok, err)
	}
	if ok, err := e.EvalUnaryTest(x, FromGo(true), nil); err != nil || !ok {
		t.Errorf("unary test `true` against true = %v (err %v), want true", ok, err)
	}
	// A range whose bounds cannot be compared with the tested value is a
	// non-match rather than an error, so a mixed-type table still evaluates.
	r, err := e.CompileUnaryTest("[1..10]")
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := e.EvalUnaryTest(r, FromGo("nope"), nil); err != nil || ok {
		t.Errorf("range against a string = %v (err %v), want false with no error", ok, err)
	}
	// Commas inside a nested call are not alternative separators.
	c, err := e.CompileUnaryTest("list contains([1, 2], ?)")
	if err != nil {
		t.Fatal(err)
	}
	if got := len(c.Alternatives()); got != 1 {
		t.Errorf("alternatives = %d, want 1", got)
	}
	if ok, err := e.EvalUnaryTest(c, FromGo(2), nil); err != nil || !ok {
		t.Errorf("list contains test = %v (err %v), want true", ok, err)
	}
}

func deepEqualNum(got, want any) bool {
	switch w := want.(type) {
	case float64:
		g, ok := got.(float64)
		return ok && math.Abs(g-w) < 1e-9
	case []any:
		g, ok := got.([]any)
		if !ok || len(g) != len(w) {
			return false
		}
		for i := range w {
			if !deepEqualNum(g[i], w[i]) {
				return false
			}
		}
		return true
	default:
		return got == want
	}
}

func TestInjectedClockDrivesTheTemporalBuiltins(t *testing.T) {
	// A model that reads the date is the one place identical inputs can produce
	// different answers. Injecting a clock is what makes such a model testable.
	fixed := time.Date(2021, 6, 15, 14, 30, 0, 0, time.UTC)
	e, err := NewEvaluator(FEEL, WithClock(func() time.Time { return fixed }))
	if err != nil {
		t.Fatal(err)
	}

	got, ok := mustEval(t, e, "today()", nil).(time.Time)
	if !ok || got.Format("2006-01-02") != "2021-06-15" {
		t.Errorf("today() = %v, want 2021-06-15", mustEval(t, e, "today()", nil))
	}
	nowVal, ok := mustEval(t, e, "now()", nil).(time.Time)
	if !ok || !nowVal.Equal(fixed) {
		t.Errorf("now() = %v, want %v", nowVal, fixed)
	}
	// Derived arithmetic must follow the same clock, or half a model is frozen
	// and the other half is not.
	if got := mustEval(t, e, `today() > date("2021-01-01")`, nil); got != true {
		t.Errorf("date comparison against the injected clock = %v, want true", got)
	}

	// Without a clock, the real one applies and the built-ins still work.
	live := newFEEL(t)
	if _, ok := mustEval(t, live, "today()", nil).(time.Time); !ok {
		t.Error("today() did not return a date without an injected clock")
	}
}
