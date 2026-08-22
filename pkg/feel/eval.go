package feel

import (
	"errors"
	"fmt"
	"time"

	pfeel "github.com/frankbardon/verdict/pkg/feel/internal/dialect"
)

// Function is an application-supplied FEEL function. Arguments arrive as FEEL
// values keyed by parameter name; the return value is converted with FromGo, so
// a handler may return plain Go values.
type Function struct {
	// Params are the required positional parameter names, in order.
	Params []string
	// Optional are parameters that may be omitted; they follow Params.
	Optional []string
	// Variadic, when non-empty, collects any remaining arguments into a list
	// bound to this name.
	Variadic string
	// Help is a one-line description surfaced by `verdict explain`.
	Help string
	// Fn is the implementation.
	Fn func(args map[string]any) (any, error)
}

// FunctionLibrary is a namespaced bundle of application functions. The
// namespace is mandatory: functions are reachable only as `namespace.name(...)`,
// which is what makes it impossible for an extension to shadow a DMN standard
// built-in.
type FunctionLibrary struct {
	Namespace string
	Functions map[string]Function
}

// ErrNamespaceRequired is returned when a function library omits its namespace
// or claims one that would collide with a standard built-in.
var ErrNamespaceRequired = errors.New("feel: a function library must declare a namespace that is not a built-in name")

// Evaluator compiles and evaluates FEEL against a scope stack. It is safe for
// concurrent use: each evaluation gets its own interpreter and the shared state
// (compile cache, global scope) is read-only after construction.
type Evaluator struct {
	compiler *Compiler
	globals  pfeel.Scope
}

// Option configures an Evaluator.
type Option func(*evaluatorOptions)

type evaluatorOptions struct {
	libs  []FunctionLibrary
	clock func() time.Time
}

// WithLibraries registers namespaced application function libraries.
func WithLibraries(libs ...FunctionLibrary) Option {
	return func(o *evaluatorOptions) { o.libs = append(o.libs, libs...) }
}

// WithClock overrides the time source the temporal built-ins read.
//
// `now()`, `today()` and anything derived from them are the one place a
// decision model can produce a different answer for identical inputs. Injecting
// a clock is what makes a model that reads the date testable — and what lets a
// replay reproduce an evaluation that happened yesterday.
func WithClock(clock func() time.Time) Option {
	return func(o *evaluatorOptions) { o.clock = clock }
}

// NewEvaluator builds an evaluator for a dialect, layering the DMN 1.5 standard
// library supplement beneath any application function libraries.
func NewEvaluator(d Dialect, opts ...Option) (*Evaluator, error) {
	var cfg evaluatorOptions
	for _, o := range opts {
		o(&cfg)
	}

	e := &Evaluator{compiler: NewCompiler(d), globals: pfeel.Scope{}}
	for name, fn := range standardSupplement() {
		e.globals[name] = fn
	}
	if cfg.clock != nil {
		// Bound above the vendored prelude's own `now`/`today`, so the injected
		// clock wins without the prelude being mutated — it is a process-wide
		// singleton, and two engines with different clocks must not fight over it.
		for name, fn := range clockFunctions(cfg.clock) {
			e.globals[name] = fn
		}
	}
	for _, lib := range cfg.libs {
		if err := e.install(lib); err != nil {
			return nil, err
		}
	}
	return e, nil
}

// clockFunctions builds the temporal built-ins that read the injected clock.
func clockFunctions(clock func() time.Time) map[string]any {
	return map[string]any{
		"now": nativeFunc(Function{
			Help: "the current date and time",
			Fn:   func(map[string]any) (any, error) { return FromGo(clock()), nil },
		}),
		"today": nativeFunc(Function{
			Help: "the current date",
			Fn: func(map[string]any) (any, error) {
				t := clock()
				return ParseTemporal(t.Format("2006-01-02"), "date")
			},
		}),
	}
}

func (e *Evaluator) install(lib FunctionLibrary) error {
	if lib.Namespace == "" || isBuiltinName(lib.Namespace) {
		return fmt.Errorf("%w (got %q)", ErrNamespaceRequired, lib.Namespace)
	}
	ns, _ := e.globals[lib.Namespace].(map[string]any)
	if ns == nil {
		ns = map[string]any{}
		e.globals[lib.Namespace] = ns
	}
	for name, fn := range lib.Functions {
		ns[name] = nativeFunc(fn)
	}
	return nil
}

func nativeFunc(f Function) *pfeel.NativeFun {
	impl := f.Fn
	nf := pfeel.NewNativeFunc(func(args map[string]any) (any, error) {
		v, err := impl(args)
		if err != nil {
			return nil, err
		}
		return FromGo(v), nil
	})
	if len(f.Params) > 0 {
		nf = nf.Required(f.Params...)
	}
	if len(f.Optional) > 0 {
		nf = nf.Optional(f.Optional...)
	}
	if f.Variadic != "" {
		nf = nf.Vararg(f.Variadic)
	}
	if f.Help != "" {
		nf = nf.Help(f.Help)
	}
	return nf
}

// Compiler exposes the evaluator's compile cache so callers can pre-compile and
// validate expressions at model-load time.
func (e *Evaluator) Compiler() *Compiler { return e.compiler }

// Dialect reports the evaluator's dialect.
func (e *Evaluator) Dialect() Dialect { return e.compiler.dialect }

// Compile parses text under the evaluator's dialect.
func (e *Evaluator) Compile(text string) (*Expr, error) { return e.compiler.Compile(text) }

// CompileUnaryTest parses a decision-table input entry.
func (e *Evaluator) CompileUnaryTest(text string) (*UnaryTest, error) {
	return e.compiler.CompileUnaryTest(text)
}

// EvalError wraps a runtime failure with the expression that produced it.
type EvalError struct {
	Text string
	Err  error
}

func (e *EvalError) Error() string { return fmt.Sprintf("feel: evaluating %q: %v", e.Text, e.Err) }
func (e *EvalError) Unwrap() error { return e.Err }

// Eval evaluates a compiled expression against vars. A nil expression yields
// null, which is what an omitted decision-table output entry means.
func (e *Evaluator) Eval(x *Expr, vars map[string]any) (any, error) {
	if x == nil {
		return Null, nil
	}
	intp := e.interpreter(vars)
	v, err := x.node.Eval(intp)
	if err != nil {
		return nil, &EvalError{Text: x.text, Err: err}
	}
	return v, nil
}

// EvalText compiles and evaluates in one step. Prefer Eval with a pre-compiled
// expression on hot paths; this exists for one-shot callers such as the CLI.
func (e *Evaluator) EvalText(text string, vars map[string]any) (any, error) {
	x, err := e.Compile(text)
	if err != nil {
		return nil, err
	}
	return e.Eval(x, vars)
}

// interpreter builds a fresh scope stack: globals underneath, caller variables
// on top. Names in vars therefore shadow function libraries, matching FEEL's
// rule that a variable in scope wins over an outer binding.
func (e *Evaluator) interpreter(vars map[string]any) *pfeel.Interpreter {
	intp := pfeel.NewIntepreter()
	intp.Push(pfeel.Scope(e.globals))
	if len(vars) > 0 {
		intp.Push(pfeel.Scope(vars))
	} else {
		intp.PushEmpty()
	}
	return intp
}

// NewFunctionValue turns a Function into a value that can be bound into a
// scope and then called from FEEL. The evaluator uses it to expose business
// knowledge models as ordinary FEEL functions, which is what lets a literal
// expression call a BKM by name exactly as DMN specifies.
func NewFunctionValue(f Function) any { return nativeFunc(f) }
