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

func (rv RangeValue) BeforePoint(p any) (bool, error) {
	pos, err := rv.Position(p)
	if err != nil {
		return false, err
	}
	return pos > 0, nil

}

func (rv RangeValue) AfterPoint(p any) (bool, error) {
	pos, err := rv.Position(p)
	if err != nil {
		return false, err
	}
	return pos < 0, nil
}

func (rv RangeValue) BeforeRange(other RangeValue) (bool, error) {
	r, err := compareInterfaces(rv.End, other.Start)
	if err != nil {
		return false, err
	}

	if !rv.EndOpen && !other.StartOpen {
		// two ranges meet
		return r < 0, nil
	} else {
		return r <= 0, nil
	}
}

func (rv RangeValue) AfterRange(other RangeValue) (bool, error) {
	r, err := compareInterfaces(rv.Start, other.End)
	if err != nil {
		return false, err
	}
	if !rv.StartOpen && !other.EndOpen {
		return r > 0, nil
	} else {
		return r >= 0, nil
	}
}

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

func (rv RangeValue) Includes(other RangeValue) (bool, error) {
	cmpStart, err := compareInterfaces(rv.Start, other.Start)
	if err != nil {
		return false, err
	}
	cmpEnd, err := compareInterfaces(rv.End, other.End)
	if err != nil {
		return false, err
	}
	if cmpStart > 0 || cmpEnd < 0 {
		return false, nil
	}

	if !(cmpStart < 0 || !rv.StartOpen || other.StartOpen) {
		return false, nil
	}

	if !(cmpEnd > 0 || !rv.EndOpen || other.EndOpen) {
		return false, nil
	}
	return true, nil
}

func (rv RangeValue) Contains(p any) bool {
	r, err := rv.Position(p)
	if err != nil {
		panic(err)
	}
	return r == 0
}

func (rv RangeValue) overlapsBefore(other RangeValue) (bool, error) {
	pos, err := other.Position(rv.End)
	if err != nil {
		return false, err
	}
	if pos != 0 {
		return false, nil
	} else if rv.EndOpen && CompareValues(rv.End, other.Start) == 0 {
		return false, nil
	} else {
		return true, nil
	}
}

func (rv RangeValue) overlapsAfter(other RangeValue) (bool, error) {
	pos, err := other.Position(rv.Start)
	if err != nil {
		return false, err
	}
	if pos != 0 {
		return false, nil
	} else if rv.EndOpen && CompareValues(rv.Start, other.End) == 0 {
		return false, nil
	} else {
		return true, nil
	}
}

func installRangeFunctions(p *Prelude) {
	prelude := rangePrelude{p}
	prelude.Bind("before", NewNativeFunc(func(kwargs map[string]any) (any, error) {
		a := kwargs["a"]
		b := kwargs["b"]
		switch va := a.(type) {
		case *RangeValue:
			switch vb := b.(type) {
			case *RangeValue:
				return va.BeforeRange(*vb)
			default:
				return va.BeforePoint(vb)
			}
		default:
			switch vb := b.(type) {
			case *RangeValue:
				return vb.AfterPoint(va)
			default:
				cmp, err := compareInterfaces(va, vb)
				if err != nil {
					return nil, err
				} else {
					return cmp < 0, nil
				}
			}
		}
	}).Required("a", "b"))

	prelude.Bind("after", NewNativeFunc(func(kwargs map[string]any) (any, error) {
		a := kwargs["a"]
		b := kwargs["b"]
		switch va := a.(type) {
		case *RangeValue:
			switch vb := b.(type) {
			case *RangeValue:
				return va.AfterRange(*vb)
			default:
				return va.AfterPoint(vb)
			}
		default:
			switch vb := b.(type) {
			case *RangeValue:
				return vb.BeforePoint(va)
			default:
				cmp, err := compareInterfaces(va, vb)
				if err != nil {
					return nil, err
				} else {
					return cmp > 0, nil
				}
			}
		}
	}).Required("a", "b"))

	prelude.Bind("meets", wrapTyped(func(a *RangeValue, b *RangeValue) (bool, error) {
		r, err := compareInterfaces(a.End, b.Start)
		if err != nil {
			return false, err
		}
		return !a.EndOpen && !b.StartOpen && r == 0, nil
	}).Required("a", "b"))

	prelude.Bind("met by", wrapTyped(func(a *RangeValue, b *RangeValue) (bool, error) {
		r, err := compareInterfaces(a.Start, b.End)
		if err != nil {
			return false, err
		}
		return !b.EndOpen && !a.StartOpen && r == 0, nil
	}).Required("a", "b"))

	prelude.Bind("overlaps", wrapTyped(func(a *RangeValue, b *RangeValue) (bool, error) {
		obefore, err := a.overlapsBefore(*b)
		if err != nil {
			return false, err
		} else if obefore {
			return true, nil
		}
		oafter, err := a.overlapsAfter(*b)
		if err != nil {
			return false, err
		} else if oafter {
			return true, nil
		}
		return false, nil
	}).Required("a", "b"))

	prelude.Bind("overlaps before", wrapTyped(func(a *RangeValue, b *RangeValue) (bool, error) {
		return a.overlapsBefore(*b)
	}).Required("a", "b"))

	prelude.Bind("overlaps after", wrapTyped(func(a *RangeValue, b *RangeValue) (bool, error) {
		return a.overlapsAfter(*b)
	}).Required("a", "b"))

	prelude.Bind("finishes", wrapTyped(func(a any, b *RangeValue) (bool, error) {
		switch va := a.(type) {
		case *RangeValue:
			r, err := compareInterfaces(va.End, b.End)
			if err != nil {
				return false, err
			}
			return r == 0 && !b.EndOpen, nil
		default:
			r, err := compareInterfaces(a, b.End)
			if err != nil {
				return false, err
			}
			return r == 0 && !b.EndOpen, nil
		}
	}).Required("a", "b"))

	prelude.Bind("starts", wrapTyped(func(a any, b *RangeValue) (bool, error) {
		switch va := a.(type) {
		case *RangeValue:
			r, err := compareInterfaces(va.Start, b.Start)
			if err != nil {
				return false, err
			}
			if r != 0 {
				return false, nil
			}
			if va.StartOpen && !b.StartOpen {
				return false, nil
			}
			if !va.StartOpen && b.StartOpen {
				return false, nil
			}
			return true, nil
		default:
			r, err := compareInterfaces(a, b.Start)
			if err != nil {
				return false, err
			}
			return r == 0 && !b.StartOpen, nil
		}
	}).Required("a", "b"))

	prelude.Bind("finished by", wrapTyped(func(a *RangeValue, b any) (bool, error) {
		switch vb := b.(type) {
		case *RangeValue:
			r, err := compareInterfaces(vb.End, a.End)
			if err != nil {
				return false, err
			}

			return r == 0 && !a.EndOpen, nil
		default:
			r, err := compareInterfaces(b, a.End)
			if err != nil {
				return false, err
			}
			return r == 0 && !a.EndOpen, nil
		}
	}).Required("a", "b"))

	prelude.Bind("started by", wrapTyped(func(a *RangeValue, b any) (bool, error) {
		switch vb := b.(type) {
		case *RangeValue:
			r, err := compareInterfaces(vb.Start, a.Start)
			if err != nil {
				return false, err
			}
			if r != 0 {
				return false, nil
			}
			if vb.StartOpen && !a.StartOpen {
				return false, nil
			}
			if !vb.StartOpen && a.StartOpen {
				return false, nil
			}
			return true, nil
		default:
			r, err := compareInterfaces(b, a.Start)
			if err != nil {
				return false, err
			}
			return r == 0 && !a.StartOpen, nil
		}
	}).Required("a", "b"))

	prelude.Bind("includes", wrapTyped(func(a *RangeValue, b any) (bool, error) {
		switch vb := b.(type) {
		case *RangeValue:
			return a.Includes(*vb)
		default:
			r, err := a.Position(vb)
			if err != nil {
				return false, err
			}
			return r == 0, nil
		}
	}).Required("a", "b"))

	prelude.Bind("during", wrapTyped(func(a any, b *RangeValue) (bool, error) {
		switch va := a.(type) {
		case *RangeValue:
			return b.Includes(*va)
		default:
			r, err := b.Position(va)
			if err != nil {
				return false, err
			}
			return r == 0, nil
		}
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
			cmpStart, err := compareInterfaces(a.Start, b.Start)
			if err != nil {
				return nil, err
			}
			cmpEnd, err := compareInterfaces(a.End, b.End)
			if err != nil {
				return nil, err
			}
			return cmpStart == 0 && cmpEnd == 0, nil
		case aRange || bRange:
			return null, nil
		default:
			r, err := compareInterfaces(kwargs["a"], kwargs["b"])
			if err != nil {
				return nil, err
			}
			return r == 0, nil
		}
	}).Required("a", "b"))

}

// rangePrelude binds every range built-in through nullOnNullOperand, so a new
// built-in added to installRangeFunctions cannot skip the null-operand rule.
type rangePrelude struct{ p *Prelude }

func (rp rangePrelude) Bind(name string, nf *NativeFun) {
	rp.p.Bind(name, nullOnNullOperand(nf))
}
