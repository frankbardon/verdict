package feel

import (
	"fmt"
	"math"
	"math/big"
	"regexp"
	"strings"
	"sync"
	"time"

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
		// A supplement built-in is a standard one: a call to it with the
		// wrong arguments is null (DMN 1.5 §10.3.4), as for the prelude's.
		supplement[name] = nativeFunc(f).Standard()
	}

	// ---- string functions -------------------------------------------------
	//
	// DMN 1.5 §10.3.4: "Whenever a parameter is outside its domain, the
	// result of the built-in is null." A null or non-string argument, an
	// invalid pattern, replacement or flags (footnote 2: "where XPath
	// specifies an error result, FEEL specifies a null result") are all
	// null, never an evaluation error.

	bind("substring before", Function{
		Params: []string{"string", "match"},
		Help:   "the substring preceding the first occurrence of match, or \"\"",
		Fn: func(a map[string]any) (any, error) {
			s, m, ok := twoStrings(a, "string", "match")
			if !ok {
				return Null, nil
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
			s, m, ok := twoStrings(a, "string", "match")
			if !ok {
				return Null, nil
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
			in, ok1 := asString(a["input"])
			pat, ok2 := asString(a["pattern"])
			repl, ok3 := asString(a["replacement"])
			if !ok1 || !ok2 || !ok3 {
				return Null, nil
			}
			re, ok := compilePattern(pat, a["flags"])
			// XPath (fn:replace, FORX0003) rejects a pattern that matches
			// the empty string.
			if !ok || re.MatchString("") {
				return Null, nil
			}
			// XPath spells capture groups $1..$9; Go spells them ${1}..${9}.
			goRepl, ok := dollarToGo(repl)
			if !ok {
				return Null, nil
			}
			return re.ReplaceAllString(in, goRepl), nil
		},
	})
	bind("matches", Function{
		Params:   []string{"input", "pattern"},
		Optional: []string{"flags"},
		Help:     "true when input matches the regular expression",
		Fn: func(a map[string]any) (any, error) {
			in, ok1 := asString(a["input"])
			pat, ok2 := asString(a["pattern"])
			if !ok1 || !ok2 {
				return Null, nil
			}
			re, ok := compilePattern(pat, a["flags"])
			if !ok {
				return Null, nil
			}
			return re.MatchString(in), nil
		},
	})
	bind("split", Function{
		Params: []string{"string", "delimiter"},
		Help:   "splits a string on a regular-expression delimiter",
		Fn: func(a map[string]any) (any, error) {
			s, d, ok := twoStrings(a, "string", "delimiter")
			if !ok {
				return Null, nil
			}
			re, ok := compilePattern(d, nil)
			// XPath (fn:tokenize, FORX0003) rejects a pattern that matches
			// the empty string.
			if !ok || re.MatchString("") {
				return Null, nil
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
			// DMN 1.5 §10.3.4.5: modulo(dividend, divisor) is
			// dividend - divisor*floor(dividend/divisor), null when the divisor
			// is 0. It runs on the exact decimals, not float64, so
			// modulo(10.1, 4.5) is 1.1 rather than 1.0999999999999996.
			d, ok := decimalArg(a["dividend"])
			if !ok {
				return Null, nil
			}
			v, ok := decimalArg(a["divisor"])
			if !ok || v.Sign() == 0 {
				return Null, nil
			}
			q := new(big.Rat).Quo(d, v)
			fl := new(big.Int).Div(q.Num(), q.Denom()) // Euclidean: floor, since Denom > 0
			r := new(big.Rat).Sub(d, new(big.Rat).Mul(v, new(big.Rat).SetInt(fl)))
			return pfeel.NewNumber(r.FloatString(decimalDigits)), nil
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
		Help:   "a copy of list with the position (1-based, negative from the end) replaced; null outside the list",
		Fn: func(a map[string]any) (any, error) {
			// DMN 1.5 §10.3.4.4: list is a list (a non-list item converted
			// to a singleton list, §10.3.2.9.4), position an integer
			// counted from 1 or, if negative, from the end, and newItem
			// "any element including null"; anything else is null (TCK
			// 1155).
			l, ok := asList(a["list"])
			if !ok {
				return Null, nil
			}
			idx, ok := listPosition(a["position"], len(l))
			if !ok {
				return Null, nil
			}
			out := make([]any, len(l))
			copy(out, l)
			out[idx] = a["newItem"]
			return out, nil
		},
	})
	bind("duplicate values", Function{
		Params: []string{"list"},
		Help:   "the values occurring more than once, in first-occurrence order",
		Fn: func(a map[string]any) (any, error) {
			l, ok := asList(a["list"])
			if !ok {
				return Null, nil
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
		Help:   "the whole years and months between two dates, null unless both are dates or both are date and times",
		Fn: func(a map[string]any) (any, error) {
			// DMN 1.5 §10.3.4.1 Table 72: from and to are both dates or both
			// date and times; §10.3.4 makes anything else — a time, a mixed
			// pair, null — null.
			from, to, ok := sameKindInstants(a["from"], a["to"])
			if !ok {
				return Null, nil
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
			if neg {
				months = -months
			}
			return pfeel.NewYearsMonthsDuration(int64(months)), nil
		},
	})
	bind("context", Function{
		Params: []string{"entries"},
		Help:   "builds a context from a list of {key, value} entries, null unless every entry has a distinct string key and a value",
		Fn: func(a map[string]any) (any, error) {
			// DMN 1.5 §10.3.4.7: entries is a list of contexts each holding a
			// key and a value; §10.3.4 makes anything else null rather than an
			// error. A lone context is the singleton list §10.3.2.9.4 converts
			// it to.
			var l []any
			switch x := a["entries"].(type) {
			case []any:
				l = x
			case map[string]any:
				l = []any{x}
			default:
				return Null, nil
			}
			//
			// §10.3.2.6: a context's keys "are distinct within a context",
			// and unlike context put and context merge, Table 81 gives
			// context(entries) no override rule — so two entries with the
			// same key are outside the domain and null (TCK 1145), not the
			// last one winning.
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
				v, ok := m["value"]
				if !ok {
					return Null, nil
				}
				if _, dup := out[k]; dup {
					return Null, nil
				}
				out[k] = v
			}
			return out, nil
		},
	})
}

// ---- helpers --------------------------------------------------------------

// sameKindInstants is a pair of arguments as instants when both are dates or
// both are date and times, the domain Table 72 gives years and months
// duration. A time, a mixed pair or a null is outside it.
func sameKindInstants(a, b any) (time.Time, time.Time, bool) {
	switch x := a.(type) {
	case *pfeel.FEELDate:
		if y, ok := b.(*pfeel.FEELDate); ok {
			return x.Date(), y.Date(), true
		}
	case *pfeel.FEELDatetime:
		if y, ok := b.(*pfeel.FEELDatetime); ok {
			return x.Time(), y.Time(), true
		}
	}
	return time.Time{}, time.Time{}, false
}

// asString is a string argument; false for null or any other kind, which
// DMN 1.5 §10.3.4 makes the built-in's result null.
func asString(v any) (string, bool) {
	s, ok := v.(string)
	return s, ok
}

func twoStrings(a map[string]any, k1, k2 string) (string, string, bool) {
	s1, ok1 := asString(a[k1])
	s2, ok2 := asString(a[k2])
	return s1, s2, ok1 && ok2
}

// listPosition resolves a list position — an integer counted from 1 or, if
// negative, from the end — to a 0-based index into a list of length n; false
// for a non-integer, 0, or a position outside the list.
func listPosition(v any, n int) (int, bool) {
	num, ok := numberArg(v)
	if !ok {
		return 0, false
	}
	r, ok := new(big.Rat).SetString(num.String())
	if !ok || !r.IsInt() || !r.Num().IsInt64() {
		return 0, false
	}
	p := r.Num().Int64()
	switch {
	case p == 0 || p > int64(n) || p < -int64(n):
		return 0, false
	case p > 0:
		return int(p - 1), true
	default:
		return n + int(p), true
	}
}

// numberArg is a numeric built-in's argument, or false when it is null or not a
// number. DMN 1.5 §10.3.4: an argument outside a function's domain makes the
// result null, not an error, and §10.3.4.5 says so of null explicitly.
func numberArg(v any) (*pfeel.Number, bool) {
	n, ok := v.(*pfeel.Number)
	return n, ok && n != nil
}

// decimalDigits is how many fractional digits a decimal argument is read and
// written at; it matches the precision a *pfeel.Number prints at.
const decimalDigits = 18

// decimalArg is a numeric argument as an exact rational, read through the
// number's decimal text so a literal such as 10.1 is exactly 101/10 rather
// than its nearest binary approximation.
func decimalArg(v any) (*big.Rat, bool) {
	n, ok := numberArg(v)
	if !ok {
		return nil, false
	}
	return new(big.Rat).SetString(n.String())
}

// asList is a list argument: a list as it is, any other non-null value as the
// singleton list DMN 1.5 §10.3.2.9.4 converts it to; false for null, which
// §10.3.4 makes the built-in's result null.
func asList(v any) ([]any, bool) {
	switch x := v.(type) {
	case nil, *pfeel.NullValue:
		return nil, false
	case []any:
		return x, true
	default:
		return []any{x}, true
	}
}

func unaryFloat(f func(float64) any) func(map[string]any) (any, error) {
	return func(a map[string]any) (any, error) {
		n, ok := numberArg(a["number"])
		if !ok {
			return Null, nil
		}
		return f(n.Float64()), nil
	}
}

// scaledRound applies a rounding mode at a decimal scale. DMN's rounding
// functions take an optional scale (default 0) and operate on the exact decimal
// value, so the arithmetic runs through big.Float at the evaluator's precision
// rather than through float64.
func scaledRound(mode func(*big.Float) *big.Float) func(map[string]any) (any, error) {
	return func(a map[string]any) (any, error) {
		// DMN 1.5 §10.3.4.5: a null or non-number n or scale makes the result
		// null, and scale is confined to [-6111..6176].
		n, ok := numberArg(a["n"])
		if !ok {
			return Null, nil
		}
		scale := 0
		if s, present := a["scale"]; present {
			sn, ok := numberArg(s)
			if !ok {
				return Null, nil
			}
			f := sn.Float64()
			if f < minScale || f > maxScale {
				return Null, nil
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

// minScale and maxScale bound the scale argument of the rounding functions
// (DMN 1.5 §10.3.4.5, decimal(n, scale)).
const (
	minScale = -6111
	maxScale = 6176
)

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
// `q` (literal) has no Go equivalent and is applied by quoting instead. Absent
// or null flags are no flags. false — so the built-in is null, DMN 1.5
// §10.3.4.3 footnote 2 — for flags that are not a string, a flag XPath does
// not define (FORX0001), or a pattern that does not compile (FORX0002), which
// includes the back-references and XPath-only classes Go's RE2 lacks.
func compilePattern(pattern string, flags any) (*regexp.Regexp, bool) {
	var f string
	switch x := flags.(type) {
	case nil, *pfeel.NullValue:
	case string:
		f = x
	default:
		return nil, false
	}
	for _, r := range f {
		if !strings.ContainsRune("smixq", r) {
			return nil, false
		}
	}
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
		return nil, false
	}
	return re, true
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
// number. false for a replacement XPath rejects (fn:replace, FORX0004): a `\`
// not followed by `\` or `$`, or a `$` not followed by a digit.
func dollarToGo(repl string) (string, bool) {
	var b strings.Builder
	for i := 0; i < len(repl); i++ {
		c := repl[i]
		if c == '\\' {
			if i+1 >= len(repl) || (repl[i+1] != '\\' && repl[i+1] != '$') {
				return "", false
			}
			if repl[i+1] == '$' {
				b.WriteString("$$")
			} else {
				b.WriteByte('\\')
			}
			i++
			continue
		}
		if c == '$' {
			if i+1 >= len(repl) || repl[i+1] < '0' || repl[i+1] > '9' {
				return "", false
			}
			b.WriteString("${")
			b.WriteByte(repl[i+1])
			b.WriteString("}")
			i++
			continue
		}
		b.WriteByte(c)
	}
	return b.String(), true
}
