package dialect

import (
	"fmt"
	"strings"
)

type Scope map[string]interface{}

type Interpreter struct {
	ScopeStack []Scope
	// MaxIterations is the iteration budget of one evaluation: the total
	// number of elements every iteration context of every for, some and
	// every may produce, nested and multi-context products included. Zero
	// means no budget, which is upstream's behaviour; the façade always sets
	// one.
	//
	// VERDICT DIVERGENCE: see NOTICE.md. The fork takes the budget as a
	// plain field rather than importing any Verdict configuration.
	MaxIterations int64
	// iterations counts the elements charged against MaxIterations so far.
	// It lives on the interpreter, which is built afresh for every
	// evaluation and never shared between goroutines, so the budget is per
	// evaluation and needs no lock.
	iterations uint64
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
	Body Node

	textRange TextRange
}

func (fd FunDef) TextRange() TextRange {
	return fd.textRange
}

func (fd FunDef) Repr() string {
	return fmt.Sprintf("(function [%s] %s)", strings.Join(fd.Args, ", "), fd.Body.Repr())
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
	s := stringNode.Value[1 : len(stringNode.Value)-1]

	s = strings.ReplaceAll(s, "\\n", "\n")
	s = strings.ReplaceAll(s, "\\\"", "\"")
	return s
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

// ExprList Expression List
type ExprList struct {
	Elements  []Node
	textRange TextRange
}

func (exprList ExprList) TextRange() TextRange {
	return exprList.textRange
}
func (exprList ExprList) Repr() string {
	s := make([]string, 0)
	for _, elem := range exprList.Elements {
		s = append(s, elem.Repr())
	}
	return fmt.Sprintf("(explist %s)", strings.Join(s, " "))
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
