package eval

import (
	"fmt"

	"github.com/frankbardon/verdict/pkg/diag"
	"github.com/frankbardon/verdict/pkg/dmn/model"
	"github.com/frankbardon/verdict/pkg/feel"
	"github.com/frankbardon/verdict/pkg/trace"
)

// bindServices exposes every decision service in the model as a callable FEEL
// function.
//
// DMN 1.5 §7.4 makes a decision service invocable from an expression, and that
// is how large models decompose without flattening: a service is a named,
// contract-bearing slice of the graph, and a decision in another slice invokes
// it by name rather than reaching into its internals.
//
// The parameters are the service's declared inputs — its input data and its
// input decisions — in declaration order, so an invocation binds exactly what
// the service's contract says it needs and nothing else.
func (p *Program) bindServices(ec *Context) map[string]any {
	out := make(map[string]any, len(p.Defs.DecisionServices))
	for _, svc := range p.Defs.DecisionServices {
		if svc.Name == "" {
			continue
		}
		out[svc.Name] = p.serviceFunction(ec, svc)
	}
	return out
}

func (p *Program) serviceFunction(defining *Context, svc *model.DecisionService) any {
	params := p.serviceParameters(svc)
	names := make([]string, 0, len(params))
	for _, prm := range params {
		names = append(names, prm.name)
	}

	return feel.NewFunctionValue(feel.Function{
		Params: names,
		Help:   fmt.Sprintf("decision service %q", svc.Name),
		Fn: func(args map[string]any) (any, error) {
			// Recursion through services is the one place the graph's acyclicity
			// does not bound the work: a service may invoke a service that
			// invokes it again through a literal expression, which the DRG's
			// requirement edges never see.
			depth := defining.shared.enterService()
			defer defining.shared.exitService()
			if depth > p.cfg.MaxDepth {
				return nil, diag.Errorf(diag.CodeRecursionExceeded,
					"decision service %q exceeded the maximum invocation depth of %d",
					svc.Name, p.cfg.MaxDepth).At(svc.ID, svc.Name)
			}

			inputs := make(map[string]any, len(params))
			for _, prm := range params {
				if v, ok := args[prm.name]; ok {
					inputs[prm.name] = v
					continue
				}
				inputs[prm.name] = feel.Null
			}

			child, finish := defining.trace.Child(svc.ID, "decisionService")
			if s, ok := child.(trace.InputSetter); ok {
				s.SetName(svc.Name)
				s.SetInputs(feel.ToGo(inputs).(map[string]any))
			}

			// A service call is a fresh evaluation of its slice: it sees its
			// declared inputs and nothing from the calling scope, which is what
			// makes it a contract rather than a macro.
			inner := &Context{
				ctx:    defining.ctx,
				prog:   p,
				vars:   map[string]any{},
				trace:  child,
				shared: defining.shared,
			}
			values, err := p.evaluateRoots(inner, plan{
				evaluate: append(append([]string{}, svc.OutputDecisions...), svc.EncapsulatedDecisions...),
				returns:  svc.OutputDecisions,
				entry:    svc.Name,
			}, feel.ToGo(inputs).(map[string]any))
			finish(values, err)
			if err != nil {
				return nil, err
			}

			// A single-output service returns that value directly; a multi-output
			// service returns a context, mirroring how decision tables shape
			// their results.
			if len(svc.OutputDecisions) == 1 {
				for _, v := range values {
					return feel.FromGo(v), nil
				}
				return feel.Null, nil
			}
			return feel.FromGo(values), nil
		},
	})
}

// serviceParameter is one declared input of a decision service.
type serviceParameter struct {
	name string
	id   string
}

// serviceParameters resolves a service's declared inputs to the names a caller
// binds them under.
func (p *Program) serviceParameters(svc *model.DecisionService) []serviceParameter {
	var out []serviceParameter
	seen := map[string]bool{}
	add := func(ref string) {
		elem, ok := p.Graph.Resolve(ref)
		if !ok {
			return
		}
		name := elem.ElementName()
		switch e := elem.(type) {
		case *model.InputData:
			name = e.InputName()
		case *model.Decision:
			name = e.OutputName()
		}
		if name == "" || seen[name] {
			return
		}
		seen[name] = true
		out = append(out, serviceParameter{name: name, id: elem.ElementID()})
	}
	for _, ref := range svc.InputData {
		add(ref)
	}
	for _, ref := range svc.InputDecisions {
		add(ref)
	}

	// A service that declares no inputs still needs the ones its decisions
	// require, or it could never be called with anything. Deriving them keeps a
	// loosely-authored model callable instead of silently null.
	if len(out) == 0 {
		if slice, err := p.Graph.Slice(
			append(append([]string{}, svc.OutputDecisions...), svc.EncapsulatedDecisions...)...); err == nil {
			for _, id := range slice {
				if elem, ok := p.Graph.Element(id); ok {
					if _, isInput := elem.(*model.InputData); isInput {
						add(id)
					}
				}
			}
		}
	}
	return out
}
