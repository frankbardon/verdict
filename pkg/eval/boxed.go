package eval

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/frankbardon/verdict/pkg/diag"
	"github.com/frankbardon/verdict/pkg/dmn/model"
	"github.com/frankbardon/verdict/pkg/feel"
	"github.com/frankbardon/verdict/pkg/trace"
)

// evalExpression is the boxed-expression dispatcher. Registered node
// evaluators are consulted first, so an application can replace the handling of
// any kind — including the built-in ones — without forking the engine.
func (p *Program) evalExpression(ec *Context, expr model.Expression) (any, error) {
	if expr == nil {
		return feel.Null, nil
	}
	if custom, ok := p.cfg.NodeEvaluators[expr.Kind()]; ok {
		return custom.Evaluate(ec, expr)
	}
	switch e := expr.(type) {
	case *model.LiteralExpression:
		return p.evalLiteral(ec, e)
	case *model.DecisionTable:
		return p.evalTable(ec, e)
	case *model.Invocation:
		return p.evalInvocation(ec, e)
	case *model.ContextExpression:
		return p.evalContext(ec, e)
	case *model.ListExpression:
		return p.evalList(ec, e)
	case *model.Relation:
		return p.evalRelation(ec, e)
	case *model.FunctionDefinition:
		return p.evalFunctionDefinition(ec, e)
	case *model.AgentDecision:
		return p.evalAgent(ec, e)
	case *model.UnknownExpression:
		ec.diagnose(diag.Warnf(diag.CodeStubEvaluated,
			"boxed expression %q is not implemented; the decision is null", e.Detail).At(e.ExprID(), ""))
		return feel.Null, nil
	default:
		return nil, diag.Errorf(diag.CodeUnsupportedExpression,
			"no evaluator registered for boxed expression kind %q", expr.Kind()).At(expr.ExprID(), "")
	}
}

func (p *Program) evalLiteral(ec *Context, e *model.LiteralExpression) (any, error) {
	if e.Text == "" {
		return feel.Null, nil
	}
	x, err := p.cfg.FEEL.Compile(e.Text)
	if err != nil {
		return nil, diag.Errorf(diag.CodeExpressionError, "%v", err).At(e.ExprID(), "")
	}
	return ec.eval(x)
}

// evalContext implements DMN's boxed context. Entries are evaluated in order
// and each becomes visible to the entries that follow it — a context is a
// sequence of let-bindings, not a simultaneous record.
//
// Per DMN 1.5 §7.3.3, a final entry with no variable name is the *result* of
// the context; otherwise the context value itself is the result.
func (p *Program) evalContext(ec *Context, e *model.ContextExpression) (any, error) {
	acc := map[string]any{}
	local := ec
	for i, entry := range e.Entries {
		v, err := p.evalExpression(local, entry.Value)
		if err != nil {
			return nil, err
		}
		name := ""
		if entry.Variable != nil {
			name = entry.Variable.Name
		}
		if name == "" {
			if i == len(e.Entries)-1 {
				return v, nil
			}
			// An unnamed entry that is not last has no binding to contribute;
			// DMN leaves this undefined, so we evaluate it for its effect on
			// diagnostics and move on.
			continue
		}
		acc[name] = v
		local = local.Derive(map[string]any{name: v})
	}
	return acc, nil
}

func (p *Program) evalList(ec *Context, e *model.ListExpression) (any, error) {
	out := make([]any, 0, len(e.Elements))
	for _, el := range e.Elements {
		v, err := p.evalExpression(ec, el)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, nil
}

// evalRelation produces a list of contexts, one per row, keyed by column name.
func (p *Program) evalRelation(ec *Context, e *model.Relation) (any, error) {
	out := make([]any, 0, len(e.Rows))
	for _, row := range e.Rows {
		rec := make(map[string]any, len(e.Columns))
		for j, col := range e.Columns {
			name := fmt.Sprintf("column_%d", j+1)
			if col != nil && col.Name != "" {
				name = col.Name
			}
			if j >= len(row) {
				rec[name] = feel.Null
				continue
			}
			v, err := p.evalExpression(ec, row[j])
			if err != nil {
				return nil, err
			}
			rec[name] = v
		}
		out = append(out, rec)
	}
	return out, nil
}

// evalFunctionDefinition turns a boxed function definition into a callable FEEL
// value. Java and PMML bodies are preserved by the reader but not executed, so
// calling one yields null plus a diagnostic rather than a crash.
func (p *Program) evalFunctionDefinition(ec *Context, e *model.FunctionDefinition) (any, error) {
	if e.FnKind != model.FunctionFEEL {
		kind := e.FnKind
		id := e.ExprID()
		return feel.NewFunctionValue(feel.Function{
			Variadic: "args",
			Help:     fmt.Sprintf("%s-bound function; not executable in Verdict", kind),
			Fn: func(map[string]any) (any, error) {
				ec.diagnose(diag.Warnf(diag.CodeStubEvaluated,
					"%s-bound function was called; Verdict returns null for it", kind).At(id, ""))
				return nil, nil
			},
		}), nil
	}
	params := make([]string, 0, len(e.Parameters))
	for _, prm := range e.Parameters {
		if prm != nil && prm.Name != "" {
			params = append(params, prm.Name)
		}
	}
	body := e.Body
	// The closure captures the defining context, so a function definition sees
	// the names visible where it was written, which is what makes a BKM able to
	// reference other BKMs it declares a knowledge requirement on.
	defining := ec
	return feel.NewFunctionValue(feel.Function{
		Params: params,
		Fn: func(args map[string]any) (any, error) {
			bindings := make(map[string]any, len(params))
			for _, name := range params {
				if v, ok := args[name]; ok {
					bindings[name] = v
				} else {
					bindings[name] = feel.Null
				}
			}
			return p.evalExpression(defining.Derive(bindings), body)
		},
	}), nil
}

// evalInvocation calls a business knowledge model with named parameters.
func (p *Program) evalInvocation(ec *Context, e *model.Invocation) (any, error) {
	bkm, ok := p.Graph.BKM(e.Called)
	if !ok {
		return nil, diag.Errorf(diag.CodeDanglingRequirement,
			"invocation calls %q, which is not a business knowledge model in this model", e.Called).
			At(e.ExprID(), "")
	}
	if bkm.Encapsulated == nil {
		ec.diagnose(diag.Warnf(diag.CodeStubEvaluated,
			"business knowledge model %q has no logic; the invocation is null", bkm.Name).At(bkm.ID, bkm.Name))
		return feel.Null, nil
	}

	child, finish := ec.trace.Child(bkm.ID, string(model.KindInvocation))
	if s, ok := child.(trace.InputSetter); ok {
		s.SetName(bkm.Name)
	}
	child.Annotate(trace.AnnBKM, bkm.Name)
	childCtx := ec.withTrace(child)

	args := make(map[string]any, len(e.Bindings))
	for i, b := range e.Bindings {
		name := ""
		if b.Parameter != nil {
			name = b.Parameter.Name
		}
		if name == "" {
			// Positional fallback: bind to the BKM's parameter at this position,
			// which is how some exporters emit single-parameter invocations.
			if i < len(bkm.Encapsulated.Parameters) && bkm.Encapsulated.Parameters[i] != nil {
				name = bkm.Encapsulated.Parameters[i].Name
			}
		}
		if name == "" {
			finish(nil, fmt.Errorf("binding %d has no parameter name", i+1))
			return nil, diag.Errorf(diag.CodeBadExpression,
				"invocation of %q has a binding with no parameter name", bkm.Name).At(e.ExprID(), "")
		}
		v, err := p.evalExpression(ec, b.Value)
		if err != nil {
			finish(nil, err)
			return nil, err
		}
		args[name] = v
	}
	if s, ok := child.(trace.InputSetter); ok {
		s.SetInputs(feel.ToGo(args).(map[string]any))
	}

	// Bind every declared parameter, defaulting the unsupplied ones to null so
	// the body sees a complete parameter list rather than unbound names.
	bindings := make(map[string]any, len(bkm.Encapsulated.Parameters))
	for _, prm := range bkm.Encapsulated.Parameters {
		if prm == nil || prm.Name == "" {
			continue
		}
		if v, ok := args[prm.Name]; ok {
			bindings[prm.Name] = v
		} else {
			bindings[prm.Name] = feel.Null
		}
	}
	for k, v := range args {
		if _, declared := bindings[k]; !declared {
			bindings[k] = v
		}
	}

	// A FEEL business knowledge model is a pure function of its arguments, so
	// two decisions that invoke it identically within one evaluation can share
	// the result. Memoisation is opt-in because the win only shows up on models
	// where a BKM is genuinely expensive or genuinely repeated, and a cache that
	// never hits is pure overhead.
	key := memoKey(bkm.ID, bindings)
	v, cached, err := ec.shared.memoise(key, func() (any, error) {
		return p.evalExpression(childCtx.Derive(bindings), bkm.Encapsulated.Body)
	})
	if cached {
		child.Annotate(trace.AnnCacheHit, true)
	}
	finish(feel.ToGo(v), err)
	return v, err
}

// memoKey builds a stable cache key from a call site and its arguments. Values
// that do not render deterministically make the key unusable, in which case the
// entry simply never hits — a wrong cache hit would be far worse than a miss.
func memoKey(id string, args map[string]any) string {
	names := make([]string, 0, len(args))
	for k := range args {
		names = append(names, k)
	}
	sort.Strings(names)

	var b strings.Builder
	b.WriteString(id)
	for _, n := range names {
		b.WriteByte(0x1f)
		b.WriteString(n)
		b.WriteByte('=')
		blob, err := json.Marshal(feel.ToGoExact(args[n]))
		if err != nil {
			return ""
		}
		b.Write(blob)
	}
	return b.String()
}
