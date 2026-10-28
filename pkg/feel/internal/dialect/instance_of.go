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
		return acceptsArity(v, len(t.Params)), nil
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
// positional arguments. It is the whole of the function-type check: a FEEL
// function definition declares no parameter types the fork keeps (grammar
// rule 56's `: type` is not parsed) and no result type at all (rule 55), so
// its parameters are Any — to which every Si of the type conforms, as
// contravariance requires — and its result type is not known without
// evaluating it. Interpretation: an unknown result type is taken to conform.
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
