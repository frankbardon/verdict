package dialect

type RangeValue struct {
	StartOpen bool
	Start     any

	EndOpen bool
	End     any
}

// HasNullEndpoint reports whether either endpoint is null. A literal such as
// `[null..10]` has no defined start, and DMN 1.5 §10.3.2.15 makes every ordering
// comparison against it null, so membership in it, and every range built-in
// over it, is null rather than an answer computed from upstream's ordering of
// null below every value. The fork builds ranges only from range literals
// (`< 10` is a comparison, not a range), so a null endpoint here always came
// from the model, never from an internal encoding of an unbounded interval.
func (rv RangeValue) HasNullEndpoint() bool {
	return isNullValue(rv.Start) || isNullValue(rv.End)
}

// nullOnNullOperand makes a range built-in yield null when its `a` or `b`
// argument is null, or is a range with a null endpoint (see HasNullEndpoint),
// or when a comparison inside it pairs values of different kinds.
// DMN 1.5 §10.3.4.7 defines each range function by ordering comparisons between
// points and endpoints, and says a built-in given a parameter outside its
// domain yields null; null is not a point. Upstream ordered null below every
// value, so `before(null, [1..10])` was false and `includes([1..10], null)`
// false, while a null where a range was expected raised a type error.
func nullOnNullOperand(nf *NativeFun) *NativeFun {
	inner := nf.fn
	nf.fn = func(args map[string]any) (any, error) {
		for _, name := range []string{"a", "b"} {
			if isNullValue(args[name]) {
				return null, nil
			}
			if rv, ok := args[name].(*RangeValue); ok && rv.HasNullEndpoint() {
				return null, nil
			}
		}
		v, err := inner(args)
		if err != nil && (isIncomparable(err) || isUnordered(err)) {
			// A point or endpoint of another kind than its counterpart
			// (`before(1, "a")`), or of a kind with no order
			// (`before(false, true)`, DMN 1.5 Table 54), is outside the
			// built-in's domain.
			return null, nil
		}
		return v, err
	}
	return nf
}

// Position and Contains are the `in` operator's (see In); the range built-ins
// use the three-valued within instead.
func (rv RangeValue) Position(p any) (int, error) {
	cmpStart, err := compareInterfaces(p, rv.Start)
	if err != nil {
		return 0, err
	}
	if rv.StartOpen {
		if cmpStart <= 0 {
			return -1, nil
		}
	} else {
		if cmpStart == 0 {
			return 0, nil
		} else if cmpStart < 0 {
			return -1, nil
		}
	}

	cmpEnd, err := compareInterfaces(p, rv.End)
	if err != nil {
		return 0, err
	}
	if rv.EndOpen && cmpEnd >= 0 {
		return 1, nil
	} else if !rv.EndOpen && cmpEnd > 0 {
		return 1, nil
	}
	return 0, nil

}

func (rv RangeValue) Contains(p any) bool {
	r, err := rv.Position(p)
	if err != nil {
		panic(err)
	}
	return r == 0
}

// The range built-ins below are written as three-valued formulas over the
// comparisons between points and endpoints, folded with Table 51's `and`,
// `or` and `not`, rather than as Go booleans over compareInterfaces' sign.
//
// VERDICT DIVERGENCE: upstream (and earlier Verdict) read every comparison
// from compareInterfaces, which orders a zoned and a zoneless time or date and
// time by adopting the zoned operand's offset, so `meets`, `coincides`,
// `starts` and the rest answered true or false for a tie that `=` calls null.
// DMN 1.5 §10.3.4.7 (Table 78) defines each range built-in by `<`, `>`, `=`,
// `<=` and `>=` between points and endpoints, so each inherits those
// operators' rule for mixed operands (see mixedZones): `<` and `>` adopt the
// zoned offset, `=` is null, and `<=`/`>=` are true when the strict
// comparison is and null otherwise. For operands that are not mixed every
// comparison is a boolean and each formula gives the answer it gave before.

// rangeCmp is a three-valued `<`, `<=`, `=`, `>=` or `>` between a and b, as
// the FEEL operators compute it: true, false or null. The error is
// compareInterfaces' (nullOnNullOperand turns a kind mismatch into null).
func rangeCmp(a any, op string, b any) (any, error) {
	r, err := compareInterfaces(a, b)
	if err != nil {
		return nil, err
	}
	mixed := mixedZones(a, b)
	switch op {
	case "<":
		return r < 0, nil
	case ">":
		return r > 0, nil
	case "=":
		if mixed {
			return null, nil
		}
		return r == 0, nil
	case "<=":
		if mixed && r >= 0 {
			return null, nil
		}
		return r <= 0, nil
	case ">=":
		if mixed && r <= 0 {
			return null, nil
		}
		return r >= 0, nil
	}
	panic("rangeCmp: unknown operator " + op)
}

// and3 is FEEL's `and` over true, false and null (DMN 1.5 Table 51): false if
// any operand is false, otherwise null if any is null, otherwise true.
func and3(vs ...any) any {
	unknown := false
	for _, v := range vs {
		switch v {
		case false:
			return false
		case true:
		default:
			unknown = true
		}
	}
	if unknown {
		return null
	}
	return true
}

// or3 is FEEL's `or` (Table 51): true if any operand is true, otherwise null
// if any is null, otherwise false.
func or3(vs ...any) any {
	unknown := false
	for _, v := range vs {
		switch v {
		case true:
			return true
		case false:
		default:
			unknown = true
		}
	}
	if unknown {
		return null
	}
	return false
}

// cmps evaluates several rangeCmp comparisons eagerly, so that a kind mismatch
// in any of them reaches nullOnNullOperand whatever the formula would have
// short-circuited, as it did when every comparison ran up front.
type cmps struct{ err error }

func (c *cmps) cmp(a any, op string, b any) any {
	if c.err != nil {
		return null
	}
	v, err := rangeCmp(a, op, b)
	if err != nil {
		c.err = err
		return null
	}
	return v
}

// lower is p's comparison with rv's start for membership: `p > start` for an
// open start, `p >= start` for a closed one.
func (rv RangeValue) lower(c *cmps, p any) any {
	if rv.StartOpen {
		return c.cmp(p, ">", rv.Start)
	}
	return c.cmp(p, ">=", rv.Start)
}

// upper is p's comparison with rv's end for membership.
func (rv RangeValue) upper(c *cmps, p any) any {
	if rv.EndOpen {
		return c.cmp(p, "<", rv.End)
	}
	return c.cmp(p, "<=", rv.End)
}

// within is `p in rv`: the conjunction of the endpoint comparisons (Table 49).
func (rv RangeValue) within(c *cmps, p any) any {
	return and3(rv.lower(c, p), rv.upper(c, p))
}

// beforePoint is `before(rv, p)`: Table 78's `range.end < point or
// (range.end = point and not(range.end included))`, which is `p >= end` for
// an open end and `p > end` for a closed one.
func (rv RangeValue) beforePoint(c *cmps, p any) any {
	if rv.EndOpen {
		return c.cmp(p, ">=", rv.End)
	}
	return c.cmp(p, ">", rv.End)
}

// afterPoint is `after(rv, p)`: `p <= start` for an open start, `p < start`
// for a closed one.
func (rv RangeValue) afterPoint(c *cmps, p any) any {
	if rv.StartOpen {
		return c.cmp(p, "<=", rv.Start)
	}
	return c.cmp(p, "<", rv.Start)
}

// beforeRange is `before(rv, other)`: rv's end is short of other's start —
// strictly when both meet them closed.
func (rv RangeValue) beforeRange(c *cmps, other RangeValue) any {
	if !rv.EndOpen && !other.StartOpen {
		return c.cmp(rv.End, "<", other.Start)
	}
	return c.cmp(rv.End, "<=", other.Start)
}

// afterRange is `after(rv, other)`.
func (rv RangeValue) afterRange(c *cmps, other RangeValue) any {
	if !rv.StartOpen && !other.EndOpen {
		return c.cmp(rv.Start, ">", other.End)
	}
	return c.cmp(rv.Start, ">=", other.End)
}

// includesRange is `includes(rv, other)`: other's start is at or after rv's
// (equal starts need rv's closed or other's open), and likewise at the end.
func (rv RangeValue) includesRange(c *cmps, other RangeValue) any {
	startOK := or3(c.cmp(rv.Start, "<", other.Start),
		and3(c.cmp(rv.Start, "=", other.Start), !rv.StartOpen || other.StartOpen))
	endOK := or3(c.cmp(rv.End, ">", other.End),
		and3(c.cmp(rv.End, "=", other.End), !rv.EndOpen || other.EndOpen))
	return and3(startOK, endOK)
}

// The range–range relations below are Table 78's formulas, conjunct for
// conjunct, over the three-valued comparisons. A literal closedness test
// (`range1.end included`) is a Go boolean, and `=` between two closednesses
// is plain boolean equality.
//
// VERDICT DIVERGENCE: upstream tested only whether one range's endpoint lay
// in the other, so `overlaps([1..10], [3..4])` was false, `overlaps
// before([3..4], [1..5])` true and `overlaps after((5..10], [1..5])` true,
// and `starts`/`started by`/`finishes`/`finished by` over two ranges compared
// only their shared endpoint (`finishes([1..10], [5..10])` was true). DMN 1.5
// §10.3.4.7 Table 78 rows (b) of each function give the formulas used here
// (issues #63, #64).

// startsEarlier is Table 78's `r.start < other.start or (r.start =
// other.start and r.start included and not(other.start included))`.
func (rv RangeValue) startsEarlier(c *cmps, other RangeValue) any {
	return or3(c.cmp(rv.Start, "<", other.Start),
		and3(c.cmp(rv.Start, "=", other.Start), !rv.StartOpen && other.StartOpen))
}

// endsNoLater is Table 78's `r.end < other.end or (r.end = other.end and
// (not(r.end included) or other.end included))`.
func (rv RangeValue) endsNoLater(c *cmps, other RangeValue) any {
	return or3(c.cmp(rv.End, "<", other.End),
		and3(c.cmp(rv.End, "=", other.End), rv.EndOpen || !other.EndOpen))
}

// reachesStartOf is Table 78's `r.end > other.start or (r.end = other.start
// and r.end included and other.start included)`.
func (rv RangeValue) reachesStartOf(c *cmps, other RangeValue) any {
	return or3(c.cmp(rv.End, ">", other.Start),
		and3(c.cmp(rv.End, "=", other.Start), !rv.EndOpen && !other.StartOpen))
}

// overlapsRange is Table 78's `overlaps(range1, range2)`: range1 reaches
// range2's start and range2 reaches range1's start.
func (rv RangeValue) overlapsRange(c *cmps, other RangeValue) any {
	return and3(rv.reachesStartOf(c, other), other.reachesStartOf(c, rv))
}

// overlapsBefore is Table 78's `overlaps before(range1, range2)`.
func (rv RangeValue) overlapsBefore(c *cmps, other RangeValue) any {
	return and3(rv.startsEarlier(c, other), rv.reachesStartOf(c, other), rv.endsNoLater(c, other))
}

// overlapsAfter is Table 78's `overlaps after(range1, range2)`, which is
// `overlaps before(range2, range1)` term for term.
func (rv RangeValue) overlapsAfter(c *cmps, other RangeValue) any {
	return other.overlapsBefore(c, rv)
}

// startsRange is Table 78's `starts(range1, range2)`: equal starts of equal
// closedness, and range1 ends no later than range2.
func (rv RangeValue) startsRange(c *cmps, other RangeValue) any {
	return and3(c.cmp(rv.Start, "=", other.Start), rv.StartOpen == other.StartOpen,
		rv.endsNoLater(c, other))
}

// finishesRange is Table 78's `finishes(range1, range2)`: equal ends of equal
// closedness, and range1 starts no earlier than range2 — `range1.start >
// range2.start or (= and (not(range1.start included) or range2.start
// included))`. `finished by(range1, range2)` is `finishes(range2, range1)`
// term for term, as `started by` is `starts` swapped.
func (rv RangeValue) finishesRange(c *cmps, other RangeValue) any {
	return and3(rv.EndOpen == other.EndOpen, c.cmp(rv.End, "=", other.End),
		or3(c.cmp(rv.Start, ">", other.Start),
			and3(c.cmp(rv.Start, "=", other.Start), rv.StartOpen || !other.StartOpen)))
}

// rangeFn adapts a three-valued formula to a built-in's return, letting a
// comparison error through to nullOnNullOperand.
func rangeFn(f func(c *cmps) any) (any, error) {
	c := &cmps{}
	v := f(c)
	if c.err != nil {
		return nil, c.err
	}
	return v, nil
}

func installRangeFunctions(p *Prelude) {
	prelude := rangePrelude{p}
	prelude.Bind("before", NewNativeFunc(func(kwargs map[string]any) (any, error) {
		a, b := kwargs["a"], kwargs["b"]
		return rangeFn(func(c *cmps) any {
			ra, aRange := a.(*RangeValue)
			rb, bRange := b.(*RangeValue)
			switch {
			case aRange && bRange:
				return ra.beforeRange(c, *rb)
			case aRange:
				return ra.beforePoint(c, b)
			case bRange:
				return rb.afterPoint(c, a)
			default:
				return c.cmp(a, "<", b)
			}
		})
	}).Required("a", "b"))

	prelude.Bind("after", NewNativeFunc(func(kwargs map[string]any) (any, error) {
		a, b := kwargs["a"], kwargs["b"]
		return rangeFn(func(c *cmps) any {
			ra, aRange := a.(*RangeValue)
			rb, bRange := b.(*RangeValue)
			switch {
			case aRange && bRange:
				return ra.afterRange(c, *rb)
			case aRange:
				return ra.afterPoint(c, b)
			case bRange:
				return rb.beforePoint(c, a)
			default:
				return c.cmp(a, ">", b)
			}
		})
	}).Required("a", "b"))

	prelude.Bind("meets", wrapTyped(func(a *RangeValue, b *RangeValue) (any, error) {
		return rangeFn(func(c *cmps) any {
			return and3(c.cmp(a.End, "=", b.Start), !a.EndOpen, !b.StartOpen)
		})
	}).Required("a", "b"))

	prelude.Bind("met by", wrapTyped(func(a *RangeValue, b *RangeValue) (any, error) {
		return rangeFn(func(c *cmps) any {
			return and3(c.cmp(a.Start, "=", b.End), !b.EndOpen, !a.StartOpen)
		})
	}).Required("a", "b"))

	prelude.Bind("overlaps", wrapTyped(func(a *RangeValue, b *RangeValue) (any, error) {
		return rangeFn(func(c *cmps) any {
			return a.overlapsRange(c, *b)
		})
	}).Required("a", "b"))

	prelude.Bind("overlaps before", wrapTyped(func(a *RangeValue, b *RangeValue) (any, error) {
		return rangeFn(func(c *cmps) any { return a.overlapsBefore(c, *b) })
	}).Required("a", "b"))

	prelude.Bind("overlaps after", wrapTyped(func(a *RangeValue, b *RangeValue) (any, error) {
		return rangeFn(func(c *cmps) any { return a.overlapsAfter(c, *b) })
	}).Required("a", "b"))

	prelude.Bind("finishes", wrapTyped(func(a any, b *RangeValue) (any, error) {
		return rangeFn(func(c *cmps) any {
			if va, ok := a.(*RangeValue); ok {
				return va.finishesRange(c, *b)
			}
			return and3(c.cmp(a, "=", b.End), !b.EndOpen)
		})
	}).Required("a", "b"))

	prelude.Bind("starts", wrapTyped(func(a any, b *RangeValue) (any, error) {
		return rangeFn(func(c *cmps) any {
			if va, ok := a.(*RangeValue); ok {
				return va.startsRange(c, *b)
			}
			return and3(c.cmp(a, "=", b.Start), !b.StartOpen)
		})
	}).Required("a", "b"))

	prelude.Bind("finished by", wrapTyped(func(a *RangeValue, b any) (any, error) {
		return rangeFn(func(c *cmps) any {
			if vb, ok := b.(*RangeValue); ok {
				return vb.finishesRange(c, *a)
			}
			return and3(c.cmp(b, "=", a.End), !a.EndOpen)
		})
	}).Required("a", "b"))

	prelude.Bind("started by", wrapTyped(func(a *RangeValue, b any) (any, error) {
		return rangeFn(func(c *cmps) any {
			if vb, ok := b.(*RangeValue); ok {
				return vb.startsRange(c, *a)
			}
			return and3(c.cmp(b, "=", a.Start), !a.StartOpen)
		})
	}).Required("a", "b"))

	prelude.Bind("includes", wrapTyped(func(a *RangeValue, b any) (any, error) {
		return rangeFn(func(c *cmps) any {
			if vb, ok := b.(*RangeValue); ok {
				return a.includesRange(c, *vb)
			}
			return a.within(c, b)
		})
	}).Required("a", "b"))

	prelude.Bind("during", wrapTyped(func(a any, b *RangeValue) (any, error) {
		return rangeFn(func(c *cmps) any {
			if va, ok := a.(*RangeValue); ok {
				return b.includesRange(c, *va)
			}
			return b.within(c, a)
		})
	}).Required("a", "b"))

	// coincides dispatches on the argument types rather than decoding them
	// into range structs: upstream's mapstructure decode turned a number (a
	// struct) into a zero RangeValue, so `coincides(1, 1)` compared two nil
	// endpoints and raised a type mismatch. DMN 1.5 §10.3.4.7 defines two
	// signatures, (point, point) as `point1 = point2` and (range, range) as
	// equal endpoints and equal closedness; a point paired with a range is
	// outside both and yields null.
	prelude.Bind("coincides", NewNativeFunc(func(kwargs map[string]any) (any, error) {
		a, aRange := kwargs["a"].(*RangeValue)
		b, bRange := kwargs["b"].(*RangeValue)
		switch {
		case aRange && bRange:
			if a.StartOpen != b.StartOpen || a.EndOpen != b.EndOpen {
				return false, nil
			}
			return rangeFn(func(c *cmps) any {
				return and3(c.cmp(a.Start, "=", b.Start), c.cmp(a.End, "=", b.End))
			})
		case aRange || bRange:
			return null, nil
		default:
			return rangeFn(func(c *cmps) any { return c.cmp(kwargs["a"], "=", kwargs["b"]) })
		}
	}).Required("a", "b"))

}

// rangePrelude binds every range built-in through nullOnNullOperand, so a new
// built-in added to installRangeFunctions cannot skip the null-operand rule.
type rangePrelude struct{ p *Prelude }

func (rp rangePrelude) Bind(name string, nf *NativeFun) {
	rp.p.Bind(name, nullOnNullOperand(nf))
}
