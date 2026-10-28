package dialect

import (
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"reflect"
	"sync/atomic"
)

// values
var null = &NullValue{}

type NullValue struct {
}

func (self NullValue) Equal(other NullValue) bool {
	return true
}

func (self NullValue) MarshalJSON() ([]byte, error) {
	return json.Marshal(nil)
}

func boolValue(condVal any) bool {
	switch v := condVal.(type) {
	case int64:
		return v != 0
	case float64:
		return v != 0.0
	case *Number:
		return !v.Equal(*Zero)
	case bool:
		return v
	case string:
		return v != ""
	case []any:
		return v != nil && len(v) > 0
	case map[string]any:
		return v != nil && len(v) > 0
	default:
		return v != nil
	}
}

func typeName(a any) string {
	switch a.(type) {
	case int64:
		return "number"
	case float64:
		return "number"
	case *Number:
		return "number"
	case bool:
		return "bool"
	case string:
		return "string"
	case []any:
		return "list"
	case map[string]any:
		return "context"
	case *NullValue:
		return "null"
	case *FEELDate:
		return "date"
	case *FEELTime:
		return "time"
	case *FEELDatetime:
		return "datetime"
	case *FEELDuration:
		return "duration"
	case *RangeValue:
		return "range"
	case *NativeFun:
		return "function"
	case *FunDef:
		return "function"
	case *Macro:
		return "function"
	default:
		return "unknown"
	}
}

func normalizeValue(v any) any {
	if v == nil {
		return null
	}
	switch vv := v.(type) {
	case int:
		return NewNumberFromInt64(int64(vv))
	case int64:
		return NewNumberFromInt64(vv)
	case float64:
		return NewNumberFromFloat(vv)
	default:
		return vv
	}
}

// normalizeScope copies a pushed frame, normalising every value in it. Only
// the frame itself is a Scope — the interpreter's stack needs that type. A
// context-typed value inside the frame (an input, an upstream decision, a BKM
// argument) stays a plain map[string]any, the type every context operator and
// built-in switches on; upstream rewrote nested maps to Scope too, so a
// context variable was not a context anywhere but DotOp (see NOTICE.md).
func normalizeScope(scope Scope) Scope {
	newScope := make(Scope, len(scope))
	for key, value := range scope {
		newScope[key] = normalizeEntry(value)
	}
	return newScope
}

// normalizeEntry normalises one frame value, turning a context (either map
// spelling) into a fresh map[string]any with its own entries normalised.
func normalizeEntry(value any) any {
	switch m := value.(type) {
	case Scope:
		return normalizeContext(m)
	case map[string]any:
		return normalizeContext(m)
	default:
		return normalizeValue(value)
	}
}

func normalizeContext(m map[string]any) map[string]any {
	out := make(map[string]any, len(m))
	for key, value := range m {
		out[key] = normalizeEntry(value)
	}
	return out
}

func NewIntepreter() *Interpreter {
	intp := &Interpreter{}
	intp.PushEmpty()
	return intp
}

func (i Interpreter) String() string {
	return "interpreter"
}

func (i Interpreter) Len() int {
	return len(i.ScopeStack)
}

func (i *Interpreter) Push(scp Scope) {
	i.ScopeStack = append(i.ScopeStack, normalizeScope(scp))
}

func (i *Interpreter) PushEmpty() {
	vars := make(Scope)
	i.Push(vars)
}

func (i *Interpreter) Pop() Scope {
	if i.Len() > 0 {
		top := i.ScopeStack[len(i.ScopeStack)-1]
		i.ScopeStack = i.ScopeStack[:len(i.ScopeStack)-1]
		return top
	}
	return nil
}

// Resolve a name from the top of scopestack to bottom
func (i Interpreter) Resolve(name string) (any, bool) {
	for at := len(i.ScopeStack) - 1; at >= 0; at-- {
		if v, ok := i.ScopeStack[at][name]; ok {
			return v, true
		}
	}
	if prelude, ok := GetPrelude().Resolve(name); ok {
		return prelude, ok
	}
	return nil, false
}

// Set the name and set to new value
func (i Interpreter) Set(name string, value any) bool {
	for at := len(i.ScopeStack) - 1; at >= 0; at-- {
		if _, ok := i.ScopeStack[at][name]; ok {
			i.ScopeStack[at][name] = value
			return true
		}
	}
	return false
}

// Bind the value to the name of current scope
func (i *Interpreter) Bind(name string, value any) {
	if i.Len() > 0 {
		i.ScopeStack[i.Len()-1][name] = normalizeValue(value)
	} else {
		panic("empty stack")
	}
}

// Node's eval functions

// Eval Evaluate Number node
func (numberNode NumberNode) Eval(intp *Interpreter) (any, error) {
	return NewNumber(numberNode.Value), nil
}

// Eval Evaluate bool node
func (boolNode BoolNode) Eval(intp *Interpreter) (any, error) {
	return boolNode.Value, nil
}

func (nullNode NullNode) Eval(intp *Interpreter) (any, error) {
	return null, nil
}

func (stringNode StringNode) Eval(intp *Interpreter) (any, error) {
	return stringNode.Content(), nil
}

func (tempNode TemporalNode) Eval(intp *Interpreter) (any, error) {
	return ParseTemporalValue(tempNode.Content())
}

func (v Var) Eval(intp *Interpreter) (any, error) {
	if v, ok := intp.Resolve(v.Name); ok {
		return v, nil
	} else {
		//return nil, NewErrKeyNotFound(v.Name)
		return null, nil
	}
}

func (rangeNode RangeNode) Eval(intp *Interpreter) (any, error) {
	startVal, err := rangeNode.Start.Eval(intp)
	if err != nil {
		return nil, err
	}
	endVal, err := rangeNode.End.Eval(intp)
	if err != nil {
		return nil, err
	}
	return &RangeValue{
		Start:     startVal,
		StartOpen: rangeNode.StartOpen,
		End:       endVal,
		EndOpen:   rangeNode.EndOpen,
	}, nil
}

func (arrNode ArrayNode) Eval(intp *Interpreter) (any, error) {
	var arr []any
	for _, elem := range arrNode.Elements {
		v, err := elem.Eval(intp)
		if err != nil {
			return nil, err
		}
		arr = append(arr, v)
	}
	return arr, nil
}

func (exprList ExprList) Eval(intp *Interpreter) (any, error) {
	var finalRet any = nil
	for _, elem := range exprList.Elements {
		v, err := elem.Eval(intp)
		if err != nil {
			return nil, err
		}
		finalRet = v
	}
	return finalRet, nil
}

func (mt MultiTests) Eval(intp *Interpreter) (any, error) {
	for _, elem := range mt.Elements {
		v, err := elem.Eval(intp)
		if err != nil {
			return nil, err
		}
		if boolValue(v) {
			return true, nil
		}
	}
	return false, nil
}

// Eval builds a context literal. DMN 1.5 §10.3.2.6: an entry's value may
// refer to the entries before it in the same literal, so each finished entry
// is bound in a frame of its own before the next is evaluated. That frame sits
// above the caller's scope, so an entry shadows an outer variable of the same
// name, and nested literals see their enclosing literal's earlier entries.
// Upstream evaluated every entry against the outer scope alone, so `{c: 1,
// r: c}` gave r = null.
func (mapNode MapNode) Eval(intp *Interpreter) (any, error) {
	mapVal := make(map[string]any)
	intp.PushEmpty()
	defer intp.Pop()
	for _, item := range mapNode.Values {

		v, err := item.Value.Eval(intp)
		if err != nil {
			return nil, err
		}
		mapVal[item.Name] = v
		intp.Bind(item.Name, v)
	}
	return mapVal, nil
}

// Eval is a path expression on a single value. DMN 1.5 §10.3.2.6 selects
// the entry named Attr from a context; a context without that entry has
// nothing to select, so the path is null (`{a: 1}.b` is null), and a path
// continuing from that null is null too (`{a: 1}.b.c`), as null propagates
// through every FEEL operator.
//
// VERDICT DIVERGENCE: upstream raised "key not found" for a missing entry and
// "type mismatch, expect map" for a path on null. See NOTICE.md.
func (dotop DotOp) Eval(intp *Interpreter) (any, error) {
	leftVal, err := dotop.Left.Eval(intp)
	if err != nil {
		return nil, err
	}
	if list, ok := leftVal.([]any); ok {
		return dotop.project(intp, list)
	}
	if m, ok := asContext(leftVal); ok {
		if val, found := m[dotop.Attr]; found {
			return val, nil
		}
		return null, nil
	}
	if isNullValue(leftVal) {
		return null, nil
	}
	if obj, ok := leftVal.(HasAttrs); ok {
		if v, found := obj.GetAttr(dotop.Attr); found {
			return normalizeValue(v), nil
		}
		return nil, NewErrKeyNotFound(dotop.Attr)
	}
	return nil, NewErrTypeMismatch("map")
}

// project is a path over a list (DMN 1.5 §10.3.2.5): "a selection using the
// "." operator with a list of contexts on its left hand side returns a list of
// selections", FEEL(f, c') for each element e' with c' augmented by the entries
// of e' — so an element without the entry gives null
// (`[{x:1, y:2}, {x:2}].y = [2, null]`). **Interpretation:** an element that is
// not a context contributes no entries, so it selects null too, unless it has
// the attribute (a date's `year`). Every element is charged against the
// iteration budget.
//
// VERDICT DIVERGENCE: upstream raised "type mismatch, expect map" for a path
// on any list. See NOTICE.md.
func (dotop DotOp) project(intp *Interpreter, list []any) (any, error) {
	if err := intp.chargeIterations(uint64(len(list))); err != nil {
		return nil, err
	}
	out := make([]any, len(list))
	for i, elem := range list {
		out[i] = null
		if m, ok := asContext(elem); ok {
			if v, found := m[dotop.Attr]; found {
				out[i] = v
			}
		} else if obj, ok := elem.(HasAttrs); ok {
			if v, found := obj.GetAttr(dotop.Attr); found {
				out[i] = normalizeValue(v)
			}
		}
	}
	return out, nil
}

func (ifExpr IfExpr) Eval(intp *Interpreter) (any, error) {
	condVal, err := ifExpr.Cond.Eval(intp)
	if err != nil {
		return nil, err
	}

	if boolValue(condVal) {
		brVal, err := ifExpr.ThenBranch.Eval(intp)
		if err != nil {
			return nil, err
		}
		return brVal, nil
	} else {
		brVal, err := ifExpr.ElseBranch.Eval(intp)
		if err != nil {
			return nil, err
		}
		return brVal, nil
	}
}

// Eval yields the integers from Start to End inclusive, ascending or
// descending as the endpoints run, DMN 1.5 §10.3.2.14: `1..3` is [1, 2, 3]
// and `3..1` is [3, 2, 1]. The grammar requires both endpoints to be integers;
// any other endpoint, null included, is outside the semantic domain and the
// result is null, which the enclosing for, some or every turns into a null
// result of its own.
func (ir IterationRange) Eval(intp *Interpreter) (any, error) {
	startVal, err := ir.Start.Eval(intp)
	if err != nil {
		return nil, err
	}
	endVal, err := ir.End.Eval(intp)
	if err != nil {
		return nil, err
	}
	from, ok := iterationEndpoint(startVal)
	if !ok {
		return nil, nil
	}
	to, ok := iterationEndpoint(endVal)
	if !ok {
		return nil, nil
	}
	step := int64(1)
	// The element count minus one, in unsigned arithmetic so that the full
	// int64 span cannot overflow. Charged against the evaluation's iteration
	// budget before anything is allocated: a model loaded over the network
	// must not be able to ask for a billion-element list.
	span := uint64(to) - uint64(from)
	if to < from {
		step = -1
		span = uint64(from) - uint64(to)
	}
	results := make([]any, 0)
	if limit := intp.iterationLimit(); limit > 0 {
		// span+1 wraps to zero only for the full 2^64-element span, which
		// no int64 budget admits.
		if span == ^uint64(0) {
			return nil, &IterationBudgetExceededError{Limit: limit}
		}
		if err := intp.chargeIterations(span + 1); err != nil {
			return nil, err
		}
		// Bounded by the budget, so sizing the list up front is safe.
		results = make([]any, 0, span+1)
	}
	for i := from; ; i += step {
		results = append(results, NewNumberFromInt64(i))
		if i == to {
			break
		}
	}
	return results, nil
}

// iterationLimit reports the budget this interpreter charges against: the
// shared Budget's limit when one is set, MaxIterations otherwise.
func (intp *Interpreter) iterationLimit() int64 {
	if intp.Budget != nil {
		return intp.Budget.Limit
	}
	return intp.MaxIterations
}

// chargeIterations spends n elements of the evaluation's iteration budget,
// failing when the total would exceed Interpreter.MaxIterations — or the
// shared Budget's limit, when one is set. A zero budget charges nothing.
func (intp *Interpreter) chargeIterations(n uint64) error {
	if intp.Budget != nil {
		return intp.Budget.charge(n)
	}
	limit := intp.MaxIterations
	if limit <= 0 {
		return nil
	}
	if n > uint64(limit)-intp.iterations {
		return &IterationBudgetExceededError{Limit: limit}
	}
	intp.iterations += n
	return nil
}

// chargeDomain charges the elements of a for, some or every domain against
// the iteration budget. An integer-range context charged its own elements
// before building them, so only a list (or singleton) domain is charged here.
func (intp *Interpreter) chargeDomain(listExpr Node, list []any) error {
	if _, isRange := listExpr.(*IterationRange); isRange {
		return nil
	}
	return intp.chargeIterations(uint64(len(list)))
}

// IterationBudget is an iteration budget shared by several evaluations,
// possibly on different goroutines: every element any of them charges is
// drawn from one counter. Verdict creates one per request, so a business
// knowledge model invoked a thousand times from a `for`, or two sibling
// decisions evaluated in parallel, draw on the same budget rather than each
// starting afresh.
//
// VERDICT DIVERGENCE: see NOTICE.md.
type IterationBudget struct {
	// Limit is the total number of elements the budget admits. Zero or
	// negative means no budget.
	Limit int64
	used  atomic.Uint64
}

// NewIterationBudget returns a shared budget of limit elements.
func NewIterationBudget(limit int64) *IterationBudget {
	return &IterationBudget{Limit: limit}
}

// Used reports the elements charged so far.
func (b *IterationBudget) Used() uint64 { return b.used.Load() }

// charge spends n elements, failing — and spending nothing — when the total
// would exceed the limit. A compare-and-swap loop keeps concurrent charges
// exact without a lock: the counter never passes the limit, so one charge
// that fails cannot make a smaller concurrent one fail spuriously.
func (b *IterationBudget) charge(n uint64) error {
	if b.Limit <= 0 {
		return nil
	}
	limit := uint64(b.Limit)
	for {
		used := b.used.Load()
		if n > limit-used {
			return &IterationBudgetExceededError{Limit: b.Limit}
		}
		if b.used.CompareAndSwap(used, used+n) {
			return nil
		}
	}
}

// IterationBudgetExceededError reports an evaluation whose iteration contexts
// would together produce more elements than Interpreter.MaxIterations allows,
// or than remain in its shared IterationBudget.
type IterationBudgetExceededError struct {
	Limit int64
}

func (e *IterationBudgetExceededError) Error() string {
	return fmt.Sprintf("iteration exceeds the limit of %d elements", e.Limit)
}

// iterationEndpoint reports v as an int64 when it is an integral FEEL number
// that fits one exactly. A string is not a number here, even one that parses.
func iterationEndpoint(v any) (int64, bool) {
	n, ok := v.(*Number)
	if !ok || !n.v.IsInt() {
		return 0, false
	}
	i, acc := n.v.Int64()
	return i, acc == big.Exact
}

func (fe ForExpr) Eval(intp *Interpreter) (any, error) {
	listLike, err := fe.ListExpr.Eval(intp)
	if err != nil {
		return nil, err
	}
	aList, ok := iterationDomain(fe.ListExpr, listLike)
	if !ok {
		// An integer-range context with a non-integer endpoint: the whole
		// for expression is null, not a type mismatch.
		return nil, nil
	}
	if err := intp.chargeDomain(fe.ListExpr, aList); err != nil {
		return nil, err
	}

	intp.PushEmpty()
	results := make([]any, 0)
	for _, val := range aList {
		intp.Bind(fe.Varname, val)

		res, err := fe.ReturnExpr.Eval(intp)
		if err != nil {
			intp.Pop()
			return nil, err
		}
		if fe.Continues {
			if res == nil {
				// The inner context was an integer range with a
				// non-integer endpoint; the whole expression is null.
				intp.Pop()
				return nil, nil
			}
			// DMN 1.5 FEEL grammar rule 44 and §10.3.2.14: several
			// iteration contexts range over their cartesian product and
			// yield one flat list. Only the inner context's own list is
			// spliced; the body's result is never flattened.
			inner, _ := res.([]any)
			results = append(results, inner...)
			continue
		}
		results = append(results, res)
	}
	intp.Pop()
	return results, nil
}

// iterationDomain turns the evaluated domain of one iteration context of a
// for, some or every into the list it iterates. DMN 1.5 implicit conversion
// to a singleton list (§10.3.2.9.4): a single value where a list is expected is
// the one-element list holding it, so a null domain is [null], not an error,
// and the body or condition decides what an absent value means. A range value
// such as [1..3] is not a list and is wrapped likewise. ok is false only for
// an integer-range context with a non-integer endpoint, whose enclosing
// expression is null rather than the singleton [null].
func iterationDomain(listExpr Node, listLike any) (list []any, ok bool) {
	if _, isRange := listExpr.(*IterationRange); isRange && listLike == nil {
		return nil, false
	}
	if aList, isList := listLike.([]any); isList {
		return aList, true
	}
	return []any{listLike}, true
}

// Quantified expressions yield a boolean, DMN 1.5 FEEL grammar rule 46 and
// §10.3.2.15: `some` is true if the condition is true for any element,
// `every` is true if it is true for all of them, and both fold a null or
// non-boolean condition into a null result when no element decides the
// answer outright. Upstream returned the first matching element from `some`
// and the list of matching elements from `every`, so neither was usable as a
// condition, and `some` leaked an interpreter scope on its early return.

// quantify evaluates cond for every element of list, bound to varname, and
// folds the results three-valued. decisive is the condition value that settles
// the answer on its own: true for `some`, false for `every`.
func quantify(intp *Interpreter, varname string, listExpr, cond Node, decisive bool) (any, error) {
	listLike, err := listExpr.Eval(intp)
	if err != nil {
		return nil, err
	}
	aList, ok := iterationDomain(listExpr, listLike)
	if !ok {
		return nil, nil
	}
	if err := intp.chargeDomain(listExpr, aList); err != nil {
		return nil, err
	}

	intp.PushEmpty()
	defer intp.Pop()
	undecided := false
	for _, val := range aList {
		intp.Bind(varname, val)
		res, err := cond.Eval(intp)
		if err != nil {
			return nil, err
		}
		b, isBool := res.(bool)
		switch {
		case !isBool:
			undecided = true
		case b == decisive:
			return decisive, nil
		}
	}
	if undecided {
		return nil, nil
	}
	return !decisive, nil
}

func (sexpr SomeExpr) Eval(intp *Interpreter) (any, error) {
	return quantify(intp, sexpr.Varname, sexpr.ListExpr, sexpr.FilterExpr, true)
}

func (ee EveryExpr) Eval(intp *Interpreter) (any, error) {
	return quantify(intp, ee.Varname, ee.ListExpr, ee.FilterExpr, false)
}

func (fd FunDef) Eval(intp *Interpreter) (any, error) {
	return &FunDef{
		Args: fd.Args,
		Body: fd.Body,
	}, nil
}

func (fd FunDef) EvalCall(intp *Interpreter, args []any) (any, error) {
	if len(args) != len(fd.Args) {
		return nil, errors.New("eval call argument size mismatch")
	}
	intp.PushEmpty()
	defer intp.Pop()
	for i, argName := range fd.Args {
		intp.Bind(argName, args[i])
	}
	return fd.Body.Eval(intp)
}

func (fc FunCall) Eval(intp *Interpreter) (any, error) {
	v, err := fc.FunRef.Eval(intp)
	if err != nil {
		return nil, err
	}
	switch r := v.(type) {
	case *FunDef:
		return fc.EvalFunDef(intp, r)
	case *NativeFun:
		return fc.EvalNativeFun(intp, r)
	case *Macro:
		return fc.EvalMacro(intp, r)
	default:
		return nil, NewErrTypeMismatch("function")
	}
}

// EvalNativeFun binds a call's arguments to a native function's parameters
// and calls it.
//
// VERDICT DIVERGENCE: upstream raised on a call whose arguments did not fit
// the parameters — too few, too many, a missing keyword — and ignored a named
// argument no parameter had, so `list replace(..., foo: 1)` ran as if foo were
// absent; a named variadic parameter (`count(list: [1, 2])`) was dropped. DMN
// 1.5 §10.3.4 makes a standard built-in invoked outside its domain null, and
// TCK 1140, 1145 and 1155 pin a wrong argument count and an unknown parameter
// name as null: so a standard built-in (see NativeFun.standard) yields null
// for any of them, and every other native function raises, unknown names
// included. A named variadic parameter is passed through as its value.
func (fc FunCall) EvalNativeFun(intp *Interpreter, funDef *NativeFun) (any, error) {
	if len(funDef.overloads) > 0 {
		return fc.evalOverloaded(intp, funDef)
	}
	argVals, err := fc.bindNativeArgs(intp, funDef)
	if err != nil {
		var bad *argBindingError
		if errors.As(err, &bad) {
			if funDef.standard {
				return null, nil
			}
			return nil, bad.err
		}
		return nil, err
	}
	return funDef.Call(intp, argVals)
}

// argBindingError marks a failure to fit a call's arguments to a native
// function's parameters, as distinct from a failure evaluating an argument.
type argBindingError struct{ err error }

func (e *argBindingError) Error() string { return e.err.Error() }
func (e *argBindingError) Unwrap() error { return e.err }

func (fc FunCall) bindNativeArgs(intp *Interpreter, funDef *NativeFun) (map[string]any, error) {
	argVals := make(map[string]any)
	if fc.keywordArgs {
		kwArgMap, err := fc.evalArgsToMap(intp)
		if err != nil {
			return nil, err
		}
		known := map[string]bool{}
		for _, argName := range funDef.requiredArgNames {
			known[argName] = true
			if v, ok := kwArgMap[argName]; ok {
				argVals[argName] = v
			} else {
				return nil, &argBindingError{NewErrKeywordArgument(argName)}
			}
		}
		for _, argName := range funDef.optionalArgNames {
			known[argName] = true
			if v, ok := kwArgMap[argName]; ok {
				argVals[argName] = v
			}
		}
		if funDef.varArgName != "" {
			known[funDef.varArgName] = true
			if v, ok := kwArgMap[funDef.varArgName]; ok {
				argVals[funDef.varArgName] = v
			}
		}
		for name := range kwArgMap {
			if !known[name] {
				return nil, &argBindingError{NewErrUnknownArgument(name)}
			}
		}
		return argVals, nil
	}
	if len(fc.Args) < len(funDef.requiredArgNames) {
		required := funDef.requiredArgNames[len(fc.Args):len(funDef.requiredArgNames)]
		return nil, &argBindingError{NewErrTooFewArguments(required)}
	}
	for i, argNode := range fc.Args {
		a, err := argNode.arg.Eval(intp)
		if err != nil {
			return nil, err
		}
		if i < len(funDef.requiredArgNames) {
			argVals[funDef.requiredArgNames[i]] = a
		} else if i < len(funDef.requiredArgNames)+len(funDef.optionalArgNames) {
			argVals[funDef.optionalArgNames[i-len(funDef.requiredArgNames)]] = a
		} else if funDef.varArgName != "" {
			if vars, ok := argVals[funDef.varArgName]; ok {
				varargs := vars.([]any)
				varargs = append(varargs, a)
				argVals[funDef.varArgName] = varargs
			} else {
				argVals[funDef.varArgName] = []any{a}
			}
		} else {
			return nil, &argBindingError{NewErrTooManyArguments()}
		}
	}
	return argVals, nil
}

// evalOverloaded calls a built-in declared with NativeFun.Overloads. A call
// that fits no overload is null for a standard built-in (see EvalNativeFun).
func (fc FunCall) evalOverloaded(intp *Interpreter, funDef *NativeFun) (any, error) {
	var args map[string]any
	var err error
	if fc.keywordArgs {
		named, err := fc.evalArgsToMap(intp)
		if err != nil {
			return nil, err
		}
		args, err = funDef.bindOverload(nil, named)
		if err != nil {
			return funDef.bindFailure(err)
		}
	} else {
		positional := make([]any, len(fc.Args))
		for i, argNode := range fc.Args {
			if positional[i], err = argNode.arg.Eval(intp); err != nil {
				return nil, err
			}
		}
		if args, err = funDef.bindOverload(positional, nil); err != nil {
			return funDef.bindFailure(err)
		}
	}
	return funDef.Call(intp, args)
}

func (fc FunCall) evalArgsToMap(intp *Interpreter) (map[string]any, error) {
	if !fc.keywordArgs {
		return nil, errors.New("funcall has no keyword args")
	}
	kwArgMap := make(map[string]any)
	for _, argNode := range fc.Args {
		a, err := argNode.arg.Eval(intp)
		if err != nil {
			return nil, err
		}
		kwArgMap[argNode.argName] = a
	}
	return kwArgMap, nil
}

func (fc FunCall) EvalMacro(intp *Interpreter, macro *Macro) (any, error) {
	if len(macro.requiredArgNames) > len(fc.Args) {
		return nil, NewErrTooFewArguments(macro.requiredArgNames[len(fc.Args):])
		//return nil, NewEvalError(-1005, "number of args of macro mismatch")
	}

	argNodes := make(map[string]Node)
	var varArgs []Node
	if fc.keywordArgs {
		kwArgMap := make(map[string]Node)
		for _, argNode := range fc.Args {
			kwArgMap[argNode.argName] = argNode.arg
		}

		for _, argName := range macro.requiredArgNames {
			if ast, ok := kwArgMap[argName]; ok {
				argNodes[argName] = ast
			} else {
				//return nil, NewEvalError(-5001, "no keyword argument", fmt.Sprintf("no keyword argument %s", argName))
				return nil, NewErrKeywordArgument(argName)
			}
		}

		for _, argName := range macro.optionalArgNames {
			if ast, ok := kwArgMap[argName]; ok {
				argNodes[argName] = ast
			}
		}
	} else {
		if len(fc.Args) < len(macro.requiredArgNames) {
			//reqArgs := strings.Join(macro.requiredArgNames[len(fc.Args):len(macro.requiredArgNames)], ", ")
			//return nil, NewEvalError(-5003, "too few arguments", fmt.Sprintf("more arguments required: %s", reqArgs))
			return nil, NewErrTooFewArguments(macro.requiredArgNames[len(fc.Args):])
		}
		for i, argNode := range fc.Args {
			if i < len(macro.requiredArgNames) {
				argNodes[macro.requiredArgNames[i]] = argNode.arg
			} else if i < len(macro.requiredArgNames)+len(macro.optionalArgNames) {
				argNodes[macro.optionalArgNames[i-len(macro.requiredArgNames)]] = argNode.arg
			} else if macro.varArgName != "" {
				varArgs = append(varArgs, argNode.arg)
			} else {
				//return nil, NewEvalError(-5002, "too many arguments")
				return nil, NewErrTooManyArguments()
			}
		}
	}
	return macro.fn(intp, argNodes, varArgs)
}

func (fc FunCall) EvalFunDef(intp *Interpreter, funDef *FunDef) (any, error) {
	if len(funDef.Args) > len(fc.Args) {
		//return nil, NewEvalError(-1004, "number of args mismatch")
		return nil, NewErrTooFewArguments(funDef.Args[len(fc.Args):])
	} else if len(funDef.Args) < len(fc.Args) {
		return nil, NewErrTooManyArguments()
	}
	//var args []any
	intp.PushEmpty()
	defer intp.Pop()

	if fc.keywordArgs {
		kwArgMap, err := fc.evalArgsToMap(intp)
		if err != nil {
			return nil, err
		}

		for _, argName := range funDef.Args {
			if v, ok := kwArgMap[argName]; ok {
				//argVals = append(argVals, v)
				intp.Bind(argName, v)
			} else {
				//return nil, NewEvalError(-5001, "no keyword argument", fmt.Sprintf("no keyword argument %s", argName))
				intp.Bind(argName, null)
			}
		}
	} else {
		for i, argNode := range fc.Args {
			a, err := argNode.arg.Eval(intp)
			if err != nil {
				return nil, err
			}
			intp.Bind(funDef.Args[i], a)
		}
	}
	ret, err := funDef.Body.Eval(intp)
	return ret, err
}

func EvalString(input string) (any, error) {
	return EvalStringWithScope(input, nil)
}

func EvalStringWithScope(input string, scope Scope) (any, error) {
	ast, err := ParseString(input)
	if err != nil {
		return nil, err
	}
	intp := NewIntepreter()
	if scope != nil {
		intp.Push(scope)
	}
	r, err := ast.Eval(intp)
	if err != nil || r == nil {
		return r, err
	}
	return unwrapFEELValue(r)
}

func unwrapFEELValue(v any) (any, error) {
	switch val := v.(type) {
	case *Number:
		return val.Float64(), nil
	case *FEELDate:
		return val.Date(), nil
	case *FEELTime:
		return val.Time(), nil
	case *FEELDatetime:
		return val.Date(), nil
	case *FEELDuration:
		return val.Duration(), nil
	case *NullValue:
		return nil, nil
	default:
		rv := reflect.ValueOf(val)
		switch rv.Kind() {
		case reflect.Slice, reflect.Array:
			result := make([]any, rv.Len())
			for i := 0; i < rv.Len(); i++ {
				uv, err := unwrapFEELValue(rv.Index(i).Interface())
				if err != nil {
					return nil, err
				}
				result[i] = uv
			}
			return result, nil
		case reflect.Map:
			result := make(map[string]any, rv.Len())
			for _, key := range rv.MapKeys() {
				mapVal, err := unwrapFEELValue(rv.MapIndex(key).Interface())
				if err != nil {
					return nil, err
				}

				if uk, err := unwrapFEELValue(key.Interface()); err == nil {
					if sk, ok := uk.(string); ok {
						result[sk] = mapVal
					} else {
						return nil, errors.New("map keys must be strings")
					}
				} else {
					return nil, err
				}

			}
			return result, nil
		default:
			return val, nil
		}
	}
}
