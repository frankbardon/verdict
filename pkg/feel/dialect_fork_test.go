package feel

import (
	"strings"
	"testing"
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
