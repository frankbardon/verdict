package verdict

import (
	"fmt"
	"time"

	"github.com/frankbardon/verdict/pkg/agent"
	"github.com/frankbardon/verdict/pkg/dmn/model"
	"github.com/frankbardon/verdict/pkg/eval"
	vfeel "github.com/frankbardon/verdict/pkg/feel"
	"github.com/frankbardon/verdict/pkg/trace"
)

// FEELDialect selects the expression grammar and, with it, the DMN conformance
// level the engine enforces.
type FEELDialect = vfeel.Dialect

// The two dialects. A model may pin its own level, which overrides the engine
// default for that model only.
const (
	// DialectFEEL is full FEEL: DMN Conformance Level 3.
	DialectFEEL = vfeel.FEEL
	// DialectSFEEL is the simple subset: DMN Conformance Level 2.
	DialectSFEEL = vfeel.SFEEL
)

// TracingMode selects how much of an evaluation is recorded.
type TracingMode = trace.Mode

// The tracing modes.
const (
	TracingOff     = trace.Off
	TracingSummary = trace.Summary
	TracingFull    = trace.Full
)

// AgentBridge answers agentDecision nodes.
type AgentBridge = agent.Bridge

// NodeEvaluator evaluates one boxed-expression kind.
type NodeEvaluator = eval.NodeEvaluator

// FunctionLibrary is a namespaced bundle of application FEEL functions.
type FunctionLibrary = vfeel.FunctionLibrary

// Clock supplies the current time. Injecting one makes evaluations that read
// the clock — `now()`, `today()`, trace timestamps — deterministic in tests.
type Clock func() time.Time

// Option configures an Engine.
type Option func(*options)

type options struct {
	dialect      FEELDialect
	functionLibs []FunctionLibrary
	bridge       AgentBridge
	tracing      TracingMode
	redact       []string
	strict       bool
	nodeEvals    map[model.Kind]NodeEvaluator
	maxLatency   time.Duration
	parallel     bool
	memoize      bool
	maxDepth     int
	clock        Clock
}

func defaultOptions() options {
	return options{
		dialect: DialectFEEL,
		tracing: TracingFull,
		// A decision that calls an agent with no declared latency policy still
		// needs a bound; 30s matches the configuration default and is generous
		// enough for a multi-turn session while still being a bound.
		maxLatency: 30 * time.Second,
		parallel:   true,
		nodeEvals:  map[model.Kind]NodeEvaluator{},
	}
}

func (o options) validate() error {
	if o.dialect != DialectFEEL && o.dialect != DialectSFEEL {
		return fmt.Errorf("verdict: unknown FEEL dialect %v", o.dialect)
	}
	if o.maxLatency < 0 {
		return fmt.Errorf("verdict: default max latency cannot be negative")
	}
	return nil
}

// feelOptions projects the engine options onto the expression evaluator's own.
func (o options) feelOptions() []vfeel.Option {
	opts := []vfeel.Option{}
	if len(o.functionLibs) > 0 {
		opts = append(opts, vfeel.WithLibraries(o.functionLibs...))
	}
	if o.clock != nil {
		// The same clock drives the trace timestamps and FEEL's now()/today(),
		// so an evaluation is deterministic on both axes or on neither.
		opts = append(opts, vfeel.WithClock(func() time.Time { return o.clock() }))
	}
	return opts
}

func (o options) evalConfig(fe *vfeel.Evaluator) eval.Config {
	var clock func() time.Time
	if o.clock != nil {
		clock = func() time.Time { return o.clock() }
	}
	return eval.Config{
		FEEL:              fe,
		Bridge:            o.bridge,
		TraceMode:         o.tracing,
		RedactPaths:       o.redact,
		Strict:            o.strict,
		DefaultMaxLatency: o.maxLatency,
		Parallel:          o.parallel,
		Memoize:           o.memoize,
		MaxDepth:          o.maxDepth,
		Clock:             clock,
		NodeEvaluators:    o.nodeEvals,
	}
}

// WithFEELDialect selects the engine-wide expression dialect. A model that
// declares its own conformance level overrides this for itself.
func WithFEELDialect(d FEELDialect) Option {
	return func(o *options) { o.dialect = d }
}

// WithNodeEvaluator overrides the handling of one boxed-expression kind. It is
// how an application adds a node type — or replaces a built-in one — without
// forking the engine.
func WithNodeEvaluator(kind string, ne NodeEvaluator) Option {
	return func(o *options) { o.nodeEvals[model.Kind(kind)] = ne }
}

// WithAgentBridge registers the bridge that answers agentDecision nodes.
func WithAgentBridge(b AgentBridge) Option {
	return func(o *options) { o.bridge = b }
}

// WithTracing selects how much of each evaluation is recorded.
func WithTracing(m TracingMode) Option {
	return func(o *options) { o.tracing = m }
}

// WithRedactedInputs names bindings whose values are replaced with a marker in
// the trace. Use it for anything that must not be persisted in an audit record.
func WithRedactedInputs(names ...string) Option {
	return func(o *options) { o.redact = append(o.redact, names...) }
}

// WithStrictMode makes the engine refuse to load a model whose static analysis
// reports an error — an ambiguous hit policy, a gap under a non-defaulted
// policy, an expression that does not compile.
func WithStrictMode(strict bool) Option {
	return func(o *options) { o.strict = strict }
}

// WithFunctionLibrary registers a namespaced application function library.
// Functions are reachable only as `namespace.name(...)`, so an extension can
// never shadow a DMN standard built-in.
func WithFunctionLibrary(libs ...FunctionLibrary) Option {
	return func(o *options) { o.functionLibs = append(o.functionLibs, libs...) }
}

// WithClock injects the time source, making evaluations deterministic in tests.
func WithClock(c Clock) Option {
	return func(o *options) { o.clock = c }
}

// WithDefaultMaxLatency bounds an agent decision that declares no policy of its
// own. Zero means unbounded.
func WithDefaultMaxLatency(d time.Duration) Option {
	return func(o *options) { o.maxLatency = d }
}

// WithParallelDecisions evaluates independent decisions in the same dependency
// layer concurrently. On by default.
func WithParallelDecisions(parallel bool) Option {
	return func(o *options) { o.parallel = parallel }
}

// WithMemoization caches the value of referentially transparent nodes on their
// inputs, within one evaluation. Agent decisions are never memoised, because
// their outputs are not referentially transparent.
func WithMemoization(on bool) Option {
	return func(o *options) { o.memoize = on }
}

// WithMaxDepth bounds recursive invocation through decision services.
func WithMaxDepth(n int) Option {
	return func(o *options) { o.maxDepth = n }
}
