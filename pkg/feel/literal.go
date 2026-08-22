package feel

import (
	"math/big"
	"time"

	pfeel "github.com/frankbardon/verdict/pkg/feel/internal/dialect"
)

// Literal is a constant an expression mentions, paired with the source text
// that produced it.
//
// Static analysis needs the boundaries a decision table's rules test against,
// and the AST that carries them is an implementation detail of the embedded
// evaluator. Exposing literals rather than nodes keeps the analyser independent
// of which FEEL implementation is underneath.
type Literal struct {
	// Text is the literal as written, for the analysis report.
	Text string
	// Value is the FEEL value.
	Value any
}

// Literals returns the constants an expression mentions, in source order.
// Sub-expressions the extractor does not understand contribute nothing, so a
// caller must treat an empty result as "not statically decidable" rather than
// as "mentions no constants".
func (e *Expr) Literals() []Literal {
	if e == nil {
		return nil
	}
	return literalsOf(e.node)
}

// Literals returns the constants a unary test mentions across all of its
// alternatives.
func (u *UnaryTest) Literals() []Literal {
	if u == nil {
		return nil
	}
	var out []Literal
	for _, alt := range u.alts {
		out = append(out, literalsOf(alt.expr.node)...)
	}
	return out
}

func literalsOf(n pfeel.Node) []Literal {
	switch v := n.(type) {
	case nil:
		return nil
	case *pfeel.NumberNode:
		return []Literal{{Text: v.Repr(), Value: pfeel.NewNumber(v.Value)}}
	case *pfeel.StringNode:
		s := v.Content()
		return []Literal{{Text: `"` + s + `"`, Value: s}}
	case *pfeel.BoolNode:
		return []Literal{{Text: "true", Value: true}, {Text: "false", Value: false}}
	case *pfeel.TemporalNode:
		val, err := pfeel.ParseTemporalValue(v.Content())
		if err != nil {
			return nil
		}
		return []Literal{{Text: v.Repr(), Value: val}}
	case *pfeel.RangeNode:
		return append(literalsOf(v.Start), literalsOf(v.End)...)
	case *pfeel.Binop:
		return append(literalsOf(v.Left), literalsOf(v.Right)...)
	case *pfeel.ArrayNode:
		return literalsOfAll(v.Elements)
	case *pfeel.MultiTests:
		return literalsOfAll(v.Elements)
	case *pfeel.ExprList:
		return literalsOfAll(v.Elements)
	default:
		return nil
	}
}

func literalsOfAll(nodes []pfeel.Node) []Literal {
	var out []Literal
	for _, n := range nodes {
		out = append(out, literalsOf(n)...)
	}
	return out
}

// IsNumber reports whether v is a FEEL number.
func IsNumber(v any) bool {
	_, ok := v.(*pfeel.Number)
	return ok
}

// IsTemporal reports whether v is a FEEL date, time or date-and-time.
func IsTemporal(v any) bool {
	switch v.(type) {
	case *pfeel.FEELDate, *pfeel.FEELTime, *pfeel.FEELDatetime:
		return true
	}
	return false
}

// NumberOffset steps a number by a whole unit, returning v unchanged when it is
// not a number. Static analysis uses it to probe just either side of a
// threshold, which is what distinguishes `>= 10` from `> 10`.
func NumberOffset(v any, delta int64) any {
	n, ok := v.(*pfeel.Number)
	if !ok {
		return v
	}
	f := new(big.Float).SetPrec(pfeel.Prec)
	if _, _, err := f.Parse(n.String(), 10); err != nil {
		return v
	}
	f.Add(f, new(big.Float).SetPrec(pfeel.Prec).SetInt64(delta))
	return pfeel.NewNumber(f.Text('f', 10))
}

// TemporalOffset shifts a date or date-and-time by d, returning v unchanged
// when it is neither.
func TemporalOffset(v any, d time.Duration) any {
	switch t := v.(type) {
	case *pfeel.FEELDate:
		return FromGo(t.Date().Add(d))
	case *pfeel.FEELDatetime:
		return FromGo(t.Time().Add(d))
	}
	return v
}
