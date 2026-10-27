package dialect

import (
	"fmt"
	"sync"
)

type ArgTypeError struct {
	index  int
	expect string
}

func (self ArgTypeError) Error() string {
	return fmt.Sprintf("type error at %d, expect %s", self.index, self.expect)
}

type ArgSizeError struct {
	has    int
	expect int
}

func (self ArgSizeError) Error() string {
	return fmt.Sprintf("argument size error, has %d, expect %d", self.has, self.expect)
}

// NativeFunDef native function
type NativeFunDef func(args map[string]interface{}) (interface{}, error)

type NativeFun struct {
	fn               NativeFunDef
	requiredArgNames []string
	optionalArgNames []string
	varArgName       string
	help             string

	// overloads, when set, replaces requiredArgNames/optionalArgNames: each
	// entry is one signature's parameter names in order. See Overloads.
	overloads [][]string

	// standard marks a DMN standard built-in: a call whose arguments do not
	// fit its parameters is null rather than an error (see EvalNativeFun).
	// The prelude sets it on every native function it binds; Standard sets
	// it on one bound elsewhere.
	standard bool
}

// Standard marks the function as a DMN standard built-in, so a call to it
// with the wrong arguments is null (DMN 1.5 §10.3.4) instead of an error.
//
// VERDICT DIVERGENCE: fork-only; see EvalNativeFun.
func (nativeFun *NativeFun) Standard() *NativeFun {
	nativeFun.standard = true
	return nativeFun
}

// bindFailure is the result of a call whose arguments fit no parameter list:
// null for a standard built-in, the binding error for anything else.
func (nativeFun *NativeFun) bindFailure(err error) (any, error) {
	if nativeFun.standard {
		return null, nil
	}
	return nil, err
}

func NewNativeFunc(fn NativeFunDef) *NativeFun {
	return &NativeFun{fn: fn}
}

func (nativeFun *NativeFun) Required(argNames ...string) *NativeFun {
	nativeFun.requiredArgNames = append(nativeFun.requiredArgNames, argNames...)
	return nativeFun
}

func (nativeFun *NativeFun) Optional(argNames ...string) *NativeFun {
	nativeFun.optionalArgNames = append(nativeFun.optionalArgNames, argNames...)
	return nativeFun
}

func (nativeFun *NativeFun) Vararg(argName string) *NativeFun {
	nativeFun.varArgName = argName
	return nativeFun
}

// Overloads declares the function's signatures, each a list of parameter
// names in order. A positional call binds the signature with as many
// parameters as it has arguments; a named call binds the signature whose
// names are exactly the ones given. The function body tells the overloads
// apart by which names are present.
//
// VERDICT DIVERGENCE: upstream bound every argument by one parameter list, so
// a built-in could not have overloads whose parameters differ by name and
// position — DMN 1.5 §10.3.4.1 Table 72's date(from) and date(year, month,
// day), say — and named arguments could not reach them.
func (nativeFun *NativeFun) Overloads(signatures ...[]string) *NativeFun {
	nativeFun.overloads = append(nativeFun.overloads, signatures...)
	return nativeFun
}

// bindOverload maps a call's arguments onto the overload they fit: positional
// values by arity, named ones by the exact set of names.
func (nativeFun *NativeFun) bindOverload(positional []any, named map[string]any) (map[string]any, error) {
	if named != nil {
	signatures:
		for _, sig := range nativeFun.overloads {
			if len(sig) != len(named) {
				continue
			}
			for _, name := range sig {
				if _, ok := named[name]; !ok {
					continue signatures
				}
			}
			return named, nil
		}
		// No signature fits: name the first missing parameter of the one
		// sharing the most names with the call.
		best, bestShared := nativeFun.overloads[0], -1
		for _, sig := range nativeFun.overloads {
			shared := 0
			for _, name := range sig {
				if _, ok := named[name]; ok {
					shared++
				}
			}
			if shared > bestShared {
				best, bestShared = sig, shared
			}
		}
		for _, name := range best {
			if _, ok := named[name]; !ok {
				return nil, NewErrKeywordArgument(name)
			}
		}
		return nil, NewErrTooManyArguments()
	}
	// No signature has this arity: the call is short of the next longer one,
	// or longer than all of them.
	var next []string
	for _, sig := range nativeFun.overloads {
		if len(sig) == len(positional) {
			args := make(map[string]any, len(sig))
			for i, name := range sig {
				args[name] = positional[i]
			}
			return args, nil
		}
		if len(sig) > len(positional) && (next == nil || len(sig) < len(next)) {
			next = sig
		}
	}
	if next != nil {
		return nil, NewErrTooFewArguments(next[len(positional):])
	}
	return nil, NewErrTooManyArguments()
}

func (nativeFun *NativeFun) Help(help string) *NativeFun {
	nativeFun.help = help
	return nativeFun
}

func (nativeFun NativeFun) ArgNameAt(at int) (string, bool) {
	if at >= 0 && at < len(nativeFun.requiredArgNames) {
		return nativeFun.requiredArgNames[at], true
	} else if at >= len(nativeFun.requiredArgNames) && at < len(nativeFun.requiredArgNames)+len(nativeFun.optionalArgNames) {
		return nativeFun.optionalArgNames[at-len(nativeFun.requiredArgNames)], true
	}
	return "", false
}

func (nativeFun *NativeFun) Call(intp *Interpreter, args map[string]interface{}) (interface{}, error) {
	v, err := nativeFun.fn(args)
	if err != nil {
		return nil, err
	}
	return normalizeValue(v), nil
}

type MacroDef func(intp *Interpreter, args map[string]Node, varArgs []Node) (interface{}, error)
type Macro struct {
	fn               MacroDef
	requiredArgNames []string
	optionalArgNames []string
	varArgName       string
	help             string
}

func NewMacro(fn MacroDef) *Macro {
	return &Macro{fn: fn}
}

func (self *Macro) Required(argNames ...string) *Macro {
	self.requiredArgNames = append(self.requiredArgNames, argNames...)
	return self
}

func (self *Macro) Optional(argNames ...string) *Macro {
	self.optionalArgNames = append(self.optionalArgNames, argNames...)
	return self
}
func (self *Macro) Vararg(argName string) *Macro {
	self.varArgName = argName
	return self
}
func (self *Macro) Help(help string) *Macro {
	self.help = help
	return self
}

type Prelude struct {
	vars map[string]interface{}
}

var loadOnce sync.Once
var inst *Prelude

func GetPrelude() *Prelude {
	loadOnce.Do(func() {
		inst = &Prelude{vars: make(map[string]interface{})}
		inst.Load()
	})
	return inst
}

func (self *Prelude) Load() {
	self.Bind("bind", NewMacro(func(intp *Interpreter, args map[string]Node, varArgs []Node) (any, error) {
		name, err := args["name"].Eval(intp)
		strName, ok := name.(string)
		if !ok {
			return nil, NewErrTypeMismatch("string")
		}
		v, err := args["value"].Eval(intp)
		if err != nil {
			return nil, err
		}
		intp.Bind(strName, v)
		return v, nil
	}).Required("name", "value").Help("bind value to name in current top scope"))

	self.Bind("set", NewMacro(func(intp *Interpreter, args map[string]Node, varArgs []Node) (any, error) {
		name, err := args["name"].Eval(intp)
		strName, ok := name.(string)
		if !ok {
			return nil, NewErrTypeMismatch("string")
		}
		v, err := args["value"].Eval(intp)
		if err != nil {
			return nil, err
		}
		if intp.Set(strName, v) {
			return v, nil
		} else {
			intp.Bind(strName, v)
			return v, nil
		}
	}).Required("name", "value").Help("bind value to name in resolved scope, if not found, it's bind to current top scope(the same as 'bind')"))

	self.Bind("block", NewMacro(func(intp *Interpreter, args map[string]Node, exprlist []Node) (interface{}, error) {
		var lastValue interface{}
		var err error
		for _, expr := range exprlist {
			lastValue, err = expr.Eval(intp)
			if err != nil {
				return nil, err
			}
		}
		return lastValue, nil
	}).Vararg("express list").Help("quote a sequence of expresses and return the last result"))

	self.Bind("help", NewMacro(func(intp *Interpreter, args map[string]Node, exprlist []Node) (interface{}, error) {
		v, err := args["value"].Eval(intp)
		if err != nil {
			return nil, err
		}
		switch vv := v.(type) {
		case *NativeFun:
			return vv.help, nil
		case *Macro:
			return vv.help, nil
		default:
			return typeName(vv), nil
		}
	}).Required("value").Help("the help information of a value"))

	self.Bind("typeof", NewMacro(func(intp *Interpreter, args map[string]Node, exprlist []Node) (interface{}, error) {
		v, err := args["value"].Eval(intp)
		if err != nil {
			return nil, err
		}
		return typeName(v), nil
	}).Required("value").Help("the type of a value"))

	installDatetimeFunctions(self)
	installBuiltinFunctions(self)
	installContextFunctions(self)
	installRangeFunctions(self)
}

func (self *Prelude) Bind(name string, value interface{}) *Prelude {
	if _, ok := self.vars[name]; ok {
		panic(fmt.Sprintf("bind(), name '%s' already bound", name))
	}
	if nf, ok := value.(*NativeFun); ok {
		nf.standard = true
	}
	self.vars[name] = normalizeValue(value)
	return self
}

func (self *Prelude) Resolve(name string) (interface{}, bool) {
	v, ok := self.vars[name]
	return v, ok
}
