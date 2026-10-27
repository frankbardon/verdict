package feel

import (
	"fmt"
	"math"
	"testing"
	"time"

	pfeel "github.com/frankbardon/verdict/pkg/feel/internal/dialect"
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
		"-(if 1 > 0 then 1 else 2)",
	}
	for _, src := range rejected {
		if _, err := e.Compile(src); err == nil {
			t.Errorf("S-FEEL accepted FEEL-only construct %q", src)
		}
	}
	accepted := []string{"1 + 2", `"a" = "b"`, "[1..10]", "x > 3 and x < 9", "-x ** 2"}
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

// TestNumericBuiltinsAreNullOutsideTheirDomain pins DMN 1.5 §10.3.4.5 and the
// §10.3.4 domain rule: a null or non-number argument, or a value the function
// is undefined for, makes a numeric built-in null rather than an error.
func TestNumericBuiltinsAreNullOutsideTheirDomain(t *testing.T) {
	e := newFEEL(t)
	for _, expr := range []string{
		`decimal(null, 2)`, `decimal(1.5, null)`, `decimal("1.5", 0)`,
		`floor(null)`, `floor("x")`, `floor(1.5, null)`, `floor(1.5, "1")`,
		`ceiling(null)`, `ceiling(true)`, `ceiling(1.5, null)`,
		`round up(null, 0)`, `round up(5, null)`, `round up("5", 0)`,
		`round down(null, 0)`, `round down(5, null)`,
		`round half up(null, 0)`, `round half up(5, null)`,
		`round half down(null, 0)`, `round half down(5, null)`,
		`decimal(1.5, 6177)`, `decimal(1.5, -6112)`, `floor(1.5, 1000000000)`,
		`abs(null)`, `abs("x")`,
		`modulo(null, 5)`, `modulo(5, null)`, `modulo("12", 5)`, `modulo(12, "5")`,
		`modulo(12, 0)`, `modulo(0, 0)`, `modulo(10.1, 0)`,
		`sqrt(null)`, `sqrt("a")`, `sqrt(-1)`,
		`log(null)`, `log("a")`, `log(0)`, `log(-1)`,
		`exp(null)`, `exp("x")`,
		`odd(null)`, `odd("x")`, `even(null)`, `even("x")`,
	} {
		v, err := e.EvalText(expr, nil)
		if err != nil {
			t.Errorf("%s: error %v, want null", expr, err)
			continue
		}
		if got := ToGo(v); got != nil {
			t.Errorf("%s = %#v, want null", expr, got)
		}
	}
}

// TestModuloIsDecimal pins every modulo example of DMN 1.5 §10.3.4.5: the
// remainder takes the divisor's sign and is computed on the exact decimals.
func TestModuloIsDecimal(t *testing.T) {
	e := newFEEL(t)
	for expr, want := range map[string]string{
		`modulo(12, 5)`:       "2",
		`modulo(-12, 5)`:      "3",
		`modulo(12, -5)`:      "-3",
		`modulo(-12, -5)`:     "-2",
		`modulo(10.1, 4.5)`:   "1.1",
		`modulo(-10.1, 4.5)`:  "3.4",
		`modulo(10.1, -4.5)`:  "-3.4",
		`modulo(-10.1, -4.5)`: "-1.1",
		`modulo(10, 2.5)`:     "0",
	} {
		v, err := e.EvalText(expr, nil)
		if err != nil {
			t.Fatalf("%s: %v", expr, err)
		}
		n, ok := v.(*pfeel.Number)
		if !ok {
			t.Fatalf("%s = %#v, want a number", expr, v)
		}
		if got := n.String(); got != want {
			t.Errorf("%s = %s, want %s", expr, got, want)
		}
	}
}

func TestYearsAndMonthsDuration(t *testing.T) {
	e := newFEEL(t)
	got := mustEval(t, e, `years and months duration(date("2020-01-15"), date("2023-03-14"))`, nil)
	if got != "P3Y1M" {
		t.Errorf("years and months duration = %v, want P3Y1M", got)
	}
	// DMN 1.5 §10.3.4: an argument outside the domain (not a date or date and
	// time, null included) is null, not an evaluation error.
	for _, expr := range []string{
		`years and months duration(1, date("2023-03-14"))`,
		`years and months duration(date("2020-01-15"), null)`,
		`years and months duration("2020-01-15", "2023-03-14")`,
		// Both must be dates or both date and times: a mixed pair or a time
		// is outside Table 72's domain.
		`years and months duration(date("2026-01-01"), date and time("2026-06-01T00:00:00"))`,
		`years and months duration(time("10:00:00"), time("11:00:00"))`,
	} {
		if got := mustEval(t, e, expr, nil); got != nil {
			t.Errorf("%s = %#v, want null", expr, got)
		}
	}
}

func TestContextBuiltinDomain(t *testing.T) {
	// DMN 1.5 §10.3.4.7: entries outside the domain are null, not an
	// evaluation error; a lone context is converted to a singleton list.
	e := newFEEL(t)
	for _, expr := range []string{`context(5)`, `context(null)`, `context([5])`, `context([{key: "a"}])`} {
		if got := mustEval(t, e, expr, nil); got != nil {
			t.Errorf("%s = %#v, want null", expr, got)
		}
	}
	got, ok := mustEval(t, e, `context({key: "a", value: 1})`, nil).(map[string]any)
	if !ok || got["a"] != 1.0 {
		t.Errorf(`context({key: "a", value: 1}) = %#v, want {a: 1}`, got)
	}
}

// TestContextEntriesHaveDistinctKeys pins DMN 1.5 §10.3.4.10 Table 81 with
// §10.3.2.6 ("These strings are distinct within a context") against TCK 1145:
// context(entries) with a repeated key is null, where context put and context
// merge override.
func TestContextEntriesHaveDistinctKeys(t *testing.T) {
	e := newFEEL(t)
	cases := []struct {
		expr string
		want any
	}{
		{`context([{key: "a", value: 1}, {key: "a", value: 2}])`, nil},
		{`context([{key: "a", value: 1}, {key: "b", value: 2}, {key: "a", value: 1}])`, nil},
		{`context([{key: "a", value: 1}, {key: "b", value: 2}])`, map[string]any{"a": 1.0, "b": 2.0}},
		{`context([{key: "a", value: 1}, {key: "b", value: 2, something: "else"}])`, map[string]any{"a": 1.0, "b": 2.0}},
		{`context({key: "a", value: null})`, map[string]any{"a": nil}},
		{`context({key: "", value: 1})`, map[string]any{"": 1.0}},
		{`context([])`, map[string]any{}},
		{`context(entries: [{key: "a", value: 1}])`, map[string]any{"a": 1.0}},
		{`context({value: 1})`, nil},
		{`context({key: null, value: 1})`, nil},
		{`context("foo")`, nil},
		{`context()`, nil},
		{`context(entris: [{key: "a", value: 1}])`, nil},
		// context put and context merge keep their override rule.
		{`context put({a: 1}, "a", 2)`, map[string]any{"a": 2.0}},
		{`context merge([{a: 1}, {a: 2}])`, map[string]any{"a": 2.0}},
	}
	for _, c := range cases {
		v, err := e.EvalText(c.expr, nil)
		if err != nil {
			t.Errorf("%s: error %v", c.expr, err)
			continue
		}
		if got := ToGo(v); !deepEqualNum(got, c.want) {
			t.Errorf("%s = %#v, want %#v", c.expr, got, c.want)
		}
	}
}

// TestStringAndListSupplementDomain pins the §10.3.4 domain rule for the
// supplement's string and list built-ins: a null or wrong-typed argument, an
// invalid regular expression, replacement or flags (footnote 2), a position
// outside the list, the wrong number of arguments and an unknown parameter
// name are all null, never an evaluation error. The "including null"
// parameters keep accepting null.
func TestStringAndListSupplementDomain(t *testing.T) {
	e := newFEEL(t)
	cases := []struct {
		expr string
		want any
	}{
		// Null and wrong-typed arguments.
		{`substring before(null, "a")`, nil},
		{`substring before("abc", null)`, nil},
		{`substring before(1, "a")`, nil},
		{`substring after(null, "a")`, nil},
		{`substring after("abc", 1)`, nil},
		{`replace(null, "a", "b")`, nil},
		{`replace("abc", null, "b")`, nil},
		{`replace("abc", "a", null)`, nil},
		{`matches(null, "a")`, nil},
		{`matches("abc", null)`, nil},
		{`matches("abc", ["a"])`, nil},
		{`matches("abc", "a", ["i"])`, nil},
		{`split(null, ",")`, nil},
		{`split("a,b", null)`, nil},
		{`list replace(null, 1, 2)`, nil},
		{`list replace([1,2,3], null, 2)`, nil},
		{`list replace([1,2,3], "2", 9)`, nil},
		{`list replace([1,2,3], 1.5, 9)`, nil},
		{`duplicate values(null)`, nil},
		// Invalid patterns, flags and replacements.
		{`matches("abc", "(")`, nil},
		{`matches("abc", "(.)\3")`, nil},
		{`matches("abc", "\3")`, nil},
		{`matches("abc", "\p{ IsBasicLatin}")`, nil},
		{`matches("abc", "a", "p")`, nil},
		{`matches("abc", "a", "X")`, nil},
		{`matches("abc", "a", " ")`, nil},
		{`replace("abc", "(", "x")`, nil},
		{`replace("abc", "", "x")`, nil},
		{`replace("abc", "b", "$")`, nil},
		{`replace("abc", "b", "x\y")`, nil},
		{`replace("abc", "b", "x", "p")`, nil},
		{`split("abc", "(")`, nil},
		// Arity and parameter names.
		{`matches("abc")`, nil},
		{`matches("abc", "a", "i", 1)`, nil},
		{`split("a,b")`, nil},
		{`split(string: "a,b", delimitr: ",")`, nil},
		{`list replace(list: [1,2,3], position: 2, newItem: 9, foo: 1)`, nil},
		{`substring before("abc")`, nil},
		// Positions outside the list.
		{`list replace([1,2,3], 0, 9)`, nil},
		{`list replace([1,2,3], 4, 9)`, nil},
		{`list replace([1,2,3], -4, 9)`, nil},
		// What stays defined.
		{`substring before("foobar", "xyz")`, ""},
		{`substring after("foobar", "xyz")`, ""},
		{`substring after("", "a")`, ""},
		{`substring after("abc", "")`, "abc"},
		{`substring before("abc", "")`, ""},
		{`matches("abracadabra", "bra", null)`, true},
		{`replace("abc", "b", "x", null)`, "axc"},
		{`replace("abc", "b", "\$")`, "a$c"},
		{`split("a;b;c;;", ";")`, []any{"a", "b", "c", "", ""}},
		{`list replace([1,2,3], 3, null)`, []any{1.0, 2.0, nil}},
		{`list replace([1,2,3], -1, 9)`, []any{1.0, 2.0, 9.0}},
		{`list replace(list: [1,2,3], position: 2, newItem: 9)`, []any{1.0, 9.0, 3.0}},
		{`list replace(1, 1, 9)`, []any{9.0}},
	}
	for _, c := range cases {
		v, err := e.EvalText(c.expr, nil)
		if err != nil {
			t.Errorf("%s: error %v, want %#v", c.expr, err, c.want)
			continue
		}
		if got := ToGo(v); !deepEqualNum(got, c.want) {
			t.Errorf("%s = %#v, want %#v", c.expr, got, c.want)
		}
	}
}

func TestConversionOverloadsTakeModelValues(t *testing.T) {
	// DMN 1.5 §10.3.4.1 Table 72: the component overloads of date and time,
	// fed from model inputs as a decision would feed them.
	e := newFEEL(t)
	vars := map[string]any{"y": FromGo(2026), "m": FromGo(2), "d": FromGo(28), "amount": FromGo("1.250,75")}
	if got := mustEval(t, e, `date(y, m, d) = date("2026-02-28")`, vars); got != true {
		t.Errorf("date(y, m, d) = %#v, want 2026-02-28", got)
	}
	if got := mustEval(t, e, `string(date(year: y, month: m + 1, day: d) - date(y, m, d))`, vars); got != "P28D" {
		t.Errorf("date difference = %#v, want P28D", got)
	}
	if got := mustEval(t, e, `date(y, 13, d)`, vars); got != nil {
		t.Errorf("date(y, 13, d) = %#v, want null", got)
	}
	if got := mustEval(t, e, `number(amount, ".", ",")`, vars); got != 1250.75 {
		t.Errorf(`number(amount, ".", ",") = %#v, want 1250.75`, got)
	}
}

func TestFunctionLibraryIsNamespaced(t *testing.T) {
	lib := FunctionLibrary{
		Namespace: "acme",
		Functions: map[string]Function{
			"double": {Params: []string{"n"}, Fn: func(a map[string]any) (any, error) {
				n, ok := numberArg(a["n"])
				if !ok {
					return nil, fmt.Errorf("n must be a number")
				}
				return n.Float64() * 2, nil
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
	// Equal is the fork's `=` with null read as false. The structural
	// comparison it replaced compared temporals field by field, so two
	// renderings of one instant were unequal although `=` said true.
	e := newFEEL(t)
	for _, p := range []struct {
		a, b string
		want bool
	}{
		{`time("10:00:00+01:00")`, `time("09:00:00Z")`, true},
		{`date and time("2020-01-01T10:00:00+01:00")`, `date and time("2020-01-01T09:00:00Z")`, true},
		{`duration("PT60M")`, `duration("PT1H")`, true},
		{`{a: 1.0, b: [1, 2]}`, `{a: 1, b: [1.00, 2]}`, true},
		{`[1, "a"]`, `[1, 2]`, false}, // `=` is null
		{`1`, `"1"`, false},           // `=` is null
		{`null`, `null`, true},
		{`1`, `null`, false},
	} {
		a, err := e.EvalText(p.a, nil)
		if err != nil {
			t.Fatal(err)
		}
		b, err := e.EvalText(p.b, nil)
		if err != nil {
			t.Fatal(err)
		}
		if got := Equal(a, b); got != p.want {
			t.Errorf("Equal(%s, %s) = %v, want %v", p.a, p.b, got, p.want)
		}
	}
	if got := mustEval(t, e, `duplicate values([time("10:00:00+01:00"), time("09:00:00Z")])`, nil); len(got.([]any)) != 1 {
		t.Errorf("duplicate values over one instant twice = %v, want one item", got)
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
	// A range whose bounds cannot be compared with the tested value is null —
	// neither a match nor an error — so a mixed-type table still evaluates.
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
	case map[string]any:
		g, ok := got.(map[string]any)
		if !ok || len(g) != len(w) {
			return false
		}
		for k, v := range w {
			gv, present := g[k]
			if !present || !deepEqualNum(gv, v) {
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

	// An injected clock's location is the engine's configured zone
	// (feel.timezone): today() is the calendar date there, not in UTC.
	kiritimati, err := time.LoadLocation("Pacific/Kiritimati")
	if err != nil {
		t.Fatal(err)
	}
	zoned, err := NewEvaluator(FEEL, WithClock(func() time.Time { return fixed.In(kiritimati) }))
	if err != nil {
		t.Fatal(err)
	}
	if got := mustEval(t, zoned, "string(today())", nil); got != "2021-06-16" {
		t.Errorf("today() with a +14:00 clock = %v, want 2021-06-16", got)
	}

	// Without a clock, the real one applies and the built-ins still work.
	live := newFEEL(t)
	if _, ok := mustEval(t, live, "today()", nil).(time.Time); !ok {
		t.Error("today() did not return a date without an injected clock")
	}
}

func TestTemporalZoneProperties(t *testing.T) {
	// DMN 1.5 §10.3.2.3 temporal properties: `time offset` is a days and
	// time duration, null for a zoneless value.
	e := newFEEL(t)
	cases := map[string]any{
		`string(time("10:00:00+02:00").time offset)`:                     "PT2H",
		`string(date and time("2024-01-01T10:00:00Z").time offset)`:      "PT0S",
		`time("10:00:00").time offset`:                                   nil,
		`date and time("2024-01-01T10:00:00").time offset`:               nil,
		`date and time("2024-01-01T10:00:00").timezone offset`:           nil,
		`string(date and time("2024-01-01T10:00:00-05:30").time offset)`: "-PT5H30M",
		`date and time("2024-01-01T10:00:00@Europe/Paris").timezone`:     "Europe/Paris",
		`date and time("2024-01-01T10:00:00+02:00").timezone`:            nil,
	}
	for expr, want := range cases {
		if got := mustEval(t, e, expr, nil); got != want {
			t.Errorf("%s = %#v, want %#v", expr, got, want)
		}
	}
}
