package feel

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

// TestForkedDefects pins the behaviour the vendored evaluator was forked to
// fix. Each case failed on upstream v1.0.6; if one of them regresses, the fork
// has been re-synced without re-applying a fix. See
// pkg/feel/internal/dialect/NOTICE.md.
func TestForkedDefects(t *testing.T) {
	e := newFEEL(t)

	t.Run("division is decimal, not integer", func(t *testing.T) {
		cases := map[string]float64{
			"1 / 3":      1.0 / 3.0,
			"0.079 / 12": 0.079 / 12,
			"7 / 2":      3.5,
			"-9 / 4":     -2.25,
		}
		for expr, want := range cases {
			got, ok := mustEval(t, e, expr, nil).(float64)
			if !ok || !approx(got, want) {
				t.Errorf("%s = %#v, want %v", expr, got, want)
			}
		}
	})

	t.Run("division by zero is null, not a panic", func(t *testing.T) {
		if got := mustEval(t, e, "1 / 0", nil); got != nil {
			t.Errorf("1 / 0 = %#v, want null", got)
		}
	})

	t.Run("keywords end a name", func(t *testing.T) {
		vars := map[string]any{"x": FromGo(true), "y": FromGo(false), "a": FromGo(1)}
		if got := mustEval(t, e, "x and y", vars); got != false {
			t.Errorf("x and y = %#v, want false (not a variable named \"x and y\")", got)
		}
		if got := mustEval(t, e, "x or y", vars); got != true {
			t.Errorf("x or y = %#v, want true", got)
		}
		if got := mustEval(t, e, "if x then 1 else 2", vars); got != 1.0 {
			t.Errorf("if x then 1 else 2 = %#v, want 1", got)
		}
		if got := mustEval(t, e, "a in [1, 2]", vars); got != true {
			t.Errorf("a in [1, 2] = %#v, want true", got)
		}
	})

	t.Run("standard names containing keywords still resolve", func(t *testing.T) {
		// `date and time`, `days and time duration` and `years and months
		// duration` all embed the reserved word `and`; the name grammar must
		// still admit them.
		if got := mustEval(t, e, `date and time("2024-03-01T09:00:00").year`, nil); got != 2024.0 {
			t.Errorf("date and time(...).year = %#v, want 2024", got)
		}
	})

	t.Run("dates are comparable", func(t *testing.T) {
		cases := map[string]any{
			`date("2024-01-01") < date("2024-06-01")`:  true,
			`date("2024-06-01") < date("2024-01-01")`:  false,
			`date("2024-06-01") >= date("2024-06-01")`: true,
			`date("2024-06-01") = date("2024-06-01")`:  true,
			// Mixed temporal types compare by the instant they denote.
			`date("2024-06-01") < date and time("2024-06-02T00:00:00")`: true,
		}
		for expr, want := range cases {
			if got := mustEval(t, e, expr, nil); got != want {
				t.Errorf("%s = %#v, want %v", expr, got, want)
			}
		}
	})

	t.Run("every interval bracket form parses", func(t *testing.T) {
		// DMN 1.5 §10.3.1.7: `]` opens an exclusive interval and `[` closes
		// one, alongside the parenthesis spellings. Upstream accepted only
		// `(a..b)`, `[a..b]`, `(a..b]` and `[a..b)`, so a table written in a
		// modeller that emits the bracket forms failed to load.
		c := NewCompiler(FEEL)
		cases := map[string][3]bool{
			// test => whether 2, 5 and 10 match
			"[2..10]": {true, true, true},
			"(2..10]": {false, true, true},
			"]2..10]": {false, true, true},
			"[2..10)": {true, true, false},
			"[2..10[": {true, true, false},
			"(2..10)": {false, true, false},
			"]2..10[": {false, true, false},
		}
		for src, want := range cases {
			ut, err := c.CompileUnaryTest(src)
			if err != nil {
				t.Errorf("%s does not compile: %v", src, err)
				continue
			}
			for i, in := range []float64{2, 5, 10} {
				got, err := e.EvalUnaryTest(ut, FromGo(in), nil)
				if err != nil {
					t.Errorf("%s against %v: %v", src, in, err)
					continue
				}
				if got != want[i] {
					t.Errorf("%s matched %v = %v, want %v", src, in, got, want[i])
				}
			}
		}
	})

	t.Run("indexing survives the interval-bracket change", func(t *testing.T) {
		// Reserving `[` as an interval's closing bracket must not cost the
		// list-index operator anywhere it is unambiguous.
		vars := map[string]any{"xs": []any{FromGo(7), FromGo(9)}}
		if got := mustEval(t, e, "xs[2]", vars); got != 9.0 {
			t.Errorf("xs[2] = %#v, want 9", got)
		}
		if got := mustEval(t, e, "[1, 2, 3][1]", nil); got != 1.0 {
			t.Errorf("[1, 2, 3][1] = %#v, want 1", got)
		}
		// Inside an interval endpoint it needs parentheses, which is the
		// documented trade-off in NOTICE.md.
		if _, err := NewCompiler(FEEL).Compile("[1..(xs[1])]"); err != nil {
			t.Errorf("[1..(xs[1])] does not compile: %v", err)
		}
	})

	t.Run("a parse error does not carry a Go stack trace", func(t *testing.T) {
		// The message reaches a modeller as a VERDICT_LOAD_013 diagnostic about
		// one cell of their table. Parser frames there are noise.
		_, err := NewCompiler(FEEL).CompileUnaryTest("<< 3")
		if err == nil {
			t.Fatal("\"<< 3\" compiled")
		}
		msg := err.Error()
		for _, leak := range []string{"callers:", "dialect.(*Parser)", "github.com/frankbardon"} {
			if strings.Contains(msg, leak) {
				t.Errorf("the parse error leaks %q:\n%s", leak, msg)
			}
		}
		// And it is anchored once, not once per wrapping layer.
		if n := strings.Count(msg, "cannot compile"); n != 1 {
			t.Errorf("the parse error names the text %d times, want 1:\n%s", n, msg)
		}
	})

	t.Run("ordering comparison with null is null", func(t *testing.T) {
		// DMN 1.5 §10.3.2: `<`, `<=`, `>` and `>=` with a null operand yield
		// null. Upstream ordered null below every value, so `2 < null` was
		// true (issue #15).
		for _, expr := range []string{
			"2 < null", "null < 2", "2 <= null", "null <= null",
			"2 > null", "null > 2", "2 >= null", "null >= null",
			"min([2]) < max([])", "max([]) > min([2])",
		} {
			if got := mustEval(t, e, expr, nil); got != nil {
				t.Errorf("%s = %#v, want null", expr, got)
			}
		}
		// Equality keeps its spec semantics: null is equal only to null.
		for expr, want := range map[string]any{
			"null = null": true, "2 = null": false, "null = 2": false,
			"null != null": false, "2 != null": true,
			"1 < 2": true, "2 <= 1": false,
		} {
			if got := mustEval(t, e, expr, nil); got != want {
				t.Errorf("%s = %#v, want %v", expr, got, want)
			}
		}
	})

	t.Run("a comparison unary test does not match a null input", func(t *testing.T) {
		// The comparison yields null, which is "not satisfied" — it must not
		// fall through to equality, where null would equal the null input.
		c := NewCompiler(FEEL)
		cases := map[string]bool{
			"< 5": false, "<= 5": false, "> 5": false, ">= 5": false,
			"? < 5": false, "< 5, null": true, "null": true,
			"not(< 5)": false, "not(< 5, 7)": false, "not(7)": true,
		}
		for src, want := range cases {
			ut, err := c.CompileUnaryTest(src)
			if err != nil {
				t.Errorf("%s does not compile: %v", src, err)
				continue
			}
			got, err := e.EvalUnaryTest(ut, nil, nil)
			if err != nil {
				t.Errorf("%s against null: %v", src, err)
				continue
			}
			if got != want {
				t.Errorf("%s matched null = %v, want %v", src, got, want)
			}
		}
	})

	t.Run("quantified expressions yield a boolean", func(t *testing.T) {
		// DMN 1.5 FEEL grammar rule 58: `some` and `every` yield true, false
		// or null. Upstream returned the first match from `some` and the list
		// of matches from `every` (issue #14).
		cases := []struct {
			expr string
			want any
		}{
			{"some h in [1,2,3] satisfies h > 1", true},
			{"some h in [1] satisfies h > 1", false},
			{"some h in [] satisfies h > 1", false},
			{"every h in [1,2,3] satisfies h > 0", true},
			{"every h in [1,2] satisfies h > 1", false},
			{"every h in [] satisfies h > 1", true},
			// Null folding: a decisive element wins, otherwise null beats
			// the default.
			{"some h in [null, 2] satisfies h > 1", true},
			{"some h in [null, 1] satisfies h > 1", nil},
			{`some h in [1, 2] satisfies "yes"`, nil},
			{"every h in [null, 1] satisfies h > 1", false},
			{"every h in [null, 2] satisfies h > 1", nil},
			{"every h in [2, 3] satisfies h", nil},
			// Composes with the boolean operators.
			{"(some h in [1,2] satisfies h > 1) and (every h in [1,2] satisfies h > 0)", true},
			{"(some h in [1] satisfies h > 1) or (every h in [1,2] satisfies h > 1)", false},
			{"x and (some h in xs satisfies h = 2)", true},
		}
		vars := map[string]any{"x": FromGo(true), "xs": FromGo([]any{1, 2, 3})}
		for _, c := range cases {
			if got := mustEval(t, e, c.expr, vars); got != c.want {
				t.Errorf("%s = %#v, want %#v", c.expr, got, c.want)
			}
		}
		// The bound name does not leak out of the expression's scope.
		if got := mustEval(t, e, "(some h in [1,2] satisfies h > 1) and h = null", nil); got != true {
			t.Errorf("h leaked out of the quantifier scope: got %#v", got)
		}
	})

	t.Run("quantified expressions accept several iteration contexts", func(t *testing.T) {
		// DMN 1.5 FEEL grammar rule 58: a comma-separated list of iteration
		// contexts ranges over the cartesian product, and a later context may
		// reference an earlier name. Upstream parsed a single context only.
		cases := []struct {
			expr string
			want any
		}{
			{"some x in [1,2], y in [3,4] satisfies x + y = 6", true},
			{"some x in [1,2], y in [3,4] satisfies x + y = 7", false},
			{"every x in [1,2], y in [3,4] satisfies x < y", true},
			{"every x in [1,2], y in [1,2] satisfies x <= y", false},
			// Dependent contexts.
			{"some x in [1,2], y in [x] satisfies y = 2", true},
			{"every x in [[1,2],[3]], y in x satisfies y > 0", true},
			{"some x in [1,2], y in [x], z in [x + y] satisfies z = 4", true},
			// Any empty context empties the product.
			{"some x in [1,2], y in [] satisfies true", false},
			{"every x in [1,2], y in [] satisfies false", true},
			{"some x in [], y in [1] satisfies true", false},
			{"every x in [], y in [1] satisfies false", true},
			// The quantify null folding holds over the whole product.
			{"some x in [1,2], y in [null, 2] satisfies y > x", true},
			{"some x in [1,2], y in [null, 1] satisfies y > x", nil},
			{"every x in [1,2], y in [null, 1] satisfies y > 1", false},
			{"every x in [1,2], y in [null, 2] satisfies y > 1", nil},
			// Composes inside a larger expression, including a call argument.
			{"count([some x in [1], y in [2] satisfies x < y, 1]) = 2", true},
		}
		for _, c := range cases {
			if got := mustEval(t, e, c.expr, nil); got != c.want {
				t.Errorf("%s = %#v, want %#v", c.expr, got, c.want)
			}
		}
		if got := mustEval(t, e, "(some x in [1], y in [2] satisfies x < y) and x = null and y = null", nil); got != true {
			t.Errorf("a bound name leaked out of the quantifier scope: got %#v", got)
		}
	})

	t.Run("for with several iteration contexts yields one flat list", func(t *testing.T) {
		// DMN 1.5 FEEL grammar rule 46 and §10.3.2.14: the contexts range over
		// their cartesian product, a later context may reference an earlier
		// name, and the result is one list. Upstream nested a list per outer
		// element. The body's own list result is never flattened.
		cases := map[string]string{
			"for x in [1,2], y in [3,4] return x * y":                 "[3 4 6 8]",
			"for x in [1,2], y in [x] return y":                       "[1 2]",
			"for x in [1,2], y in [x], z in [x + y] return [x, y, z]": "[[1 1 2] [2 2 4]]",
			"for x in [1,2], y in [] return x":                        "[]",
			"for x in [1,2] return x * 10":                            "[10 20]",
			"for x in [1,2] return [x]":                               "[[1] [2]]",
			"for x in [1,2], y in [3] return [x, y]":                  "[[1 3] [2 3]]",
			"count(for x in [1,2,3], y in [1,2] return x + y)":        "6",
		}
		for expr, want := range cases {
			got := mustEval(t, e, expr, nil)
			if s := fmt.Sprint(got); s != want {
				t.Errorf("%s = %s, want %s", expr, s, want)
			}
		}
	})

	t.Run("a quantifier over a non-list iterates the singleton list", func(t *testing.T) {
		// DMN 1.5 implicit conversion: a single value where a list is expected
		// is the one-element list holding it. Upstream raised a type mismatch,
		// failing the evaluation. Null is the singleton [null], so the
		// condition decides what an absent domain means.
		cases := []struct {
			expr string
			want any
		}{
			{"some x in 5 satisfies x > 1", true},
			{"every x in 5 satisfies x > 10", false},
			{"every x in 5 satisfies x = 5", true},
			{`some s in "a" satisfies s = "a"`, true},
			// Null domain: the predicate is evaluated once, over null.
			{"some x in null satisfies x > 1", nil},
			{"every x in null satisfies x > 1", nil},
			{"some x in null satisfies x = null", true},
			{"every x in null satisfies x != null", false},
			// Every iteration context, including a dependent one.
			{"some x in 1, y in [2, 3] satisfies x + y = 4", true},
			{"some x in [1, 2], y in 3 satisfies x + y = 5", true},
			{"every x in [1, 2], y in x satisfies y > 0", true},
			{"some x in [1, 2], y in x satisfies y > 1", true},
			{"every x in 1, y in null satisfies y = null", true},
		}
		for _, c := range cases {
			if got := mustEval(t, e, c.expr, nil); got != c.want {
				t.Errorf("%s = %#v, want %#v", c.expr, got, c.want)
			}
		}
		vars := map[string]any{"n": FromGo(7)}
		if got := mustEval(t, e, "some x in n satisfies x = 7", vars); got != true {
			t.Errorf("some over a scalar variable = %#v, want true", got)
		}
	})

	t.Run("a quantified result matches a true input entry", func(t *testing.T) {
		c := NewCompiler(FEEL)
		ut, err := c.CompileUnaryTest("true")
		if err != nil {
			t.Fatal(err)
		}
		for expr, want := range map[string]bool{
			"some h in [1,2,3] satisfies h > 2":  true,
			"every h in [1,2,3] satisfies h > 2": false,
		} {
			in, err := e.EvalText(expr, nil)
			if err != nil {
				t.Fatalf("%s: %v", expr, err)
			}
			got, err := e.EvalUnaryTest(ut, in, nil)
			if err != nil {
				t.Fatalf("%s against true: %v", expr, err)
			}
			if got != want {
				t.Errorf("true matched %s = %v, want %v", expr, got, want)
			}
		}
	})

	t.Run("range membership with null is null", func(t *testing.T) {
		// DMN 1.5 §10.3.2: membership is the endpoint comparisons, each null
		// for a null operand. Upstream ordered null below the start, so
		// `null in [1..10]` was false and its negation true.
		for _, expr := range []string{"null in [1..10]", "null in (1..10)", "null in ]1..10["} {
			if got := mustEval(t, e, expr, nil); got != nil {
				t.Errorf("%s = %#v, want null", expr, got)
			}
		}
		for expr, want := range map[string]any{
			"5 in [1..10]": true, "11 in [1..10]": false, "null in [1, null]": true,
		} {
			if got := mustEval(t, e, expr, nil); got != want {
				t.Errorf("%s = %#v, want %v", expr, got, want)
			}
		}
	})

	t.Run("a range unary test does not match a null input", func(t *testing.T) {
		c := NewCompiler(FEEL)
		cases := []struct {
			src   string
			input any
			want  bool
		}{
			{"[1..10]", nil, false}, {"(1..10)", nil, false}, {"]1..10[", nil, false},
			{"? in [1..10]", nil, false}, {"[1..10], null", nil, true},
			{"not([1..10])", nil, false}, {"not(? in [1..10])", nil, false},
			{"not([1..10])", 11, true}, {"not([1..10])", 5, false},
		}
		for _, tc := range cases {
			ut, err := c.CompileUnaryTest(tc.src)
			if err != nil {
				t.Errorf("%s does not compile: %v", tc.src, err)
				continue
			}
			got, err := e.EvalUnaryTest(ut, FromGo(tc.input), nil)
			if err != nil {
				t.Errorf("%s against %v: %v", tc.src, tc.input, err)
				continue
			}
			if got != tc.want {
				t.Errorf("%s matched %v = %v, want %v", tc.src, tc.input, got, tc.want)
			}
		}
	})

	t.Run("a range with a null endpoint yields null", func(t *testing.T) {
		// DMN 1.5 §10.3.2: membership and the range built-ins are endpoint
		// comparisons, each null for a null endpoint. Upstream ordered null
		// below every value, so `[null..10]` behaved as "up to 10".
		for _, expr := range []string{
			"5 in [null..10]", "5 in (5..null]", "15 in [null..10]", "5 in [null..null]",
			"before([null..10], [20..30])", "after([20..30], (5..null])",
			"before(1, [null..10])", "after([null..10], 20)",
			"meets([1..5], [5..null])", "met by([5..null], [1..5])",
			"overlaps([null..10], [5..20])", "overlaps before([1..10], [5..null])",
			"overlaps after([5..null], [1..10])", "includes([null..10], 5)",
			"includes([1..10], [null..5])", "during(5, [null..10])",
			"during([null..5], [1..10])", "starts(1, [1..null])", "started by([null..5], 1)",
			"finishes(10, [null..10])", "finished by([1..null], 10)",
			"coincides([null..10], [null..10])",
		} {
			if got := mustEval(t, e, expr, nil); got != nil {
				t.Errorf("%s = %#v, want null", expr, got)
			}
		}
		for expr, want := range map[string]any{
			"5 in [1..10]": true, "before([1..5], [6..10])": true,
			"includes([1..10], 5)": true, "overlaps([1..5], [6..10])": false,
			"before(1, 2)": true,
		} {
			if got := mustEval(t, e, expr, nil); got != want {
				t.Errorf("%s = %#v, want %v", expr, got, want)
			}
		}
	})

	t.Run("a range unary test with a null endpoint never matches", func(t *testing.T) {
		c := NewCompiler(FEEL)
		cases := []struct {
			src   string
			input any
			want  bool
		}{
			{"[null..10]", 5, false}, {"[null..10]", 15, false}, {"(5..null]", 7, false},
			{"? in [null..10]", 5, false}, {"not([null..10])", 5, false},
			{"not([null..10])", 15, false}, {"[null..10], 5", 5, true},
			// Open-ended tests are comparisons, not null-endpoint ranges.
			{"< 10", 5, true}, {"< 10", 15, false}, {">= 10", 10, true}, {"not(< 10)", 15, true},
		}
		for _, tc := range cases {
			ut, err := c.CompileUnaryTest(tc.src)
			if err != nil {
				t.Errorf("%s does not compile: %v", tc.src, err)
				continue
			}
			got, err := e.EvalUnaryTest(ut, FromGo(tc.input), nil)
			if err != nil {
				t.Errorf("%s against %v: %v", tc.src, tc.input, err)
				continue
			}
			if got != tc.want {
				t.Errorf("%s matched %v = %v, want %v", tc.src, tc.input, got, tc.want)
			}
		}
	})

	t.Run("arithmetic with null or mismatched operands is null", func(t *testing.T) {
		// DMN 1.5 §10.3.2: arithmetic over a null operand, or over a pair of
		// types the operator is not defined for, is null. Upstream raised a
		// type mismatch, and concatenated a string with a number.
		vars := map[string]any{"x": FromGo(3)}
		for _, expr := range []string{
			"1 + null", "null - 1", "null * 2", "2 / null", "null ** 2", "2 ** null",
			"-null", "null / 0", "x + missing",
			`1 + "a"`, `"a" + 1`, `"a" - "b"`, `"a" * 2`, "true + 1", "[1] * 2", `-"a"`,
			`@"2024-01-01T00:00:00" + null`, `null - @"2024-01-01T00:00:00"`,
		} {
			if got := mustEval(t, e, expr, vars); got != nil {
				t.Errorf("%s = %#v, want null", expr, got)
			}
		}
		// Division by zero is still null, and valid typed arithmetic is intact.
		for expr, want := range map[string]any{
			"1 / 0": nil, "0 / 0": nil,
			"1 + 2": 3.0, "x - 1": 2.0, "x * 2": 6.0, "x / 2": 1.5,
			`"a" + "b"`: "ab",
			`@"2024-01-02T00:00:00" - @"2024-01-01T00:00:00"`: 24 * time.Hour,
		} {
			if got := mustEval(t, e, expr, vars); got != want {
				t.Errorf("%s = %#v, want %#v", expr, got, want)
			}
		}
		want := time.Date(2024, time.January, 1, 1, 0, 0, 0, time.UTC)
		got, ok := mustEval(t, e, `@"2024-01-01T00:00:00" + duration("PT1H")`, nil).(time.Time)
		if !ok || !got.Equal(want) {
			t.Errorf("date and time + duration = %#v, want %v", got, want)
		}
	})

	t.Run("exponentiation and negation parse and evaluate", func(t *testing.T) {
		// DMN 1.5 grammar rules 4e and 4f. Upstream parsed neither.
		vars := map[string]any{"x": FromGo(3)}
		for expr, want := range map[string]any{
			"2 ** 3": 8.0, "2 ** -1": 0.5, "2 * 3 ** 2": 18.0, "2 ** 3 ** 2": 64.0,
			"-x": -3.0, "- x": -3.0, "-(x + 1)": -4.0, "--x": 3.0, "1 - -x": 4.0,
			"-x ** 2": 9.0, "-2 ** 2": 4.0,
			"0 ** -1": nil, "(-8) ** 0.5": nil, "10 ** 100000": nil,
			`-duration("PT1H")`: -time.Hour,
		} {
			if got := mustEval(t, e, expr, vars); got != want {
				t.Errorf("%s = %#v, want %#v", expr, got, want)
			}
		}
		if got, ok := mustEval(t, e, "2 ** 0.5", nil).(float64); !ok || !approx(got, 1.4142135623730951) {
			t.Errorf("2 ** 0.5 = %#v, want sqrt(2)", got)
		}
	})

	t.Run("multi-word names without keywords still resolve", func(t *testing.T) {
		vars := map[string]any{"Credit Rating": "good"}
		if got := mustEval(t, e, `Credit Rating = "good"`, vars); got != true {
			t.Errorf(`Credit Rating = "good" evaluated to %#v, want true`, got)
		}
	})
}

func approx(a, b float64) bool {
	d := a - b
	if d < 0 {
		d = -d
	}
	return d < 1e-9
}
