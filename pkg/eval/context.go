package eval

import (
	"context"
	"sync"

	"github.com/frankbardon/verdict/pkg/diag"
	"github.com/frankbardon/verdict/pkg/feel"
	"github.com/frankbardon/verdict/pkg/trace"
)

// Context is the state one node evaluation sees: the Go context, the visible
// names, the trace writer for the node, and the shared diagnostic sink.
//
// A Context is derived, never mutated in place by a child: entering a nested
// scope produces a new Context whose vars map shadows the parent's. That is
// what makes concurrent evaluation of independent decisions safe without a
// lock around the variable map.
type Context struct {
	ctx  context.Context
	prog *Program

	// vars holds every name visible here, as FEEL values.
	vars map[string]any

	trace trace.Writer

	// shared is the per-evaluation state: diagnostics, memo cache, depth.
	shared *evalState
}

// evalState is the state shared by every node in one evaluation.
type evalState struct {
	mu    sync.Mutex
	diags []diag.Diagnostic

	memo map[string]any

	// depth guards against unbounded recursion through decision services. The
	// DRG's acyclicity bounds requirement edges but not invocation: a literal
	// expression can call a service that calls back into this one.
	depth int
}

// enterService increments the service-invocation depth and reports the new
// value.
func (s *evalState) enterService() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.depth++
	return s.depth
}

func (s *evalState) exitService() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.depth--
}

// memoise returns a cached value for key, or computes and stores one. It is
// used only for referentially transparent work — business knowledge model
// invocations with identical arguments — never for agent decisions, whose
// answers are not a function of their inputs.
func (s *evalState) memoise(key string, compute func() (any, error)) (any, bool, error) {
	if s.memo == nil || key == "" {
		v, err := compute()
		return v, false, err
	}
	s.mu.Lock()
	if v, ok := s.memo[key]; ok {
		s.mu.Unlock()
		return v, true, nil
	}
	s.mu.Unlock()

	// The computation runs outside the lock: a business knowledge model may
	// itself invoke others, and holding the lock across that would deadlock the
	// moment two independent decisions in the same layer both call one.
	v, err := compute()
	if err != nil {
		return nil, false, err
	}
	s.mu.Lock()
	s.memo[key] = v
	s.mu.Unlock()
	return v, false, nil
}

// Go returns the evaluation's context.Context.
func (ec *Context) Go() context.Context { return ec.ctx }

// Vars returns the names visible at this node. The map must not be mutated;
// use Derive to add bindings.
func (ec *Context) Vars() map[string]any { return ec.vars }

// Trace returns the writer for the node being evaluated.
func (ec *Context) Trace() trace.Writer { return ec.trace }

// FEEL returns the expression evaluator, so a custom NodeEvaluator can compile
// and evaluate expressions with the same dialect and function libraries.
func (ec *Context) FEEL() *feel.Evaluator { return ec.prog.cfg.FEEL }

// Derive returns a Context with additional bindings layered over this one.
// Passing a nil or empty map returns the receiver unchanged.
func (ec *Context) Derive(bindings map[string]any) *Context {
	if len(bindings) == 0 {
		return ec
	}
	vars := make(map[string]any, len(ec.vars)+len(bindings))
	for k, v := range ec.vars {
		vars[k] = v
	}
	for k, v := range bindings {
		vars[k] = v
	}
	out := *ec
	out.vars = vars
	return &out
}

// withTrace returns a Context writing to a different trace node.
func (ec *Context) withTrace(w trace.Writer) *Context {
	out := *ec
	out.trace = w
	return &out
}

// eval evaluates a compiled expression against the visible names. A nil
// expression is null, which is what an omitted decision-table entry means.
func (ec *Context) eval(x *feel.Expr) (any, error) {
	if x == nil {
		return feel.Null, nil
	}
	if err := ec.ctx.Err(); err != nil {
		return nil, err
	}
	return ec.prog.cfg.FEEL.Eval(x, ec.vars)
}

// diagnose records an evaluation-time diagnostic.
func (ec *Context) diagnose(d diag.Diagnostic) {
	ec.shared.mu.Lock()
	defer ec.shared.mu.Unlock()
	ec.shared.diags = append(ec.shared.diags, d)
}

// Diagnose lets a custom NodeEvaluator contribute a diagnostic.
func (ec *Context) Diagnose(d diag.Diagnostic) { ec.diagnose(d) }

func (s *evalState) diagnostics() []diag.Diagnostic {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]diag.Diagnostic(nil), s.diags...)
}
