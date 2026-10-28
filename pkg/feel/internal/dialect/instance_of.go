package dialect

import (
	"fmt"
	"strings"
)

// VERDICT DIVERGENCE: `instance of` (DMN 1.5 grammar rules 51 and 52, Table
// 61) is fork-only; upstream did not parse it. See NOTICE.md.

// Type kinds of a TypeExpr, one per alternative of grammar rule 52.
const (
	TypeKindName     = "name"
	TypeKindList     = "list"
	TypeKindRange    = "range"
	TypeKindContext  = "context"
	TypeKindFunction = "function"
)

// TypeExpr is a parsed FEEL type, grammar rule 52: a qualified name, or one of
// the parameterised types `list<T>`, `range<T>`, `context<k: T, …>` and
// `function<T, …> -> T`.
type TypeExpr struct {
	Kind string
	// Name is the qualified name of a TypeKindName.
	Name string
	// Elem is the parameter of a list or range type.
	Elem *TypeExpr
	// Keys and Fields are a context type's entries, in order.
	Keys   []string
	Fields []*TypeExpr
	// Params and Result are a function type's signature.
	Params []*TypeExpr
	Result *TypeExpr
}

// primitiveTypes maps each primitive node of the type lattice (DMN 1.5
// §10.3.2.9, Table 43 and §10.3.2.3) to the test for a non-null value of it.
var primitiveTypes = map[string]func(any) bool{
	"number": func(v any) bool {
		switch v.(type) {
		case *Number, int64, float64:
			return true
		}
		return false
	},
	"string":  func(v any) bool { _, ok := v.(string); return ok },
	"boolean": func(v any) bool { _, ok := v.(bool); return ok },
	"date":    func(v any) bool { _, ok := v.(*FEELDate); return ok },
	"time":    func(v any) bool { _, ok := v.(*FEELTime); return ok },
	"date and time": func(v any) bool {
		_, ok := v.(*FEELDatetime)
		return ok
	},
	"days and time duration": func(v any) bool {
		d, ok := v.(*FEELDuration)
		return ok && !d.IsYearsMonths()
	},
	"years and months duration": func(v any) bool {
		d, ok := v.(*FEELDuration)
		return ok && d.IsYearsMonths()
	},
}

// Repr is the type as FEEL spells it.
func (t *TypeExpr) Repr() string {
	switch t.Kind {
	case TypeKindList, TypeKindRange:
		return fmt.Sprintf("%s<%s>", t.Kind, t.Elem.Repr())
	case TypeKindContext:
		entries := make([]string, len(t.Keys))
		for i, k := range t.Keys {
			entries[i] = k + ": " + t.Fields[i].Repr()
		}
		return "context<" + strings.Join(entries, ", ") + ">"
	case TypeKindFunction:
		params := make([]string, len(t.Params))
		for i, p := range t.Params {
			params[i] = p.Repr()
		}
		return "function<" + strings.Join(params, ", ") + "> -> " + t.Result.Repr()
	default:
		return t.Name
	}
}

// mapped reports whether the type maps to a node of the type lattice (Table
// 61, first row): every name in it is a primitive type, `Any` or `Null`. Model
// itemDefinition names are not in scope for the fork, so they, a misspelt name
// and a generic name written without its parameters (`list`, `context`) are
// all unmapped.
func (t *TypeExpr) mapped() bool {
	switch t.Kind {
	case TypeKindList, TypeKindRange:
		return t.Elem.mapped()
	case TypeKindContext:
		for _, f := range t.Fields {
			if !f.mapped() {
				return false
			}
		}
		return true
	case TypeKindFunction:
		for _, p := range t.Params {
			if !p.mapped() {
				return false
			}
		}
		return t.Result.mapped()
	default:
		if t.Name == "Any" || t.Name == "Null" {
			return true
		}
		_, ok := primitiveTypes[t.Name]
		return ok
	}
}

// InstanceOf is `e instance of type`, grammar rule 51.
type InstanceOf struct {
	Expr Node
	Type *TypeExpr

	textRange TextRange
}

func (io InstanceOf) TextRange() TextRange {
	return io.textRange
}

func (io InstanceOf) Repr() string {
	return fmt.Sprintf("(instance-of %s %s)", io.Expr.Repr(), io.Type.Repr())
}

// Eval applies DMN 1.5 Table 61: null when the type maps to no lattice node;
// true for a null value checked against `Null`; true when the value is not
// null and its type conforms to the type (§10.3.2.9.2); false otherwise. So
// `null instance of number` and `null instance of Any` are false.
func (io InstanceOf) Eval(intp *Interpreter) (any, error) {
	v, err := io.Expr.Eval(intp)
	if err != nil {
		return nil, err
	}
	if !io.Type.mapped() {
		return null, nil
	}
	if io.Type.Kind == TypeKindName && io.Type.Name == "Null" {
		return isNullValue(v), nil
	}
	if isNullValue(v) {
		return false, nil
	}
	return conformsTo(intp, v, io.Type)
}

// conformsTo reports whether the type of v conforms to t (§10.3.2.9.2). A null
// inside a value — a list element, a context entry, a range endpoint — has type
// Null, which conforms to every type, so `[null] instance of list<number>` and
// `{a: null} instance of context<a: string>` are true. A list's type is the
// least upper bound of its elements' types, so it conforms to list<T> exactly
// when every element does; an empty list conforms to every list type. Checking
// a list's elements charges the iteration budget.
func conformsTo(intp *Interpreter, v any, t *TypeExpr) (bool, error) {
	if isNullValue(v) {
		return true, nil
	}
	switch t.Kind {
	case TypeKindList:
		list, ok := v.([]any)
		if !ok {
			return false, nil
		}
		if err := intp.chargeIterations(uint64(len(list))); err != nil {
			return false, err
		}
		for _, elem := range list {
			if ok, err := conformsTo(intp, elem, t.Elem); !ok || err != nil {
				return false, err
			}
		}
		return true, nil
	case TypeKindRange:
		var rv *RangeValue
		switch r := v.(type) {
		case *RangeValue:
			rv = r
		case RangeValue:
			rv = &r
		default:
			return false, nil
		}
		for _, end := range []any{rv.Start, rv.End} {
			if ok, err := conformsTo(intp, end, t.Elem); !ok || err != nil {
				return false, err
			}
		}
		return true, nil
	case TypeKindContext:
		var ctx map[string]any
		switch m := v.(type) {
		case map[string]any:
			ctx = m
		case Scope:
			ctx = m
		default:
			return false, nil
		}
		for i, key := range t.Keys {
			entry, present := ctx[key]
			if !present {
				return false, nil
			}
			if ok, err := conformsTo(intp, entry, t.Fields[i]); !ok || err != nil {
				return false, err
			}
		}
		return true, nil
	case TypeKindFunction:
		if !acceptsArity(v, len(t.Params)) {
			return false, nil
		}
		if fd, ok := v.(*FunDef); ok && fd.ParamTypes != nil {
			// Contravariance: each Si of the type must conform to the
			// declared Ti of the parameter, so the function accepts every
			// argument the type promises it.
			for i, s := range t.Params {
				if !typeConforms(s, fd.ParamTypes[i]) {
					return false, nil
				}
			}
		}
		return true, nil
	default:
		switch t.Name {
		case "Any":
			return true, nil
		case "Null":
			return false, nil
		}
		return primitiveTypes[t.Name](v), nil
	}
}

// acceptsArity reports whether v is a function that can be invoked with n
// positional arguments. With typeConforms over a FunDef's declared parameter
// types it is the function-type check (§10.3.2.9.2). A parameter declared
// without a type is Any (§10.3.2.13.2), to which every Si of the type
// conforms, as contravariance requires; a built-in's parameters are taken as
// Any too. A FEEL function definition declares no result type (rule 55), so
// its result type is not known without evaluating it. Interpretation: an
// unknown result type is taken to conform.
func acceptsArity(v any, n int) bool {
	switch f := v.(type) {
	case *FunDef:
		return len(f.Args) == n
	case *NativeFun:
		if len(f.overloads) > 0 {
			for _, sig := range f.overloads {
				if len(sig) == n {
					return true
				}
			}
			return false
		}
		return arityFits(n, len(f.requiredArgNames), len(f.optionalArgNames), f.varArgName != "")
	case *Macro:
		return arityFits(n, len(f.requiredArgNames), len(f.optionalArgNames), f.varArgName != "")
	}
	return false
}

func arityFits(n, required, optional int, vararg bool) bool {
	if n < required {
		return false
	}
	return vararg || n <= required+optional
}

// declaredType is a parameter's declared type for checking: Any when none was
// declared (§10.3.2.13.2) or when it maps to no lattice node.
//
// Interpretation: model itemDefinition names are not in scope for the fork,
// so a parameter typed with one — or with a misspelt name — is not checked
// rather than making every invocation null.
func declaredType(t *TypeExpr) *TypeExpr {
	if t == nil || !t.mapped() {
		return anyType
	}
	return t
}

var anyType = &TypeExpr{Kind: TypeKindName, Name: "Any"}

// typeConforms reports whether type s conforms to type t, s <: t, by the
// rules of DMN 1.5 §10.3.2.9.2: equivalence; Null <: T <: Any; list and range
// types by their element type; a context type with at least t's entries, each
// conforming; and a function type of the same arity whose parameter types are
// contravariant and result type covariant. A type that maps to no lattice
// node is taken as Any (see declaredType).
func typeConforms(s, t *TypeExpr) bool {
	s, t = declaredType(s), declaredType(t)
	if t.Kind == TypeKindName && t.Name == "Any" {
		return true
	}
	if s.Kind == TypeKindName && s.Name == "Null" {
		return true
	}
	if s.Kind != t.Kind {
		return false
	}
	switch t.Kind {
	case TypeKindList, TypeKindRange:
		return typeConforms(s.Elem, t.Elem)
	case TypeKindContext:
		for i, key := range t.Keys {
			found := false
			for j, sk := range s.Keys {
				if sk == key {
					if !typeConforms(s.Fields[j], t.Fields[i]) {
						return false
					}
					found = true
					break
				}
			}
			if !found {
				return false
			}
		}
		return true
	case TypeKindFunction:
		if len(s.Params) != len(t.Params) {
			return false
		}
		for i := range t.Params {
			if !typeConforms(t.Params[i], s.Params[i]) {
				return false
			}
		}
		return typeConforms(s.Result, t.Result)
	default:
		return s.Name == t.Name
	}
}

// conformFrame checks each bound argument in frame against its parameter's
// declared type, replacing it with its implicitly converted value where the
// conversion is what makes it conform. ok is false when an argument does not
// conform: DMN 1.5 §10.3.2.13.2, "The arguments ... conform to the argument
// list [X1, ... Xn] if type(ei) conforms to ti before or after applying
// implicit conversions or ti is not specified in Xi ... if the arguments do
// not conform to the argument list, the result of the invocation is null."
func (fd *FunDef) conformFrame(intp *Interpreter, frame Scope) (bool, error) {
	for i, t := range fd.ParamTypes {
		if t == nil || !t.mapped() {
			continue
		}
		name := fd.Args[i]
		v, ok, err := conformArg(intp, frame[name], t)
		if !ok || err != nil {
			return false, err
		}
		frame[name] = v
	}
	return true, nil
}

// conformArg checks v against the mapped type t, first as it is and then
// after each implicit conversion of §10.3.2.9.4 that can apply: to a
// singleton list where t is a list type, from a singleton list where it is
// not, and from a date to the date and time at its UTC midnight where t is
// date and time. It returns the value to bind: v itself, or its conversion.
func conformArg(intp *Interpreter, v any, t *TypeExpr) (any, bool, error) {
	if ok, err := conformsTo(intp, v, t); ok || err != nil {
		return v, ok, err
	}
	var converted any
	switch {
	case t.Kind == TypeKindList:
		if _, isList := v.([]any); isList {
			return nil, false, nil
		}
		converted = []any{v}
	case t.Kind == TypeKindName && t.Name == "date and time":
		d, isDate := v.(*FEELDate)
		if !isDate {
			return nil, false, nil
		}
		converted = &FEELDatetime{t: d.utcMidnight(), hasOffset: true}
	default:
		list, isList := v.([]any)
		if !isList || len(list) != 1 {
			return nil, false, nil
		}
		converted = list[0]
	}
	ok, err := conformsTo(intp, converted, t)
	if !ok || err != nil {
		return nil, false, err
	}
	return converted, true, nil
}
