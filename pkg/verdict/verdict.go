// Package verdict is the public API of the Verdict decision engine: a
// DMN-aligned evaluator for Go with an agent-backed node kind.
//
// The library is the source of truth. The optional `verdict serve` surface wraps this
// package; nothing here depends on it. A Go service — including Nexus — imports
// verdict directly and never talks to a server over the wire.
//
//	engine := verdict.NewEngine(verdict.WithAgentBridge(bridge))
//	model, err := engine.LoadModel(verdict.FromFile("loan_approval.dmn"))
//	result, err := engine.Evaluate(ctx, model.ID, verdict.Inputs{"Applicant": app})
package verdict

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/frankbardon/verdict/pkg/analyze"
	"github.com/frankbardon/verdict/pkg/diag"
	"github.com/frankbardon/verdict/pkg/dmn/model"
	"github.com/frankbardon/verdict/pkg/eval"
	vfeel "github.com/frankbardon/verdict/pkg/feel"
	"github.com/frankbardon/verdict/pkg/trace"
)

// Inputs are the caller-supplied values for a model's input data, keyed by
// input name.
type Inputs map[string]any

// Result is the outcome of an evaluation.
type Result struct {
	// Outputs are the values of the evaluated entry points.
	Outputs map[string]any
	// Trace is the execution record; nil when tracing is off.
	Trace *trace.Trace
	// Diagnostics are the model's load-time findings plus anything this
	// evaluation reported.
	Diagnostics []diag.Diagnostic
	// Duration is the wall time of the evaluation.
	Duration time.Duration
}

// Model is a loaded, prepared decision model.
type Model struct {
	// ID is the model's DMN id, or its name when the document has no id.
	ID string
	// Name is the document's name.
	Name string
	// Version is the document's version; empty when unversioned.
	Version string
	// Hash is the content address of the source document.
	Hash string

	program *eval.Program
	report  *analyze.Report
	diags   []diag.Diagnostic
}

// Definitions exposes the parsed DMN document.
func (m *Model) Definitions() *model.Definitions { return m.program.Defs }

// Graph exposes the indexed Decision Requirements Graph.
func (m *Model) Graph() *model.Graph { return m.program.Graph }

// Diagnostics returns the model's load-time diagnostics, including static
// analysis findings.
func (m *Model) Diagnostics() []diag.Diagnostic { return m.diags }

// Analysis returns the static-analysis report: gaps, overlaps and completeness
// per decision table.
func (m *Model) Analysis() *analyze.Report { return m.report }

// Decisions lists the model's decisions in document order.
func (m *Model) Decisions() []*model.Decision { return m.program.Defs.Decisions }

// Services lists the model's decision services in document order.
func (m *Model) Services() []*model.DecisionService { return m.program.Defs.DecisionServices }

// TopLevelDecisions lists the decisions nothing else depends on — the model's
// natural outputs and the default evaluation entry point.
func (m *Model) TopLevelDecisions() []string { return m.program.TopLevelDecisions() }

// Engine holds a registry of loaded models, the FEEL evaluator, the node-kind
// registry and any registered bridges. Engines are safe for concurrent use and
// are designed to be shared across goroutines.
type Engine struct {
	opts options

	feel *vfeel.Evaluator

	mu sync.RWMutex
	// models is keyed by model ID; each entry holds every loaded version.
	models map[string]*versionSet
}

// versionSet holds the concurrently loaded versions of one model ID.
type versionSet struct {
	// byVersion is keyed by the model's declared version. The empty string is
	// the slot for an unversioned document.
	byVersion map[string]*Model
	// latest is the version Evaluate resolves to when the caller pins nothing.
	latest string
}

// NewEngine builds an engine. It returns an error only when the options
// themselves are contradictory — a bad FEEL dialect, a function library that
// would shadow a standard built-in.
func NewEngine(opts ...Option) (*Engine, error) {
	cfg := defaultOptions()
	for _, o := range opts {
		o(&cfg)
	}
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	fe, err := vfeel.NewEvaluator(cfg.dialect, cfg.feelOptions()...)
	if err != nil {
		return nil, fmt.Errorf("verdict: %w", err)
	}
	return &Engine{opts: cfg, feel: fe, models: map[string]*versionSet{}}, nil
}

// MustNewEngine is NewEngine, panicking on a configuration error. It exists for
// package-level engine variables in applications whose options are constants.
func MustNewEngine(opts ...Option) *Engine {
	e, err := NewEngine(opts...)
	if err != nil {
		panic(err)
	}
	return e
}

// LoadModel parses, analyses and registers a model.
//
// Loading is idempotent by content: loading the same bytes twice returns the
// same *Model without reparsing, which is what makes a hot-reload watcher cheap.
func (e *Engine) LoadModel(src ModelSource) (*Model, error) {
	defs, readDiags, err := src.read()
	if err != nil {
		return nil, err
	}

	if existing := e.findByHash(defs.Hash); existing != nil {
		return existing, nil
	}

	dialect := e.opts.dialect
	if defs.Level == model.Level2 {
		dialect = vfeel.SFEEL
	} else if defs.Level == model.Level3 {
		dialect = vfeel.FEEL
	}
	fe := e.feel
	if dialect != e.opts.dialect {
		// The document pins a conformance level that differs from the engine
		// default, so it gets its own evaluator rather than silently running
		// under the wrong dialect.
		fe, err = vfeel.NewEvaluator(dialect, e.opts.feelOptions()...)
		if err != nil {
			return nil, fmt.Errorf("verdict: model %q declares conformance level %s: %w", defs.ID, defs.Level, err)
		}
	}

	program, prepDiags, err := eval.Prepare(defs, e.opts.evalConfig(fe))
	if err != nil {
		return nil, err
	}

	report := analyze.Analyze(program, analyze.Options{Strict: e.opts.strict})

	all := append(append([]diag.Diagnostic{}, readDiags...), prepDiags...)
	all = append(all, report.Diagnostics...)

	if e.opts.strict {
		var ds diag.Set
		ds.Add(all...)
		if ds.HasErrors() {
			return nil, fmt.Errorf("verdict: model %q failed strict validation: %w", defs.ID, ds.Err())
		}
	}

	m := &Model{
		ID:      defs.ID,
		Name:    defs.Name,
		Version: defs.Version,
		Hash:    defs.Hash,
		program: program,
		report:  report,
		diags:   all,
	}
	e.register(m)
	return m, nil
}

func (e *Engine) register(m *Model) {
	e.mu.Lock()
	defer e.mu.Unlock()
	set := e.models[m.ID]
	if set == nil {
		set = &versionSet{byVersion: map[string]*Model{}}
		e.models[m.ID] = set
	}
	set.byVersion[m.Version] = m
	set.latest = m.Version
}

func (e *Engine) findByHash(hash string) *Model {
	if hash == "" {
		return nil
	}
	e.mu.RLock()
	defer e.mu.RUnlock()
	for _, set := range e.models {
		for _, m := range set.byVersion {
			if m.Hash == hash {
				return m
			}
		}
	}
	return nil
}

// Model resolves a model by ID, returning the most recently loaded version.
func (e *Engine) Model(modelID string) (*Model, bool) {
	return e.ModelVersion(modelID, "")
}

// ModelVersion resolves a specific version of a model. An empty version means
// the most recently loaded one.
func (e *Engine) ModelVersion(modelID, version string) (*Model, bool) {
	e.mu.RLock()
	defer e.mu.RUnlock()
	set := e.models[modelID]
	if set == nil {
		return nil, false
	}
	if version == "" {
		version = set.latest
	}
	m, ok := set.byVersion[version]
	return m, ok
}

// ListModels returns every loaded model, ordered by ID then version.
func (e *Engine) ListModels() []*Model {
	e.mu.RLock()
	defer e.mu.RUnlock()
	var out []*Model
	for _, set := range e.models {
		for _, m := range set.byVersion {
			out = append(out, m)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].ID != out[j].ID {
			return out[i].ID < out[j].ID
		}
		return out[i].Version < out[j].Version
	})
	return out
}

// Unload removes a model version from the registry. An empty version removes
// every version of the ID.
func (e *Engine) Unload(modelID, version string) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	set := e.models[modelID]
	if set == nil {
		return false
	}
	if version == "" {
		delete(e.models, modelID)
		return true
	}
	if _, ok := set.byVersion[version]; !ok {
		return false
	}
	delete(set.byVersion, version)
	if len(set.byVersion) == 0 {
		delete(e.models, modelID)
		return true
	}
	if set.latest == version {
		for v := range set.byVersion {
			set.latest = v
			break
		}
	}
	return true
}

// Evaluate runs a model's top-level decisions.
func (e *Engine) Evaluate(ctx context.Context, modelID string, inputs Inputs) (*Result, error) {
	return e.evaluate(ctx, modelID, "", eval.Request{Inputs: inputs})
}

// EvaluateDecision runs a single decision and its dependencies.
func (e *Engine) EvaluateDecision(ctx context.Context, modelID, decisionID string, inputs Inputs) (*Result, error) {
	return e.evaluate(ctx, modelID, "", eval.Request{Decisions: []string{decisionID}, Inputs: inputs})
}

// EvaluateService runs a named decision service.
func (e *Engine) EvaluateService(ctx context.Context, modelID, serviceID string, inputs Inputs) (*Result, error) {
	return e.evaluate(ctx, modelID, "", eval.Request{Service: serviceID, Inputs: inputs})
}

// EvaluateVersion pins the model version an evaluation runs against.
func (e *Engine) EvaluateVersion(ctx context.Context, modelID, version string, req Request) (*Result, error) {
	return e.evaluate(ctx, modelID, version, eval.Request{
		Decisions: req.Decisions, Service: req.Service, Inputs: req.Inputs,
	})
}

// Request is the general form of an evaluation request, for callers that need
// to pin a version or name several decisions at once.
type Request struct {
	// Decisions names the decisions to evaluate. Empty means the model's
	// top-level decisions.
	Decisions []string
	// Service names a decision service to evaluate instead of Decisions.
	Service string
	// Inputs are the caller-supplied input-data values.
	Inputs Inputs
}

func (e *Engine) evaluate(ctx context.Context, modelID, version string, req eval.Request) (*Result, error) {
	m, ok := e.ModelVersion(modelID, version)
	if !ok {
		return nil, fmt.Errorf("verdict: no model %q is loaded", modelID)
	}
	res, err := m.program.Evaluate(ctx, req)
	if res == nil {
		return nil, err
	}
	return &Result{
		Outputs:     res.Outputs,
		Trace:       res.Trace,
		Diagnostics: res.Diagnostics,
		Duration:    res.Duration,
	}, err
}

// Analyze returns a model's static-analysis report.
func (e *Engine) Analyze(modelID string) (*analyze.Report, error) {
	m, ok := e.Model(modelID)
	if !ok {
		return nil, fmt.Errorf("verdict: no model %q is loaded", modelID)
	}
	return m.report, nil
}
