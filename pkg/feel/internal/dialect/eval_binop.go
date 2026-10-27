package dialect

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

func (binop Binop) Eval(intp *Interpreter) (any, error) {
	switch binop.Op {
	case "and":
		return binop.andOp(intp)
	case "or":
		return binop.orOp(intp)
	case "+":
		return binop.addOp(intp)
	case "-":
		return binop.subOp(intp)
	case "*":
		return binop.mulOp(intp)
	case "/":
		return binop.divOp(intp)
	case "**":
		return binop.powOp(intp)
	case "%":
		return binop.modOp(intp)
	case ">":
		return binop.compareGTOp(intp)
	case ">=":
		return binop.compareGEOp(intp)
	case "<":
		return binop.compareLTOp(intp)
	case "<=":
		return binop.compareLEOp(intp)
	case "!=":
		return binop.notEqalOp(intp)
	case "[]":
		return binop.indexAtOp(intp)
	case "=":
		return binop.equalOp(intp)
	case "in":
		return binop.inOp(intp)
	default:
		return nil, NewEvalError(-3000, "no such binary op", fmt.Sprintf("Binary op %s not exist or not supported", binop.Op))
	}
}

type evalNumbers func(a, b *Number) any
type evalStrings func(a, b string) any

func (binop Binop) numberOp(intp *Interpreter, en evalNumbers, op string) (any, error) {
	leftVal, err := binop.Left.Eval(intp)
	if err != nil {
		return nil, err
	}
	rightVal, err := binop.Right.Eval(intp)
	if err != nil {
		return nil, err
	}
	if leftNumber, ok := leftVal.(*Number); ok {
		if rightNumber, ok := rightVal.(*Number); ok {
			return en(leftNumber, rightNumber), nil
		}
	}
	// VERDICT DIVERGENCE: a null operand, or a pair of types the operator is
	// not defined over, is null rather than an error (DMN 1.5 §10.3.2.15). See
	// the comment on typedOp.
	return null, nil
}

func CompareValues(leftVal, rightVal any) int {
	r, err := compareInterfaces(leftVal, rightVal)
	if err != nil {
		panic(err)
	} else {
		return r
	}
}

func compareInterfaces(leftVal, rightVal any) (int, error) {
	if _, ok := leftVal.(*NullValue); ok {
		if _, ok = rightVal.(*NullValue); ok {
			return 0, nil
		}
		// every (right) value is bigger than NullValue
		return 1, nil
	}
	if _, ok := rightVal.(*NullValue); ok {
		// every (left) value is bigger than NullValue
		return -1, nil
	}

	switch v := leftVal.(type) {
	case string:
		if rightString, ok := rightVal.(string); ok {
			return strings.Compare(v, rightString), nil
		}
	case *Number:
		if rightNumber, ok := rightVal.(*Number); ok {
			return v.Compare(*rightNumber), nil
		}
	case bool:
		if rightBool, ok := rightVal.(bool); ok {
			if v == rightBool {
				return 0, nil
			} else if v == false {
				return -1, nil
			} else {
				return 1, nil
			}
		}
	case []any:
		if _, ok := rightVal.([]any); ok {
			return 0, NewEvalError(codeUnordered, "unordered type", "lists have no order")
		}
	case map[string]any:
		if _, ok := rightVal.(map[string]any); ok {
			return 0, NewEvalError(codeUnordered, "unordered type", "contexts have no order")
		}
	case *FEELDuration:
		// Upstream had no duration case, so every duration comparison raised.
		if rightDur, ok := rightVal.(*FEELDuration); ok {
			if r, ok := compareDurations(v, rightDur); ok {
				return r, nil
			}
		}
	}
	// Temporals are compared last, and through instantOf rather than through a
	// HasTime type switch.
	//
	// Upstream matched only the HasTime interface, which FEELDate does not
	// satisfy — it exposes Date(), not Time() — so comparing two dates raised a
	// type error. Date comparison is the bread and butter of a decision table
	// ("before the cutoff", "older than 18"), so a decision engine cannot ship
	// without it.
	if leftTime, ok := instantOf(leftVal); ok {
		if rightTime, ok := instantOf(rightVal); ok {
			switch {
			case leftTime.Equal(rightTime):
				return 0, nil
			case leftTime.Before(rightTime):
				return -1, nil
			default:
				return 1, nil
			}
		}
	}
	if comparisonKind(leftVal) != comparisonKind(rightVal) {
		return 0, NewEvalError(codeIncomparable, "incomparable types", fmt.Sprintf("cannot compare %T with %T", leftVal, rightVal))
	}
	return 0, NewEvalError(-3106, "invalid types", fmt.Sprintf("bad type in comparation, %T vs. %T", leftVal, rightVal))
}

// codeIncomparable is the EvalError code compareInterfaces reports when its
// operands are of different kinds (a number and a string, a years-and-months
// and a days-and-time duration). It is distinct from -3106, which still covers
// two values of one kind that the fork cannot order.
//
// VERDICT DIVERGENCE: upstream raised -3106 for both, and `=`, `<`, `<=`, `>`,
// `>=`, range membership and the range built-ins all propagated it as an
// evaluation error. DMN 1.5 §10.3.2.15 says inputs outside an operator's
// domain yield null, and Table 52 restricts every comparison to two operands
// of the same kind; §10.3.2.2 gives `"1" = 1` as null explicitly. Those
// callers turn this code into null (see isIncomparable).
const codeIncomparable = -3107

// isIncomparable reports whether err is compareInterfaces' different-kinds
// error, at any depth of a list or context comparison.
func isIncomparable(err error) bool {
	var evalError *EvalError
	return errors.As(err, &evalError) && evalError.Code == codeIncomparable
}

// comparisonKind names the FEEL kind a value is compared as. Values of
// different kinds are never comparable. The three temporal types share one
// kind because compareInterfaces orders their mixtures through instantOf; the
// two duration types do not, because DMN keeps them distinct.
func comparisonKind(v any) string {
	switch x := v.(type) {
	case string:
		return "string"
	case *Number:
		return "number"
	case bool:
		return "boolean"
	case []any:
		return "list"
	case map[string]any:
		return "context"
	case *RangeValue:
		return "range"
	case *FEELDuration:
		if x.isYearsMonths() {
			return "years and months duration"
		}
		return "days and time duration"
	}
	if _, ok := instantOf(v); ok {
		return "temporal"
	}
	return fmt.Sprintf("%T", v)
}

// instantOf reports the instant a temporal value denotes, whichever of FEEL's
// three temporal types it is.
func instantOf(v any) (time.Time, bool) {
	switch t := v.(type) {
	case HasTime:
		return t.Time(), true
	case HasDate:
		return t.Date(), true
	}
	return time.Time{}, false
}

// codeUnordered is the EvalError code compareInterfaces reports for two lists
// or two contexts: values of one kind that FEEL defines equality over but no
// order (DMN 1.5 Table 54 lists no `<` for either).
//
// VERDICT DIVERGENCE: upstream ordered lists lexicographically and contexts by
// size, and its list comparison answered 1 for two equal lists of the same
// length, so `[1, 2] = [1, 2]` was false and `[1, 2] < [1, 3]` true. Equality
// now goes through feelEqual, and the ordering operators turn this code into
// null (§10.3.2.15: inputs outside an operator's domain are null).
const codeUnordered = -3108

// isUnordered reports whether err is compareInterfaces' no-order error.
func isUnordered(err error) bool {
	var evalError *EvalError
	return errors.As(err, &evalError) && evalError.Code == codeUnordered
}

// feelEqual implements FEEL's `=` (DMN 1.5 §10.3.2.15, Tables 52 and 53) and
// yields true, false or null:
//
//   - a null operand makes a boolean: `null = null` is true, `2 = null` false;
//   - two lists are equal when they have the same length N and e1[i] = e2[i]
//     for every i; two contexts when they have the same set of keys K and
//     e1.k = e2.k for every k. Both are the three-valued conjunction of the
//     element comparisons, so one false pair makes the result false and,
//     failing that, one null pair (`[1, "a"] = [1, 2]`) makes it null;
//   - operands of different kinds are null (§10.3.2.2: `"1" = 1` is null);
//   - anything else is compareInterfaces' verdict.
//
// VERDICT DIVERGENCE: upstream compared lists through an ordering that never
// answered 0 for two equal lists, so `[1, 2] = [1, 2]` was false.
func feelEqual(a, b any) (any, error) {
	if isNullValue(a) || isNullValue(b) {
		return isNullValue(a) && isNullValue(b), nil
	}
	switch av := a.(type) {
	case []any:
		bv, ok := b.([]any)
		if !ok {
			break
		}
		if len(av) != len(bv) {
			return false, nil
		}
		unknown := false
		for i := range av {
			r, err := feelEqual(av[i], bv[i])
			if err != nil {
				return nil, err
			}
			switch r {
			case false:
				return false, nil
			case true:
			default:
				unknown = true
			}
		}
		if unknown {
			return null, nil
		}
		return true, nil
	case map[string]any:
		bv, ok := b.(map[string]any)
		if !ok {
			break
		}
		if len(av) != len(bv) {
			return false, nil
		}
		for k := range av {
			if _, ok := bv[k]; !ok {
				return false, nil
			}
		}
		unknown := false
		for k, v := range av {
			r, err := feelEqual(v, bv[k])
			if err != nil {
				return nil, err
			}
			switch r {
			case false:
				return false, nil
			case true:
			default:
				unknown = true
			}
		}
		if unknown {
			return null, nil
		}
		return true, nil
	}
	r, err := compareInterfaces(a, b)
	if err != nil {
		if isIncomparable(err) {
			return null, nil
		}
		return nil, err
	}
	return r == 0, nil
}

// valuesEqual reports whether a = b is true. The list built-ins that search or
// deduplicate by equality (`list contains`, `index of`, `union`,
// `distinct values`) and list membership (`in`) use it, so a list or a context
// element is found by value, as it is by `=`.
func valuesEqual(a, b any) bool {
	r, err := feelEqual(a, b)
	return err == nil && r == true
}

// typedOp implements FEEL's `+` and `-`.
//
// VERDICT DIVERGENCE: upstream raised a type mismatch when an operand was null
// or when the operand types had no defined sum or difference, so `1 + null`
// failed the whole evaluation; and it concatenated a string with a number, so
// `1 + "a"` was "1a". DMN 1.5 §10.3.2.15 defines arithmetic only over the type
// pairs its semantic tables list — numbers, two strings for `+`, and the
// temporal/duration combinations — and every other combination, a null
// operand included, is null. A missing input in `a + b` is an absent answer,
// not a broken model.
func (binop Binop) typedOp(intp *Interpreter, es evalStrings, en evalNumbers, op string) (any, error) {
	leftVal, err := binop.Left.Eval(intp)
	if err != nil {
		return nil, err
	}
	rightVal, err := binop.Right.Eval(intp)
	if err != nil {
		return nil, err
	}

	switch v := leftVal.(type) {
	case string:
		if es != nil {
			if rightString, ok := rightVal.(string); ok {
				return es(v, rightString), nil
			}
		}
	case *Number:
		if en != nil {
			if rightNumber, ok := rightVal.(*Number); ok {
				return en(v, rightNumber), nil
			}
		}
	case *FEELDatetime:
		// Upstream subtracted anything with a Time() — a time included, so
		// date and time - time was a meaningless duration — and had no date
		// operand, so date and time - date was null.
		switch r := rightVal.(type) {
		case *FEELDuration:
			if op == "-" {
				r = r.Negative()
			}
			return v.Add(r), nil
		case *FEELDatetime:
			if op == "-" {
				return subInstants(v.t, r.t)
			}
		case *FEELDate:
			if op == "-" {
				return subInstants(v.t, r.utcMidnight())
			}
		}
	case *FEELDuration:
		// Upstream had no duration case, so duration ± duration fell through
		// to null.
		if rightDur, ok := rightVal.(*FEELDuration); ok {
			if op == "-" {
				rightDur = rightDur.Negative()
			}
			sum, ok, err := addDurations(v, rightDur)
			if err != nil {
				return nil, err
			}
			if ok {
				return sum, nil
			}
			break
		}
		// Table 57: addition of a duration to a date, a date and time or a
		// time commutes; subtraction from a duration is undefined.
		if op != "+" {
			break
		}
		switch r := rightVal.(type) {
		case *FEELDate:
			return r.AddDuration(v), nil
		case *FEELDatetime:
			return r.Add(v), nil
		case *FEELTime:
			if !v.isYearsMonths() {
				return r.AddDuration(v), nil
			}
		}
	case *FEELDate:
		// Upstream had no date case, so date ± duration and date - date fell
		// through to null.
		switch r := rightVal.(type) {
		case *FEELDuration:
			if op == "-" {
				r = r.Negative()
			}
			return v.AddDuration(r), nil
		case *FEELDate:
			if op == "-" {
				return subInstants(v.utcMidnight(), r.utcMidnight())
			}
		case *FEELDatetime:
			if op == "-" {
				return subInstants(v.utcMidnight(), r.t)
			}
		}
	case *FEELTime:
		// Upstream had no time case, so time ± duration and time - time fell
		// through to null. Table 57 pairs a time with a days and time
		// duration only; with a years and months duration it is null.
		switch r := rightVal.(type) {
		case *FEELDuration:
			if !r.isYearsMonths() {
				if op == "-" {
					r = r.Negative()
				}
				return v.AddDuration(r), nil
			}
		case *FEELTime:
			if op == "-" {
				return subInstants(v.t, r.t)
			}
		}
	}
	return null, nil
}

func (binop Binop) addOp(intp *Interpreter) (any, error) {
	return binop.typedOp(
		intp,
		func(a, b string) any { return a + b },
		func(a, b *Number) any { return a.Add(b) },
		"+",
	)
}

func (binop Binop) subOp(intp *Interpreter) (any, error) {
	return binop.typedOp(
		intp,
		nil,
		func(a, b *Number) any { return a.Sub(b) },
		"-")
}

func (binop Binop) mulOp(intp *Interpreter) (any, error) {
	return binop.numberOp(
		intp,
		func(a, b *Number) any { return a.Mul(b) },
		"*")
}

// divOp implements FEEL's `/`.
//
// Upstream routed division through IntDiv, so `1 / 3` evaluated to 0 and every
// ratio in a decision model silently collapsed to an integer. FEEL numbers are
// decimals: division is exact division at the evaluator's working precision,
// and division by zero is null rather than a panic (DMN 1.5 table 51).
func (binop Binop) divOp(intp *Interpreter) (any, error) {
	return binop.numberOp(
		intp,
		func(a, b *Number) any {
			if b.IsZero() {
				return null
			}
			return a.Div(b)
		},
		"/")
}

// powOp implements FEEL's `**`, which upstream did not parse at all.
//
// An integer exponent is computed exactly at the evaluator's working precision
// by repeated squaring; any other exponent goes through float64. A result that
// is not a finite number — `0 ** -1`, `(-8) ** 0.5`, an overflow — is null, as
// division by zero is (DMN 1.5 §10.3.2.15).
func (binop Binop) powOp(intp *Interpreter) (any, error) {
	return binop.numberOp(
		intp,
		func(a, b *Number) any {
			if p, ok := a.Pow(b); ok {
				return p
			}
			return null
		},
		"**")
}

// Negation implements FEEL's unary `-`, which upstream did not parse: a
// leading minus was only ever part of a numeric literal, so `-x` and `-(a + b)`
// were syntax errors. DMN 1.5 §10.3.2.15 defines negation over numbers and
// durations; anything else, null included, is null.
type Negation struct {
	Operand Node

	textRange TextRange
}

func (n Negation) TextRange() TextRange { return n.textRange }
func (n Negation) Repr() string         { return fmt.Sprintf("(- %s)", n.Operand.Repr()) }

func (n Negation) Eval(intp *Interpreter) (any, error) {
	v, err := n.Operand.Eval(intp)
	if err != nil {
		return nil, err
	}
	switch x := v.(type) {
	case *Number:
		return NewNumberFromInt64(0).Sub(x), nil
	case *FEELDuration:
		return x.Negative(), nil
	}
	return null, nil
}

// orderingOp implements FEEL's `<`, `<=`, `>` and `>=`.
//
// Upstream routed them through compareInterfaces, which orders null below every
// other value, so `2 < null` and `null >= null` evaluated to true. DMN 1.5
// §10.3.2.15 makes an ordering comparison with a null operand null, not a
// boolean: there is no answer to whether an absent value is smaller.
// Equality keeps compareInterfaces' treatment, because `null = null` is true
// and `2 = null` is false in the spec too.
func (binop Binop) orderingOp(intp *Interpreter, holds func(r int) bool) (any, error) {
	leftVal, err := binop.Left.Eval(intp)
	if err != nil {
		return false, err
	}
	rightVal, err := binop.Right.Eval(intp)
	if err != nil {
		return false, err
	}
	if isNullValue(leftVal) || isNullValue(rightVal) {
		return null, nil
	}
	r, err := compareInterfaces(leftVal, rightVal)
	if err != nil {
		if isIncomparable(err) || isUnordered(err) {
			return null, nil
		}
		return false, err
	}
	return holds(r), nil
}

func isNullValue(v any) bool {
	if v == nil {
		return true
	}
	_, ok := v.(*NullValue)
	return ok
}

func (binop Binop) compareGTOp(intp *Interpreter) (any, error) {
	return binop.orderingOp(intp, func(r int) bool { return r > 0 })
}

func (binop Binop) compareGEOp(intp *Interpreter) (any, error) {
	return binop.orderingOp(intp, func(r int) bool { return r >= 0 })
}

func (binop Binop) compareLTOp(intp *Interpreter) (any, error) {
	return binop.orderingOp(intp, func(r int) bool { return r < 0 })
}

func (binop Binop) compareLEOp(intp *Interpreter) (any, error) {
	return binop.orderingOp(intp, func(r int) bool { return r <= 0 })
}

// equalOp implements FEEL's `=` through feelEqual: operands of different kinds
// compare as null (DMN 1.5 §10.3.2.2: `"1" = 1` is null); a null operand still
// makes a boolean, so `null = null` is true and `2 = null` is false; lists and
// contexts compare element by element.
func (binop Binop) equalOp(intp *Interpreter) (any, error) {
	leftVal, err := binop.Left.Eval(intp)
	if err != nil {
		return false, err
	}
	rightVal, err := binop.Right.Eval(intp)
	if err != nil {
		return false, err
	}
	r, err := feelEqual(leftVal, rightVal)
	if err != nil {
		return false, err
	}
	return r, nil
}

// notEqalOp implements FEEL's `!=`, which DMN 1.5 §10.3.2.15 defines as
// not(e1 = e2): null wherever `=` is null.
func (binop Binop) notEqalOp(intp *Interpreter) (any, error) {
	leftVal, err := binop.Left.Eval(intp)
	if err != nil {
		return false, err
	}
	rightVal, err := binop.Right.Eval(intp)
	if err != nil {
		return false, err
	}
	r, err := feelEqual(leftVal, rightVal)
	if err != nil {
		var evalError *EvalError
		if errors.As(err, &evalError) && evalError.Code == -3106 {
			// type mismatch
			return false, nil
		}
		return false, err
	}
	if b, ok := r.(bool); ok {
		return !b, nil
	}
	return null, nil
}

func (binop Binop) modOp(intp *Interpreter) (any, error) {
	return binop.numberOp(
		intp,
		func(a, b *Number) any { return a.IntMod(b) },
		"%")
}

// circuit break operators
func (binop Binop) andOp(intp *Interpreter) (any, error) {
	leftVal, err := binop.Left.Eval(intp)
	if err != nil {
		return nil, err
	}
	leftBool := boolValue(leftVal)
	if !leftBool {
		return false, nil
	}
	rightVal, err := binop.Right.Eval(intp)
	if err != nil {
		return nil, err
	}
	rightBool := boolValue(rightVal)
	return rightBool, nil
}

func (binop Binop) orOp(intp *Interpreter) (any, error) {
	leftVal, err := binop.Left.Eval(intp)
	if err != nil {
		return nil, err
	}
	leftBool := boolValue(leftVal)
	if leftBool {
		return true, nil
	}
	rightVal, err := binop.Right.Eval(intp)
	if err != nil {
		return nil, err
	}
	rightBool := boolValue(rightVal)
	return rightBool, nil
}

func (binop Binop) indexAtOp(intp *Interpreter) (any, error) {
	leftVal, err := binop.Left.Eval(intp)
	if err != nil {
		return nil, err
	}
	rightVal, err := binop.Right.Eval(intp)
	if err != nil {
		return nil, err
	}
	switch v := leftVal.(type) {
	case []any:
		if nRight, ok := rightVal.(*Number); ok {
			at := nRight.Int()
			if at <= 0 || at > len(v) {
				return nil, NewErrIndex("index out of range")
			}
			return v[at-1], nil
		} else {
			//return nil, NewEvalError(-3200, "non int index")
			return nil, NewErrIndex("non int index")
		}
	case map[string]any:
		if strRight, ok := rightVal.(string); ok {
			if elem, ok := v[strRight]; ok {
				return elem, nil
			} else {
				//return nil, NewEvalError(-3201, "key not found")
				return nil, NewErrKeyNotFound(strRight)
			}
		} else {
			//return nil, NewEvalError(-3200, "non string index")
			return nil, NewErrIndex("non string index")
		}
	default:
		//return nil, NewEvalError(-3202, "non indexable value")
		return nil, NewErrIndex("non-indexable value")
	}
}

func (binop Binop) inOp(intp *Interpreter) (any, error) {
	leftVal, err := binop.Left.Eval(intp)
	if err != nil {
		return nil, err
	}
	rightVal, err := binop.Right.Eval(intp)
	if err != nil {
		return nil, err
	}
	switch rv := rightVal.(type) {
	case *RangeValue:
		// Range membership is the conjunction of the endpoint comparisons, and
		// DMN 1.5 §10.3.2.15 makes an ordering comparison with a null operand
		// null. Upstream ordered null below every value, so `null in [1..10]`
		// was false and `not(null in [1..10])` was true.
		// A null endpoint (`[null..10]`) is the same unknown on the range's
		// side of those comparisons.
		if isNullValue(leftVal) || rv.HasNullEndpoint() {
			return null, nil
		}
		// A point of another kind than the endpoints (`5 in ["a".."z"]`)
		// makes those comparisons null too, where upstream panicked.
		if _, err := rv.Position(leftVal); err != nil {
			if isIncomparable(err) {
				return null, nil
			}
			return nil, err
		}
		return rv.Contains(leftVal), nil
	case []any:
		for _, kv := range rv {
			if valuesEqual(leftVal, kv) {
				return true, nil
			}
		}
		return false, nil
	default:
		return nil, NewErrBadOp(typeName(leftVal), "in", typeName(rightVal))
	}
}
