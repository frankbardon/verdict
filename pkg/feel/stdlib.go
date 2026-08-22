package feel

import (
	"fmt"
	"math"
	"math/big"
	"regexp"
	"strings"
	"sync"

	pfeel "github.com/frankbardon/verdict/pkg/feel/internal/dialect"
)

// The embedded evaluator ships a substantial but incomplete DMN built-in
// library. standardSupplement adds the DMN 1.5 §10.3.4 functions it omits, so
// Verdict presents the whole standard library regardless of what the upstream
// prelude happens to carry. Where a name already exists upstream it is left
// alone: the supplement is additive, never a redefinition, which keeps Verdict
// tracking upstream fixes instead of forking behaviour.
//
// Names here are bound in a scope *below* caller variables and *above* the
// upstream prelude, which is what a standard built-in should be: overridable by
// a model's own variables, never by an application function library (those are
// namespace-qualified and cannot reach these names).
var supplementOnce sync.Once
var supplement map[string]any

func standardSupplement() map[string]any {
	supplementOnce.Do(buildSupplement)
	return supplement
}

// isBuiltinName reports whether a name is claimed by the standard library,
// either upstream or in the supplement. Function libraries may not use one of
// these as their namespace.
func isBuiltinName(name string) bool {
	if _, ok := standardSupplement()[name]; ok {
		return true
	}
	_, ok := pfeel.GetPrelude().Resolve(name)
	return ok
}

func buildSupplement() {
	supplement = map[string]any{}
	bind := func(name string, f Function) {
		// Never shadow an upstream built-in: if the vendored prelude already
		// provides the name, its implementation is the one DMN callers get.
		if _, exists := pfeel.GetPrelude().Resolve(name); exists {
			return
		}
		supplement[name] = nativeFunc(f)
	}

	// ---- string functions -------------------------------------------------

	bind("substring before", Function{
		Params: []string{"string", "match"},
		Help:   "the substring preceding the first occurrence of match, or \"\"",
		Fn: func(a map[string]any) (any, error) {
			s, m, err := twoStrings(a, "string", "match")
			if err != nil {
				return nil, err
			}
			i := strings.Index(s, m)
			if i < 0 {
				return "", nil
			}
			return s[:i], nil
		},
	})
	bind("substring after", Function{
		Params: []string{"string", "match"},
		Help:   "the substring following the first occurrence of match, or \"\"",
		Fn: func(a map[string]any) (any, error) {
			s, m, err := twoStrings(a, "string", "match")
			if err != nil {
				return nil, err
			}
			i := strings.Index(s, m)
			if i < 0 {
				return "", nil
			}
			return s[i+len(m):], nil
		},
	})
	bind("replace", Function{
		Params:   []string{"input", "pattern", "replacement"},
		Optional: []string{"flags"},
		Help:     "regular-expression replacement; $1..$9 refer to capture groups",
		Fn: func(a map[string]any) (any, error) {
			in, err := asString(a["input"], "input")
			if err != nil {
				return nil, err
			}
			pat, err := asString(a["pattern"], "pattern")
			if err != nil {
				return nil, err
			}
			repl, err := asString(a["replacement"], "replacement")
			if err != nil {
				return nil, err
			}
			re, err := compilePattern(pat, a["flags"])
			if err != nil {
				return nil, err
			}
			// XPath spells capture groups $1..$9; Go spells them ${1}..${9}.
			return re.ReplaceAllString(in, dollarToGo(repl)), nil
		},
	})
	bind("matches", Function{
		Params:   []string{"input", "pattern"},
		Optional: []string{"flags"},
		Help:     "true when input matches the regular expression",
		Fn: func(a map[string]any) (any, error) {
			in, err := asString(a["input"], "input")
			if err != nil {
				return nil, err
			}
			pat, err := asString(a["pattern"], "pattern")
			if err != nil {
				return nil, err
			}
			re, err := compilePattern(pat, a["flags"])
			if err != nil {
				return nil, err
			}
			return re.MatchString(in), nil
		},
	})
	bind("split", Function{
		Params: []string{"string", "delimiter"},
		Help:   "splits a string on a regular-expression delimiter",
		Fn: func(a map[string]any) (any, error) {
			s, d, err := twoStrings(a, "string", "delimiter")
			if err != nil {
				return nil, err
			}
			re, err := regexp.Compile(d)
			if err != nil {
				return nil, fmt.Errorf("split: bad delimiter pattern %q: %w", d, err)
			}
			parts := re.Split(s, -1)
			out := make([]any, len(parts))
			for i, p := range parts {
				out[i] = p
			}
			return out, nil
		},
	})

	// ---- numeric functions ------------------------------------------------

	bind("floor", Function{Params: []string{"n"}, Optional: []string{"scale"},
		Help: "largest value <= n at the given decimal scale (default 0)",
		Fn:   scaledRound(func(f *big.Float) *big.Float { return floorBig(f) })})
	bind("ceiling", Function{Params: []string{"n"}, Optional: []string{"scale"},
		Help: "smallest value >= n at the given decimal scale (default 0)",
		Fn:   scaledRound(func(f *big.Float) *big.Float { return ceilBig(f) })})
	bind("round up", Function{Params: []string{"n", "scale"},
		Help: "rounds away from zero",
		Fn:   scaledRound(roundAwayFromZero)})
	bind("round down", Function{Params: []string{"n", "scale"},
		Help: "rounds toward zero",
		Fn:   scaledRound(truncBig)})
	bind("round half up", Function{Params: []string{"n", "scale"},
		Help: "rounds to nearest, ties away from zero",
		Fn:   scaledRound(roundHalfUp)})
	bind("round half down", Function{Params: []string{"n", "scale"},
		Help: "rounds to nearest, ties toward zero",
		Fn:   scaledRound(roundHalfDown)})
	bind("decimal", Function{Params: []string{"n", "scale"},
		Help: "rounds n to scale decimal places, ties to even",
		Fn:   scaledRound(roundHalfEven)})

	bind("modulo", Function{
		Params: []string{"dividend", "divisor"},
		Help:   "the remainder, taking the sign of the divisor",
		Fn: func(a map[string]any) (any, error) {
			d, err := asFloat(a["dividend"], "dividend")
			if err != nil {
				return nil, err
			}
			v, err := asFloat(a["divisor"], "divisor")
			if err != nil {
				return nil, err
			}
			if v == 0 {
				return Null, nil
			}
			return d - v*math.Floor(d/v), nil
		},
	})
	bind("sqrt", Function{Params: []string{"number"}, Help: "square root, null for negatives",
		Fn: unaryFloat(func(f float64) any {
			if f < 0 {
				return Null
			}
			return math.Sqrt(f)
		})})
	bind("log", Function{Params: []string{"number"}, Help: "natural logarithm, null for non-positives",
		Fn: unaryFloat(func(f float64) any {
			if f <= 0 {
				return Null
			}
			return math.Log(f)
		})})
	bind("exp", Function{Params: []string{"number"}, Help: "e raised to the given power",
		Fn: unaryFloat(func(f float64) any { return math.Exp(f) })})
	bind("odd", Function{Params: []string{"number"}, Help: "true when the integral value is odd",
		Fn: unaryFloat(func(f float64) any {
			if f != math.Trunc(f) {
				return false
			}
			return math.Mod(math.Abs(f), 2) == 1
		})})
	bind("even", Function{Params: []string{"number"}, Help: "true when the integral value is even",
		Fn: unaryFloat(func(f float64) any {
			if f != math.Trunc(f) {
				return false
			}
			return math.Mod(math.Abs(f), 2) == 0
		})})

	// ---- list functions ---------------------------------------------------

	bind("list replace", Function{
		Params: []string{"list", "position", "newItem"},
		Help:   "a copy of list with the 1-based position replaced",
		Fn: func(a map[string]any) (any, error) {
			l, err := asList(a["list"], "list")
			if err != nil {
				return nil, err
			}
			p, err := asFloat(a["position"], "position")
			if err != nil {
				return nil, err
			}
			idx := int(p)
			if idx < 1 || idx > len(l) {
				return Null, nil
			}
			out := make([]any, len(l))
			copy(out, l)
			out[idx-1] = a["newItem"]
			return out, nil
		},
	})
	bind("duplicate values", Function{
		Params: []string{"list"},
		Help:   "the values occurring more than once, in first-occurrence order",
		Fn: func(a map[string]any) (any, error) {
			l, err := asList(a["list"], "list")
			if err != nil {
				return nil, err
			}
			var out []any
			for i, v := range l {
				dupe := false
				for j := 0; j < i; j++ {
					if Equal(l[j], v) {
						dupe = true
						break
					}
				}
				if dupe {
					continue
				}
				count := 0
				for _, w := range l {
					if Equal(w, v) {
						count++
					}
				}
				if count > 1 {
					out = append(out, v)
				}
			}
			if out == nil {
				out = []any{}
			}
			return out, nil
		},
	})

	// ---- conversion -------------------------------------------------------

	bind("years and months duration", Function{
		Params: []string{"from", "to"},
		Help:   "the whole years and months between two dates",
		Fn: func(a map[string]any) (any, error) {
			from, ok := timeOf(a["from"])
			if !ok {
				return nil, fmt.Errorf("years and months duration: `from` must be a date or date and time")
			}
			to, ok := timeOf(a["to"])
			if !ok {
				return nil, fmt.Errorf("years and months duration: `to` must be a date or date and time")
			}
			neg := to.Before(from)
			if neg {
				from, to = to, from
			}
			months := (to.Year()-from.Year())*12 + int(to.Month()) - int(from.Month())
			if to.Day() < from.Day() {
				months--
			}
			if months < 0 {
				months = 0
			}
			return &pfeel.FEELDuration{Neg: neg, Years: months / 12, Months: months % 12}, nil
		},
	})
	bind("context", Function{
		Params: []string{"entries"},
		Help:   "builds a context from a list of {key, value} entries",
		Fn: func(a map[string]any) (any, error) {
			l, err := asList(a["entries"], "entries")
			if err != nil {
				return nil, err
			}
			out := map[string]any{}
			for _, e := range l {
				m, ok := e.(map[string]any)
				if !ok {
					return Null, nil
				}
				k, ok := m["key"].(string)
				if !ok {
					return Null, nil
				}
				out[k] = m["value"]
			}
			return out, nil
		},
	})
}

// ---- helpers --------------------------------------------------------------

func asString(v any, name string) (string, error) {
	s, ok := v.(string)
	if !ok {
		return "", fmt.Errorf("argument %s must be a string, got %s", name, TypeName(v))
	}
	return s, nil
}

func twoStrings(a map[string]any, k1, k2 string) (string, string, error) {
	s1, err := asString(a[k1], k1)
	if err != nil {
		return "", "", err
	}
	s2, err := asString(a[k2], k2)
	if err != nil {
		return "", "", err
	}
	return s1, s2, nil
}

func asFloat(v any, name string) (float64, error) {
	n, ok := v.(*pfeel.Number)
	if !ok {
		return 0, fmt.Errorf("argument %s must be a number, got %s", name, TypeName(v))
	}
	return n.Float64(), nil
}

func asList(v any, name string) ([]any, error) {
	l, ok := v.([]any)
	if !ok {
		return nil, fmt.Errorf("argument %s must be a list, got %s", name, TypeName(v))
	}
	return l, nil
}

func unaryFloat(f func(float64) any) func(map[string]any) (any, error) {
	return func(a map[string]any) (any, error) {
		v, err := asFloat(a["number"], "number")
		if err != nil {
			return nil, err
		}
		return f(v), nil
	}
}

// scaledRound applies a rounding mode at a decimal scale. DMN's rounding
// functions take an optional scale (default 0) and operate on the exact decimal
// value, so the arithmetic runs through big.Float at the evaluator's precision
// rather than through float64.
func scaledRound(mode func(*big.Float) *big.Float) func(map[string]any) (any, error) {
	return func(a map[string]any) (any, error) {
		n, ok := a["n"].(*pfeel.Number)
		if !ok {
			return nil, fmt.Errorf("argument n must be a number, got %s", TypeName(a["n"]))
		}
		scale := 0
		if s, present := a["scale"]; present {
			f, err := asFloat(s, "scale")
			if err != nil {
				return nil, err
			}
			scale = int(f)
		}
		v := new(big.Float).SetPrec(pfeel.Prec)
		if _, _, err := v.Parse(n.String(), 10); err != nil {
			return nil, fmt.Errorf("cannot interpret %s as a decimal: %w", n, err)
		}
		shift := pow10(scale)
		v.Mul(v, shift)
		v = mode(v)
		v.Quo(v, shift)
		return pfeel.NewNumber(v.Text('f', maxInt(scale, 0)+2)), nil
	}
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func pow10(scale int) *big.Float {
	out := new(big.Float).SetPrec(pfeel.Prec).SetInt64(1)
	ten := new(big.Float).SetPrec(pfeel.Prec).SetInt64(10)
	n := scale
	if n < 0 {
		n = -n
	}
	for i := 0; i < n; i++ {
		out.Mul(out, ten)
	}
	if scale < 0 {
		out.Quo(new(big.Float).SetPrec(pfeel.Prec).SetInt64(1), out)
	}
	return out
}

func floorBig(f *big.Float) *big.Float {
	t := truncBig(new(big.Float).SetPrec(pfeel.Prec).Set(f))
	if f.Sign() < 0 && t.Cmp(f) != 0 {
		t.Sub(t, big.NewFloat(1))
	}
	return t
}

func ceilBig(f *big.Float) *big.Float {
	t := truncBig(new(big.Float).SetPrec(pfeel.Prec).Set(f))
	if f.Sign() > 0 && t.Cmp(f) != 0 {
		t.Add(t, big.NewFloat(1))
	}
	return t
}

func truncBig(f *big.Float) *big.Float {
	i, _ := f.Int(nil)
	return new(big.Float).SetPrec(pfeel.Prec).SetInt(i)
}

func roundAwayFromZero(f *big.Float) *big.Float {
	if f.Sign() < 0 {
		return floorBig(f)
	}
	return ceilBig(f)
}

func roundHalfUp(f *big.Float) *big.Float   { return roundHalf(f, true, false) }
func roundHalfDown(f *big.Float) *big.Float { return roundHalf(f, false, false) }
func roundHalfEven(f *big.Float) *big.Float { return roundHalf(f, false, true) }

// roundHalf rounds to the nearest integer. `up` breaks ties away from zero;
// `even` breaks ties toward the even neighbour (banker's rounding, DMN's
// `decimal`). With both false, ties go toward zero.
func roundHalf(f *big.Float, up, even bool) *big.Float {
	t := truncBig(f)
	frac := new(big.Float).SetPrec(pfeel.Prec).Sub(f, t)
	frac.Abs(frac)
	half := big.NewFloat(0.5)
	switch frac.Cmp(half) {
	case -1:
		return t
	case 1:
		return roundAwayFromZero(f)
	}
	if up {
		return roundAwayFromZero(f)
	}
	if !even {
		return t
	}
	i, _ := t.Int(nil)
	if i.Bit(0) == 0 {
		return t
	}
	return roundAwayFromZero(f)
}

// compilePattern translates an XPath 2.0 regular expression flag string into
// Go's inline flag syntax. DMN inherits XPath's `s`, `m`, `i` and `x` flags;
// `q` (literal) has no Go equivalent and is applied by quoting instead.
func compilePattern(pattern string, flags any) (*regexp.Regexp, error) {
	f, _ := flags.(string)
	if strings.Contains(f, "q") {
		pattern = regexp.QuoteMeta(pattern)
		f = strings.ReplaceAll(f, "q", "")
	}
	var inline string
	for _, r := range f {
		switch r {
		case 'i', 's', 'm':
			inline += string(r)
		case 'x':
			// XPath's `x` strips whitespace from the pattern itself.
			pattern = stripPatternWhitespace(pattern)
		}
	}
	if inline != "" {
		pattern = "(?" + inline + ")" + pattern
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		return nil, fmt.Errorf("bad regular expression %q: %w", pattern, err)
	}
	return re, nil
}

func stripPatternWhitespace(p string) string {
	var b strings.Builder
	escaped := false
	inClass := false
	for _, r := range p {
		switch {
		case escaped:
			escaped = false
		case r == '\\':
			escaped = true
		case r == '[':
			inClass = true
		case r == ']':
			inClass = false
		case !inClass && (r == ' ' || r == '\t' || r == '\n' || r == '\r'):
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

// dollarToGo rewrites XPath's $N capture references into Go's ${N} form so a
// following digit in the replacement text is not swallowed into the group
// number.
func dollarToGo(repl string) string {
	var b strings.Builder
	for i := 0; i < len(repl); i++ {
		c := repl[i]
		if c == '\\' && i+1 < len(repl) {
			b.WriteByte(repl[i+1])
			i++
			continue
		}
		if c == '$' && i+1 < len(repl) && repl[i+1] >= '0' && repl[i+1] <= '9' {
			b.WriteString("${")
			b.WriteByte(repl[i+1])
			b.WriteString("}")
			i++
			continue
		}
		if c == '$' {
			b.WriteString("$$")
			continue
		}
		b.WriteByte(c)
	}
	return b.String()
}
