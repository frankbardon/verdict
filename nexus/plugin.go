package nexus

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	nexusengine "github.com/frankbardon/nexus/pkg/engine"

	"github.com/frankbardon/verdict/pkg/agent"
	"github.com/frankbardon/verdict/pkg/diag"
	"github.com/frankbardon/verdict/pkg/trace"
	"github.com/frankbardon/verdict/pkg/verdict"
)

// PluginID is the Nexus plugin identifier.
const PluginID = "nexus.decision.verdict"

// pluginVersion tracks the module, not the Verdict core: the plugin's contract
// with the bus can change without the engine changing, and vice versa.
const pluginVersion = "1.0.0"

// Plugin gives a Nexus agent deterministic decision-making as a first-class
// capability.
//
// It loads decision models at boot, listens for `decision.requested` on the
// bus, evaluates, and emits `decision.completed` with the outputs and the full
// trace. The agent never leaves the process to make a rules-based decision, and
// never has to be trusted to apply the rules itself.
//
// When the loaded models contain agentDecision nodes, the plugin wires a Bridge
// back into the same engine, so a decision graph can put its genuinely
// judgemental questions to a session and keep the rest deterministic.
type Plugin struct {
	engine *verdict.Engine
	bus    nexusengine.EventBus
	logger *slog.Logger

	config pluginConfig

	mu sync.RWMutex
	// loaded maps model ID to the loaded model, so a request naming a model
	// resolves without touching the engine's own lock on the hot path.
	loaded map[string]*verdict.Model
	// defaultModel is used by a request that names none. It is set only when
	// exactly one model is loaded: guessing between several would make the
	// choice invisible at the call site.
	defaultModel string

	bridge *Bridge
	unsub  []func()
}

type pluginConfig struct {
	// Paths are model files or directories to load at boot.
	Paths []string
	// Strict refuses to load a model whose static analysis reports an error.
	Strict bool
	// Tracing is "off", "summary" or "full".
	Tracing string
	// Redact names bindings whose values are kept out of the trace.
	Redact []string
	// PublishTrace republishes each trace node on the bus as it completes.
	PublishTrace bool
	// SessionStrategy configures the agent bridge.
	SessionStrategy SessionStrategy
	// Channel prefixes the bridge's session references and trace events.
	Channel string
	// Role and Model configure the default LLM runner.
	Role  string
	Model string
	// Posture, when set, switches the bridge to a delegate-backed runner: each
	// agentDecision becomes a full sub-agent run under this posture.
	Posture string
	// DefaultMaxLatency bounds an agent decision that declares no policy.
	DefaultMaxLatency time.Duration
}

// NewPlugin constructs the plugin. Nexus's plugin registry calls it.
func NewPlugin() *Plugin {
	return &Plugin{loaded: map[string]*verdict.Model{}}
}

// ID implements engine.Plugin.
func (p *Plugin) ID() string { return PluginID }

// Name implements engine.Plugin.
func (p *Plugin) Name() string { return "Verdict decision engine" }

// Version implements engine.Plugin.
func (p *Plugin) Version() string { return pluginVersion }

// Dependencies implements engine.Plugin.
func (p *Plugin) Dependencies() []string { return nil }

// Requires implements engine.Plugin. The plugin activates nothing: a model
// without agentDecision nodes needs no LLM at all, and one that has them will
// have had a provider configured by whoever wrote it.
func (p *Plugin) Requires() []nexusengine.Requirement { return nil }

// Capabilities implements engine.Plugin.
func (p *Plugin) Capabilities() []nexusengine.Capability {
	return []nexusengine.Capability{{
		Name:        "decision.evaluate",
		Description: "evaluates DMN decision models and returns auditable, traced outcomes",
	}}
}

// Subscriptions implements engine.Plugin.
func (p *Plugin) Subscriptions() []nexusengine.EventSubscription {
	return []nexusengine.EventSubscription{
		{EventType: EventDecisionRequested},
	}
}

// Emissions implements engine.Plugin.
func (p *Plugin) Emissions() []string {
	return []string{
		EventDecisionCompleted,
		EventDecisionFailed,
		EventAgentDecisionRequested,
		EventAgentDecisionCompleted,
		EventTraceNode,
	}
}

// Init implements engine.Plugin.
func (p *Plugin) Init(ctx nexusengine.PluginContext) error {
	p.bus = ctx.Bus
	p.logger = ctx.Logger
	if p.logger == nil {
		p.logger = slog.Default()
	}
	p.config = parseConfig(ctx.Config)

	bridge, err := NewBridge(
		WithBus(ctx.Bus),
		WithSession(ctx.Session),
		WithLogger(p.logger),
		WithSessionStrategy(p.config.SessionStrategy),
		WithChannel(p.config.Channel),
		WithRunner(&LLMRunner{Bus: ctx.Bus, Role: p.config.Role, Model: p.config.Model}),
	)
	if err != nil {
		return fmt.Errorf("%s: %w", PluginID, err)
	}
	p.bridge = bridge

	opts := []verdict.Option{
		verdict.WithAgentBridge(agent.Bridge(bridge)),
		verdict.WithStrictMode(p.config.Strict),
	}
	if mode, ok := trace.ParseMode(p.config.Tracing); ok {
		opts = append(opts, verdict.WithTracing(mode))
	}
	if len(p.config.Redact) > 0 {
		opts = append(opts, verdict.WithRedactedInputs(p.config.Redact...))
	}
	if p.config.DefaultMaxLatency > 0 {
		opts = append(opts, verdict.WithDefaultMaxLatency(p.config.DefaultMaxLatency))
	}

	eng, err := verdict.NewEngine(opts...)
	if err != nil {
		return fmt.Errorf("%s: %w", PluginID, err)
	}
	p.engine = eng

	// Subscriptions() declares the contract; this is where the handler is
	// actually attached, which is the convention the contract harness checks.
	p.unsub = append(p.unsub, ctx.Bus.Subscribe(EventDecisionRequested, p.onDecisionRequested))
	return nil
}

// Ready implements engine.Plugin. Models are loaded here rather than in Init so
// that a model whose agentDecision nodes need a provider finds one already
// initialised.
func (p *Plugin) Ready() error {
	files, err := expandPaths(p.config.Paths)
	if err != nil {
		return fmt.Errorf("%s: %w", PluginID, err)
	}
	if len(files) == 0 {
		p.logger.Warn("verdict: no decision models configured; decision.requested will be refused",
			"plugin", PluginID)
		return nil
	}

	for _, f := range files {
		m, err := p.engine.LoadModel(verdict.FromFile(f))
		if err != nil {
			return fmt.Errorf("%s: loading %s: %w", PluginID, f, err)
		}
		p.mu.Lock()
		p.loaded[m.ID] = m
		p.mu.Unlock()

		errors, warnings := countDiagnostics(m.Diagnostics())
		p.logger.Info("verdict: model loaded",
			"model", m.ID, "version", m.Version, "file", f,
			"decisions", len(m.Decisions()), "errors", errors, "warnings", warnings)
		for _, d := range m.Diagnostics() {
			if d.Severity == diag.SeverityError {
				p.logger.Error("verdict: model diagnostic", "model", m.ID, "diagnostic", d.String())
			}
		}
	}

	p.mu.Lock()
	if len(p.loaded) == 1 {
		for id := range p.loaded {
			p.defaultModel = id
		}
	}
	p.mu.Unlock()
	return nil
}

// Shutdown implements engine.Plugin.
func (p *Plugin) Shutdown(context.Context) error {
	for _, u := range p.unsub {
		u()
	}
	p.unsub = nil
	return nil
}

// Engine exposes the plugin's Verdict engine, so a host that wants to evaluate
// in-process — without a round trip through the bus — can.
func (p *Plugin) Engine() *verdict.Engine { return p.engine }

// Bridge exposes the plugin's agent bridge, so a host can reuse it for a Verdict
// engine of its own.
func (p *Plugin) Bridge() *Bridge { return p.bridge }

// onDecisionRequested evaluates a model and answers on the bus.
func (p *Plugin) onDecisionRequested(ev nexusengine.Event[any]) {
	req, ok := ev.Payload.(DecisionRequest)
	if !ok {
		if ptr, isPtr := ev.Payload.(*DecisionRequest); isPtr && ptr != nil {
			req = *ptr
		} else {
			p.logger.Warn("verdict: decision.requested carried an unexpected payload",
				"type", fmt.Sprintf("%T", ev.Payload))
			return
		}
	}

	modelID, err := p.resolveModel(req.ModelID)
	if err != nil {
		p.fail(req, "", err)
		return
	}

	// Under the per-evaluation strategy each request is its own evaluation, so
	// the bridge's shared session reference resets here.
	if p.bridge != nil {
		p.bridge.Reset()
	}

	ctx := context.Background()
	res, err := p.engine.EvaluateVersion(ctx, modelID, req.Version, verdict.Request{
		Decisions: decisionList(req.Decision),
		Service:   req.Service,
		Inputs:    req.Inputs,
	})
	if err != nil {
		var t *trace.Trace
		var ds []string
		if res != nil {
			t, ds = res.Trace, renderDiagnostics(res.Diagnostics)
		}
		p.emit(EventDecisionFailed, DecisionFailed{
			RequestID: req.RequestID, ModelID: modelID,
			Error: err.Error(), Trace: t, Diagnostics: ds,
		})
		return
	}

	if p.config.PublishTrace && res.Trace != nil {
		p.publishTrace(req.RequestID, modelID, res.Trace)
	}
	p.emit(EventDecisionCompleted, DecisionCompleted{
		RequestID:   req.RequestID,
		ModelID:     modelID,
		Version:     req.Version,
		Outputs:     res.Outputs,
		Trace:       res.Trace,
		Diagnostics: renderDiagnostics(res.Diagnostics),
		Duration:    res.Duration,
	})
}

func (p *Plugin) resolveModel(requested string) (string, error) {
	p.mu.RLock()
	defer p.mu.RUnlock()

	if requested != "" {
		if _, ok := p.loaded[requested]; ok {
			return requested, nil
		}
		return "", fmt.Errorf("no model %q is loaded (loaded: %s)", requested, strings.Join(p.modelIDs(), ", "))
	}
	if p.defaultModel != "" {
		return p.defaultModel, nil
	}
	if len(p.loaded) == 0 {
		return "", fmt.Errorf("no decision models are loaded")
	}
	return "", fmt.Errorf(
		"a request must name a model when more than one is loaded (loaded: %s)",
		strings.Join(p.modelIDs(), ", "))
}

// modelIDs lists the loaded model IDs. Callers hold the read lock.
func (p *Plugin) modelIDs() []string {
	out := make([]string, 0, len(p.loaded))
	for id := range p.loaded {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

func (p *Plugin) fail(req DecisionRequest, modelID string, err error) {
	p.logger.Error("verdict: decision request failed", "model", modelID, "error", err)
	p.emit(EventDecisionFailed, DecisionFailed{
		RequestID: req.RequestID, ModelID: modelID, Error: err.Error(),
	})
}

func (p *Plugin) emit(eventType string, payload any) {
	if p.bus == nil {
		return
	}
	if err := p.bus.Emit(eventType, payload); err != nil {
		p.logger.Warn("verdict: emitting event failed", "event", eventType, "error", err)
	}
}

// publishTrace republishes every node of a trace, so a dashboard or terminal UI
// sees the decision graph rather than only its answer.
func (p *Plugin) publishTrace(requestID, modelID string, t *trace.Trace) {
	if t == nil || t.Root == nil {
		return
	}
	var walk func(n *trace.Node, depth int)
	walk = func(n *trace.Node, depth int) {
		p.emit(EventTraceNode, TraceNode{
			RequestID: requestID, ModelID: modelID, Node: n, Depth: depth,
		})
		for _, c := range n.Children {
			walk(c, depth+1)
		}
	}
	walk(t.Root, 0)
}

func decisionList(d string) []string {
	if d == "" {
		return nil
	}
	return []string{d}
}

func renderDiagnostics(ds []diag.Diagnostic) []string {
	if len(ds) == 0 {
		return nil
	}
	out := make([]string, 0, len(ds))
	for _, d := range ds {
		out = append(out, d.String())
	}
	return out
}

func countDiagnostics(ds []diag.Diagnostic) (errors, warnings int) {
	for _, d := range ds {
		switch d.Severity {
		case diag.SeverityError:
			errors++
		case diag.SeverityWarning:
			warnings++
		}
	}
	return errors, warnings
}

// expandPaths turns configured paths into model files. A directory contributes
// its model files non-recursively: a decision model directory is a flat set of
// models, and recursing would pick up fixtures and vendor copies.
func expandPaths(paths []string) ([]string, error) {
	var out []string
	for _, p := range paths {
		info, err := os.Stat(p)
		if err != nil {
			return nil, fmt.Errorf("model path %s: %w", p, err)
		}
		if !info.IsDir() {
			out = append(out, p)
			continue
		}
		entries, err := os.ReadDir(p)
		if err != nil {
			return nil, fmt.Errorf("model directory %s: %w", p, err)
		}
		for _, e := range entries {
			if e.IsDir() || !isModelFile(e.Name()) {
				continue
			}
			out = append(out, filepath.Join(p, e.Name()))
		}
	}
	sort.Strings(out)
	return out, nil
}

func isModelFile(name string) bool {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".dmn", ".xml", ".json", ".vdj":
		return true
	}
	return false
}
