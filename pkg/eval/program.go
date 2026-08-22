// Package eval is Verdict's evaluator: it turns a parsed DMN model into a
// prepared program and then executes decisions, decision services and boxed
// expressions against caller inputs, recording a trace as it goes.
package eval

import (
	"fmt"
	"strings"
	"text/template"
	"time"

	"github.com/frankbardon/verdict/pkg/agent"
	"github.com/frankbardon/verdict/pkg/diag"
	"github.com/frankbardon/verdict/pkg/dmn/model"
	"github.com/frankbardon/verdict/pkg/feel"
	"github.com/frankbardon/verdict/pkg/trace"
)

// NodeEvaluator evaluates one boxed-expression kind. Registering an evaluator
// for a kind replaces the built-in handling for that kind, which is the
// extension point behind the engine's WithNodeEvaluator option.
type NodeEvaluator interface {
	// Evaluate produces the expression's value. The scope holds every name
	// currently visible, already converted to FEEL values.
	Evaluate(ec *Context, expr model.Expression) (any, error)
}

// NodeEvaluatorFunc adapts a function to NodeEvaluator.
type NodeEvaluatorFunc func(ec *Context, expr model.Expression) (any, error)

// Evaluate implements NodeEvaluator.
func (f NodeEvaluatorFunc) Evaluate(ec *Context, expr model.Expression) (any, error) {
	return f(ec, expr)
}

// Config is everything the evaluator needs that is not the model itself.
type Config struct {
	// FEEL is the expression evaluator. Required.
	FEEL *feel.Evaluator
	// Bridge answers agentDecision nodes. Nil means agent decisions fail, which
	// their failure policy then handles.
	Bridge agent.Bridge

	// TraceMode selects how much of an evaluation is recorded.
	TraceMode trace.Mode
	// RedactPaths name bindings whose values are replaced in the trace.
	RedactPaths []string

	// Strict makes the loader reject models whose static analysis reports
	// error-severity findings, and makes ambiguous hit policies fatal.
	Strict bool

	// DefaultMaxLatency bounds an agent decision that declares no policy of its
	// own. Zero means unbounded, which is almost never what a caller wants; the
	// engine's own default is applied before Config reaches here.
	DefaultMaxLatency time.Duration

	// Parallel evaluates independent decisions in the same dependency layer
	// concurrently.
	Parallel bool

	// Memoize caches the value of referentially transparent nodes on the hash of
	// their inputs, within a single evaluation. Agent decisions are never
	// memoised.
	Memoize bool

	// MaxDepth bounds recursive decision-service invocation.
	MaxDepth int

	// Clock supplies timestamps. Nil means time.Now.
	Clock func() time.Time

	// NodeEvaluators overrides the built-in handling of a boxed-expression kind.
	NodeEvaluators map[model.Kind]NodeEvaluator
}

// Program is a model prepared for evaluation: the DRG indexed, every expression
// compiled, every prompt template parsed.
//
// Preparation is where a model's expression errors surface. Doing it once at
// load time rather than on first evaluation means a model that will fail is
// rejected at deploy, not at 3am.
type Program struct {
	Defs  *model.Definitions
	Graph *model.Graph

	cfg Config

	// tables holds the compiled form of every decision table, keyed by the
	// table's identity in the model.
	tables map[*model.DecisionTable]*compiledTable
	// templates holds the parsed prompt template of every agent decision.
	templates map[*model.AgentDecision]*template.Template
	// agentValidators holds the compiled validator of every agent decision.
	agentValidators map[*model.AgentDecision]*feel.Expr

	// diags are the preparation diagnostics, replayed on every evaluation so a
	// caller always sees the model's known problems alongside its outputs.
	diags []diag.Diagnostic
}

// Diagnostics returns the preparation diagnostics.
func (p *Program) Diagnostics() []diag.Diagnostic { return p.diags }

// Config exposes the program's evaluation configuration.
func (p *Program) Config() Config { return p.cfg }

// Prepare compiles a parsed model. It returns the program together with any
// diagnostics; an error means the model cannot be evaluated at all — a cyclic
// DRG, or, in strict mode, any error-severity diagnostic.
func Prepare(defs *model.Definitions, cfg Config) (*Program, []diag.Diagnostic, error) {
	if cfg.FEEL == nil {
		return nil, nil, fmt.Errorf("eval: Config.FEEL is required")
	}
	if cfg.Clock == nil {
		cfg.Clock = time.Now
	}
	if cfg.MaxDepth <= 0 {
		cfg.MaxDepth = defaultMaxDepth
	}

	graph, problems, err := model.BuildGraph(defs)
	if err != nil {
		return nil, nil, diag.Errorf(diag.CodeCycle, "%v", err).In(defs.ID)
	}

	p := &Program{
		Defs:            defs,
		Graph:           graph,
		cfg:             cfg,
		tables:          map[*model.DecisionTable]*compiledTable{},
		templates:       map[*model.AgentDecision]*template.Template{},
		agentValidators: map[*model.AgentDecision]*feel.Expr{},
	}

	var ds diag.Set
	for _, pr := range problems {
		switch pr.Kind {
		case model.ProblemDangling:
			ds.Add(diag.Errorf(diag.CodeDanglingRequirement,
				"requirement references %q, which is not an element of this model", pr.Ref).
				At(pr.ElementID, pr.ElementName).In(defs.ID))
		case model.ProblemDuplicateID:
			ds.Add(diag.Errorf(diag.CodeDuplicateID,
				"more than one DRG element uses the id %q", pr.ElementID).
				At(pr.ElementID, pr.ElementName).In(defs.ID))
		case model.ProblemMissingID:
			ds.Add(diag.Errorf(diag.CodeDuplicateID,
				"DRG element has no id and no name").At("", pr.ElementName).In(defs.ID))
		}
	}

	for _, d := range defs.Decisions {
		p.prepareExpression(&ds, d.ID, d.Name, d.Logic)
	}
	for _, b := range defs.BKMs {
		if b.Encapsulated != nil {
			p.prepareExpression(&ds, b.ID, b.Name, b.Encapsulated)
		}
	}
	for _, s := range defs.DecisionServices {
		p.prepareService(&ds, s)
	}

	p.diags = ds.All()
	if cfg.Strict && ds.HasErrors() {
		return p, p.diags, fmt.Errorf("eval: model %q has %d error-severity diagnostics and strict mode is on: %w",
			defs.ID, len(ds.Errors()), ds.Err())
	}
	return p, p.diags, nil
}

const defaultMaxDepth = 32

// prepareExpression walks a boxed expression, compiling everything compilable
// and recording a diagnostic for everything that will not.
func (p *Program) prepareExpression(ds *diag.Set, elemID, elemName string, expr model.Expression) {
	switch e := expr.(type) {
	case nil:
		return
	case *model.LiteralExpression:
		p.compileText(ds, elemID, elemName, e.Text)
	case *model.DecisionTable:
		p.prepareTable(ds, elemID, elemName, e)
	case *model.Invocation:
		if _, ok := p.Graph.BKM(e.Called); !ok {
			ds.Add(diag.Errorf(diag.CodeDanglingRequirement,
				"invocation calls %q, which is not a business knowledge model in this model", e.Called).
				At(elemID, elemName).In(p.Defs.ID))
		}
		for _, b := range e.Bindings {
			p.prepareExpression(ds, elemID, elemName, b.Value)
		}
	case *model.ContextExpression:
		for _, entry := range e.Entries {
			p.prepareExpression(ds, elemID, elemName, entry.Value)
		}
	case *model.ListExpression:
		for _, el := range e.Elements {
			p.prepareExpression(ds, elemID, elemName, el)
		}
	case *model.Relation:
		for _, row := range e.Rows {
			for _, cell := range row {
				p.prepareExpression(ds, elemID, elemName, cell)
			}
		}
	case *model.FunctionDefinition:
		if e.FnKind == model.FunctionFEEL {
			p.prepareExpression(ds, elemID, elemName, e.Body)
		}
	case *model.AgentDecision:
		p.prepareAgent(ds, elemID, elemName, e)
	case *model.UnknownExpression:
		// Already reported by the reader; nothing to compile.
	}
}

func (p *Program) compileText(ds *diag.Set, elemID, elemName, text string) *feel.Expr {
	if strings.TrimSpace(text) == "" {
		return nil
	}
	x, err := p.cfg.FEEL.Compile(text)
	if err != nil {
		code := diag.CodeBadExpression
		var ce *feel.CompileError
		if asCompileError(err, &ce) && ce.Reason != "" {
			code = diag.CodeDialectViolation
		}
		ds.Add(diag.Errorf(code, "%v", err).At(elemID, elemName).In(p.Defs.ID).With("expression", text))
		return nil
	}
	return x
}

func (p *Program) prepareService(ds *diag.Set, s *model.DecisionService) {
	for _, ref := range append(append([]string{}, s.OutputDecisions...), s.EncapsulatedDecisions...) {
		if _, ok := p.Graph.Decision(ref); !ok {
			ds.Add(diag.Errorf(diag.CodeDanglingRequirement,
				"decision service references decision %q, which this model does not define", ref).
				At(s.ID, s.Name).In(p.Defs.ID))
		}
	}
	if len(s.OutputDecisions) == 0 {
		ds.Add(diag.Warnf(diag.CodeUnknownService,
			"decision service declares no output decisions; it returns an empty context").
			At(s.ID, s.Name).In(p.Defs.ID))
	}
}

func asCompileError(err error, out **feel.CompileError) bool {
	for err != nil {
		if ce, ok := err.(*feel.CompileError); ok {
			*out = ce
			return true
		}
		u, ok := err.(interface{ Unwrap() error })
		if !ok {
			return false
		}
		err = u.Unwrap()
	}
	return false
}
