package dialect

import "fmt"

// VERDICT DIVERGENCE: `between` (DMN 1.5 grammar rule 49.b, Table 55) is
// fork-only; upstream did not parse it. See NOTICE.md.

// Between is `e1 between e2 and e3`, grammar rule 49.b.
type Between struct {
	Expr Node
	Low  Node
	High Node

	textRange TextRange
}

func (b Between) TextRange() TextRange {
	return b.textRange
}

func (b Between) Repr() string {
	return fmt.Sprintf("(between %s %s %s)", b.Expr.Repr(), b.Low.Repr(), b.High.Repr())
}

// Eval applies DMN 1.5 Table 55: `e1 between e2 and e3` is `e1 >= e2 and
// e1 <= e3`, inclusive at both ends. Each operand is evaluated once, then the
// two comparisons and the three-valued `and` are the ordinary operators, so
// incomparable or null operands give null exactly where the equivalent
// expression does: `"a" between 1 and 5` and `null between 1 and 5` are null,
// while `9 between null and 5` is `null and false`, which is false.
func (b Between) Eval(intp *Interpreter) (any, error) {
	v, err := b.Expr.Eval(intp)
	if err != nil {
		return nil, err
	}
	lo, err := b.Low.Eval(intp)
	if err != nil {
		return nil, err
	}
	hi, err := b.High.Eval(intp)
	if err != nil {
		return nil, err
	}
	expr := Binop{
		Op:    "and",
		Left:  &Binop{Op: ">=", Left: valueNode{v}, Right: valueNode{lo}},
		Right: &Binop{Op: "<=", Left: valueNode{v}, Right: valueNode{hi}},
	}
	return expr.Eval(intp)
}
