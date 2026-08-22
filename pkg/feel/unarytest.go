package feel

import (
	"errors"
	"strings"
	"sync"

	pfeel "github.com/frankbardon/verdict/pkg/feel/internal/dialect"
)

// UnaryTest is a compiled decision-table input entry.
//
// A unary test is a distinct DMN production from an expression (DMN 1.5 §7.3.1,
// grammar rule 15): the tested value is implicit. `< 10` is a comparison
// against it, `[1..10]` is a containment check, `"low", "medium"` is a
// disjunction of equality tests, and `not(...)` negates the whole entry.
//
// The embedded evaluator parses these forms but evaluates them as ordinary
// expressions — a bare range yields a range value, a bare literal yields the
// literal — so Verdict owns the unary-test semantics here: split the entry into
// its top-level alternatives, evaluate each as an expression with the tested
// value bound to `?`, and then interpret the resulting value as a test.
type UnaryTest struct {
	text   string
	negate bool
	// alts are the comma-separated alternatives. A nil slice is the irrelevance
	// marker "-", which matches any value.
	alts []*unaryAlt
}

type unaryAlt struct {
	expr *Expr
	// source is the alternative as the modeller wrote it, before the input-ref
	// rewrite, so diagnostics quote the original cell.
	source string
	// literalBool marks an alternative whose source is a bare `true` or `false`.
	// Such an entry is an equality test against a boolean input, not a constant
	// test outcome, so it must not be short-circuited by the boolean branch of
	// the result interpreter.
	literalBool bool
}

// Text returns the source entry.
func (u *UnaryTest) Text() string {
	if u == nil {
		return "-"
	}
	return u.text
}

// MatchesAnything reports whether the entry is the irrelevance marker.
func (u *UnaryTest) MatchesAnything() bool { return u == nil || len(u.alts) == 0 }

// Alternatives exposes the compiled alternatives for static analysis: the gap
// and overlap detector reads them to build each rule's input domain.
func (u *UnaryTest) Alternatives() []*Expr {
	if u == nil {
		return nil
	}
	out := make([]*Expr, 0, len(u.alts))
	for _, a := range u.alts {
		out = append(out, a.expr)
	}
	return out
}

// Negated reports whether the entry is wrapped in `not(...)`.
func (u *UnaryTest) Negated() bool { return u != nil && u.negate }

// CompileUnaryTest parses a decision-table input entry.
//
// An empty entry, or the irrelevance marker "-", compiles to nil, which the
// evaluator treats as an unconditional match.
func (c *Compiler) CompileUnaryTest(text string) (*UnaryTest, error) {
	if IsIrrelevant(text) {
		return nil, nil
	}
	v, _ := c.utCache.LoadOrStore(text, &unaryEntry{})
	e := v.(*unaryEntry)
	e.once.Do(func() { e.test, e.err = c.compileUnaryTest(text) })
	return e.test, e.err
}

type unaryEntry struct {
	once sync.Once
	test *UnaryTest
	err  error
}

func (c *Compiler) compileUnaryTest(text string) (*UnaryTest, error) {
	body := trimSpace(text)
	u := &UnaryTest{text: text}
	if inner, ok := stripNot(body); ok {
		u.negate = true
		body = inner
		// `not()` with nothing inside is degenerate; treat it as "matches
		// nothing" rather than failing the whole model load.
		if trimSpace(body) == "" {
			u.alts = []*unaryAlt{}
			return u, nil
		}
	}
	for _, part := range splitTopLevel(body, ',') {
		part = trimSpace(part)
		if part == "" {
			continue
		}
		x, err := c.Compile(rewriteInputRef(part))
		if err != nil {
			// Compile already anchored the failure to the text it could not
			// parse. Wrapping it again produces "cannot compile X: cannot
			// compile X: ...", which reads like two separate failures.
			var ce *CompileError
			if errors.As(err, &ce) {
				return nil, ce
			}
			return nil, &CompileError{Text: part, Dialect: c.dialect, Err: err}
		}
		u.alts = append(u.alts, &unaryAlt{expr: x, source: part, literalBool: isBoolLiteral(x)})
	}
	if len(u.alts) == 0 {
		return nil, &CompileError{Text: text, Dialect: c.dialect, Reason: "entry has no test"}
	}
	return u, nil
}

func isBoolLiteral(x *Expr) bool {
	_, ok := x.node.(*pfeel.BoolNode)
	return ok
}

// EvalUnaryTest evaluates an input entry with the tested value bound to `?`.
//
// A nil test is the irrelevance marker and matches unconditionally.
func (e *Evaluator) EvalUnaryTest(u *UnaryTest, input any, vars map[string]any) (bool, error) {
	if u.MatchesAnything() {
		if u != nil && u.negate {
			return false, nil
		}
		return true, nil
	}
	scope := make(map[string]any, len(vars)+1)
	for k, v := range vars {
		scope[k] = v
	}
	scope["?"] = input
	scope[inputRefName] = input

	matched := false
	for _, alt := range u.alts {
		v, err := e.Eval(alt.expr, scope)
		if err != nil {
			return false, err
		}
		ok, err := interpretTestResult(v, input, alt.literalBool)
		if err != nil {
			return false, &EvalError{Text: alt.source, Err: err}
		}
		if ok {
			matched = true
			break
		}
	}
	if u.negate {
		return !matched, nil
	}
	return matched, nil
}

// interpretTestResult applies DMN's rule for turning the value of a unary-test
// alternative into a match decision.
func interpretTestResult(v, input any, literalBool bool) (bool, error) {
	if b, ok := v.(bool); ok && !literalBool {
		// A comparison, a range membership check or a predicate call.
		return b, nil
	}
	switch tv := v.(type) {
	case *pfeel.RangeValue:
		return rangeContains(tv, input)
	case []any:
		for _, item := range tv {
			if Equal(item, input) {
				return true, nil
			}
		}
		return false, nil
	}
	// Any other value — including a bare boolean literal — is an equality test.
	return Equal(v, input), nil
}

// rangeContains asks a FEEL range whether it contains the tested value.
// Comparing incomparable types is not an error in a decision table: it simply
// means the rule does not match, which is what lets a table mix string and
// numeric inputs across rules without every row erroring out.
func rangeContains(r *pfeel.RangeValue, input any) (bool, error) {
	if IsNull(input) {
		return false, nil
	}
	if _, err := r.Position(input); err != nil {
		return false, nil
	}
	return r.Contains(input), nil
}

// inputRefName is the identifier the tested value is also bound to.
//
// DMN spells the tested value `?`. The embedded parser accepts `?` only where
// a unary test may begin — `? > 5` and the implicit form `> 5` both work — but
// rejects it as an ordinary argument, so `list contains([1, 2], ?)` fails to
// parse. Rewriting `?` to a plain identifier before parsing, and binding the
// tested value under both names at evaluation time, makes every position work
// without forking the parser. The name is deliberately not a legal FEEL name a
// modeller would write, so it cannot collide with a model variable.
const inputRefName = "__verdict_input"

// rewriteInputRef replaces `?` tokens outside string literals with
// inputRefName.
func rewriteInputRef(s string) string {
	if !strings.Contains(s, "?") {
		return s
	}
	var b strings.Builder
	inString, escaped := false, false
	for _, r := range s {
		switch {
		case escaped:
			escaped = false
		case inString && r == '\\':
			escaped = true
		case r == '"':
			inString = !inString
		case !inString && r == '?':
			b.WriteString(inputRefName)
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

// stripNot recognises an entry of the form `not(...)` where the parentheses
// wrap the entire entry, and returns its contents.
func stripNot(s string) (string, bool) {
	const prefix = "not"
	if !strings.HasPrefix(s, prefix) {
		return "", false
	}
	rest := trimSpace(s[len(prefix):])
	if !strings.HasPrefix(rest, "(") || !strings.HasSuffix(rest, ")") {
		return "", false
	}
	inner := rest[1 : len(rest)-1]
	// Reject `not(a) and not(b)`-style entries where the closing parenthesis we
	// matched is not the one that opened: those are ordinary expressions.
	if depth := netDepth(inner); depth != 0 {
		return "", false
	}
	if unbalancedClose(inner) {
		return "", false
	}
	return inner, true
}

// netDepth reports the bracket balance of s, ignoring bracket characters inside
// string literals.
func netDepth(s string) int {
	depth := 0
	forEachSignificantRune(s, func(r rune) {
		switch r {
		case '(', '[', '{':
			depth++
		case ')', ']', '}':
			depth--
		}
	})
	return depth
}

// unbalancedClose reports whether s ever closes more brackets than it opened,
// which means an apparently wrapping parenthesis pair does not actually wrap.
func unbalancedClose(s string) bool {
	depth, bad := 0, false
	forEachSignificantRune(s, func(r rune) {
		switch r {
		case '(', '[', '{':
			depth++
		case ')', ']', '}':
			depth--
			if depth < 0 {
				bad = true
			}
		}
	})
	return bad
}

// splitTopLevel splits s on sep, ignoring separators inside string literals or
// nested brackets. It is how a unary test's comma-separated alternatives are
// separated without mistaking `list contains([1,2], ?)` for two alternatives.
func splitTopLevel(s string, sep rune) []string {
	var parts []string
	var cur strings.Builder
	depth := 0
	inString := false
	escaped := false
	for _, r := range s {
		switch {
		case escaped:
			escaped = false
		case inString && r == '\\':
			escaped = true
		case r == '"':
			inString = !inString
		case inString:
			// fall through to the append below
		case r == '(' || r == '[' || r == '{':
			depth++
		case r == ')' || r == ']' || r == '}':
			depth--
		case r == sep && depth == 0:
			parts = append(parts, cur.String())
			cur.Reset()
			continue
		}
		cur.WriteRune(r)
	}
	parts = append(parts, cur.String())
	return parts
}

// forEachSignificantRune visits every rune of s that is not inside a string
// literal.
func forEachSignificantRune(s string, fn func(rune)) {
	inString := false
	escaped := false
	for _, r := range s {
		switch {
		case escaped:
			escaped = false
			continue
		case inString && r == '\\':
			escaped = true
			continue
		case r == '"':
			inString = !inString
			continue
		case inString:
			continue
		}
		fn(r)
	}
}

// SplitList splits a comma-separated FEEL list — a decision table's
// inputValues or outputValues attribute — into its elements, ignoring commas
// inside string literals and nested brackets.
func SplitList(list string) []string {
	parts := splitTopLevel(list, ',')
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = trimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}
