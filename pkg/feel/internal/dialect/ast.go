package dialect

import (
	"fmt"
	"strings"
	"unicode"
	"unicode/utf16"
	"unicode/utf8"
)

type Scope map[string]interface{}

type Interpreter struct {
	ScopeStack []Scope
	// MaxIterations is the iteration budget of one evaluation: the total
	// number of elements every iteration context of every for, some and
	// every may produce, nested and multi-context products included. Zero
	// means no budget, which is upstream's behaviour; the façade always sets
	// one. It is ignored when Budget is set.
	//
	// VERDICT DIVERGENCE: see NOTICE.md. The fork takes the budget as a
	// plain field rather than importing any Verdict configuration.
	MaxIterations int64
	// iterations counts the elements charged against MaxIterations so far.
	// It lives on the interpreter, which is built afresh for every
	// evaluation and never shared between goroutines, so it needs no lock.
	iterations uint64
	// Budget, when set, is an iteration budget shared with other
	// evaluations — every expression of one Verdict request, including those
	// running concurrently on other goroutines. Charges go to it instead of
	// MaxIterations/iterations.
	//
	// VERDICT DIVERGENCE: see NOTICE.md.
	Budget *IterationBudget
}

type Node interface {
	Repr() string
	Eval(*Interpreter) (interface{}, error)
	TextRange() TextRange
}

type HasAttrs interface {
	GetAttr(name string) (interface{}, bool)
}

type TextRange struct {
	Start ScanPosition
	End   ScanPosition
}

// Binop binary operator
type Binop struct {
	Op    string
	Left  Node
	Right Node

	textRange TextRange
}

func (binop Binop) TextRange() TextRange {
	return binop.textRange
}
func (binop Binop) Repr() string {
	return fmt.Sprintf("(%s %s %s)", binop.Op, binop.Left.Repr(), binop.Right.Repr())
}

// DotOp function call
type DotOp struct {
	Left Node
	Attr string

	textRange TextRange
}

func (dotop DotOp) TextRange() TextRange {
	return dotop.textRange
}

func (dotop DotOp) Repr() string {
	return fmt.Sprintf("(. %s %s)", dotop.Left.Repr(), dotop.Attr)
}

// function call
type funcallArg struct {
	argName string
	arg     Node
}

type FunCall struct {
	FunRef      Node
	Args        []funcallArg
	keywordArgs bool

	textRange TextRange
}

func (fc FunCall) TextRange() TextRange {
	return fc.textRange
}
func (fc FunCall) Repr() string {
	argReprs := make([]string, 0)
	if fc.keywordArgs {
		for _, arg := range fc.Args {
			s := fmt.Sprintf("(%s %s)", arg.argName, arg.arg.Repr())
			argReprs = append(argReprs, s)
		}
	} else {
		for _, arg := range fc.Args {
			argReprs = append(argReprs, arg.arg.Repr())
		}
	}
	return fmt.Sprintf("(call %s [%s])", fc.FunRef.Repr(), strings.Join(argReprs, ", "))
}

// FunDef function definition
type FunDef struct {
	Args []string
	// ParamTypes holds the type declared on each parameter, parallel to Args
	// (DMN 1.5 grammar rule 56: parameter name [":" type]); an entry is nil
	// where none was declared, and the slice is nil when no parameter has a
	// type. An undeclared type is Any (§10.3.2.13.2).
	//
	// VERDICT DIVERGENCE: upstream did not parse parameter types. See
	// NOTICE.md.
	ParamTypes []*TypeExpr
	Body       Node

	// closure is the scope stack the definition was evaluated in (DMN 1.5
	// §10.3.2.13.2: a FEEL function is a lexical closure). It holds the
	// frames themselves, not copies, so a context literal's frame that is
	// still being filled — a recursive entry — is visible at call time. Nil on
	// a parsed node; set on the value FunDef.Eval returns.
	//
	// VERDICT DIVERGENCE: upstream had no closure. See NOTICE.md.
	closure []Scope

	textRange TextRange
}

func (fd FunDef) TextRange() TextRange {
	return fd.textRange
}

func (fd FunDef) Repr() string {
	params := make([]string, len(fd.Args))
	for i, name := range fd.Args {
		params[i] = name
		if i < len(fd.ParamTypes) && fd.ParamTypes[i] != nil {
			params[i] = name + ": " + fd.ParamTypes[i].Repr()
		}
	}
	return fmt.Sprintf("(function [%s] %s)", strings.Join(params, ", "), fd.Body.Repr())
}

// Var variable
type Var struct {
	Name      string
	textRange TextRange
}

func (v Var) TextRange() TextRange {
	return v.textRange
}

func (v Var) Repr() string {
	if strings.Contains(v.Name, " ") {
		return fmt.Sprintf("`%s`", v.Name)
	}
	return v.Name
}

type NumberNode struct {
	Value string

	textRange TextRange
}

func (numberNode NumberNode) TextRange() TextRange {
	return numberNode.textRange
}
func (numberNode NumberNode) Repr() string {
	return numberNode.Value
}

type BoolNode struct {
	Value bool

	textRange TextRange
}

func (boolNode BoolNode) TextRange() TextRange {
	return boolNode.textRange
}
func (boolNode BoolNode) Repr() string {
	if boolNode.Value {
		return "true"
	} else {
		return "false"
	}
}

type NullNode struct {
	textRange TextRange
}

func (nullNode NullNode) Repr() string {
	return "null"
}
func (nullNode NullNode) TextRange() TextRange {
	return nullNode.textRange
}

type StringNode struct {
	Value string

	textRange TextRange
}

func (stringNode StringNode) Repr() string {
	return stringNode.Value
}
func (stringNode StringNode) TextRange() TextRange {
	return stringNode.textRange
}
func (stringNode StringNode) Content() string {
	// trim leading and trailing quotes
	return unescapeString(stringNode.Value[1 : len(stringNode.Value)-1])
}

// unescapeString decodes the escape sequences of a string literal's body.
//
// VERDICT DIVERGENCE: upstream replaced only \n and \", so "\\d" reached a
// regular expression as \\d (a literal backslash, then d) and "\t", "\r",
// "\'", "\\" and "\u0041" were kept verbatim. DMN 1.5 §10.3.1.2 grammar rule
// 66 (string literal) admits the escapes \', \", \\, \n, \r, \t and the
// code points \uXXXX and \UXXXXXX; a \uXXXX high surrogate followed by a
// \uXXXX low surrogate is one code point. Interpretation, where the grammar
// is silent: an escape it does not define (\d, \3, a short \u) is kept
// verbatim, backslash included, so a regular expression written with single
// backslashes keeps working; a lone surrogate or an out-of-range \U decodes
// to U+FFFD, the only thing a Go string can hold in its place.
func unescapeString(s string) string {
	if !strings.Contains(s, `\`) {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c != '\\' || i+1 == len(s) {
			b.WriteByte(c)
			continue
		}
		switch s[i+1] {
		case 'n':
			b.WriteByte('\n')
		case 'r':
			b.WriteByte('\r')
		case 't':
			b.WriteByte('\t')
		case '\'', '"', '\\':
			b.WriteByte(s[i+1])
		case 'u':
			r, ok := hexRune(s[i+2:], 4)
			if !ok {
				b.WriteByte(c)
				continue
			}
			n := 6
			if utf16.IsSurrogate(r) {
				hi := r
				r = utf8.RuneError
				if strings.HasPrefix(s[i+6:], `\u`) {
					if lo, ok := hexRune(s[i+8:], 4); ok {
						if pair := utf16.DecodeRune(hi, lo); pair != utf8.RuneError {
							r, n = pair, 12
						}
					}
				}
			}
			b.WriteRune(r)
			i += n - 1
			continue
		case 'U':
			r, ok := hexRune(s[i+2:], 6)
			if !ok {
				b.WriteByte(c)
				continue
			}
			if !utf8.ValidRune(r) {
				r = utf8.RuneError
			}
			b.WriteRune(r)
			i += 7
			continue
		default:
			b.WriteByte(c)
			continue
		}
		i++
	}
	return b.String()
}

// quoteString writes s as a FEEL string literal, quotes included, using only
// the escapes of DMN 1.5 §10.3.1.2 grammar rule 66, so that
// unescapeString(body) == s for every s. `"` and `\` are always escaped (an
// unescaped backslash before an unknown character would be kept verbatim,
// but one before n or u would not); newline, carriage return and tab use
// their short escapes; any other non-printable code point is written as
// \uXXXX, or \UXXXXXX beyond the Basic Multilingual Plane. Printable
// characters, non-ASCII ones included (é, 🐎), are written literally, and so
// are bytes that are not valid UTF-8, which unescapeString passes through.
//
// VERDICT DIVERGENCE: fork-only; see the string() row of NOTICE.md.
func quoteString(b *strings.Builder, s string) {
	const hex = "0123456789ABCDEF"
	b.WriteByte('"')
	for i := 0; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		switch {
		case r == utf8.RuneError && size == 1:
			b.WriteByte(s[i])
		case r == '"' || r == '\\':
			b.WriteByte('\\')
			b.WriteByte(byte(r))
		case r == '\n':
			b.WriteString(`\n`)
		case r == '\r':
			b.WriteString(`\r`)
		case r == '\t':
			b.WriteString(`\t`)
		case unicode.IsPrint(r):
			b.WriteString(s[i : i+size])
		case r <= 0xFFFF:
			b.WriteString(`\u`)
			for shift := 12; shift >= 0; shift -= 4 {
				b.WriteByte(hex[r>>shift&0xF])
			}
		default:
			b.WriteString(`\U`)
			for shift := 20; shift >= 0; shift -= 4 {
				b.WriteByte(hex[r>>shift&0xF])
			}
		}
		i += size
	}
	b.WriteByte('"')
}

// hexRune reads exactly n hexadecimal digits from the start of s.
func hexRune(s string, n int) (rune, bool) {
	if len(s) < n {
		return 0, false
	}
	var r rune
	for _, c := range []byte(s[:n]) {
		var d byte
		switch {
		case c >= '0' && c <= '9':
			d = c - '0'
		case c >= 'a' && c <= 'f':
			d = c - 'a' + 10
		case c >= 'A' && c <= 'F':
			d = c - 'A' + 10
		default:
			return 0, false
		}
		r = r<<4 | rune(d)
	}
	return r, true
}

type mapItem struct {
	Name  string
	Value Node
}

type MapNode struct {
	Values []mapItem

	textRange TextRange
}

func (mapNode MapNode) TextRange() TextRange {
	return mapNode.textRange
}
func (mapNode MapNode) Repr() string {
	var ss []string
	for _, item := range mapNode.Values {
		s := fmt.Sprintf("(\"%s\" %s)", item.Name, item.Value.Repr())
		ss = append(ss, s)
	}
	return fmt.Sprintf("(map %s)", strings.Join(ss, " "))
}

type TemporalNode struct {
	Value     string
	textRange TextRange
}

func (tempNode TemporalNode) TextRange() TextRange {
	return tempNode.textRange
}
func (tempNode TemporalNode) Repr() string {
	return tempNode.Value
}

func (tempNode TemporalNode) Content() string {
	return tempNode.Value[2 : len(tempNode.Value)-1]
}

type RangeNode struct {
	StartOpen bool
	Start     Node

	EndOpen bool
	End     Node

	textRange TextRange
}

func (rangeNode RangeNode) TextRange() TextRange {
	return rangeNode.textRange
}
func (rangeNode RangeNode) Repr() string {
	startQuote := "["
	if rangeNode.StartOpen {
		startQuote = "("
	}
	endQuote := "]"
	if rangeNode.EndOpen {
		endQuote = ")"
	}
	return fmt.Sprintf("%s%s..%s%s", startQuote, rangeNode.Start.Repr(), rangeNode.End.Repr(), endQuote)
}

type IfExpr struct {
	Cond       Node
	ThenBranch Node
	ElseBranch Node

	textRange TextRange
}

func (ifExpr IfExpr) TextRange() TextRange {
	return ifExpr.textRange
}
func (ifExpr IfExpr) Repr() string {
	return fmt.Sprintf("(if %s %s %s)", ifExpr.Cond.Repr(), ifExpr.ThenBranch.Repr(), ifExpr.ElseBranch.Repr())
}

type ArrayNode struct {
	Elements  []Node
	textRange TextRange
}

func (arrNode ArrayNode) TextRange() TextRange {
	return arrNode.textRange
}
func (arrNode ArrayNode) Repr() string {
	s := make([]string, 0)
	for _, elem := range arrNode.Elements {
		s = append(s, elem.Repr())
	}
	return fmt.Sprintf("[%s]", strings.Join(s, ", "))
}

type MultiTests struct {
	Elements  []Node
	textRange TextRange
}

func (mt MultiTests) TextRange() TextRange {
	return mt.textRange
}
func (mt MultiTests) Repr() string {
	s := make([]string, 0)
	for _, elem := range mt.Elements {
		s = append(s, elem.Repr())
	}
	return fmt.Sprintf("(multitests %s)", strings.Join(s, " "))
}

// ForExpr FOR expression. A `for` with several iteration contexts parses as
// nested ForExprs, one per context; every one but the innermost has Continues
// set, and its ReturnExpr is the next context's ForExpr.
type ForExpr struct {
	Varname    string
	ListExpr   Node
	ReturnExpr Node
	Continues  bool
	textRange  TextRange
}

func (fe ForExpr) TextRange() TextRange {
	return fe.textRange
}
func (fe ForExpr) Repr() string {
	return fmt.Sprintf("(for %s %s %s)", fe.Varname, fe.ListExpr.Repr(), fe.ReturnExpr.Repr())
}

// IterationRange is the integer-range form of an iteration context,
// `name in Start .. End`, DMN 1.5 FEEL grammar rule 63. It is only ever the
// ListExpr of a ForExpr, SomeExpr or EveryExpr; a bare `a..b` is not an
// expression anywhere else.
type IterationRange struct {
	Start     Node
	End       Node
	textRange TextRange
}

func (ir IterationRange) TextRange() TextRange {
	return ir.textRange
}
func (ir IterationRange) Repr() string {
	return fmt.Sprintf("(iterrange %s %s)", ir.Start.Repr(), ir.End.Repr())
}

// SomeExpr some expression
type SomeExpr struct {
	Varname    string
	ListExpr   Node
	FilterExpr Node
	textRange  TextRange
}

func (sexpr SomeExpr) TextRange() TextRange {
	return sexpr.textRange
}
func (sexpr SomeExpr) Repr() string {
	return fmt.Sprintf("(some \"%s\" %s %s)", sexpr.Varname, sexpr.ListExpr.Repr(), sexpr.FilterExpr.Repr())
}

// EveryExpr Every expression
type EveryExpr struct {
	Varname    string
	ListExpr   Node
	FilterExpr Node

	textRange TextRange
}

func (ee EveryExpr) TextRange() TextRange {
	return ee.textRange
}
func (ee EveryExpr) Repr() string {
	return fmt.Sprintf("(every \"%s\" %s %s)", ee.Varname, ee.ListExpr.Repr(), ee.FilterExpr.Repr())
}
