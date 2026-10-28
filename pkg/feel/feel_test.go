package feel

import (
	"fmt"
	"math"
	"strings"
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

// TestNamesEndAtClosingDelimiters pins that a bare name or path may be the
// last thing before `}`, `]` or `)` — DMN 1.5 §10.3.1.2 grammar rules 57-59
// put no restriction on a context's last entry.
func TestNamesEndAtClosingDelimiters(t *testing.T) {
	e := newFEEL(t)
	vars := map[string]any{
		"a":             FromGo(map[string]any{"n": 5}),
		"b":             FromGo(2),
		"Credit Rating": FromGo("A"),
	}
	cases := []struct {
		expr string
		want any
	}{
		{`{v: a}`, map[string]any{"v": map[string]any{"n": 5.0}}},
		{`{v: a.n}`, map[string]any{"v": 5.0}},
		{`{v:a.n}`, map[string]any{"v": 5.0}},
		{`{v: a.n, w: 1}`, map[string]any{"v": 5.0, "w": 1.0}},
		{`{v: (a.n)}`, map[string]any{"v": 5.0}},
		{`[{key: "k", value: a.n}]`, []any{map[string]any{"key": "k", "value": 5.0}}},
		{`{v: a}.v.n`, 5.0},
		{`{r: Credit Rating}`, map[string]any{"r": "A"}},
		{`{outer: {inner: b}}.outer.inner`, 2.0},
		{`[a.n, b]`, []any{5.0, 2.0}},
		{`abs(b)`, 2.0},
		{`(b)`, 2.0},
	}
	for _, c := range cases {
		if got := mustEval(t, e, c.expr, vars); !deepEqualNum(got, c.want) {
			t.Errorf("%s = %#v, want %#v", c.expr, got, c.want)
		}
	}
}

// TestContextEntriesSeeEarlierEntries pins DMN 1.5 §10.3.2.6: a context
// entry's value may refer to the entries before it in the same literal. An
// entry shadows an outer variable of the same name, nested literals see their
// enclosing literal's earlier entries, and a function called while the literal
// is being built sees them too.
func TestContextEntriesSeeEarlierEntries(t *testing.T) {
	e := newFEEL(t)
	vars := map[string]any{"c": FromGo(9), "xs": FromGo([]any{1, 7, 3})}
	cases := []struct {
		expr string
		want any
	}{
		{`{c: 1, r: c}`, map[string]any{"c": 1.0, "r": 1.0}},
		{`{c: 1, r: c}.r`, 1.0},
		{`{r: c}`, map[string]any{"r": 9.0}},
		{`{a: 1, b: a + 1, d: b * 10}`, map[string]any{"a": 1.0, "b": 2.0, "d": 20.0}},
		{`{a: 2, n: {b: a + 1, m: {d: b + a}}}.n.m.d`, 5.0},
		{`{"a b": 1, r: a b + 1}.r`, 2.0},
		{`{f: function(x) x + 1, r: f(2)}.r`, 3.0},
		{`{t: 5, r: list replace(xs, function(item, newItem) item > t, 0)}.r`, []any{1.0, 0.0, 3.0}},
		// A later entry is not in scope for an earlier one, and the frame does
		// not outlive the literal.
		{`{r: q, q: 1}.r`, nil},
		{`{q: 1}.q + c`, 10.0},
	}
	for _, c := range cases {
		if got := mustEval(t, e, c.expr, vars); !deepEqualNum(got, c.want) {
			t.Errorf("%s = %#v, want %#v", c.expr, got, c.want)
		}
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
		// A list-literal cell holding ranges tests them by membership
		// (DMN 1.5 Table 55, issue #62).
		{"[[2..4], [1..3]]", 1, true},
		{"[[1..3], 7]", 5, false},
		{"[[1..3], 7]", 7, true},
		{"not([[1..3], 7])", 5, true},
		{"not([[1..3], 7])", 2, false},
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

// TestRangeRelationsInExpressionsAndCells holds the Table 78 range relations
// (DMN 1.5 §10.3.4.7) both as expressions and as decision-table input entries
// over a range-valued input, where `?` is the tested range (issues #63, #64).
func TestRangeRelationsInExpressionsAndCells(t *testing.T) {
	e := newFEEL(t)
	exprs := map[string]any{
		"overlaps([1..10], [3..4])":       true,
		"overlaps before([3..4], [1..5])": false,
		"overlaps after((5..10], [1..5])": false,
		"finishes([1..10], [5..10])":      false,
		"starts([1..10), [1..10])":        true,
		"started by([1..10], [1..5))":     true,
		"finished by([1..10], (1..10])":   true,
	}
	for expr, want := range exprs {
		if got := mustEval(t, e, expr, nil); got != want {
			t.Errorf("%s = %#v, want %#v", expr, got, want)
		}
	}
	cells := []struct {
		entry, input string
		want         bool
	}{
		{"overlaps(?, [3..4])", "[1..10]", true},
		{"overlaps(?, [11..20])", "[1..10]", false},
		{"overlaps before(?, [1..5])", "[3..4]", false},
		{"overlaps before(?, [3..8])", "[1..5]", true},
		{"overlaps after(?, [1..5])", "(5..10]", false},
		{"overlaps after(?, [1..5])", "[3..8]", true},
		{"starts(?, [1..10])", "[1..5]", true},
		{"starts(?, [1..10])", "(1..5]", false},
		{"started by(?, [1..5])", "[1..10]", true},
		{"finishes(?, [5..10])", "[1..10]", false},
		{"finishes(?, [1..10])", "[5..10]", true},
		{"finished by(?, [5..10))", "[1..10]", false},
		{"not(overlaps(?, [3..4]))", "[1..10]", false},
	}
	for _, c := range cells {
		x, err := e.CompileUnaryTest(c.entry)
		if err != nil {
			t.Fatalf("CompileUnaryTest(%q): %v", c.entry, err)
		}
		in, err := e.EvalText(c.input, nil)
		if err != nil {
			t.Fatalf("EvalText(%q): %v", c.input, err)
		}
		got, err := e.EvalUnaryTest(x, in, nil)
		if err != nil {
			t.Fatalf("EvalUnaryTest(%q, %s): %v", c.entry, c.input, err)
		}
		if got != c.want {
			t.Errorf("cell %q against %s = %v, want %v", c.entry, c.input, got, c.want)
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
		// Interpretation (#60): Table 76 is silent on a fraction; odd and
		// even are defined on integers, so one is outside the domain.
		`odd(1.5)`, `even(1.5)`, `odd(-0.5)`, `even(2.000001)`,
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

// TestNumericBuiltinsAreDecimal pins issue #41: sqrt, log and exp are
// computed at decimal128's 34 significant digits (DMN 1.5 §10.3.2.3.1) rather
// than through float64, and odd and even read the exact integer, so one above
// 2^53 is not rounded to an even float64 neighbour first.
func TestNumericBuiltinsAreDecimal(t *testing.T) {
	e := newFEEL(t)
	// Each right-hand side is the true value rounded to 34 significant digits;
	// `=` compares at the evaluator's full precision, so a float64 result
	// (about 16 digits) fails every one.
	for _, expr := range []string{
		`sqrt(2) = 1.414213562373095048801688724209698`,
		`sqrt(16) = 4`,
		`sqrt(0) = 0`,
		`sqrt(0.0001) = 0.01`,
		`sqrt(10 ** -40) = 10 ** -20`,
		`exp(1) = 2.718281828459045235360287471352662`,
		`exp(-1) = 0.3678794411714423215955237701614609`,
		`exp(0) = 1`,
		`exp(100) = number("2.688117141816135448412625551580014e43")`,
		`log(10) = 2.302585092994045684017991454684364`,
		`log(1) = 0`,
		`log(2) = 0.6931471805599453094172321214581766`,
		`log(0.5) = -0.6931471805599453094172321214581766`,
		`log(exp(5)) = 5`,
		`odd(9007199254740993)`,
		`not(even(9007199254740993))`,
		`even(9007199254740994)`,
		`odd(123456789012345678901234567890123)`,
		`odd(-7)`,
		`even(0)`,
		`odd(2.5) = null`,
		`even(2.5) = null`,
	} {
		if got := mustEval(t, e, expr, nil); got != true {
			t.Errorf("%s = %#v, want true", expr, got)
		}
	}
	// Extreme arguments answer at once rather than iterating (#20): exp past
	// decimal128's range is null, far below it 0; log of an extreme magnitude
	// is one short series.
	for expr, want := range map[string]any{
		`exp(1000000000)`:           nil,
		`exp(14150)`:                nil,
		`exp(-1000000000)`:          0.0,
		`log(10 ** 6000) > 13815`:   true,
		`log(10 ** -6000) < -13815`: true,
		`exp(14000) > 10 ** 6080`:   true,
	} {
		if got := mustEval(t, e, expr, nil); got != want {
			t.Errorf("%s = %#v, want %#v", expr, got, want)
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
		// No 18-digit truncation of either the operands or the result
		// (issue #59).
		`modulo(0.00000000000000000001, 3)`:  "0.00000000000000000001",
		`modulo(-0.00000000000000000001, 3)`: "2.99999999999999999999",
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

func TestNumbersPrintAtDecimal128Precision(t *testing.T) {
	// DMN 1.5 §10.3.2.3.1: a number is decimal128, 34 significant digits;
	// §10.3.4.1 Table 72 string(from). Printing used to truncate to 18
	// fractional digits, so exp(-50) printed "0" (issue #59).
	e := newFEEL(t)
	for expr, want := range map[string]string{
		`string(exp(-50))`: "0.0000000000000000000001928749847963917783017342816527013",
		`string(0.1000000000000000055511151231257827)`: "0.1000000000000000055511151231257827",
		`string(1/3)`:                            "0.3333333333333333333333333333333333",
		`string(-1/3)`:                           "-0.3333333333333333333333333333333333",
		`string(10)`:                             "10",
		`string(1.50)`:                           "1.5",
		`string(0.1)`:                            "0.1",
		`string(-12.5)`:                          "-12.5",
		`string(0)`:                              "0",
		`string(-0)`:                             "0",
		`string(10 ** -40)`:                      "0.0000000000000000000000000000000000000001",
		`string(10 ** 40)`:                       "10000000000000000000000000000000000000000",
		`string(2 ** 120)`:                       "1329227995784915872903807060280345000",
		`"n=" + string(0.000000000000000000005)`: "n=0.000000000000000000005",
	} {
		if got := mustEval(t, e, expr, nil); got != want {
			t.Errorf("%s = %#v, want %q", expr, got, want)
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

// TestGetEntriesIsAList pins DMN 1.5 §10.3.4.10 Table 81: get entries(m) is a
// FEEL list of key-value contexts, for a literal context and a context-typed
// variable alike, and ToGo converts its elements like any list's.
func TestGetEntriesIsAList(t *testing.T) {
	e := newFEEL(t)
	vars := map[string]any{"x": FromGo(map[string]any{"y": 2, "w": "s"})}
	cases := []struct {
		expr string
		want any
	}{
		{`get entries({y: 2}) = [{key: "y", value: 2}]`, true},
		{`get entries(x)[1].key`, "w"},
		{`get entries(x)[2].value`, 2.0},
		{`count(get entries(x))`, 2.0},
		{`for e in get entries(x) return e.value`, []any{"s", 2.0}},
		{`get entries({y: 1.5})`, []any{map[string]any{"key": "y", "value": 1.5}}},
		{`get entries(x)`, []any{
			map[string]any{"key": "w", "value": "s"},
			map[string]any{"key": "y", "value": 2.0},
		}},
		{`get entries(null)`, nil},
	}
	for _, c := range cases {
		v, err := e.EvalText(c.expr, vars)
		if err != nil {
			t.Errorf("%s: error %v", c.expr, err)
			continue
		}
		if got := ToGo(v); !deepEqualNum(got, c.want) {
			t.Errorf("%s = %#v, want %#v", c.expr, got, c.want)
		}
	}
}

// TestListIndex pins DMN 1.5 §10.3.2.5 and Table 68: L[1] is the first
// element, L[-1] the last, L[-n] the nth from the end; an index the list does
// not reach — 0, past either end — is null, and so is a non-integer index,
// which no row of Table 68 covers. Indexing behaves the same on a literal, a
// variable, a function result and a nested index.
func TestListIndex(t *testing.T) {
	e := newFEEL(t)
	vars := map[string]any{
		"xs": FromGo([]any{1, 2, 3}),
		"m":  FromGo(map[string]any{"a": 1, "b": 2}),
	}
	cases := []struct {
		expr string
		want any
	}{
		{`[1, 2, 3][1]`, 1.0},
		{`[1, 2, 3][3]`, 3.0},
		{`[1, 2, 3][-1]`, 3.0},
		{`[1, 2, 3][-3]`, 1.0},
		{`[1, 2, 3][4]`, nil},
		{`[1, 2, 3][-4]`, nil},
		{`[1, 2, 3][0]`, nil},
		{`[1, 2, 3][1.5]`, nil},
		{`[1, 2, 3][-1.5]`, nil},
		{`[1, 2, 3][2.0]`, 2.0},
		{`[1, 2, 3][99999999999999999999999]`, nil},
		{`[1, 2, 3][-99999999999999999999999]`, nil},
		{`xs[-1]`, 3.0},
		{`xs[-2]`, 2.0},
		{`xs[0]`, nil},
		{`get entries(m)[-1].key`, "b"},
		{`reverse(xs)[-1]`, 1.0},
		{`[[1, 2], [3]][-1][-1]`, 3.0},
		{`[[1, 2], [3]][-2][-1]`, 2.0},
		{`[1, null, 3][2]`, nil},
	}
	for _, c := range cases {
		v, err := e.EvalText(c.expr, vars)
		if err != nil {
			t.Errorf("%s: error %v", c.expr, err)
			continue
		}
		if got := ToGo(v); !deepEqualNum(got, c.want) {
			t.Errorf("%s = %#v, want %#v", c.expr, got, c.want)
		}
	}
}

// TestListFilterAndPath pins DMN 1.5 §10.3.2.5 and Table 68: a boolean
// filter is evaluated per item with `item` bound and, when the item is a
// context, its entries in scope by name; an item whose filter is false, null or
// not a boolean is dropped. A numeric bracket is still an index. A path on a
// list of contexts projects over the elements, null where one lacks the entry.
// Filters and paths work the same on a literal, a context-typed variable and a
// built-in's result.
func TestListFilterAndPath(t *testing.T) {
	e := newFEEL(t)
	vars := map[string]any{
		"cs": FromGo([]any{map[string]any{"a": 1}, map[string]any{"a": 2}}),
		"m":  FromGo(map[string]any{"x": 1, "y": 2}),
		"n":  FromGo(2),
		"xs": FromGo([]any{1, 2, 3}),
	}
	a2 := []any{map[string]any{"a": 2.0}}
	cases := []struct {
		expr string
		want any
	}{
		{`[{a: 1}, {a: 2}][item.a = 2]`, a2},
		{`[{a: 1}, {a: 2}][a = 2]`, a2},
		{`cs[item.a = 2]`, a2},
		{`cs[a = 2]`, a2},
		{`[{a: 1}, {a: 2}].a`, []any{1.0, 2.0}},
		{`cs.a`, []any{1.0, 2.0}},
		{`get entries(m).key`, []any{"x", "y"}},
		{`get entries({x: 1, y: 2})[key = "y"].value`, []any{2.0}},
		{`get entries(m)[value > 1].key`, []any{"y"}},
		{`[{x: 1, y: 2}, {x: 2}].y`, []any{2.0, nil}},
		{`[1, 2, 3][item > 1]`, []any{2.0, 3.0}},
		{`xs[item > n]`, []any{3.0}},
		{`[{x: 1}, {x: null}, {x: "a"}][x < 2]`, []any{map[string]any{"x": 1.0}}},
		{`[1, 2, 3][if item = 2 then null else true]`, []any{1.0, 3.0}},
		{`[1, 2, 3][item > 5]`, []any{}},
		{`[][item > 1]`, []any{}},
		{`[[1, 2], [3, 4]][item[1] = 3]`, []any{[]any{3.0, 4.0}}},
		{`count(cs[a > 0])`, 2.0},
		{`xs[n]`, 2.0},
		{`xs[-1]`, 3.0},
		{`xs[0]`, nil},
		{`[][1]`, nil},
	}
	for _, c := range cases {
		v, err := e.EvalText(c.expr, vars)
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

// TestListReplaceMatchFunction pins the match form of `list replace` (DMN
// 1.5 §10.3.4.4): every item the function of (item, newItem) returns true
// for is replaced. Issue #54.
func TestListReplaceMatchFunction(t *testing.T) {
	e := newFEEL(t)
	vars := map[string]any{"limit": FromGo(5)}
	cases := []struct {
		expr string
		want any
	}{
		// The spec example, positionally and by name.
		{`list replace([2,4,6,8], function(item, newItem) item < newItem, 5)`, []any{5.0, 5.0, 6.0, 8.0}},
		{`list replace(list: [2,4,6,8], match: function(item, newItem) item < newItem, newItem: 5)`, []any{5.0, 5.0, 6.0, 8.0}},
		{`list replace(match: function(item, newItem) item = 4, newItem: null, list: [2,4,6])`, []any{2.0, nil, 6.0}},
		// The predicate sees outer variables and iteration bindings.
		{`list replace([2,4,6,8], function(item, newItem) item > limit, 0)`, []any{2.0, 4.0, 0.0, 0.0}},
		{`for t in [3] return list replace([2,4], function(x, y) x > t, 0)`, []any{[]any{2.0, 0.0}}},
		// A result that is not true is no match.
		{`list replace([2,4,6,8], function(item, newItem) 1, 5)`, []any{2.0, 4.0, 6.0, 8.0}},
		{`list replace([2,4], function(item, newItem) null, 5)`, []any{2.0, 4.0}},
		{`list replace([2,4], function(item, newItem) false, 5)`, []any{2.0, 4.0}},
		// An empty list or a singleton conversion.
		{`list replace([], function(item, newItem) true, 5)`, []any{}},
		{`list replace(1, function(item, newItem) true, 5)`, []any{5.0}},
		// Outside the domain: null.
		{`list replace([2,4], function(item) true, 5)`, nil},
		{`list replace([2,4], function(a, b, c) true, 5)`, nil},
		{`list replace(null, function(item, newItem) true, 5)`, nil},
		{`list replace(list: [2,4], match: 1, newItem: 5)`, nil},
		{`list replace(list: [2,4], match: function(a, b) true, position: 1, newItem: 5)`, nil},
		// The position form is unchanged.
		{`list replace([1,2,3], 2, 9)`, []any{1.0, 9.0, 3.0}},
		{`list replace(list: [1,2,3], position: -1, newItem: 9)`, []any{1.0, 2.0, 9.0}},
	}
	for _, c := range cases {
		v, err := e.EvalText(c.expr, vars)
		if err != nil {
			t.Errorf("%s: error %v, want %#v", c.expr, err, c.want)
			continue
		}
		if got := ToGo(v); !deepEqualNum(got, c.want) {
			t.Errorf("%s = %#v, want %#v", c.expr, got, c.want)
		}
	}

	// An error inside the predicate propagates, as it does from sort's.
	if _, err := e.EvalText(`list replace([1,2], function(a, b) a.x.y, 5)`, nil); err == nil {
		t.Error("a failing predicate: want an evaluation error, got none")
	}
}

func TestStringLiteralsAndCharacters(t *testing.T) {
	// DMN 1.5 §10.3.1.2 grammar rule 66: a string literal's escapes decode
	// (issue #55). §10.3.4.3 Table 73: lengths and positions count
	// characters, which are code points (issue #56).
	e := newFEEL(t)
	cases := []struct {
		expr string
		want any
	}{
		{`"a\\b"`, `a\b`},
		{`"a\tb"`, "a\tb"},
		{`"a\rb"`, "a\rb"},
		{`"a\nb"`, "a\nb"},
		{`"a\'b"`, "a'b"},
		{`"a\"b"`, `a"b`},
		{`"\u0041\u00e9"`, "Aé"},
		{`"\uD83D\uDC0E"`, "🐎"},
		{`"\U01F40E"`, "🐎"},
		{`"\\"`, `\`},
		{`"\\n"`, `\n`},
		// Escapes the grammar does not define stay verbatim.
		{`"\d"`, `\d`},
		{`"\u12"`, `\u12`},
		{`"\uD83D"`, "\uFFFD"},
		// A regular expression written with an escaped backslash reaches the
		// engine as one backslash.
		{`matches("a1", "\\d")`, true},
		{`matches("ad", "\\d")`, false},
		{`replace("a1b2", "\\d", "#")`, "a#b#"},
		{`string length("a\\b")`, 3.0},
		{`string length("\uD83D\uDC0E")`, 1.0},
		// Characters, not bytes.
		{`string length("🐎ab")`, 3.0},
		{`string length("é")`, 1.0},
		{`string length("")`, 0.0},
		{`substring("🐎ab", 2)`, "ab"},
		{`substring("🐎ab", -2, 1)`, "a"},
		{`substring before("🐎ab", "b")`, "🐎a"},
		{`substring after("🐎ab", "🐎")`, "ab"},
		{`upper case("éa🐎")`, "ÉA🐎"},
		{`lower case("ÉA")`, "éa"},
		{`starts with("🐎ab", "🐎")`, true},
		{`ends with("ab🐎", "🐎")`, true},
		{`contains("a🐎b", "🐎")`, true},
		{`matches("🐎", "^.$")`, true},
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

// TestListAggregates pins DMN 1.5 §10.3.4.4 Table 75 for count, min, max,
// sum, product, mean, median, stddev and mode (#58): a null or out-of-domain
// argument is null, never an error; an empty list is 0 for count, [] for
// mode and null for the rest; a null or non-number item makes an arithmetic
// aggregate null, while count counts it (an interpretation; TCK 0094-004
// fixes only product). TCK ids: 0061 median, 0062 mode, 0063 stddev, 0094
// product.
func TestListAggregates(t *testing.T) {
	e := newFEEL(t)
	cases := []struct {
		expr string
		want any
	}{
		// Null or out-of-domain argument.
		{`count(null)`, nil},
		{`count(list: null)`, nil},
		{`sum(null)`, nil},
		{`product(null)`, nil},
		{`mean(null)`, nil},
		{`median(null)`, nil},
		{`stddev(null)`, nil},
		{`mode(null)`, nil},
		{`min(null)`, nil},
		{`max(null)`, nil},
		{`sum("a")`, nil},
		{`mean([1, "a"])`, nil},
		{`median([1, 2, "foo", 4])`, nil},
		{`mode([1, 2, "foo", 4])`, nil},
		{`stddev([1, 2, "foo", 4])`, nil},
		{`product([1, 2, "foo", 4])`, nil},
		{`sum([[1, 2]])`, nil},
		// Empty list.
		{`count([])`, 0.0},
		{`sum([])`, nil},
		{`product([])`, nil},
		{`mean([])`, nil},
		{`median([])`, nil},
		{`stddev([])`, nil},
		{`min([])`, nil},
		{`max([])`, nil},
		{`mode([])`, []any{}},
		// A null item.
		{`count([1, null])`, 2.0},
		{`sum([1, null])`, nil},
		{`product([1, 2, null, 4])`, nil},
		{`mean([1, null])`, nil},
		{`median([1, null])`, nil},
		{`stddev([1, null, 3])`, nil},
		{`mode([1, null])`, nil},
		{`max([1, null])`, nil},
		// In the domain, including singleton conversion and varargs.
		{`count([1, [2, 3]])`, 2.0},
		{`count(1, 2, 3)`, 3.0},
		{`count(5)`, 1.0},
		{`sum([1, 2, 3])`, 6.0},
		{`sum(1, 2, 3)`, 6.0},
		{`sum(list: [1, 2])`, 3.0},
		{`product([2, 3, 4])`, 24.0},
		{`product(6)`, 6.0},
		{`mean([1, 2, 3, 4])`, 2.5},
		{`mean(5)`, 5.0},
		{`median([8, 2, 5, 3, 4])`, 4.0},
		{`median([6, 1, 2, 3])`, 2.5},
		{`median(4)`, 4.0},
		{`stddev(47)`, nil},
		{`stddev([47])`, nil},
		{`stddev([2, 4, 7, 5]) = 2.081665999466132735282297706979931`, true},
		{`mode([6, 1, 9, 6, 1])`, []any{1.0, 6.0}},
		{`mode([3, 6, 1, 9, 6, 1, 3])`, []any{1.0, 3.0, 6.0}},
		{`mode(6, 3, 9, 6, 6)`, []any{6.0}},
		{`min([3, 1, 2])`, 1.0},
		{`max(3, 1, 2)`, 3.0},
		{`string(mean([1, 2, 3, 4]))`, "2.5"},
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

// TestAggregateIsTheBuiltin pins that feel.Aggregate, the door COLLECT
// aggregation uses, answers exactly as the FEEL built-ins do.
func TestAggregateIsTheBuiltin(t *testing.T) {
	list := []any{FromGo(3), FromGo(1), Null}
	cases := map[string]any{"count": 3.0, "sum": nil, "min": nil, "max": nil, "mode": nil}
	for name, want := range cases {
		got, ok := Aggregate(name, list)
		if !ok {
			t.Fatalf("Aggregate(%q) unknown", name)
		}
		if g := ToGo(got); !deepEqualNum(g, want) {
			t.Errorf("Aggregate(%q, [3, 1, null]) = %#v, want %#v", name, g, want)
		}
	}
	if got, _ := Aggregate("sum", nil); !IsNull(got) {
		t.Errorf("Aggregate(sum, []) = %#v, want null", got)
	}
	if got, _ := Aggregate("count", nil); ToGo(got) != 0.0 {
		t.Errorf("Aggregate(count, []) = %#v, want 0", ToGo(got))
	}
	if _, ok := Aggregate("all", nil); ok {
		t.Error("Aggregate(all) is not a list aggregate")
	}
}

func TestAllAnyAreThreeValued(t *testing.T) {
	// DMN 1.5 §10.3.4.4; TCK 0059/0060. A null item decides nothing; a
	// non-boolean item puts the call outside its domain.
	e := newFEEL(t)
	cases := []struct {
		expr string
		want any
	}{
		{`all([false, null])`, false},
		{`all([true, null])`, nil},
		{`all([false, 123])`, nil},
		{`all([])`, true},
		{`all(true)`, true},
		{`all(123)`, nil},
		{`all(null)`, nil},
		{`all(true, true)`, true},
		{`any([true, null])`, true},
		{`any([false, null])`, nil},
		{`any([])`, false},
		{`any(false)`, false},
		{`any(false, true)`, true},
		{`any(null)`, nil},
		// Table 55: a scalar right side of `in` is equality.
		{`1 in 1`, true},
		{`"a" in "a"`, true},
		{`"a" in "b"`, false},
		{`1 in "a"`, nil},
	}
	for _, c := range cases {
		v, err := e.EvalText(c.expr, nil)
		if err != nil {
			t.Errorf("%s: error %v, want %#v", c.expr, err, c.want)
			continue
		}
		if got := ToGo(v); got != c.want {
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
	// A Go time comes back as its FEEL lexical form. The location is
	// normalised to a fixed offset (the embedded library's parse grammar
	// carries an offset, not a zone name) and UTC prints as Z.
	if got := out["t"]; got != "2024-03-01T12:30:00Z" {
		t.Errorf("time round trip = %v, want 2024-03-01T12:30:00Z (the instant %v)", got, when)
	}
	if got, want := out["d"], "PT1H30M"; got != want {
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

// TestCompareFollowsFEELOrdering pins Compare to FEEL's `<` (DMN 1.5 Table 54)
// and to min/max (§10.3.4.4), which COLLECT MIN/MAX and the analyser share:
// booleans, nulls and pairs of different kinds have no order.
func TestCompareFollowsFEELOrdering(t *testing.T) {
	e := newFEEL(t)
	// Compare takes FEEL values, so evaluate without mustEval's ToGo.
	feelValue := func(text string) any {
		t.Helper()
		v, err := e.EvalText(text, nil)
		if err != nil {
			t.Fatalf("EvalText(%q): %v", text, err)
		}
		return v
	}
	ordered := []struct {
		a, b string
		want int
	}{
		{`1`, `2`, -1},
		{`"b"`, `"a"`, 1},
		{`duration("P1D")`, `duration("PT24H")`, 0},
		{`duration("P1Y")`, `duration("P11M")`, 1},
		{`date("2024-01-01")`, `date and time("2024-01-01T00:00:00Z")`, 0},
		{`time("10:00:00")`, `time("11:00:00")`, -1},
	}
	for _, p := range ordered {
		a, b := feelValue(p.a), feelValue(p.b)
		if c, ok := Compare(a, b); !ok || c != p.want {
			t.Errorf("Compare(%s, %s) = %d, %v; want %d, true", p.a, p.b, c, ok, p.want)
		}
	}
	unordered := [][2]string{
		{`false`, `true`},
		{`true`, `true`},
		{`1`, `"1"`},
		{`1`, `null`},
		{`null`, `null`},
		{`duration("P1D")`, `duration("P1M")`},
		{`time("10:00:00")`, `date("2024-01-01")`},
		{`[1]`, `[2]`},
		{`[1..2]`, `[1..2]`},
	}
	for _, p := range unordered {
		a, b := feelValue(p[0]), feelValue(p[1])
		if c, ok := Compare(a, b); ok {
			t.Errorf("Compare(%s, %s) = %d, true; want not comparable", p[0], p[1], c)
		}
		// Compare and min agree: wherever Compare has no order, min is null.
		if got := mustEval(t, e, "min(["+p[0]+", "+p[1]+"])", nil); !IsNull(got) {
			t.Errorf("min([%s, %s]) = %v; want null, as Compare has no order", p[0], p[1], got)
		}
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

	if got := mustEval(t, e, "today()", nil); got != "2021-06-15" {
		t.Errorf("today() = %v, want 2021-06-15", got)
	}
	if got := mustEval(t, e, "now()", nil); got != "2021-06-15T14:30:00Z" {
		t.Errorf("now() = %v, want 2021-06-15T14:30:00Z", got)
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
	if v, err := live.EvalText("today()", nil); err != nil || TypeName(v) != "date" {
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

// TestKeyOfSeparatesKinds pins issue #53: KeyOf is the BKM memo key, so two
// distinct FEEL values must never share one, while identical values must.
func TestKeyOfSeparatesKinds(t *testing.T) {
	e := newFEEL(t)
	key := func(expr string) string {
		t.Helper()
		v, err := e.EvalText(expr, nil)
		if err != nil {
			t.Fatalf("EvalText(%q): %v", expr, err)
		}
		k, ok := KeyOf(v)
		if !ok {
			t.Fatalf("KeyOf(%s) is unusable", expr)
		}
		return k
	}
	distinct := [][2]string{
		{`date and time("2024-03-05T10:00:00")`, `date and time("2024-03-05T10:00:00Z")`},
		{`date("2024-03-05")`, `date and time("2024-03-05T00:00:00Z")`},
		{`date("2024-03-05")`, `date and time("2024-03-05T00:00:00")`},
		{`time("10:00:00Z")`, `date and time("0000-01-01T10:00:00Z")`},
		{`time("10:00:00")`, `time("10:00:00Z")`},
		{`time("10:00:00")`, `time("10:00:00.5")`},
		{`"1"`, `1`},
		{`"2024-03-05"`, `date("2024-03-05")`},
		{`"P1Y"`, `duration("P1Y")`},
		{`duration("P1D")`, `duration("PT25H")`},
		{`[1, "1"]`, `["1", 1]`},
		{`{a: 1}`, `{a: "1"}`},
		{`[1..2]`, `(1..2]`},
		{`null`, `"null"`},
		{`true`, `"true"`},
	}
	for _, p := range distinct {
		if a, b := key(p[0]), key(p[1]); a == b {
			t.Errorf("KeyOf(%s) = KeyOf(%s) = %s", p[0], p[1], a)
		}
	}
	same := [][2]string{
		{`time("10:00:00.250")`, `time("10:00:00.25")`},
		{`{b: 2, a: 1}`, `{a: 1, b: 2}`},
		{`duration("P1D")`, `duration("PT24H")`},
		{`date and time("2024-03-05T10:00:00@Europe/Paris")`, `date and time("2024-03-05T10:00:00@Europe/Paris")`},
	}
	for _, p := range same {
		if a, b := key(p[0]), key(p[1]); a != b {
			t.Errorf("KeyOf(%s) = %s, KeyOf(%s) = %s; want equal", p[0], a, p[1], b)
		}
	}
	if k, ok := KeyOf(int64(3)); !ok || k != key(`3`) {
		t.Errorf("KeyOf(int64(3)) = %q, %v; want the key of FEEL 3", k, ok)
	}
	if _, ok := KeyOf(struct{}{}); ok {
		t.Errorf("KeyOf of an unknown Go value should be unusable")
	}
}

// TestToGoEmitsFEELLexicalForms pins issue #46: the boundary conversion prints
// every temporal and both duration kinds in their FEEL lexical form, keeping
// kind and zone-ness, while numbers stay float64.
func TestToGoEmitsFEELLexicalForms(t *testing.T) {
	e := newFEEL(t)
	for expr, want := range map[string]any{
		`date("2024-03-05")`:                                "2024-03-05",
		`time("10:00:00")`:                                  "10:00:00",
		`time("10:00:00Z")`:                                 "10:00:00Z",
		`time("10:00:00+02:00")`:                            "10:00:00+02:00",
		`date and time("2024-03-05T10:00:00")`:              "2024-03-05T10:00:00",
		`date and time("2024-03-05T10:00:00Z")`:             "2024-03-05T10:00:00Z",
		`date and time("2024-03-05T10:00:00+02:00")`:        "2024-03-05T10:00:00+02:00",
		`date and time("2024-03-05T10:00:00@Europe/Paris")`: "2024-03-05T10:00:00@Europe/Paris",
		`date and time("2024-03-05T10:00:00.25")`:           "2024-03-05T10:00:00.25",
		`duration("P1DT2H")`:                                "P1DT2H",
		`duration("P1Y2M")`:                                 "P1Y2M",
		`duration("-PT0.5S")`:                               "-PT0.5S",
		`1000000`:                                           1000000.0,
		`0.1`:                                               0.1,
	} {
		if got := mustEval(t, e, expr, nil); got != want {
			t.Errorf("ToGo(%s) = %#v, want %#v", expr, got, want)
		}
	}
	// Contexts, the fork's own Scope map included, and lists convert their
	// elements the same way.
	got := ToGo(pfeel.Scope{"d": mustFEEL(t, e, `date("2024-03-05")`), "l": []any{mustFEEL(t, e, `duration("PT1H")`)}})
	m, ok := got.(map[string]any)
	if !ok || m["d"] != "2024-03-05" || m["l"].([]any)[0] != "PT1H" {
		t.Errorf("ToGo(Scope) = %#v", got)
	}
}

// TestFromGoKeepsSubSecondTimes pins the inbound half of #46: a Go time with a
// fraction of a second reaches FEEL with it, not truncated to the second.
func TestFromGoKeepsSubSecondTimes(t *testing.T) {
	in := time.Date(2024, 3, 5, 10, 0, 0, 250_000_000, time.UTC)
	if got := ToGo(FromGo(in)); got != "2024-03-05T10:00:00.25Z" {
		t.Errorf("ToGo(FromGo(%v)) = %#v, want 2024-03-05T10:00:00.25Z", in, got)
	}
}

// TestFromGoKeepsIANAZones pins #68: a Go time whose Location is a real IANA
// zone reaches FEEL with that zone id (DMN 1.5 §10.3.2.3.5), so `.timezone`
// reads it back; anything else keeps the offset, which is always lossless.
func TestFromGoKeepsIANAZones(t *testing.T) {
	paris, err := time.LoadLocation("Europe/Paris")
	if err != nil {
		t.Fatal(err)
	}
	// 2024-10-27 02:30 happens twice in Paris, at +02:00 and then +01:00.
	// `2024-10-27T02:30:00@Europe/Paris` can name only one of them — the one
	// time.Date picks, as the FEEL parser does — so the other keeps its offset.
	named := time.Date(2024, 10, 27, 2, 30, 0, 0, paris)
	other := time.Date(2024, 10, 27, 0, 30, 0, 0, time.UTC).In(paris)
	if other.Equal(named) {
		other = time.Date(2024, 10, 27, 1, 30, 0, 0, time.UTC).In(paris)
	}
	e, err := NewEvaluator(FEEL)
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name string
		in   time.Time
		want string
		zone any
	}{
		{"paris summer", time.Date(2024, 7, 1, 10, 0, 0, 0, paris), "2024-07-01T10:00:00@Europe/Paris", "Europe/Paris"},
		{"paris winter", time.Date(2024, 1, 15, 10, 0, 0, 0, paris), "2024-01-15T10:00:00@Europe/Paris", "Europe/Paris"},
		{"paris nanoseconds", time.Date(2024, 7, 1, 10, 0, 0, 123456789, paris), "2024-07-01T10:00:00.123456789@Europe/Paris", "Europe/Paris"},
		{"DST overlap, the occurrence the id names", named, "2024-10-27T02:30:00@Europe/Paris", "Europe/Paris"},
		{"DST overlap, the other occurrence", other, other.Format("2006-01-02T15:04:05-07:00"), nil},
		{"utc", time.Date(2024, 7, 1, 10, 0, 0, 0, time.UTC), "2024-07-01T10:00:00Z", nil},
		{"unnamed fixed zone", time.Date(2024, 7, 1, 10, 0, 0, 0, time.FixedZone("", 3600)), "2024-07-01T10:00:00+01:00", nil},
		{"fixed zone named CET in July", time.Date(2024, 7, 1, 10, 0, 0, 0, time.FixedZone("CET", 3600)), "2024-07-01T10:00:00+01:00", nil},
		{"fixed zone named like an offset", time.Date(2024, 7, 1, 10, 0, 0, 0, time.FixedZone("+01:00", 3600)), "2024-07-01T10:00:00+01:00", nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			v := FromGo(c.in)
			if got := ToGo(v); got != c.want {
				t.Errorf("ToGo(FromGo(%v)) = %#v, want %q", c.in, got, c.want)
			}
			zone, err := e.EvalText("x.timezone", map[string]any{"x": v})
			if err != nil {
				t.Fatal(err)
			}
			if ToGo(zone) != c.zone {
				t.Errorf("x.timezone = %#v, want %#v", ToGo(zone), c.zone)
			}
			eq, err := e.EvalText(`x = date and time("`+c.in.UTC().Format(time.RFC3339Nano)+`")`, map[string]any{"x": v})
			if err != nil {
				t.Fatal(err)
			}
			if eq != true {
				t.Errorf("FromGo(%v) is not the same instant (= gave %#v)", c.in, eq)
			}
		})
	}

	// time.Local names the host's zone, never a zone id, whatever the host is.
	local := FromGo(time.Date(2024, 7, 1, 10, 0, 0, 0, time.Local))
	if s, _ := ToGo(local).(string); strings.Contains(s, "@") {
		t.Errorf("ToGo(FromGo(local time)) = %q, want an offset form", s)
	}
}

func mustFEEL(t *testing.T, e *Evaluator, text string) any {
	t.Helper()
	v, err := e.EvalText(text, nil)
	if err != nil {
		t.Fatalf("EvalText(%q): %v", text, err)
	}
	return v
}

// TestScopeLeaksAreStillContexts pins the façade backstops for issue #67. The
// fork no longer hands context variables out as its frame type, Scope, but a
// Scope that leaks (lists are not normalised when a frame is pushed, and a
// future re-sync could reintroduce the upstream rewrite) must still be keyed,
// named and consumed as the context it is.
func TestScopeLeaksAreStillContexts(t *testing.T) {
	sc := pfeel.Scope{"x": pfeel.NewNumberFromInt64(1), "n": map[string]any{"y": pfeel.NewNumberFromInt64(2)}}
	plain := map[string]any{"x": pfeel.NewNumberFromInt64(1), "n": map[string]any{"y": pfeel.NewNumberFromInt64(2)}}
	ks, ok1 := KeyOf(sc)
	kp, ok2 := KeyOf(plain)
	if !ok1 || !ok2 || ks != kp {
		t.Errorf("KeyOf(Scope) = %q (%v), KeyOf(map) = %q (%v); want one usable key", ks, ok1, kp, ok2)
	}
	if got := TypeName(sc); got != "context" {
		t.Errorf("TypeName(Scope) = %q, want context", got)
	}

	e := newFEEL(t)
	vars := map[string]any{"l": []any{pfeel.Scope{"key": "k", "value": pfeel.NewNumberFromInt64(1)}}}
	for _, expr := range []string{`context(l) = {k: 1}`, `context(l[1]) = {k: 1}`} {
		if got := mustEval(t, e, expr, vars); got != true {
			t.Errorf("%s = %#v, want true", expr, got)
		}
	}
}
