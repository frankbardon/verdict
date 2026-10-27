package feel

import (
	"errors"
	"fmt"
	"strings"
	"sync"
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
		// DMN 1.5 §10.3.1.2 (grammar rules 9, 11): `]` opens an exclusive interval and `[` closes
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
		// DMN 1.5 §10.3.2.15: `<`, `<=`, `>` and `>=` with a null operand yield
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

	t.Run("comparison of different kinds is null", func(t *testing.T) {
		// DMN 1.5 §10.3.2.2 (`"1" = 1` is null) and §10.3.2.15 (inputs outside
		// an operator's domain are null; Table 52 needs two operands of one
		// kind). Upstream raised "bad type in comparation" for every one.
		for _, expr := range []string{
			`1 = "a"`, `"a" = 1`, `1 != "a"`, `true = 1`, `[1] = ["a"]`, `{a: 1} = {a: "x"}`,
			`1 < "a"`, `1 <= "a"`, `"a" > 1`, `"a" >= 1`, `true < 1`,
			`date("2024-01-01") < 5`, `date("2024-01-01") = "2024-01-01"`,
			`duration("P1Y") < duration("P1D")`, `duration("P1Y") = duration("P1D")`,
			`duration("P1M") != duration("PT1H")`,
			`5 in ["a".."z"]`,
			`before(1, "a")`, `after("a", 1)`, `before(1, ["a".."z"])`, `after(["a".."z"], 1)`,
			`includes([1..10], "a")`, `during("a", [1..10])`, `starts(1, ["a".."z"])`,
			`finishes("z", [1..10])`, `meets([1..5], ["a".."z"])`, `overlaps([1..5], ["a".."z"])`,
			`coincides(1, "a")`, `coincides([1..5], ["a".."e"])`,
			`before(duration("P1Y"), duration("P1D"))`,
		} {
			if got := mustEval(t, e, expr, nil); got != nil {
				t.Errorf("%s = %#v, want null", expr, got)
			}
		}
		// Null equality and same-kind comparisons are unchanged.
		for expr, want := range map[string]any{
			"null = null": true, "2 = null": false, `"a" = null`: false, "2 != null": true,
			`1 = 1`: true, `"a" = "a"`: true, `"a" != "b"`: true, `1 < 2`: true, `"a" < "b"`: true, `[1] = [1, 2]`: false,
			`duration("P1Y") = duration("P12M")`: true, `duration("PT23H") < duration("P1D")`: true,
			`date("2024-01-01") < date and time("2024-01-02T00:00:00")`: true,
			`5 in [1..10]`: true, `before(1, 2)`: true, `coincides("a", "a")`: true,
		} {
			if got := mustEval(t, e, expr, nil); got != want {
				t.Errorf("%s = %#v, want %v", expr, got, want)
			}
		}
	})

	t.Run("lists and contexts compare element-wise", func(t *testing.T) {
		// DMN 1.5 §10.3.2.15, Table 53: lists are equal when they have the same
		// length and every element pair is `=`; contexts when they have the
		// same keys and every value pair is `=`. The result is the three-valued
		// conjunction of those comparisons. Table 54 gives neither kind an
		// order, so `<` and friends are null. Upstream answered false for any
		// two equal lists and ordered lists lexicographically.
		for expr, want := range map[string]any{
			`[1, 2] = [1, 2]`: true, `[1, 2] = [2, 1]`: false, `[1] = [1, 2]`: false, `[] = []`: true,
			`[[1, 2], [3]] = [[1, 2], [3]]`: true, `[[1, 2], [3]] = [[1, 2], [4]]`: false,
			`[1, null] = [1, null]`: true, `[1, null] = [1, 2]`: false, `[1, 2] = [1.0, 2.00]`: true,
			`[1, "a"] = [2, 2]`: false,
			`{a: 1} = {a: 1}`:   true, `{a: 1} = {a: 2}`: false, `{a: 1} = {b: 1}`: false,
			`{a: 1, b: 2} = {b: 2, a: 1}`: true, `{a: 1} = {a: 1, b: 2}`: false,
			`{a: [1, {b: 2}]} = {a: [1, {b: 2}]}`: true, `{a: null} = {a: null}`: true,
			`[1, 2] != [1, 2]`: false, `[1, 2] != [2, 1]`: true, `{a: 1} != {a: 2}`: true,
			`[1, 2] = null`: false, `null != {a: 1}`: true,
		} {
			if got := mustEval(t, e, expr, nil); got != want {
				t.Errorf("%s = %#v, want %v", expr, got, want)
			}
		}
		for _, expr := range []string{
			`[1, "a"] = [1, 2]`, `[1, "a"] != [1, 2]`, `{a: 1, b: "x"} = {a: 1, b: 2}`, `[[1, "a"]] = [[1, 2]]`,
			`[1, 2] < [1, 3]`, `[1, 2] <= [1, 2]`, `[1] > []`, `[1] >= [1]`,
			`{a: 1} < {a: 2}`, `{a: 1} >= {a: 1}`,
		} {
			if got := mustEval(t, e, expr, nil); got != nil {
				t.Errorf("%s = %#v, want null", expr, got)
			}
		}
	})

	t.Run("list built-ins find lists and contexts by value", func(t *testing.T) {
		// `in`, `list contains`, `index of`, `union`, `distinct values` and
		// `duplicate values` all search by `=`, so they inherit Table 53.
		for expr, want := range map[string]string{
			`[1, 2] in [[1, 2], [3]]`:                   "true",
			`{a: 1} in [{a: 1}]`:                        "true",
			`[2, 1] in [[1, 2], [3]]`:                   "false",
			`list contains([[1, 2], [3]], [1, 2])`:      "true",
			`list contains([{a: 1}], {a: 1})`:           "true",
			`list contains([[1, 2]], [2, 1])`:           "false",
			`index of([[1], [2], [1]], [1])`:            "[1 3]",
			`index of([{a: 1}, {a: 2}], {a: 2})`:        "[2]",
			`union([[1], [2]], [[1], [3]])`:             "[[1] [2] [3]]",
			`distinct values([[1, 2], [1, 2], [2, 1]])`: "[[1 2] [2 1]]",
			`distinct values([{a: 1}, {a: 1}])`:         "[map[a:1]]",
			`duplicate values([[1], [2], [1]])`:         "[[1]]",
		} {
			if got := fmt.Sprint(mustEval(t, e, expr, nil)); got != want {
				t.Errorf("%s = %s, want %s", expr, got, want)
			}
		}
	})

	t.Run("a unary test of another kind than its input does not match", func(t *testing.T) {
		// A decision table mixing kinds across rules must not error out: a
		// mismatched test is "not satisfied" (the comparisons are null), and
		// not(...) over a null comparison is null, so not a match either.
		c := NewCompiler(FEEL)
		cases := []struct {
			src   string
			input any
			want  bool
		}{
			{`"a"`, 5, false}, {`"a", 5`, 5, true}, {"5", "a", false},
			{"< 5", "a", false}, {">= 5", "a", false}, {`< "m"`, 5, false},
			{"? = 5", "a", false}, {"? != 5", "a", false},
			{"[1..10]", "a", false}, {`["a".."z"]`, 5, false},
			{"not(< 5)", "a", false}, {`not(< 5, "a")`, "a", false}, {`not(< 5, "b")`, "a", false}, {`not("b")`, "a", true},
			{`< 5`, 3, true},
			{"[1, 2]", []any{1, 2}, true}, {"[1, 2]", []any{2, 1}, false}, {"[1, 2]", 2, true},
			{"? = [1, 2]", []any{1, 2}, true}, {"not([1, 2])", []any{1, 2}, false},
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
		// DMN 1.5 FEEL grammar rule 46: `some` and `every` yield true, false
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
		// DMN 1.5 FEEL grammar rule 46: a comma-separated list of iteration
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
		// DMN 1.5 FEEL grammar rule 44 and §10.3.2.14: the contexts range over
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

	t.Run("a for over a non-list iterates the singleton list", func(t *testing.T) {
		// DMN 1.5 implicit conversion (§10.3.2.9.4), as for quantifiers: a single
		// value where a list is expected is the one-element list holding it.
		// Upstream raised a type mismatch. Null is the singleton [null], and a
		// range value is wrapped, not iterated; `1..3` still iterates.
		cases := map[string]string{
			"for x in 5 return x * 2":                 "[10]",
			"for x in null return x":                  "[<nil>]",
			`for s in "a" return s + "b"`:             "[ab]",
			"for x in n return x + 1":                 "[8]",
			"for x in 1, y in [2, 3] return x + y":    "[3 4]",
			"for x in [1, 2], y in 3 return x * y":    "[3 6]",
			"for x in [1, 2], y in x return y":        "[1 2]",
			"for x in [1, 2], y in null return y":     "[<nil> <nil>]",
			"for x in 1, y in 2, z in 3 return x+y+z": "[6]",
			"for x in [1..3] return 2 in x":           "[true]",
			"count(for x in [1..3] return x)":         "1",
			"for x in 1..3 return x":                  "[1 2 3]",
			"for x in 2, y in 1..x return y":          "[1 2]",
		}
		vars := map[string]any{"n": FromGo(7)}
		for expr, want := range cases {
			if s := fmt.Sprint(mustEval(t, e, expr, vars)); s != want {
				t.Errorf("%s = %s, want %s", expr, s, want)
			}
		}
	})

	t.Run("an iteration context iterates an integer range", func(t *testing.T) {
		// DMN 1.5 FEEL grammar rule 63 and §10.3.2.14: `name in a..b` iterates
		// the integers from a to b inclusive, descending when b < a. Upstream
		// rejected the `..` at parse time. Non-integer endpoints are outside
		// the semantic domain and make the whole expression null. The fork
		// also admits the form in some/every, where rule 46 has a plain
		// expression.
		lists := map[string]string{
			"for i in 1..3 return i":                     "[1 2 3]",
			"for i in 3..1 return i":                     "[3 2 1]",
			"for i in 2..2 return i":                     "[2]",
			"for i in -1..1 return i":                    "[-1 0 1]",
			"for i in 1..-1 return i":                    "[1 0 -1]",
			"for i in 0..4 return i ** 2":                "[0 1 4 9 16]",
			"for i in 1 + 1..2 * 2 return i":             "[2 3 4]",
			"for i in 1..2, j in [10, 20] return i + j":  "[11 21 12 22]",
			"for i in [1, 2], j in i..1 return [i, j]":   "[[1 1] [2 2] [2 1]]",
			"for i in 1..2, j in 1..i return j":          "[1 1 2]",
			"for i in 1..n return i":                     "[1 2 3]",
			"count(for i in 1..3, j in 1..3 return i*j)": "9",
		}
		vars := map[string]any{"n": FromGo(3)}
		for expr, want := range lists {
			if s := fmt.Sprint(mustEval(t, e, expr, vars)); s != want {
				t.Errorf("%s = %s, want %s", expr, s, want)
			}
		}
		cases := []struct {
			expr string
			want any
		}{
			{"some x in 1..3 satisfies x = 2", true},
			{"some x in 1..3 satisfies x = 4", false},
			{"every x in 1..3 satisfies x > 0", true},
			{"every x in 3..1 satisfies x > 1", false},
			{"some x in [1, 2], y in x..3 satisfies x + y = 6", false},
			{"some x in [1, 3], y in x..3 satisfies x + y = 6", true},
			// Non-integer endpoints: null, never a type mismatch or [null].
			{"for i in 1..2.5 return i", nil},
			{"for i in 1.5..3 return i", nil},
			{"for i in null..3 return i", nil},
			{`for i in "a"..3 return i`, nil},
			{"for i in [1, 2], j in 1..1.5 return j", nil},
			{"some x in 1..null satisfies x = 1", nil},
			{"every x in 0.5..2 satisfies x > 0", nil},
			// A range value is not an integer-range iteration context: it is
			// still the singleton holding the range (E1-S5).
			{"some x in [1..3] satisfies 2 in x", true},
		}
		for _, c := range cases {
			if got := mustEval(t, e, c.expr, nil); got != c.want {
				t.Errorf("%s = %#v, want %#v", c.expr, got, c.want)
			}
		}
	})

	t.Run("one evaluation has one iteration budget", func(t *testing.T) {
		// Upstream had no integer-range form and the fork's first version had
		// no bound, so `for i in 1..1000000000` built a billion-element list;
		// the next bounded each `a..b` context alone, so nested contexts still
		// multiplied. Every element any iteration context of one evaluation
		// produces — ranges and list domains alike — is now charged against
		// one budget, a range before it is allocated. The façade always sets
		// one.
		capped, err := NewEvaluator(FEEL, WithMaxIterations(6))
		if err != nil {
			t.Fatal(err)
		}
		within := []string{
			"for i in 1..6 return i",
			"for i in 6..1 return i",
			"some x in -1..1 satisfies x = 5",
			// 2 + 2*2: the outer domain and both inner ones.
			"for i in 1..2, j in 1..2 return i * j",
			"for i in [1, 2], j in [3, 4] return i * j",
			// Sequential contexts in one expression share the budget: 3 + 3.
			"count(for i in 1..3 return i) + count(for j in [1, 2, 3] return j)",
		}
		for _, expr := range within {
			if _, err := capped.EvalText(expr, nil); err != nil {
				t.Errorf("%s under a budget of 6: %v", expr, err)
			}
		}
		over := []string{
			"for i in 1..7 return i",
			"for i in 7..1 return i",
			"every x in 0..6 satisfies x >= 0",
			// 1 + 7: a list context and a range context.
			"for i in [1], j in 1..7 return j",
			// 3 + 3*3 = 12: each context alone is well inside the budget.
			"for i in 1..3, j in 1..3 return i * j",
			"for i in 1..3 return for j in 1..3 return j",
			"some i in 1..3 satisfies some j in 1..3 satisfies j > 3",
			// List domains are charged too.
			"for i in [1, 2, 3], j in [1, 2, 3] return j",
			"for x in xs return x",
			"count(for i in 1..4 return i) + count(for j in [1, 2, 3] return j)",
			// The full int64 span: the length check must not overflow.
			"for i in -9223372036854775808..9223372036854775807 return i",
		}
		vars := map[string]any{"xs": []any{1, 2, 3, 4, 5, 6, 7}}
		for _, expr := range over {
			_, err := capped.EvalText(expr, vars)
			if !errors.Is(err, ErrIterationBudgetExceeded) {
				t.Errorf("%s under a budget of 6: err = %v, want ErrIterationBudgetExceeded", expr, err)
			}
		}
		// The budget is per evaluation: a second evaluation of the same
		// compiled expression starts from zero again.
		x, err := capped.Compile("for i in 1..6 return i")
		if err != nil {
			t.Fatal(err)
		}
		for n := 0; n < 3; n++ {
			if _, err := capped.Eval(x, nil); err != nil {
				t.Errorf("evaluation %d of a within-budget expression: %v", n+1, err)
			}
		}
		if got := newFEEL(t).MaxIterations(); got != DefaultMaxIterations {
			t.Errorf("default budget = %d, want %d", got, DefaultMaxIterations)
		}
		for _, expr := range []string{
			"for i in 1..1000000000 return i",
			"for i in 1..1001, j in 1..1000 return j",
		} {
			if _, err := newFEEL(t).EvalText(expr, nil); !errors.Is(err, ErrIterationBudgetExceeded) {
				t.Errorf("%s under the default budget: err = %v, want ErrIterationBudgetExceeded", expr, err)
			}
		}
	})

	t.Run("concurrent evaluations do not share an iteration budget", func(t *testing.T) {
		// One evaluator and one compiled expression, as sibling decisions
		// evaluating in parallel share them. Each evaluation spends the whole
		// budget; were the counter shared, all but the first would fail.
		capped, err := NewEvaluator(FEEL, WithMaxIterations(1000))
		if err != nil {
			t.Fatal(err)
		}
		x, err := capped.Compile("count(for i in 1..10, j in 1..90 return j)")
		if err != nil {
			t.Fatal(err)
		}
		const workers = 16
		var wg sync.WaitGroup
		errs := make(chan error, workers*10)
		for w := 0; w < workers; w++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for n := 0; n < 10; n++ {
					if _, err := capped.Eval(x, nil); err != nil {
						errs <- err
					}
				}
			}()
		}
		wg.Wait()
		close(errs)
		for err := range errs {
			t.Errorf("a within-budget evaluation failed alongside others: %v", err)
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
		// DMN 1.5 §10.3.2.15: membership is the endpoint comparisons, each null
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
		// DMN 1.5 §10.3.2.15: membership and the range built-ins are endpoint
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

	t.Run("a range built-in with a null operand yields null", func(t *testing.T) {
		// DMN 1.5 §10.3.4.7: the range functions are ordering comparisons over
		// points, and a parameter outside a built-in's domain makes it null.
		// Upstream ordered a null point below every value, and raised a type
		// mismatch for a null where a range was expected.
		for _, expr := range []string{
			"before(null, [1..10])", "before([1..10], null)", "before(null, 5)", "before(5, null)",
			"after(null, 5)", "after(5, null)", "after(null, [1..10])", "after([1..10], null)",
			"meets(null, [1..5])", "meets([1..5], null)", "met by(null, [1..5])", "met by([1..5], null)",
			"overlaps(null, [1..5])", "overlaps([1..5], null)",
			"overlaps before(null, [1..5])", "overlaps before([1..5], null)",
			"overlaps after(null, [1..5])", "overlaps after([1..5], null)",
			"finishes(null, [1..10])", "finishes(10, null)",
			"finished by([1..10], null)", "finished by(null, 10)",
			"includes([1..10], null)", "includes(null, 5)",
			"during(null, [1..10])", "during(5, null)",
			"starts(null, [1..10])", "starts(1, null)",
			"started by([1..10], null)", "started by(null, 1)",
			"coincides(null, 5)", "coincides(5, null)", "coincides(null, null)",
			"coincides(null, [1..5])", "coincides([1..5], null)",
		} {
			if got := mustEval(t, e, expr, nil); got != nil {
				t.Errorf("%s = %#v, want null", expr, got)
			}
		}
		// Non-null operands are untouched.
		for expr, want := range map[string]any{
			"before(1, [2..10])": true, "after(11, [1..10])": true, "before(5, 1)": false,
			"includes([1..10], 5)": true, "during(5, [1..10])": true,
			"starts(1, [1..10])": true, "finishes(10, [1..10])": true,
			"coincides([1..5], [1..5])": true,
		} {
			if got := mustEval(t, e, expr, nil); got != want {
				t.Errorf("%s = %#v, want %v", expr, got, want)
			}
		}
	})

	t.Run("range built-ins accept a point in a point position", func(t *testing.T) {
		// DMN 1.5 §10.3.4.7: coincides(point1, point2) is point1 = point2, and
		// coincides(range1, range2) compares endpoints and closedness.
		// Upstream decoded a number into a zero range, so coincides(1, 1)
		// raised "bad type in comparation, <nil> vs. <nil>". The point/point
		// and point/range overloads of the other range built-ins already
		// worked; they are pinned here so a re-sync cannot regress them.
		for expr, want := range map[string]any{
			"coincides(1, 1)": true, "coincides(1, 2)": false,
			`coincides("a", "a")`:                               true,
			`coincides(date("2024-01-01"), date("2024-01-01"))`: true,
			"coincides([1..5], [1..5])":                         true, "coincides([1..5], (1..5])": false,
			"coincides([1..5], [1..5))": false, "coincides([1..5], [1..6])": false,
			"before(1, 10)": true, "before(10, 1)": false, "before(1, 1)": false,
			"after(10, 1)": true, "after(1, 10)": false, "after(1, 1)": false,
			"before(1, [2..10])": true, "before(2, (2..10])": true, "before(2, [2..10])": false,
			"before([1..10], 11)": true, "before([1..10), 10)": true, "before([1..10], 10)": false,
			"after(11, [1..10])": true, "after(10, [1..10))": true, "after(10, [1..10])": false,
			"after([5..10], 4)": true, "after((5..10], 5)": true, "after([5..10], 5)": false,
			"finishes(10, [1..10])": true, "finishes(10, [1..10))": false,
			"finished by([1..10], 10)": true, "finished by([1..10), 10)": false,
			"starts(1, [1..10])": true, "starts(1, (1..10])": false,
			"started by([1..10], 1)": true, "started by((1..10], 1)": false,
			"includes([1..10], 1)": true, "includes((1..10], 1)": false, "includes([1..10], 11)": false,
			"during(5, [1..10])": true, "during(1, (1..10])": false, "during(10, [1..10))": false,
		} {
			if got := mustEval(t, e, expr, nil); got != want {
				t.Errorf("%s = %#v, want %v", expr, got, want)
			}
		}
		// A point paired with a range matches neither coincides signature.
		for _, expr := range []string{"coincides(1, [1..1])", "coincides([1..1], 1)"} {
			if got := mustEval(t, e, expr, nil); got != nil {
				t.Errorf("%s = %#v, want null", expr, got)
			}
		}
	})

	t.Run("abs dispatches on the argument type", func(t *testing.T) {
		// DMN 1.5 §10.3.4.5: abs(n) over a number, a days and time duration or
		// a years and months duration. Upstream decoded every argument into a
		// duration, so abs(2) was the empty duration "PT" and abs(null) "PT"
		// too. Anything outside the domain is null (§10.3.4). A negative Go
		// duration or date-and-time difference also kept its sign in the
		// fields rather than in Neg, so abs left it negative.
		vars := map[string]any{"x": FromGo(-3), "d": FromGo(-5 * time.Hour)}
		for expr, want := range map[string]any{
			"abs(2)": 2.0, "abs(-2)": 2.0, "abs(-2.5)": 2.5, "abs(0)": 0.0, "abs(10)": 10.0, "abs(-10)": 10.0,
			"abs(x)": 3.0, "abs(n: -7)": 7.0, "abs(-x)": 3.0,
			`abs(duration("-PT5H"))`: 5 * time.Hour, `abs(duration("PT5H"))`: 5 * time.Hour,
			`abs(@"-PT5H")`: 5 * time.Hour, "abs(d)": 5 * time.Hour, "-d": 5 * time.Hour,
			`abs(date and time("2024-01-01T00:00:00") - date and time("2024-01-02T01:00:00"))`:    25 * time.Hour,
			`string(date and time("2024-01-01T00:00:00") - date and time("2024-01-02T01:00:00"))`: "-P1DT1H",
			`string(d)`: "-PT5H",
			`abs(duration("-P1Y")) = duration("P1Y")`:     true,
			`abs(duration("-P1Y2M")) = duration("P1Y2M")`: true,
			`abs(duration("P1Y")) = duration("P1Y")`:      true,
			"abs(null)":                                   nil, `abs("a")`: nil, "abs(true)": nil, "abs([1])": nil,
			`abs(date("2024-01-01"))`: nil, "abs([1..2])": nil,
		} {
			if got := mustEval(t, e, expr, vars); got != want {
				t.Errorf("%s = %#v, want %#v", expr, got, want)
			}
		}
	})

	t.Run("numeric built-ins answer numbers", func(t *testing.T) {
		// The audit that found abs: the other DMN 1.5 §10.3.4.5 numeric
		// built-ins are Verdict's supplement (pkg/feel/stdlib.go), which reads
		// a *Number directly rather than through upstream's mapstructure
		// decode, so none of them mis-dispatches a number. Pinned here with
		// the spec's own examples so a re-sync that brings an upstream
		// overload of any of these names (which would then take precedence
		// over the supplement) cannot regress them silently.
		for expr, want := range map[string]any{
			"floor(1.5)": 1.0, "floor(-1.5)": -2.0, "floor(-1.56, 1)": -1.6, "floor(2)": 2.0,
			"ceiling(1.5)": 2.0, "ceiling(-1.5)": -1.0, "ceiling(-1.56, 1)": -1.5, "ceiling(2)": 2.0,
			"round up(5.5, 0)": 6.0, "round up(-5.5, 0)": -6.0, "round up(1.121, 2)": 1.13, "round up(-1.126, 2)": -1.13,
			"round down(5.5, 0)": 5.0, "round down(-5.5, 0)": -5.0, "round down(1.121, 2)": 1.12, "round down(-1.126, 2)": -1.12,
			"round half up(5.5, 0)": 6.0, "round half up(-5.5, 0)": -6.0, "round half up(1.121, 2)": 1.12, "round half up(-1.126, 2)": -1.13,
			"round half down(5.5, 0)": 5.0, "round half down(-5.5, 0)": -5.0, "round half down(1.121, 2)": 1.12, "round half down(-1.126, 2)": -1.13,
			"decimal(1/3, 2)": 0.33, "decimal(1.5, 0)": 2.0, "decimal(2.5, 0)": 2.0, "decimal(2, 0)": 2.0,
			"modulo(12, 5)": 2.0, "modulo(-12, 5)": 3.0, "modulo(12, -5)": -3.0, "modulo(-12, -5)": -2.0, "modulo(10, 0)": nil,
			"sqrt(16)": 4.0, "sqrt(-1)": nil,
			"odd(5)": true, "odd(2)": false, "even(5)": false, "even(2)": true,
		} {
			if got := mustEval(t, e, expr, nil); got != want {
				t.Errorf("%s = %#v, want %#v", expr, got, want)
			}
		}
		for expr, want := range map[string]float64{"log(10)": 2.302585092994046, "exp(5)": 148.4131591025766} {
			if got, ok := mustEval(t, e, expr, nil).(float64); !ok || !approx(got, want) {
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
		// DMN 1.5 §10.3.2.15: arithmetic over a null operand, or over a pair of
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
		// DMN 1.5 grammar rules 4.c and 4.d. Upstream parsed neither.
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

	t.Run("binary minus without spaces is subtraction", func(t *testing.T) {
		// Upstream scanned `-1` as a negative literal even after an operand, so
		// `x-1` was `x` then `-1` and evaluated to -1, and `1..n-1` did not
		// parse. A minus is now always its own token.
		vars := map[string]any{"x": FromGo(5), "n": FromGo(4), "a": FromGo(7), "b": FromGo(2)}
		for expr, want := range map[string]any{
			"x-1": 4.0, "2-1": 1.0, "(3)-1": 2.0, "a-b": 5.0, "floor(2.5)-1": 1.0,
			"[1,2][1]-1": 0.0, "x-1-1": 3.0, "x - -1": 6.0, "x--1": 6.0,
			"-1": -1.0, "2 * -1": -2.0, "2*-1": -2.0, "-9 / 4": -2.25, "-0.5": -0.5,
			"[-1, -2][2]": -2.0, "count([-1, -2])": 2.0,
		} {
			if got := mustEval(t, e, expr, vars); got != want {
				t.Errorf("%s = %#v, want %#v", expr, got, want)
			}
		}
		for expr, want := range map[string]string{
			"for i in 1..n-1 return i": "[1 2 3]",
			"for i in 1..-1 return i":  "[1 0 -1]",
		} {
			if got := fmt.Sprint(mustEval(t, e, expr, vars)); got != want {
				t.Errorf("%s = %s, want %s", expr, got, want)
			}
		}
	})

	t.Run("negative numbers in unary tests", func(t *testing.T) {
		for _, d := range []Dialect{FEEL, SFEEL} {
			c := NewCompiler(d)
			for src, want := range map[string][3]bool{
				// test => whether -5, -1 and 0 match
				"-1":       {false, true, false},
				"< -1":     {true, false, false},
				">= -1":    {false, true, true},
				"[-5..-1]": {true, true, false},
				"(-5..-1)": {false, false, false},
				"-5, 0":    {true, false, true},
			} {
				ut, err := c.CompileUnaryTest(src)
				if err != nil {
					t.Errorf("dialect %v: %s does not compile: %v", d, src, err)
					continue
				}
				for i, in := range []float64{-5, -1, 0} {
					got, err := e.EvalUnaryTest(ut, FromGo(in), nil)
					if err != nil {
						t.Errorf("%s against %v: %v", src, in, err)
						continue
					}
					if got != want[i] {
						t.Errorf("dialect %v: %s matched %v = %v, want %v", d, src, in, got, want[i])
					}
				}
			}
		}
		// The analyser reads a negated literal as the boundary it denotes.
		ut, err := NewCompiler(FEEL).CompileUnaryTest("[-5..-1]")
		if err != nil {
			t.Fatal(err)
		}
		lits := ut.Literals()
		if len(lits) != 2 || lits[0].Text != "-5" || lits[1].Text != "-1" {
			t.Errorf("[-5..-1] literals = %#v, want -5 and -1", lits)
		}
	})

	t.Run("days-and-time durations with a day part", func(t *testing.T) {
		// DMN 1.5 §10.3.2.3.7: a days and time duration is xs:dayTimeDuration,
		// PnDTnHnMnS, whose T appears only before a time component.
		strs := map[string]string{
			`string(duration("P1D"))`:                                         "P1D",
			`string(@"P1D")`:                                                  "P1D",
			`string(@"P1DT2H30M")`:                                            "P1DT2H30M",
			`string(duration("-P2D"))`:                                        "-P2D",
			`string(duration("PT1H"))`:                                        "PT1H",
			`string(duration("PT0S"))`:                                        "PT0S",
			`string(duration("P1Y2M"))`:                                       "P1Y2M",
			`string(date("2026-01-01") + duration("P1D"))`:                    "2026-01-02",
			`string(date("2026-01-01") - duration("P1D"))`:                    "2025-12-31",
			`string(date("2026-01-01") + duration("PT23H"))`:                  "2026-01-01",
			`string(date("2026-01-15") + duration("P1Y2M"))`:                  "2027-03-15",
			`string(date and time("2026-01-01T00:00:00") + @"P1DT1H")`:        "2026-01-02T01:00:00",
			`string(date and time("2026-01-01T00:00:00") - duration("-P2D"))`: "2026-01-03T00:00:00",
		}
		for expr, want := range strs {
			if got := mustEval(t, e, expr, nil); got != want {
				t.Errorf("%s = %#v, want %q", expr, got, want)
			}
		}
		if got := mustEval(t, e, `duration("P1D").days`, nil); got != 1.0 {
			t.Errorf(`duration("P1D").days = %#v, want 1`, got)
		}
		bools := map[string]bool{
			`duration("P1D") = duration("PT24H")`: true,
			`duration("P1D") = @"PT23H"`:          false,
			`duration("P1D") > duration("PT23H")`: true,
			`duration("-P2D") < duration("PT1H")`: true,
			`duration("P1Y") = duration("P12M")`:  true,
			`duration("P1Y") < duration("P1Y1M")`: true,
			`duration("P1DT1H") >= @"PT25M"`:      true,
		}
		for expr, want := range bools {
			if got := mustEval(t, e, expr, nil); got != want {
				t.Errorf("%s = %#v, want %v", expr, got, want)
			}
		}
		// Neither xs:dayTimeDuration nor xs:yearMonthDuration: rejected as any
		// malformed duration string is, never read as a partial duration —
		// null from duration() (§10.3.4), a syntax error as an at-literal
		// (see "conversion built-ins are null outside their domain").
		for _, expr := range []string{`duration("P1Y2D")`, `duration("PT")`, `duration("P1DT")`} {
			if got := mustEval(t, e, expr, nil); got != nil {
				t.Errorf("%s = %#v, want null", expr, got)
			}
		}
		if _, err := e.Compile(`@"P1Y2D"`); err == nil {
			t.Errorf(`@"P1Y2D" compiled, want a syntax error`)
		}
		// The two duration kinds are distinct types and do not compare: the
		// comparison is null (see "comparison of different kinds is null").
		if got := mustEval(t, e, `duration("P1Y") = duration("P1D")`, nil); got != nil {
			t.Errorf(`duration("P1Y") = duration("P1D") = %#v, want null`, got)
		}
	})

	t.Run("durations normalise and take fractional seconds", func(t *testing.T) {
		// DMN 1.5 §10.3.2.3.7 and §10.3.2.3.8: no component of an
		// xs:dayTimeDuration or xs:yearMonthDuration is capped, and a duration
		// is normalised so the sum of its components is minimised. Upstream
		// raised over 12 months, 24 hours or 60 minutes or seconds, and parsed
		// whole seconds only.
		strs := map[string]string{
			// Table 72's own example, and the two in §10.3.2.3.7/8.
			`string(duration("P26M"))`:    "P2Y2M",
			`string(duration("P0Y13M"))`:  "P1Y1M",
			`string(duration("P0DT25H"))`: "P1DT1H",
			`string(duration("PT36H"))`:   "P1DT12H",
			`string(@"PT36H")`:            "P1DT12H",
			`string(duration("PT90M"))`:   "PT1H30M",
			`string(duration("PT3600S"))`: "PT1H",
			`string(duration("P14M"))`:    "P1Y2M",
			`string(duration("P12M"))`:    "P1Y",
			`string(duration("-PT36H"))`:  "-P1DT12H",
			`string(duration("PT1.5S"))`:  "PT1.5S",
			`string(duration("PT1.50S"))`: "PT1.5S",
			`string(duration("PT0.5S"))`:  "PT0.5S",
			`string(duration("PT.25S"))`:  "PT0.25S",
			`string(duration("PT1.S"))`:   "PT1S",
			`string(duration("PT0.0S"))`:  "PT0S",
			`string(duration("-PT0S"))`:   "PT0S",
			`string(duration("PT61.5S"))`: "PT1M1.5S",
			// Nanosecond resolution, exactly; zeros past it are harmless.
			`string(duration("PT0.000000001S"))`:              "PT0.000000001S",
			`string(duration("PT0.1234567890000S"))`:          "PT0.123456789S",
			`string(duration("PT1.5S") + duration("PT0.5S"))`: "PT2S",
			`string(duration("PT0.1S") + duration("PT0.2S"))`: "PT0.3S",
			`string(duration("P1Y") - duration("P2M"))`:       "P10M",
			`string(duration("P2M") - duration("P1Y"))`:       "-P10M",
			`string(duration("PT12H") + duration("PT12H"))`:   "P1D",
			`string(duration("P1D") - duration("PT36H"))`:     "-PT12H",
			`string(duration("P1Y") - duration("P1Y"))`:       "P0M",
		}
		for expr, want := range strs {
			if got := mustEval(t, e, expr, nil); got != want {
				t.Errorf("%s = %#v, want %q", expr, got, want)
			}
		}
		bools := map[string]bool{
			`duration("PT36H") = duration("P1DT12H")`:                                                            true,
			`duration("PT90M") = duration("PT1H30M")`:                                                            true,
			`duration("P14M") = duration("P1Y2M")`:                                                               true,
			`duration("P2Y2M") = duration("P26M")`:                                                               true,
			`duration("PT1.5S") + duration("PT0.5S") = duration("PT2S")`:                                         true,
			`duration("PT1.5S") > duration("PT1S")`:                                                              true,
			`duration("PT0.5S") < duration("PT1S")`:                                                              true,
			`date and time("2026-01-01T00:00:00") + duration("PT1.5S") = date and time("2026-01-01T00:00:01.5")`: true,
			`date and time("2026-01-01T00:00:00") + duration("PT1.5S") + duration("PT0.5S") = date and time("2026-01-01T00:00:02")`: true,
			`date and time("2026-01-01T00:00:00") + duration("PT36H") = date and time("2026-01-02T12:00:00")`:                       true,
			`date and time("2026-01-01T00:00:00") + duration("P26M") = date and time("2028-03-01T00:00:00")`:                        true,
			`date and time("2026-01-01T00:00:01.5") - date and time("2026-01-01T00:00:00") = duration("PT1.5S")`:                    true,
			// A zero years and months duration is the identity of its kind.
			`duration("P1Y") - duration("P1Y") + duration("P1M") = duration("P1M")`: true,
		}
		for expr, want := range bools {
			if got := mustEval(t, e, expr, nil); got != want {
				t.Errorf("%s = %#v, want %v", expr, got, want)
			}
		}
		// Components read back normalised; fractional seconds as a number.
		nums := map[string]float64{
			`duration("PT36H").days`: 1, `duration("PT36H").hours`: 12,
			`duration("P26M").years`: 2, `duration("P26M").months`: 2,
			`duration("PT1.5S").seconds`: 1.5, `duration("PT90M").minutes`: 30,
		}
		for expr, want := range nums {
			if got := mustEval(t, e, expr, nil); got != want {
				t.Errorf("%s = %#v, want %v", expr, got, want)
			}
		}
		if got := mustEval(t, e, `duration("PT1.5S")`, nil); got != 1500*time.Millisecond {
			t.Errorf(`duration("PT1.5S") = %#v, want 1.5s`, got)
		}
		// Adding the two kinds is outside + 's domain (§10.3.2.15): null.
		for _, expr := range []string{`duration("P1Y") + duration("P1D")`, `duration("PT1H") - duration("P1M")`} {
			if got := mustEval(t, e, expr, nil); got != nil {
				t.Errorf("%s = %#v, want null", expr, got)
			}
		}
	})

	t.Run("Table 57 temporal addition and subtraction", func(t *testing.T) {
		// DMN 1.5 §10.3.2.15 Table 57, row by row as the fork supports it.
		// Upstream had no case for a duration on the left of +, for a date or
		// a time on the left of either operator, or for date and time - date,
		// so all of these were null; and date and time - time subtracted the
		// time's instant on 0000-01-01.
		strs := map[string]string{
			// date and time - date and time; a date is UTC midnight.
			`string(date and time("2026-01-02T01:00:00") - date and time("2026-01-01T00:00:00"))`: "P1DT1H",
			`string(date("2026-01-02") - date("2026-01-01"))`:                                     "P1D",
			`string(date("2026-01-01") - date("2026-03-01"))`:                                     "-P59D",
			`string(date("2024-03-01") - date("2024-02-01"))`:                                     "P29D",
			`string(date and time("2026-01-02T06:00:00") - date("2026-01-01"))`:                   "P1DT6H",
			`string(date("2026-01-02") - date and time("2026-01-01T18:00:00"))`:                   "PT6H",
			// time - time.
			`string(time("10:30:00") - time("08:00:00"))`:        "PT2H30M",
			`string(time("08:00:00") - time("10:30:00"))`:        "-PT2H30M",
			`string(time("10:00:00") - time("10:00:00"))`:        "PT0S",
			`string(time("10:00:00+02:00") - time("08:00:00Z"))`: "PT0S",
			// date and time ± years and months duration, and commuted.
			`string(date and time("2026-01-15T10:00:00") + duration("P1Y2M"))`: "2027-03-15T10:00:00",
			`string(date and time("2026-01-15T10:00:00") - duration("P1M"))`:   "2025-12-15T10:00:00",
			`string(duration("P1Y") + date and time("2026-01-01T10:00:00"))`:   "2027-01-01T10:00:00",
			// date and time ± days and time duration, and commuted.
			`string(duration("P1DT1H") + date and time("2026-01-01T00:00:00"))`: "2026-01-02T01:00:00",
			// time ± days and time duration wraps within the day, and commutes.
			`string(time("10:00:00+02:00") + duration("PT1H"))`: "11:00:00+02:00",
			`string(time("23:00:00Z") + duration("PT2H"))`:      "01:00:00Z",
			`string(time("01:00:00Z") - duration("PT2H"))`:      "23:00:00Z",
			`string(time("10:00:00Z") + duration("P3DT1H"))`:    "11:00:00Z",
			`string(time("10:00:00Z") - duration("-PT1.5S"))`:   "10:00:01Z",
			`string(duration("PT90M") + time("10:00:00Z"))`:     "11:30:00Z",
			// date ± years and months duration, and commuted.
			`string(date("2026-01-15") + duration("P1M"))`: "2026-02-15",
			`string(date("2026-01-15") - duration("P1Y"))`: "2025-01-15",
			`string(@"P1M" + date("2026-01-15"))`:          "2026-02-15",
			// date ± days and time duration, and commuted.
			`string(duration("P1D") + date("2026-01-01"))`:   "2026-01-02",
			`string(duration("PT23H") + date("2026-01-01"))`: "2026-01-01",
			`string(date("2026-01-01") - duration("PT1H"))`:  "2025-12-31",
		}
		for expr, want := range strs {
			if got := mustEval(t, e, expr, nil); got != want {
				t.Errorf("%s = %#v, want %q", expr, got, want)
			}
		}
		bools := map[string]bool{
			`time("23:00:00") + duration("PT2H") = time("01:00:00")`:             true,
			`duration("P1D") + time("10:00:00") = time("10:00:00")`:              true,
			`date("2026-01-02") - date("2026-01-01") = duration("PT24H")`:        true,
			`date and time("2026-01-02T00:00:00") - date("2026-01-01") = @"P1D"`: true,
			`duration("P1D") + date("2026-01-01") = date("2026-01-01") + @"P1D"`: true,
			`time("12:00:00") - time("11:00:00") < duration("PT90M")`:            true,
			// years and months between dates stays a built-in, not date - date.
			`years and months duration(date("2026-01-01"), date("2027-03-15")) = duration("P1Y2M")`: true,
		}
		for expr, want := range bools {
			if got := mustEval(t, e, expr, nil); got != want {
				t.Errorf("%s = %#v, want %v", expr, got, want)
			}
		}
		// Pairs Table 57 does not list, and subtraction from a duration, which
		// it marks undefined, are null.
		for _, expr := range []string{
			`duration("P1D") - date("2026-01-01")`,
			`duration("P1D") - date and time("2026-01-01T00:00:00")`,
			`duration("PT1H") - time("10:00:00")`,
			`duration("P1M") - date("2026-01-01")`,
			`time("10:00:00") + duration("P1Y")`, `time("10:00:00") - duration("P1M")`,
			`duration("P1M") + time("10:00:00")`,
			`date("2026-01-01") + date("2026-01-02")`,
			`date and time("2026-01-01T00:00:00") + date and time("2026-01-01T00:00:00")`,
			`time("10:00:00") + time("11:00:00")`,
			`date and time("2026-01-01T10:00:00") - time("09:00:00")`,
			`time("10:00:00") - date and time("2026-01-01T09:00:00")`,
			`date("2026-01-01") - time("09:00:00")`, `time("09:00:00") - date("2026-01-01")`,
			`date("2026-01-01") + time("09:00:00")`,
		} {
			got, err := e.EvalText(expr, nil)
			if err != nil {
				t.Errorf("%s raised %v, want null", expr, err)
			} else if ToGo(got) != nil {
				t.Errorf("%s = %#v, want null", expr, ToGo(got))
			}
		}
		// A difference beyond time.Duration's range raises rather than
		// saturating, as a too-long duration string does.
		if _, err := e.EvalText(`date("9999-12-31") - date("0001-01-01")`, nil); err == nil ||
			!strings.Contains(err.Error(), "duration exceeds the supported range") {
			t.Errorf(`date("9999-12-31") - date("0001-01-01") err = %v, want the duration range error`, err)
		}
	})

	t.Run("years and months duration pins the day to the end of the month", func(t *testing.T) {
		// DMN 1.5 §9.4 gives date ± years and months duration the semantics of
		// XPath op:add-yearMonthDuration-to-date(Time), whose XSD 1.1 Part 2
		// Appendix E algorithm pins a day past the end of the resulting month
		// to its last day. Upstream built the result with time.Date, which
		// rolled it forwards: 2026-01-31 + P1M was 2026-03-03.
		strs := map[string]string{
			`string(date("2026-01-31") + duration("P1M"))`:   "2026-02-28",
			`string(date("2028-01-31") + duration("P1M"))`:   "2028-02-29",
			`string(date("2026-03-31") - duration("P1M"))`:   "2026-02-28",
			`string(date("2026-01-31") + duration("P1Y1M"))`: "2027-02-28",
			`string(date("2028-02-29") + duration("P1Y"))`:   "2029-02-28",
			`string(date("2028-02-29") - duration("P4Y"))`:   "2024-02-29",
			`string(date("2026-05-31") + duration("-P1M"))`:  "2026-04-30",
			`string(duration("P1M") + date("2026-01-31"))`:   "2026-02-28",
			`string(date("2026-01-30") + duration("P1M"))`:   "2026-02-28",
			`string(date("2026-01-28") + duration("P1M"))`:   "2026-02-28",
			`string(date("2026-08-31") + duration("P1M"))`:   "2026-09-30",
			`string(date("2026-01-31") - duration("P2M"))`:   "2025-11-30",
			// A date and time keeps its time of day and its zone or offset.
			`string(date and time("2026-01-31T10:30:15") + duration("P1M"))`: "2026-02-28T10:30:15",
		}
		for expr, want := range strs {
			if got := mustEval(t, e, expr, nil); got != want {
				t.Errorf("%s = %#v, want %q", expr, got, want)
			}
		}
		// years and months duration(from, to) counts whole months by the day
		// of the month and is not the inverse of the pinning: Jan 31 to Feb 28
		// is not a whole month, though Jan 31 + P1M is Feb 28.
		bools := map[string]bool{
			`years and months duration(date("2026-01-31"), date("2026-02-28")) = duration("P0M")`:  true,
			`years and months duration(date("2026-01-31"), date("2026-03-31")) = duration("P2M")`:  true,
			`years and months duration(date("2026-02-28"), date("2026-03-31")) = duration("P1M")`:  true,
			`years and months duration(date("2026-03-31"), date("2026-02-28")) = duration("-P1M")`: true,
		}
		for expr, want := range bools {
			if got := mustEval(t, e, expr, nil); got != want {
				t.Errorf("%s = %#v, want %v", expr, got, want)
			}
		}
	})

	t.Run("conversion built-ins are null outside their domain", func(t *testing.T) {
		// DMN 1.5 §10.3.4: "Whenever a parameter is outside its domain, the
		// result of the built-in is null." Upstream raised "fail to parse
		// temporal value" for a malformed date, time, date and time or
		// duration string, returned 0 for number("abc"), and "&{}" for
		// string(null) (Table 72's own example is string(null) = null).
		for _, expr := range []string{
			`duration("P1Y2D")`, `duration("nope")`, `duration("")`, `duration(1)`, `duration(null)`, `duration(duration("P1D"))`,
			`date("nope")`, `date("2024-13-01")`, `date("")`, `date(1)`, `date(null)`, `date(true)`, `date(date("2024-01-01"))`,
			`time("25:00")`, `time("nope")`, `time("")`, `time(1)`, `time(null)`,
			`date and time("x")`, `date and time("2024-01-01")`, `date and time(1)`, `date and time(null)`, `date and time(date("2024-01-01"))`,
			`number("abc")`, `number("")`, `number(" 1")`, `number("1,000")`, `number("Inf")`, `number("0x10")`, `number("1.")`, `number(true)`, `number(null)`,
			`string(null)`,
			`years and months duration(1, 2)`, `years and months duration(null, date("2013-08-24"))`, `years and months duration(date("2011-12-22"), "x")`,
		} {
			got, err := e.EvalText(expr, nil)
			if err != nil {
				t.Errorf("%s raised %v, want null", expr, err)
			} else if ToGo(got) != nil {
				t.Errorf("%s = %#v, want null", expr, ToGo(got))
			}
		}
		// A missing input bound to a conversion is null, not a failed evaluation.
		if got := mustEval(t, e, `date(d)`, map[string]any{"d": FromGo("not a date")}); got != nil {
			t.Errorf(`date(d) with d = "not a date" = %#v, want null`, got)
		}

		// Valid inputs are unchanged.
		strs := map[string]string{
			`string(duration("P1Y2M"))`:  "P1Y2M",
			`string(duration("P1D"))`:    "P1D",
			`string(date("2012-12-25"))`: "2012-12-25",
			`string(1.1)`:                "1.1",
			`string(years and months duration(date("2011-12-22"), date("2013-08-24")))`: "P1Y8M",
		}
		for expr, want := range strs {
			if got := mustEval(t, e, expr, nil); got != want {
				t.Errorf("%s = %#v, want %q", expr, got, want)
			}
		}
		nums := map[string]float64{
			`number("12")`: 12, `number("-1.5")`: -1.5, `number(".5")`: 0.5, `number("1e3")`: 1000, `number("-2E-1")`: -0.2, `number(7)`: 7,
		}
		for expr, want := range nums {
			if got := mustEval(t, e, expr, nil); got != want {
				t.Errorf("%s = %#v, want %v", expr, got, want)
			}
		}
		bools := map[string]bool{
			`date("2012-12-25") + duration("P1D") = date("2012-12-26")`:                                            true,
			`date and time("2012-12-24T23:59:00") + duration("PT1M") = date and time("2012-12-25T00:00:00")`:       true,
			`time("23:59:00") = time("23:59:00")`:                                                                  true,
			`duration("P2Y2M") > duration("P2Y1M")`:                                                                true,
			`date and time("2012-12-24T23:59:00") - date and time("2012-12-22T03:45:00") = duration("P2DT20H14M")`: true,
			// The UTC designator Table 72's examples use, in either case.
			`time("23:59:00z") = time("23:59:00Z")`:                                true,
			`date and time("2012-12-25T11:00:00Z") = @"2012-12-25T11:00:00+00:00"`: true,
			`date and time("2012-12-25T12:00:00+01:00") = @"2012-12-25T11:00:00Z"`: true,
		}
		for expr, want := range bools {
			if got := mustEval(t, e, expr, nil); got != want {
				t.Errorf("%s = %#v, want %v", expr, got, want)
			}
		}

		// Inside the domain but beyond the fork: still an evaluation error,
		// never a null that reads as "the input was malformed". An IANA zone
		// id, a days and time duration beyond time.Duration's range or finer
		// than a nanosecond, and the date(date and time) / time(time | date
		// and time) overloads, which the fork does not implement.
		for _, expr := range []string{
			`duration("P200000D")`, `duration("PT0.0000000001S")`,
			`date(date and time("2012-12-25T11:00:00"))`, `time(time("10:00:00"))`, `time(date and time("2012-12-25T11:00:00"))`,
		} {
			if v, err := e.EvalText(expr, nil); err == nil {
				t.Errorf("%s = %#v, want an evaluation error", expr, ToGo(v))
			}
		}

		// An at-literal is syntax, not a call: DMN 1.5 §10.3.1.2's additional
		// syntax rules require rule 65's string to be a date, time, date and
		// time or duration string, so a malformed one fails to compile — a
		// VERDICT_LOAD_013 at model load — rather than evaluating to null.
		for _, expr := range []string{`@"nope"`, `@"P1Y2D"`, `@""`, `@"2024-13-01"`, `x < @"25:00"`} {
			if _, err := e.Compile(expr); err == nil {
				t.Errorf("%s compiled, want a syntax error", expr)
			} else {
				var ce *CompileError
				if !errors.As(err, &ce) {
					t.Errorf("%s: error %T is not a *CompileError", expr, err)
				}
			}
		}
		if _, err := e.CompileUnaryTest(`< @"nope"`); err == nil {
			t.Errorf(`unary test < @"nope" compiled, want a syntax error`)
		}
		for _, expr := range []string{`@"2024-01-01"`, `@"10:00:00"`, `@"2024-01-01T10:00:00Z"`, `@"P1D"`, `@"P1Y2M"`} {
			if _, err := e.Compile(expr); err != nil {
				t.Errorf("%s: %v", expr, err)
			}
		}
	})

	t.Run("multi-word names without keywords still resolve", func(t *testing.T) {
		vars := map[string]any{"Credit Rating": "good"}
		if got := mustEval(t, e, `Credit Rating = "good"`, vars); got != true {
			t.Errorf(`Credit Rating = "good" evaluated to %#v, want true`, got)
		}
	})

	t.Run("not, and and or are three-valued", func(t *testing.T) {
		// DMN 1.5 §10.3.2.15, Table 51. Only a boolean counts as one; null and
		// every other kind are "otherwise". Upstream v1.0.6 coerced operands
		// through boolValue, so null, 0 and "" read as false and 1 as true.
		operands := map[string]any{
			"true": true, "false": false, "null": nil, "1": nil, `"x"`: nil,
		}
		and := func(a, b any) any {
			switch {
			case a == false || b == false:
				return false
			case a == true && b == true:
				return true
			}
			return nil
		}
		or := func(a, b any) any {
			switch {
			case a == true || b == true:
				return true
			case a == false && b == false:
				return false
			}
			return nil
		}
		for l, lv := range operands {
			for r, rv := range operands {
				for op, want := range map[string]any{"and": and(lv, rv), "or": or(lv, rv)} {
					expr := l + " " + op + " " + r
					if got := mustEval(t, e, expr, nil); got != want {
						t.Errorf("%s = %#v, want %#v", expr, got, want)
					}
				}
			}
		}
		nots := map[string]any{
			"not(true)":            false,
			"not(false)":           true,
			"not(null)":            nil,
			"not(0)":               nil,
			`not("")`:              nil,
			"not([])":              nil,
			`not(5 in ["a".."z"])`: nil,
			"not(from: true)":      false,
			"not(true and null)":   nil,
			"not(false and null)":  true,
		}
		for expr, want := range nots {
			if got := mustEval(t, e, expr, nil); got != want {
				t.Errorf("%s = %#v, want %#v", expr, got, want)
			}
		}
	})

	t.Run("booleans have no order", func(t *testing.T) {
		// DMN 1.5 §10.3.2.15, Table 54 defines no ordering over booleans.
		// Upstream v1.0.6 ordered false below true.
		for _, op := range []string{"<", "<=", ">", ">="} {
			for _, pair := range [][2]string{{"true", "false"}, {"false", "true"}, {"true", "true"}, {"false", "false"}} {
				expr := pair[0] + " " + op + " " + pair[1]
				if got := mustEval(t, e, expr, nil); got != nil {
					t.Errorf("%s = %#v, want null", expr, got)
				}
			}
		}
		// Equality is defined over booleans, and unaffected.
		for expr, want := range map[string]any{"true = true": true, "true = false": false, "true != false": true} {
			if got := mustEval(t, e, expr, nil); got != want {
				t.Errorf("%s = %#v, want %#v", expr, got, want)
			}
		}
	})

	t.Run("ranges compare by endpoints and closedness and have no order", func(t *testing.T) {
		// DMN 1.5 §10.3.2.15, Table 53: ranges are equal when their endpoints
		// and closedness are; Table 54 orders no range. Upstream v1.0.6 raised
		// "bad type in comparation" (-3106) for every one of these.
		cases := map[string]any{
			"[1..2] = [1..2]":         true,
			"[1..2] = [1..3]":         false,
			"[1..2] = (1..2]":         false,
			"[1..2] = [1..2)":         false,
			"[1..2] = ]1..2]":         false,
			"[1..2] != [1..2]":        false,
			"[1..2] != (1..2)":        true,
			`["a".."b"] = ["a".."b"]`: true,
			`[1..2] = ["a".."b"]`:     nil,
			"[1..2] = 1":              nil,
			"[[1..2]] = [[1..2]]":     true,
			"[1..2] in [[1..2], 3]":   true,
			"[1..2] < [3..4]":         nil,
			"[1..2] <= [1..2]":        nil,
			"[3..4] > [1..2]":         nil,
			"[1..2] >= [1..2]":        nil,
		}
		for expr, want := range cases {
			if got := mustEval(t, e, expr, nil); got != want {
				t.Errorf("%s = %#v, want %#v", expr, got, want)
			}
		}
	})

	t.Run("list membership is a three-valued disjunction of equalities", func(t *testing.T) {
		// DMN 1.5 §10.3.2.15, Table 49: e in [e1, e2, …] is e = e1 or e = e2
		// or …, folded by Table 51. Upstream v1.0.6 answered false wherever no
		// element was equal, so `"a" in [1, 2]` was false and its negation true.
		cases := map[string]any{
			`"a" in [1, 2]`:       nil,
			`1 in [1, "a"]`:       true,
			`"a" in [1, "a"]`:     true,
			"3 in [1, 2]":         false,
			"3 in []":             false,
			"null in [1, null]":   true,
			"null in [1, 2]":      false,
			"[1] in [[1], [2]]":   true,
			`[1] in [["a"], [2]]`: nil,
			`not("a" in [1, 2])`:  nil,
		}
		for expr, want := range cases {
			if got := mustEval(t, e, expr, nil); got != want {
				t.Errorf("%s = %#v, want %#v", expr, got, want)
			}
		}
	})

	t.Run("min and max reject items outside their domain", func(t *testing.T) {
		// DMN 1.5 §10.3.4.4 defines min and max over comparable items, and
		// §10.3.4 makes a built-in given a parameter outside its domain null.
		// Upstream v1.0.6 skipped items it could not order (`min([1, "a"])`
		// was 1), ordered null above every value and false below true, and returned the
		// first of a list of lists or contexts.
		cases := map[string]any{
			"min([[1], [2]])":       nil,
			"max([[1], [2]])":       nil,
			"min([{a: 1}, {a: 2}])": nil,
			"max([{a: 1}])":         nil,
			`min([1, "a"])`:         nil,
			`max(["a", 1])`:         nil,
			"min([true, false])":    nil,
			"max([true, false])":    nil,
			"max([true])":           nil,
			"min([1, null])":        nil,
			"max([1, null])":        nil,
			"min([[1..2], [3..4]])": nil,
			"min([])":               nil,
			"max([])":               nil,
			`max("a", "c", "b")`:    "c",
			`min(["b", "a"])`:       "a",
			"max([duration(\"P1D\"), duration(\"PT1H\")]) = duration(\"P1D\")":         true,
			"min([date(\"2026-01-02\"), date(\"2026-01-01\")]) = date(\"2026-01-01\")": true,
			`max([duration("P1D"), duration("P1M")])`:                                  nil,
		}
		for expr, want := range cases {
			if got := mustEval(t, e, expr, nil); got != want {
				t.Errorf("%s = %#v, want %#v", expr, got, want)
			}
		}
		for expr, want := range map[string]float64{"min([3, 1, 2])": 1, "max(3, 1, 2)": 3, "min([5])": 5} {
			if got, ok := mustEval(t, e, expr, nil).(float64); !ok || got != want {
				t.Errorf("%s = %#v, want %v", expr, got, want)
			}
		}
	})

	t.Run("a unary entry of another kind than its input is null, so its not(...) does not match either", func(t *testing.T) {
		// DMN 1.5 §10.3.2.15 and §10.3.2.2 make `=` and `in` between values of
		// different kinds null, and Table 54 orders no booleans; Table 51 makes
		// not(null) null. The unary-test layer used a two-valued equality and
		// read an incomparable range as false, so not("gold") matched 5 and
		// not([false..true]) matched true. Cells now go through the fork's
		// exported Equal and In.
		c := NewCompiler(FEEL)
		cases := []struct {
			src   string
			input any
			want  bool
		}{
			{`"gold"`, 5, false}, {`not("gold")`, 5, false},
			{`["a".."z"]`, 5, false}, {`not(["a".."z"])`, 5, false},
			{"[false..true]", true, false}, {"not([false..true])", true, false},
			{`["a", "b"]`, 5, false}, {`not(["a", "b"])`, 5, false},
			{"not(true)", 5, false}, {`not(1, "a")`, 2, false},
			// Same kind: still two-valued.
			{`not("gold")`, "silver", true}, {`not(["a", "b"])`, "c", true},
			{"not([1..10])", 11, true}, {`not(1, "a")`, 1, false},
			{"not(true)", false, true},
			// A list input against a list cell: `? = cell or ? in cell`.
			{"[1, 2]", []any{1, 2}, true}, {"[[1, 2], [3]]", []any{3}, true},
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

	t.Run("booleans are unordered for range membership and range built-ins", func(t *testing.T) {
		// DMN 1.5 §10.3.2.15, Table 54 orders no booleans, so membership in a
		// boolean range and the §10.3.4.7 range functions over booleans are
		// outside their domain. Upstream v1.0.6 ordered false below true.
		for _, expr := range []string{
			"true in [false..true]",
			"false in [false..true]",
			"not(true in [false..true])",
			"before(false, true)",
			"after(true, false)",
			"includes([false..true], true)",
			"coincides(true, true)",
			"coincides([false..true], [false..true])",
		} {
			if got := mustEval(t, e, expr, nil); got != nil {
				t.Errorf("%s = %#v, want null", expr, got)
			}
		}
	})

	t.Run("zoned and zoneless times and date and times are distinct values", func(t *testing.T) {
		// DMN 1.5 §10.3.2.3.4 and §10.3.2.3.6: only a value with a time offset
		// has a UTC form. Table 53 (`=`) is defined on UTC forms, so a zoned
		// against a zoneless value is null; Table 54 (`<`, `>`) has the
		// zoneless one adopt the other's offset, and makes `<=` `< or =`;
		// Table 57 leaves a mixed subtraction undefined. Each case failed on
		// upstream v1.0.6, which read a zoneless value as UTC.
		cases := map[string]any{
			// = and != (rule 2)
			`time("10:00:00") = time("10:00:00Z")`:                                               nil,
			`time("10:00:00") != time("10:00:00Z")`:                                              nil,
			`@"10:00:00" = @"10:00:00Z"`:                                                         nil,
			`time("10:00:00") = time("10:00:00")`:                                                true,
			`time("10:00:00+02:00") = time("08:00:00Z")`:                                         true,
			`time("10:00:00+02:00") != time("08:00:00Z")`:                                        false,
			`date and time("2024-01-01T10:00:00") = date and time("2024-01-01T10:00:00Z")`:       nil,
			`date and time("2024-01-01T10:00:00") != date and time("2024-01-01T10:00:00Z")`:      nil,
			`date and time("2024-01-01T10:00:00+02:00") = date and time("2024-01-01T08:00:00Z")`: true,
			`date and time("2024-01-01T10:00:00") = date and time("2024-01-01T10:00:00")`:        true,
			`[time("10:00:00")] = [time("10:00:00Z")]`:                                           nil,
			`time("10:00:00") in [time("10:00:00Z")]`:                                            nil,
			// < and > adopt the zoned operand's offset (rule 3)
			`time("10:00:00") < time("11:00:00Z")`:                                              true,
			`time("10:00:00") < time("10:00:00Z")`:                                              false,
			`time("10:00:00") > time("10:00:00Z")`:                                              false,
			`time("10:00:00") < time("11:00:00+02:00")`:                                         true,
			`time("11:00:00+02:00") > time("10:00:00")`:                                         true,
			`date and time("2024-01-01T10:00:00") < date and time("2024-01-01T09:00:00-02:00")`: false,
			`date and time("2024-01-01T10:00:00") > date and time("2024-01-01T09:00:00-02:00")`: true,
			// <= and >= are `< or =`, so true or null when mixed (rule 4)
			`time("10:00:00") <= time("10:00:00Z")`:                                         nil,
			`time("10:00:00") >= time("10:00:00Z")`:                                         nil,
			`time("10:00:00") <= time("11:00:00Z")`:                                         true,
			`time("12:00:00") <= time("11:00:00Z")`:                                         nil,
			`time("10:00:00") >= time("09:00:00Z")`:                                         true,
			`date and time("2024-01-01T10:00:00") <= date and time("2024-01-01T10:00:00Z")`: nil,
			`time("10:00:00") <= time("10:00:00")`:                                          true,
			`time("10:00:00+02:00") <= time("08:00:00Z")`:                                   true,
			// Range membership is `>=`/`<=` (Table 49) and inherits them
			`time("10:00:00") in [time("09:00:00Z")..time("11:00:00Z")]`: true,
			`time("10:00:00") in [time("10:00:00Z")..time("11:00:00Z")]`: nil,
			`time("10:00:00") in (time("10:00:00Z")..time("11:00:00Z")]`: false,
			`time("08:00:00") in [time("09:00:00Z")..time("11:00:00Z")]`: nil,
			`time("10:00:00Z") in [time("09:00:00")..time("11:00:00")]`:  true,
			// - between two date and times or two times (rule 5)
			`date and time("2024-01-01T10:00:00") - date and time("2024-01-01T08:00:00Z")`:               nil,
			`date and time("2024-01-01T10:00:00Z") - date and time("2024-01-01T08:00:00")`:               nil,
			`time("10:00:00") - time("08:00:00Z")`:                                                       nil,
			`time("10:00:00Z") - time("08:00:00")`:                                                       nil,
			`string(date and time("2024-01-01T10:00:00+02:00") - date and time("2024-01-01T07:00:00Z"))`: "PT1H",
			`string(date and time("2024-01-01T10:00:00") - date and time("2024-01-01T07:00:00"))`:        "PT3H",
			`string(time("10:00:00+02:00") - time("07:00:00Z"))`:                                         "PT1H",
			`string(time("10:00:00") - time("07:00:00"))`:                                                "PT3H",
			// + and - duration keep zone-ness and offset (rule 6)
			`string(date and time("2024-01-01T10:00:00") + duration("PT1H"))`:                                 "2024-01-01T11:00:00",
			`date and time("2024-01-01T10:00:00") + duration("PT1H") = date and time("2024-01-01T11:00:00")`:  true,
			`date and time("2024-01-01T10:00:00") + duration("PT1H") = date and time("2024-01-01T11:00:00Z")`: nil,
			`date and time("2024-01-01T10:00:00") + duration("P1M") = date and time("2024-02-01T10:00:00Z")`:  nil,
			`(date and time("2024-01-01T10:00:00+02:00") + duration("P1M")).time offset = duration("PT2H")`:   true,
			`string(time("23:00:00") + duration("PT2H"))`:                                                     "01:00:00",
			`time("23:00:00") + duration("PT2H") = time("01:00:00Z")`:                                         nil,
			`(time("23:00:00+02:00") - duration("PT2H")).time offset = duration("PT2H")`:                      true,
			// time offset, timezone and string() (rule 7)
			`time("10:00:00").time offset`:                                                                         nil,
			`time("10:00:00").timezone`:                                                                            nil,
			`date and time("2024-01-01T10:00:00").time offset`:                                                     nil,
			`date and time("2024-01-01T10:00:00").timezone`:                                                        nil,
			`time("10:00:00Z").time offset = duration("PT0S")`:                                                     true,
			`date and time("2024-01-01T10:00:00-05:00").time offset = duration("-PT5H")`:                           true,
			`string(time("10:00:00"))`:                                                                             "10:00:00",
			`string(date and time("2024-01-01T10:00:00"))`:                                                         "2024-01-01T10:00:00",
			`time(string(time("10:00:00"))) = time("10:00:00")`:                                                    true,
			`date and time(string(date and time("2024-01-01T10:00:00"))) = date and time("2024-01-01T10:00:00")`:   true,
			`date and time(string(date and time("2024-01-01T10:00:00Z"))) = date and time("2024-01-01T10:00:00Z")`: true,
			// A date takes the zone-ness of the date and time it meets
			`date("2024-01-01") = date and time("2024-01-01T00:00:00")`:          true,
			`date("2024-01-01") = date and time("2024-01-01T00:00:00Z")`:         true,
			`date("2024-01-01") = date and time("2024-01-01T00:00:00+02:00")`:    false,
			`string(date and time("2024-01-02T06:00:00Z") - date("2024-01-01"))`: "P1DT6H",
			`string(date("2024-01-02") - date and time("2024-01-01T18:00:00Z"))`: "PT6H",
			// min and max order as `<` does
			`max(time("10:00:00"), time("11:00:00Z")) = time("11:00:00Z")`: true,
		}
		for expr, want := range cases {
			if got := mustEval(t, e, expr, nil); got != want {
				t.Errorf("%s = %#v, want %#v", expr, got, want)
			}
		}
	})

	t.Run("zoned and zoneless values in the Go-side comparisons and unary tests", func(t *testing.T) {
		// feel.Equal and the unary-test layer derive from the fork's `=`, and
		// feel.Compare from its `<`, so decision tables, the ANY hit policy
		// and COLLECT MIN/MAX follow the same zone rules.
		local, _ := ParseTemporal("10:00:00", "time")
		utc, _ := ParseTemporal("10:00:00Z", "time")
		later, _ := ParseTemporal("11:00:00Z", "time")
		if Equal(local, utc) {
			t.Errorf("Equal(10:00:00, 10:00:00Z) = true, want false (null)")
		}
		if r, ok := Compare(local, later); !ok || r >= 0 {
			t.Errorf("Compare(10:00:00, 11:00:00Z) = %d, %v; want -1, true", r, ok)
		}
		c := NewCompiler(FEEL)
		for src, want := range map[string]bool{
			`time("10:00:00Z")`:                      false,
			`time("10:00:00")`:                       true,
			`< time("11:00:00Z")`:                    true,
			`<= time("10:00:00Z")`:                   false,
			`[time("10:00:00Z")..time("11:00:00Z")]`: false,
			`(time("09:00:00Z")..time("11:00:00Z"))`: true,
		} {
			ut, err := c.CompileUnaryTest(src)
			if err != nil {
				t.Fatalf("%s does not compile: %v", src, err)
			}
			got, err := e.EvalUnaryTest(ut, local, nil)
			if err != nil {
				t.Errorf("%s against 10:00:00: %v", src, err)
				continue
			}
			if got != want {
				t.Errorf("%s matched 10:00:00 = %v, want %v", src, got, want)
			}
		}
	})

	t.Run("IANA zone ids resolve and every zoned value prints in its lexical form", func(t *testing.T) {
		// DMN 1.5 §10.3.2.3.6: a date and time with an IANA zone id has the
		// offset that zone's rules give at that date, and compares with an
		// offset value by instant. Table 72: string() is the lexical form, so
		// an offset prints as `Z` or `+05:00` and only a zone id as `@id`.
		// Upstream could not read `@Europe/Paris`, read `@CET` as UTC, printed
		// `+05:00` as `@+0500` and `Z` as `@UTC`, and named a zone for a value
		// written with an offset.
		cases := map[string]any{
			`date and time("2026-01-01T10:00:00@Europe/Paris") = date and time("2026-01-01T09:00:00Z")`:      true,
			`@"2026-01-01T10:00:00@Europe/Paris" = @"2026-01-01T09:00:00Z"`:                                  true,
			`date and time("2026-07-01T10:00:00@Europe/Paris") = date and time("2026-07-01T10:00:00+02:00")`: true,
			`date and time("2026-01-01T10:00:00@Europe/Paris") < date and time("2026-01-01T09:30:00Z")`:      true,
			`date and time("2026-01-01T10:00:00@Europe/Paris") = date and time("2026-01-01T10:00:00")`:       nil,
			`date and time("2026-01-01T10:00:00@CET") = date and time("2026-01-01T09:00:00Z")`:               true,
			`date and time("2026-01-01T10:00:00@Europe/Paris").timezone`:                                     "Europe/Paris",
			`string(date and time("2026-01-01T10:00:00@Europe/Paris").time offset)`:                          "PT1H",
			`date and time("2026-01-01T10:00:00+01:00").timezone`:                                            nil,
			`date and time("2026-01-01T10:00:00Z").timezone`:                                                 nil,
			`time("10:00:00+01:00").timezone`:                                                                nil,
			`string(date and time("2026-02-28T23:59:59+05:00"))`:                                             "2026-02-28T23:59:59+05:00",
			`string(date and time("2026-02-28T23:59:59-03:30"))`:                                             "2026-02-28T23:59:59-03:30",
			`string(date and time("2026-02-28T23:59:59Z"))`:                                                  "2026-02-28T23:59:59Z",
			`string(date and time("2026-02-28T23:59:59+00:00"))`:                                             "2026-02-28T23:59:59Z",
			`string(date and time("2026-01-01T10:00:00@Europe/Paris"))`:                                      "2026-01-01T10:00:00@Europe/Paris",
			`string(date and time("2026-03-15T10:00:00@Europe/Paris") + duration("P1M"))`:                    "2026-04-15T10:00:00@Europe/Paris",
			`string(time("10:00:00+02:00"))`:                                                                 "10:00:00+02:00",
			`string(time("10:00:00Z"))`:                                                                      "10:00:00Z",
			`string(time("10:00:00@Etc/UTC"))`:                                                               "10:00:00@Etc/UTC",
			// A time takes a zone id only when its offset never varies.
			`time("10:00:00@Europe/Paris")`:                     nil,
			`time("10:00:00@Etc/GMT-2") = time("08:00:00Z")`:    true,
			`time("10:00:00@Etc/UTC").timezone`:                 "Etc/UTC",
			`string(time("10:00:00@Etc/GMT-2").time offset)`:    "PT2H",
			`time("10:00:00@Etc/UTC") = time("10:00:00")`:       nil,
			`time(string(time("10:00:00@Etc/GMT-2"))).timezone`: "Etc/GMT-2",
		}
		for expr, want := range cases {
			if got := mustEval(t, e, expr, nil); got != want {
				t.Errorf("%s = %#v, want %#v", expr, got, want)
			}
		}
		// x = date and time(string(x)), and string() is stable, for zoned,
		// zoneless and IANA values alike.
		for _, lit := range []string{
			"2026-02-28T23:59:59+05:00", "2026-02-28T23:59:59Z", "2026-02-28T23:59:59",
			"2026-01-01T10:00:00@Europe/Paris", "2026-07-01T10:00:00@America/New_York",
		} {
			for expr, want := range map[string]any{
				fmt.Sprintf(`date and time(string(date and time(%q))) = date and time(%q)`, lit, lit): true,
				fmt.Sprintf(`string(date and time(string(date and time(%q))))`, lit):                  lit,
			} {
				if got := mustEval(t, e, expr, nil); got != want {
					t.Errorf("%s = %#v, want %#v", expr, got, want)
				}
			}
		}
		for _, lit := range []string{"10:00:00+05:00", "10:00:00Z", "10:00:00", "10:00:00@Etc/UTC"} {
			if got := mustEval(t, e, fmt.Sprintf(`string(time(string(time(%q))))`, lit), nil); got != lit {
				t.Errorf("time %s round-tripped to %#v", lit, got)
			}
		}
		// A zone id the database does not know still raises, and an
		// at-literal time with a varying zone id is a load error naming why.
		for _, src := range []string{
			`date and time("2026-01-01T10:00:00@Mars/Olympus")`,
			`date and time("2026-01-01T10:00:00@Local")`,
		} {
			if _, err := e.EvalText(src, nil); err == nil || !strings.Contains(err.Error(), "time zone id") {
				t.Errorf("%s: err = %v, want an unsupported time zone id", src, err)
			}
		}
		if _, err := e.EvalText(`@"10:00:00@Europe/Paris"`, nil); err == nil || !strings.Contains(err.Error(), "zone id") {
			t.Errorf(`@"10:00:00@Europe/Paris": err = %v, want a zone id error`, err)
		}
	})

	t.Run("an empty duration P is malformed", func(t *testing.T) {
		// Upstream's years-and-months pattern made both components optional,
		// so the bare P parsed as a zero duration. xs:yearMonthDuration and
		// xs:duration require at least one component (DMN 1.5 §10.3.2.3.8).
		for _, expr := range []string{`duration("P")`, `duration("-P")`} {
			if got := mustEval(t, e, expr, nil); got != nil {
				t.Errorf("%s = %#v, want null", expr, got)
			}
		}
		for _, expr := range []string{`@"P"`, `@"-P"`, `1 + @"P"`} {
			if _, err := e.Compile(expr); err == nil {
				t.Errorf("%s compiled, want a syntax error", expr)
			}
		}
	})

	t.Run("a zero years and months duration keeps its kind", func(t *testing.T) {
		// Upstream inferred a duration's kind from its fields, so a zero
		// years and months duration was PT0S. DMN 1.5 §10.3.2.3.7/§10.3.2.3.8
		// keep the kinds distinct types; a zero is no exception.
		strs := map[string]string{
			`string(duration("P1Y") - duration("P1Y"))`:                                 "P0M",
			`string(duration("P0Y"))`:                                                   "P0M",
			`string(duration("-P0M"))`:                                                  "P0M",
			`string(-(duration("P1M") - duration("P1M")))`:                              "P0M",
			`string(abs(duration("P1Y") - duration("P12M")))`:                           "P0M",
			`string(years and months duration(date("2026-01-01"), date("2026-01-15")))`: "P0M",
			`string(duration("P0D"))`:                                                   "PT0S",
			`string(duration("PT1H") - duration("PT1H"))`:                               "PT0S",
			`string(date("2026-01-31") + (duration("P1Y") - duration("P1Y")))`:          "2026-01-31",
			`string(date and time("2026-01-31T10:00:00") - duration("P0M"))`:            "2026-01-31T10:00:00",
			`string(duration("P1Y") - duration("P1Y") + duration("P2M"))`:               "P2M",
		}
		for expr, want := range strs {
			if got := mustEval(t, e, expr, nil); got != want {
				t.Errorf("%s = %#v, want %q", expr, got, want)
			}
		}
		bools := map[string]bool{
			`duration("P1Y") - duration("P1Y") = duration("P0M")`: true,
			`duration("P0M") < duration("P1M")`:                   true,
		}
		for expr, want := range bools {
			if got := mustEval(t, e, expr, nil); got != want {
				t.Errorf("%s = %#v, want %v", expr, got, want)
			}
		}
		// A zero of one kind does not join the other (Table 57): mixing the
		// kinds is null, zero or not, and so is comparing them.
		for _, expr := range []string{
			`duration("P1Y") - duration("P1Y") + duration("PT1H")`,
			`duration("PT0S") + duration("P1M")`,
			`duration("P0M") = duration("PT0S")`,
			`duration("P0M") < duration("PT1S")`,
			`time("10:00:00") + duration("P0M")`,
		} {
			if got := mustEval(t, e, expr, nil); got != nil {
				t.Errorf("%s = %#v, want null", expr, got)
			}
		}
		// Converted to Go, a years and months zero stays its lexical form
		// rather than becoming a zero time.Duration.
		if got := mustEval(t, e, `duration("P1Y") - duration("P1Y")`, nil); got != "P0M" {
			t.Errorf(`duration("P1Y") - duration("P1Y") = %#v, want "P0M"`, got)
		}
	})

	t.Run("durations multiply and divide by numbers", func(t *testing.T) {
		// Upstream's * and / took numbers only, so every duration scaling was
		// null. DMN 1.5 Table 59 defines duration * number, number * duration
		// and duration / number; the result keeps the duration's kind, and a
		// fraction of the kind's unit (a month, a nanosecond) truncates toward
		// zero. Each case failed on upstream v1.0.6.
		strs := map[string]string{
			`string(duration("PT1H") * 2)`:        "PT2H",
			`string(2 * duration("PT1H"))`:        "PT2H",
			`string(duration("P1Y") / 2)`:         "P6M",
			`string(duration("P1M") / 3)`:         "P0M",
			`string(duration("P1Y") * 1.5)`:       "P1Y6M",
			`string(duration("-P1M") / 3)`:        "P0M",
			`string(duration("-P1Y") * 1.5)`:      "-P1Y6M",
			`string(duration("P5M") * -0.5)`:      "-P2M",
			`string(duration("PT1S") * 0.1)`:      "PT0.1S",
			`string(duration("PT1S") / 3)`:        "PT0.333333333S",
			`string(duration("-PT1S") / 3)`:       "-PT0.333333333S",
			`string(duration("P1D") * 0.5)`:       "PT12H",
			`string(duration("PT0.3S") * 3)`:      "PT0.9S",
			`string(duration("PT1H") * 0)`:        "PT0S",
			`string(duration("P1Y") * 0)`:         "P0M",
			`string(0.1 * duration("PT10S") * 3)`: "PT3S",
		}
		for expr, want := range strs {
			if got := mustEval(t, e, expr, nil); got != want {
				t.Errorf("%s = %#v, want %q", expr, got, want)
			}
		}
		// Division by zero, a non-number operand and number / duration are
		// null, not errors.
		for _, expr := range []string{
			`duration("PT1H") / 0`,
			`duration("P1Y") / 0`,
			`2 / duration("PT1H")`,
			`duration("PT1H") * "2"`,
			`duration("PT1H") * null`,
			`null * duration("PT1H")`,
			`duration("PT1H") * duration("PT1H")`,
			`duration("PT1H") / true`,
		} {
			if got := mustEval(t, e, expr, nil); got != nil {
				t.Errorf("%s = %#v, want null", expr, got)
			}
		}
	})

	t.Run("syntax errors report a 1-based line and column", func(t *testing.T) {
		// Upstream printed the scanner's 0-based row and column, so an error
		// at the very start read "at 0 0", like an unset position, and every
		// other position was off by one. The at-literal error is still
		// VERDICT_LOAD_013 (a *CompileError); only the position changed.
		cases := map[string]string{
			`@"P1Y2D"`:                       "line 1, column 1",
			`1 + @"P1Y2D"`:                   "line 1, column 5",
			"1 +\n  @\"P1Y2D\"":              "line 2, column 3",
			"// note\nx +\n\tλ + @\"P1Y2D\"": "line 3, column 6",
			"1 +\n  foo )":                   "line 2, column 7",
		}
		for expr, want := range cases {
			_, err := e.Compile(expr)
			if err == nil {
				t.Errorf("%q compiled, want a syntax error", expr)
				continue
			}
			var ce *CompileError
			if !errors.As(err, &ce) {
				t.Errorf("%q: error %T is not a *CompileError", expr, err)
			}
			if !strings.Contains(err.Error(), "at "+want) {
				t.Errorf("%q: error %q does not report %q", expr, err, want)
			}
		}
	})

	t.Run("today() is the UTC date whatever the process's zone", func(t *testing.T) {
		// Upstream built today() in time.Local and kept that location, so a
		// host east or west of UTC disagreed with every parsed date for part
		// of each day. Between them, +14 and -12 differ from UTC's date at
		// every moment, so this fails on the upstream code whenever it runs.
		saved := time.Local
		t.Cleanup(func() { time.Local = saved })
		for _, zone := range []string{"Pacific/Kiritimati", "Etc/GMT+12"} {
			loc, err := time.LoadLocation(zone)
			if err != nil {
				t.Fatal(err)
			}
			time.Local = loc
			before := time.Now().UTC().Format("2006-01-02")
			got := mustEval(t, e, `string(today())`, nil)
			after := time.Now().UTC().Format("2006-01-02")
			if got != before && got != after {
				t.Errorf("with time.Local = %s, today() = %v, want the UTC date %s", zone, got, before)
			}
			if got := mustEval(t, e, fmt.Sprintf(`today() = date(%q) or today() = date(%q)`, before, after), nil); got != true {
				t.Errorf("with time.Local = %s, today() is not the UTC date: %v", zone, got)
			}
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
