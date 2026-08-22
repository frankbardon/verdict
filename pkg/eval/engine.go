package eval

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/frankbardon/verdict/pkg/diag"
	"github.com/frankbardon/verdict/pkg/dmn/model"
	"github.com/frankbardon/verdict/pkg/feel"
	"github.com/frankbardon/verdict/pkg/trace"
)

// Result is the outcome of one evaluation.
type Result struct {
	// Outputs are the values of the evaluated entry points, keyed by decision
	// output name.
	Outputs map[string]any
	// Trace is the execution record. Nil when tracing is off.
	Trace *trace.Trace
	// Diagnostics are the model's load-time diagnostics plus anything the
	// evaluation itself reported.
	Diagnostics []diag.Diagnostic
	// Duration is the wall time of the evaluation.
	Duration time.Duration
}

// plan is a resolved entry point: the decisions to evaluate and the subset of
// them whose values the caller gets back.
type plan struct {
	evaluate []string
	returns  []string
	entry    string
}

// Request selects what to evaluate.
type Request struct {
	// Decisions names the decisions to evaluate, by ID or name. Empty means
	// every decision that nothing else in the model depends on — the DRG's
	// top-level outputs.
	Decisions []string
	// Service names a decision service to evaluate instead of Decisions.
	Service string
	// Inputs are the caller-supplied values, keyed by input-data name.
	Inputs map[string]any
}

// Evaluate runs a request against the program.
func (p *Program) Evaluate(ctx context.Context, req Request) (*Result, error) {
	start := p.cfg.Clock()

	pl, err := p.resolveEntry(req)
	if err != nil {
		return nil, err
	}

	rec := trace.NewRecorder(p.cfg.TraceMode, p.cfg.RedactPaths, p.cfg.Clock)
	rootWriter, finishRoot := rec.Begin(pl.entry, pl.entry, "evaluation")

	state := &evalState{}
	if p.cfg.Memoize {
		state.memo = map[string]any{}
	}
	ec := &Context{ctx: ctx, prog: p, vars: map[string]any{}, trace: rootWriter, shared: state}

	outputs, err := p.evaluateRoots(ec, pl, req.Inputs)
	finishRoot(outputs, err)

	result := &Result{
		Outputs:     outputs,
		Trace:       rec.Trace(p.Defs.ID, p.Defs.Hash, pl.entry, start),
		Diagnostics: append(append([]diag.Diagnostic(nil), p.diags...), state.diagnostics()...),
		Duration:    p.cfg.Clock().Sub(start),
	}
	if err != nil {
		return result, err
	}
	return result, nil
}

// resolveEntry turns a request into an evaluation plan.
//
// A decision service is the case that makes the distinction between "evaluate"
// and "return" matter: its encapsulated decisions must run, because its output
// decisions depend on them, but they are deliberately not part of its contract
// and must not appear in the caller's outputs.
func (p *Program) resolveEntry(req Request) (plan, error) {
	if req.Service != "" {
		svc, ok := p.Graph.Service(req.Service)
		if !ok {
			return plan{}, diag.Errorf(diag.CodeUnknownService,
				"model %q has no decision service %q", p.Defs.ID, req.Service).In(p.Defs.ID)
		}
		return plan{
			evaluate: append(append([]string{}, svc.OutputDecisions...), svc.EncapsulatedDecisions...),
			returns:  svc.OutputDecisions,
			entry:    svc.Name,
		}, nil
	}
	if len(req.Decisions) > 0 {
		for _, d := range req.Decisions {
			if _, ok := p.Graph.Decision(d); !ok {
				return plan{}, diag.Errorf(diag.CodeDanglingRequirement,
					"model %q has no decision %q", p.Defs.ID, d).In(p.Defs.ID)
			}
		}
		return plan{evaluate: req.Decisions, returns: req.Decisions, entry: joinNames(req.Decisions)}, nil
	}
	roots := p.TopLevelDecisions()
	if len(roots) == 0 {
		return plan{}, diag.Errorf(diag.CodeMissingLogic,
			"model %q defines no decisions", p.Defs.ID).In(p.Defs.ID)
	}
	return plan{evaluate: roots, returns: roots, entry: p.Defs.ID}, nil
}

// TopLevelDecisions lists the decisions no other decision requires — the
// natural outputs of the model, and the default entry point.
func (p *Program) TopLevelDecisions() []string {
	var out []string
	for _, d := range p.Defs.Decisions {
		depended := false
		for _, dep := range p.Graph.RequiredBy(d.ID) {
			if _, isDecision := p.Graph.Decision(dep); isDecision {
				depended = true
				break
			}
			if _, isService := p.Graph.Service(dep); isService {
				// A decision service referencing a decision does not make it an
				// intermediate result; the decision is still a model output.
				continue
			}
			depended = true
		}
		if !depended {
			out = append(out, d.ID)
		}
	}
	sort.Strings(out)
	return out
}

// evaluateRoots evaluates the transitive closure of roots and returns the roots'
// values.
func (p *Program) evaluateRoots(ec *Context, pl plan, inputs map[string]any) (map[string]any, error) {
	slice, err := p.Graph.Slice(pl.evaluate...)
	if err != nil {
		return nil, diag.Errorf(diag.CodeCycle, "%v", err).In(p.Defs.ID)
	}

	// Bind the caller's inputs and expose every BKM as a callable, so a literal
	// expression can reference a BKM by name exactly as DMN specifies.
	vars, err := p.bindInputs(ec, slice, inputs)
	if err != nil {
		return nil, err
	}
	ec = ec.Derive(vars)

	// Evaluate layer by layer. Everything in a layer depends only on earlier
	// layers, so a layer's members are independent and may run concurrently.
	values := map[string]any{}
	var mu sync.Mutex

	for _, layer := range p.Graph.Layers(slice) {
		work := make([]*model.Decision, 0, len(layer))
		for _, id := range layer {
			elem, _ := p.Graph.Element(id)
			d, isDecision := elem.(*model.Decision)
			if !isDecision {
				continue
			}
			if _, alreadyBound := ec.vars[d.OutputName()]; alreadyBound {
				// The caller supplied this decision's value directly, which is
				// how a decision service's input decisions are satisfied.
				continue
			}
			work = append(work, d)
		}
		if len(work) == 0 {
			continue
		}

		results, err := p.runLayer(ec, work)
		if err != nil {
			return nil, err
		}
		bindings := make(map[string]any, len(results))
		mu.Lock()
		for i, d := range work {
			for _, name := range p.bindingNames(d) {
				bindings[name] = results[i]
			}
			values[d.ID] = results[i]
		}
		mu.Unlock()
		ec = ec.Derive(bindings)
	}

	out := make(map[string]any, len(pl.returns))
	for _, ref := range pl.returns {
		d, ok := p.Graph.Decision(ref)
		if !ok {
			continue
		}
		v, ok := values[d.ID]
		if !ok {
			v, ok = ec.vars[d.OutputName()]
		}
		if !ok {
			continue
		}
		// The result is keyed the way the model refers to the decision: its
		// declared variable when it has one, otherwise the output clause name a
		// tool-exported model uses downstream.
		key := d.OutputName()
		if alias := outputClauseAlias(d); alias != "" {
			key = alias
		}
		out[key] = feel.ToGo(v)
	}
	return out, nil
}

// runLayer evaluates a set of independent decisions, concurrently when the
// configuration allows it.
func (p *Program) runLayer(ec *Context, work []*model.Decision) ([]any, error) {
	results := make([]any, len(work))

	// Trace nodes are opened here, in work order, before any evaluation starts.
	// Opening them inside each goroutine would append them in *completion*
	// order, which makes two traces of the same evaluation differ for no
	// semantic reason — and a trace you cannot diff is a trace you cannot use
	// to explain a change in behaviour.
	writers := make([]trace.Writer, len(work))
	finishers := make([]func(any, error), len(work))
	for i, d := range work {
		writers[i], finishers[i] = p.openDecisionNode(ec, d)
	}

	if !p.cfg.Parallel || len(work) == 1 {
		for i, d := range work {
			v, err := p.evalDecision(ec, d, writers[i], finishers[i])
			if err != nil {
				return nil, err
			}
			results[i] = v
		}
		return results, nil
	}

	// Cancel the remaining siblings as soon as one fails: a decision that has
	// already failed the evaluation makes its siblings' work worthless, and an
	// in-flight agent call is expensive to leave running.
	ctx, cancel := context.WithCancel(ec.ctx)
	defer cancel()

	var wg sync.WaitGroup
	errs := make([]error, len(work))
	for i, d := range work {
		wg.Add(1)
		go func(i int, d *model.Decision) {
			defer wg.Done()
			local := *ec
			local.ctx = ctx
			v, err := p.evalDecision(&local, d, writers[i], finishers[i])
			results[i], errs[i] = v, err
			if err != nil {
				cancel()
			}
		}(i, d)
	}
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			return nil, err
		}
	}
	return results, nil
}

// openDecisionNode opens the trace node for a decision and labels it.
func (p *Program) openDecisionNode(ec *Context, d *model.Decision) (trace.Writer, func(any, error)) {
	child, finish := ec.trace.Child(d.ID, kindOf(d.Logic))
	if s, ok := child.(trace.InputSetter); ok {
		s.SetName(d.Name)
		s.SetInputs(p.decisionInputs(ec, d))
	}
	return child, finish
}

// evalDecision evaluates one decision into an already-opened trace node.
func (p *Program) evalDecision(ec *Context, d *model.Decision, child trace.Writer, finish func(any, error)) (any, error) {
	if err := ec.ctx.Err(); err != nil {
		finish(nil, err)
		return nil, err
	}
	if d.Logic == nil {
		ec.diagnose(diag.Warnf(diag.CodeMissingLogic,
			"decision has no decision logic; it evaluates to null").At(d.ID, d.Name))
		finish(nil, nil)
		return feel.Null, nil
	}

	nodeCtx := ec.withTrace(child).Derive(map[string]any{currentDecisionNameKey: d.Name})
	v, err := p.evalExpression(nodeCtx, d.Logic)
	finish(feel.ToGo(v), err)
	if err != nil {
		return nil, decisionError(d, err)
	}
	return v, nil
}

// decisionInputs collects the values this decision's own requirements resolved
// to, which is what makes a trace node self-contained: a reader can see exactly
// what the decision was given without walking back up the graph.
func (p *Program) decisionInputs(ec *Context, d *model.Decision) map[string]any {
	if p.cfg.TraceMode != trace.Full {
		return nil
	}
	out := map[string]any{}
	for _, ref := range append(append([]string{}, d.RequiredInputs...), d.RequiredDecisions...) {
		elem, ok := p.Graph.Resolve(ref)
		if !ok {
			continue
		}
		name := elem.ElementName()
		switch e := elem.(type) {
		case *model.InputData:
			name = e.InputName()
		case *model.Decision:
			name = e.OutputName()
		}
		if v, ok := ec.vars[name]; ok {
			out[name] = feel.ToGo(v)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// decisionError annotates a failure with the decision it came from.
//
// A diagnostic anywhere in the chain is returned as-is rather than wrapped
// again. Without that, a failure deep inside a recursive decision service
// accumulates one "decision X: evaluating Y:" prefix per frame on the way out,
// and the message a user finally reads is a wall of repetition wrapped around
// the one sentence that mattered.
func decisionError(d *model.Decision, err error) error {
	var dg diag.Diagnostic
	if errors.As(err, &dg) {
		if dg.ElementID == "" {
			return dg.At(d.ID, d.Name)
		}
		return dg
	}
	return fmt.Errorf("decision %q: %w", d.Name, err)
}

// bindInputs builds the initial variable scope: the caller's input data plus
// every business knowledge model as a callable value.
func (p *Program) bindInputs(ec *Context, slice []string, inputs map[string]any) (map[string]any, error) {
	vars := make(map[string]any, len(inputs)+len(p.Defs.BKMs))

	// Callers may address an input by its variable name, its element name or its
	// ID; index all three so a model authored in any tool accepts the same
	// payload.
	supplied := map[string]any{}
	for k, v := range inputs {
		supplied[k] = feel.FromGo(v)
	}

	for _, id := range slice {
		elem, ok := p.Graph.Element(id)
		if !ok {
			continue
		}
		switch e := elem.(type) {
		case *model.InputData:
			name := e.InputName()
			v, found := lookupInput(supplied, name, e.Name, e.ID)
			if !found {
				ec.diagnose(diag.Warnf(diag.CodeMissingInput,
					"input data %q was not supplied; it is null for this evaluation", name).At(e.ID, e.Name))
				v = feel.Null
			}
			vars[name] = v
		case *model.BusinessKnowledgeModel:
			if e.Encapsulated == nil {
				continue
			}
			fn, err := p.evalFunctionDefinition(ec, e.Encapsulated)
			if err != nil {
				return nil, err
			}
			vars[e.Name] = fn
		}
	}

	// Every decision service is callable from FEEL, which is how a large model
	// decomposes without flattening (DMN 1.5 §7.4).
	for name, fn := range p.bindServices(ec) {
		if _, taken := vars[name]; !taken {
			vars[name] = fn
		}
	}

	// A caller may also supply a decision's value directly, which satisfies a
	// decision service's declared input decisions.
	for _, d := range p.Defs.Decisions {
		if v, ok := lookupInput(supplied, d.OutputName(), d.Name, d.ID); ok {
			vars[d.OutputName()] = v
		}
	}
	return vars, nil
}

func lookupInput(supplied map[string]any, names ...string) (any, bool) {
	for _, n := range names {
		if n == "" {
			continue
		}
		if v, ok := supplied[n]; ok {
			return v, true
		}
	}
	return nil, false
}

// bindingNames lists every name a decision's value is bound under.
//
// DMN binds a decision's result to its output variable, which is what
// OutputName reports. Several widely used editors — Camunda Modeler and dmn-js
// among them — omit the `variable` element on a single-output decision table
// and then reference the decision downstream by its *output clause* name
// instead. Binding both spellings is what makes a model exported from those
// tools evaluate without being rewritten; the alias is added only when it
// differs, and only when it cannot shadow another element's name.
func (p *Program) bindingNames(d *model.Decision) []string {
	names := []string{d.OutputName()}
	alias := outputClauseAlias(d)
	if alias == "" || alias == d.OutputName() {
		return names
	}
	// An alias must never take a name the model already uses for something
	// else: shadowing an input data element with a decision result would change
	// the meaning of every expression that reads it. Resolving to the decision
	// itself is not a collision — editors routinely give a decision and its
	// single output clause the same identifier.
	if other, taken := p.Graph.Resolve(alias); taken && other.ElementID() != d.ID {
		return names
	}
	return append(names, alias)
}

// outputClauseAlias reports the output clause name of a single-output decision
// table, or "" when the decision is anything else.
func outputClauseAlias(d *model.Decision) string {
	if d.Variable != nil && d.Variable.Name != "" {
		// The model said what the variable is called; there is nothing to infer.
		return ""
	}
	t, ok := d.Logic.(*model.DecisionTable)
	if !ok || len(t.Outputs) != 1 {
		return ""
	}
	return t.Outputs[0].Name
}

// kindOf reports the boxed-expression kind of a decision's logic, for the trace.
func kindOf(e model.Expression) string {
	if e == nil {
		return "none"
	}
	return string(e.Kind())
}

func joinNames(refs []string) string {
	if len(refs) == 1 {
		return refs[0]
	}
	return fmt.Sprintf("%v", refs)
}
