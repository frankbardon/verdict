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
		// VERDICT DIVERGENCE: upstream ordered false below true, which min,
		// max, range membership and the range built-ins inherited. DMN 1.5
		// Table 54 defines no order over booleans; equality goes through
		// feelEqual instead.
		if _, ok := rightVal.(bool); ok {
			return 0, NewEvalError(codeUnordered, "unordered type", "booleans have no order")
		}
	case *RangeValue:
		// VERDICT DIVERGENCE: upstream raised -3106 for two ranges, so
		// `[1..2] = [1..2]` failed. Table 54 defines no order over ranges;
		// equality goes through feelEqual (Table 53).
		if _, ok := rightVal.(*RangeValue); ok {
			return 0, NewEvalError(codeUnordered, "unordered type", "ranges have no order")
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
			leftTime, rightTime = adoptOffset(leftVal, rightVal, leftTime, rightTime)
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

// zoneness reports whether v is a time or a date and time (ok) and, if so,
// whether it has a time offset or zone. A date has no zone-ness of its own: it
// adopts the other operand's (see mixedZones).
func zoneness(v any) (zoned, ok bool) {
	switch t := v.(type) {
	case *FEELTime:
		return t.hasOffset, true
	case FEELTime:
		return t.hasOffset, true
	case *FEELDatetime:
		return t.hasOffset, true
	case FEELDatetime:
		return t.hasOffset, true
	}
	return false, false
}

// mixedZones reports whether a and b are times or date and times of which
// exactly one has a time offset: a zoned value against a zoneless (local) one.
//
// VERDICT DIVERGENCE: upstream read a zoneless value as UTC, so the case could
// not arise. DMN 1.5 §10.3.2.3.4 and §10.3.2.3.6 give only a zoned value a UTC
// form, and the operators treat the mixture specially:
//
//   - `=` (Table 53) compares valuedt/valuet, which a local value lacks, so a
//     mixed `=` is null and so is `!=`;
//   - `<` and `>` (Table 54): "If one input has a null timezone offset, that
//     input uses the timezone offset of the other input" (adoptOffset);
//   - `<=` and `>=` are `e1 < e2 or e1 = e2` (Table 54), so a mixed one is
//     true when the strict comparison is and null otherwise;
//   - date and time - date and time (Table 57) "is undefined for the case
//     where only one of the values has a timezone", so it is null; Verdict
//     reads time - time the same way, which Table 57 does not state (NOTICE).
//
// A date meeting a date and time stands for its UTC midnight (§10.3.2.3.5)
// but takes the date and time's zone-ness, so it is never mixed; that too is
// an interpretation recorded in NOTICE.md.
func mixedZones(a, b any) bool {
	za, aok := zoneness(a)
	zb, bok := zoneness(b)
	return aok && bok && za != zb
}

// adoptOffset re-reads the wall clock of the zoneless one of two mixed
// operands at the zoned one's offset (DMN 1.5 Table 54), and returns both
// instants. For operands that are not mixed it returns them unchanged.
func adoptOffset(a, b any, at, bt time.Time) (time.Time, time.Time) {
	if !mixedZones(a, b) {
		return at, bt
	}
	if za, _ := zoneness(a); za {
		return at, atOffsetOf(bt, at)
	}
	return atOffsetOf(at, bt), bt
}

// atOffsetOf is local's wall clock at zoned's time offset.
func atOffsetOf(local, zoned time.Time) time.Time {
	_, offset := zoned.Zone()
	return time.Date(local.Year(), local.Month(), local.Day(),
		local.Hour(), local.Minute(), local.Second(), local.Nanosecond(),
		time.FixedZone("", offset))
}

// codeUnordered is the EvalError code compareInterfaces reports for two lists,
// two contexts, two booleans or two ranges: values of one kind that FEEL
// defines equality over but no order (DMN 1.5 Table 54 lists no `<` for any of
// them).
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
//   - two booleans are equal when they are the same boolean;
//   - two ranges are equal when both closednesses are the same and both pairs
//     of endpoints are `=`, again three-valued over the endpoint pairs;
//   - operands of different kinds are null (§10.3.2.2: `"1" = 1` is null);
//   - a zoned and a zoneless time or date and time are null (Table 53; see
//     mixedZones), while two zoned values are equal when they denote the
//     same instant, whatever their offsets;
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
	case bool:
		if bv, ok := b.(bool); ok {
			return av == bv, nil
		}
	case *RangeValue:
		bv, ok := b.(*RangeValue)
		if !ok {
			break
		}
		if av.StartOpen != bv.StartOpen || av.EndOpen != bv.EndOpen {
			return false, nil
		}
		unknown := false
		for _, pair := range [][2]any{{av.Start, bv.Start}, {av.End, bv.End}} {
			r, err := feelEqual(pair[0], pair[1])
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
	if mixedZones(a, b) {
		return null, nil
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

// Equal implements FEEL's `=` for callers outside this package, and yields
// true, false or null. A pair the fork cannot compare at all (two functions)
// is false, as it is for valuesEqual and list membership.
//
// VERDICT DIVERGENCE: upstream had no exported equality. pkg/feel used its own
// two-valued structural comparison for the ANY hit policy, unary-test equality
// and `duplicate values`, which disagreed with `=` on temporals and durations
// and could not say "unknown"; it now derives from this, so FEEL has one
// equality (DMN 1.5 §10.3.2.15, Tables 52 and 53).
func Equal(a, b any) any {
	r, err := feelEqual(a, b)
	if err != nil {
		return false
	}
	return r
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
			// VERDICT DIVERGENCE: a zoned minus a zoneless value is null
			// (Table 57: "undefined for the case where only one of the values
			// has a timezone"). A date takes the other operand's zone-ness.
			if op == "-" && !mixedZones(v, r) {
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
			// VERDICT DIVERGENCE: null when one time is zoned and the other
			// not, as for date and times; see mixedZones.
			if op == "-" && !mixedZones(v, r) {
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
	return binop.scaleOp(
		intp,
		func(a, b *Number) any { return a.Mul(b) },
		false)
}

// scaleOp is `*` (divide false) or `/` (divide true): number by number through
// en, and a duration scaled by a number through scaleDuration. Multiplication
// commutes, so number * duration scales too; number / duration is undefined.
//
// VERDICT DIVERGENCE: upstream routed both through numberOp, so a duration
// operand was null (DMN 1.5 §10.3.2.15, Table 59 defines duration * number,
// number * duration, duration / number and duration / duration of one kind,
// the last a number). Every other pairing, null included, is still null.
func (binop Binop) scaleOp(intp *Interpreter, en evalNumbers, divide bool) (any, error) {
	leftVal, err := binop.Left.Eval(intp)
	if err != nil {
		return nil, err
	}
	rightVal, err := binop.Right.Eval(intp)
	if err != nil {
		return nil, err
	}
	var dur *FEELDuration
	var factor *Number
	switch l := leftVal.(type) {
	case *Number:
		switch r := rightVal.(type) {
		case *Number:
			return en(l, r), nil
		case *FEELDuration:
			if !divide {
				dur, factor = r, l
			}
		}
	case *FEELDuration:
		switch r := rightVal.(type) {
		case *Number:
			dur, factor = l, r
		case *FEELDuration:
			if divide {
				if q, ok := divideDurations(l, r); ok {
					return q, nil
				}
			}
		}
	}
	if dur == nil {
		return null, nil
	}
	res, ok, err := scaleDuration(dur, factor, divide)
	if err != nil {
		return nil, err
	}
	if !ok {
		return null, nil
	}
	return res, nil
}

// divOp implements FEEL's `/`.
//
// Upstream routed division through IntDiv, so `1 / 3` evaluated to 0 and every
// ratio in a decision model silently collapsed to an integer. FEEL numbers are
// decimals: division is exact division at the evaluator's working precision,
// and division by zero is null rather than a panic (DMN 1.5 table 51).
func (binop Binop) divOp(intp *Interpreter) (any, error) {
	return binop.scaleOp(
		intp,
		func(a, b *Number) any {
			if b.IsZero() {
				return null
			}
			return a.Div(b)
		},
		true)
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
//
// orEqual marks `<=` and `>=`, which Table 54 defines as `e1 < e2 or e1 = e2`.
// For a zoned and a zoneless operand `=` is null (see mixedZones), so the
// three-valued `or` is true when the strict comparison holds and null
// otherwise: `time("10:00:00") <= time("10:00:00Z")` is null.
func (binop Binop) orderingOp(intp *Interpreter, holds func(r int) bool, orEqual bool) (any, error) {
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
	// VERDICT DIVERGENCE: two booleans, two ranges, two lists and two contexts
	// are unordered (compareInterfaces reports codeUnordered), so `true < false`
	// and `[1..2] < [3..4]` are null. DMN 1.5 §10.3.2.15 Table 54 defines `<`,
	// `<=`, `>` and `>=` for none of those kinds, and inputs outside an
	// operator's domain are null. Equality is feelEqual's and is unaffected.
	r, err := compareInterfaces(leftVal, rightVal)
	if err != nil {
		if isIncomparable(err) || isUnordered(err) {
			return null, nil
		}
		return false, err
	}
	if orEqual && mixedZones(leftVal, rightVal) {
		if r != 0 && holds(r) {
			return true, nil
		}
		return null, nil
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
	return binop.orderingOp(intp, func(r int) bool { return r > 0 }, false)
}

func (binop Binop) compareGEOp(intp *Interpreter) (any, error) {
	return binop.orderingOp(intp, func(r int) bool { return r >= 0 }, true)
}

func (binop Binop) compareLTOp(intp *Interpreter) (any, error) {
	return binop.orderingOp(intp, func(r int) bool { return r < 0 }, false)
}

func (binop Binop) compareLEOp(intp *Interpreter) (any, error) {
	return binop.orderingOp(intp, func(r int) bool { return r <= 0 }, true)
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

// andOp and orOp implement FEEL's three-valued conjunction and disjunction
// (DMN 1.5 §10.3.2.15, Table 51). Only a boolean counts as one: null and every
// non-boolean operand are "otherwise", so `true and null` is null, `false and
// null` false, `true or 1` true and `false or "x"` null. The left operand still
// short-circuits when it alone decides the result (`false and …`, `true or …`).
//
// VERDICT DIVERGENCE: upstream coerced each operand through boolValue, which
// reads null, 0, "" and empty collections as false and anything else as true,
// so `true and null` was false and `false or 1` true.
func (binop Binop) andOp(intp *Interpreter) (any, error) {
	leftVal, err := binop.Left.Eval(intp)
	if err != nil {
		return nil, err
	}
	if b, ok := leftVal.(bool); ok && !b {
		return false, nil
	}
	rightVal, err := binop.Right.Eval(intp)
	if err != nil {
		return nil, err
	}
	rightBool, rightOK := rightVal.(bool)
	switch {
	case rightOK && !rightBool:
		return false, nil
	case rightOK && leftVal == true:
		return true, nil
	default:
		return null, nil
	}
}

func (binop Binop) orOp(intp *Interpreter) (any, error) {
	leftVal, err := binop.Left.Eval(intp)
	if err != nil {
		return nil, err
	}
	if b, ok := leftVal.(bool); ok && b {
		return true, nil
	}
	rightVal, err := binop.Right.Eval(intp)
	if err != nil {
		return nil, err
	}
	rightBool, rightOK := rightVal.(bool)
	switch {
	case rightOK && rightBool:
		return true, nil
	case rightOK && leftVal == false:
		return false, nil
	default:
		return null, nil
	}
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

// inZonedRange is `e in range` when e is a zoned time or date and time and an
// endpoint a zoneless one, or the reverse. Table 49 makes `e in [a..b]` the
// conjunction `e >= a and e <= b` (`>`/`<` for an open end), so a closed end
// is the three-valued `>=`/`<=` of orderingOp and an open one the plain `>`/`<`
// with the other operand's offset adopted; the two fold with `and` (Table 51).
func inZonedRange(e any, rv *RangeValue) any {
	bound := func(end any, open bool, sign int) any {
		r, err := compareInterfaces(e, end)
		if err != nil {
			return null
		}
		strict := r*sign > 0
		if open || strict || !mixedZones(e, end) {
			return strict || (!open && r == 0)
		}
		return null
	}
	lo, hi := bound(rv.Start, rv.StartOpen, 1), bound(rv.End, rv.EndOpen, -1)
	if lo == false || hi == false {
		return false
	}
	if lo == true && hi == true {
		return true
	}
	return null
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
	return In(leftVal, rightVal)
}

// In implements FEEL's `e in container` for a range or a list container, or
// any other value as equality, and yields true, false or null. inOp evaluates its operands and delegates here.
//
// VERDICT DIVERGENCE: upstream had no exported membership test. Verdict's
// unary-test layer (pkg/feel/unarytest.go) interprets a range or list cell as
// `? in cell` (DMN 1.5 §10.3.2.9, Table 49), and calling this rather than
// re-deriving membership keeps one implementation, so a decision-table cell
// and the `in` operator cannot disagree about a mixed-kind or unordered input.
func In(leftVal, rightVal any) (any, error) {
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
		// So does a point of an unordered kind (`true in [false..true]`):
		// DMN 1.5 Table 54 orders no booleans.
		if _, err := rv.Position(leftVal); err != nil {
			if isIncomparable(err) || isUnordered(err) {
				return null, nil
			}
			return nil, err
		}
		if mixedZones(leftVal, rv.Start) || mixedZones(leftVal, rv.End) {
			return inZonedRange(leftVal, rv), nil
		}
		return rv.Contains(leftVal), nil
	case []any:
		// VERDICT DIVERGENCE: `e in [e1, e2, …]` is `e = e1 or e = e2 or …`
		// (DMN 1.5 §10.3.2.15, Table 49), folded three-valued (Table 51): an
		// element that is `=` makes it true, failing that an element whose
		// `=` is null makes it null (`"a" in [1, 2]`), otherwise false.
		// Upstream answered a boolean from valuesEqual, so `"a" in [1, 2]`
		// was false and `not("a" in [1, 2])` true. A pair the fork cannot
		// compare at all (two functions) still counts as not equal.
		unknown := false
		for _, kv := range rv {
			r, err := feelEqual(leftVal, kv)
			if err != nil {
				continue
			}
			switch r {
			case true:
				return true, nil
			case false:
			default:
				unknown = true
			}
		}
		if unknown {
			return null, nil
		}
		return false, nil
	default:
		// VERDICT DIVERGENCE: upstream raised a bad-op error. DMN 1.5 Table
		// 55: `e1 in e2` with e2 neither a list nor a range is `e1 = e2`, so
		// `1 in 1` is true and `1 in "a"` null (TCK 0072-feel-in).
		return feelEqual(leftVal, rightVal)
	}
}
