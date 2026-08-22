package nexus

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	nexusengine "github.com/frankbardon/nexus/pkg/engine"
	"github.com/frankbardon/nexus/pkg/events"

	"github.com/frankbardon/verdict/pkg/agent"
	"github.com/frankbardon/verdict/pkg/dmn/model"
)

// SessionStrategy decides how agentDecision invocations map onto sessions.
type SessionStrategy string

const (
	// PerDecision gives every agentDecision node its own session reference.
	// Isolation is maximal: one node's conversation cannot colour another's.
	// This is the default, and the right choice unless you have measured that
	// shared context helps.
	PerDecision SessionStrategy = "per-decision"
	// PerEvaluation shares one session across all agent nodes in a single
	// evaluation, so later nodes can see what earlier ones were asked. The
	// middle ground: useful context, bounded blast radius.
	PerEvaluation SessionStrategy = "per-evaluation"
	// Shared reuses one long-lived session across every evaluation. Cheapest
	// and best-informed, and the only strategy where one request's data can
	// reach another's prompt — use it only where that is acceptable.
	Shared SessionStrategy = "shared"
)

// Runner executes one agent question. It is the seam between the bridge's
// bookkeeping — sessions, events, transcripts, trace annotations — and the
// mechanism that actually produces an answer.
type Runner interface {
	// Run answers a question and returns the raw response text plus telemetry.
	Run(ctx context.Context, call Call) (Answer, error)
}

// Call is one question put to a session.
type Call struct {
	// DecisionID and DecisionName identify the graph node asking.
	DecisionID   string
	DecisionName string
	// Prompt is the rendered prompt from the model, already augmented with the
	// output-shape instruction.
	Prompt string
	// Inputs are the bound values the node chose to expose.
	Inputs map[string]any
	// OutputType is the declared answer shape.
	OutputType model.TypeSpec
	// Schema is OutputType rendered as JSON Schema for structured output.
	Schema map[string]any
	// SessionRef is the session this call belongs to under the configured
	// strategy.
	SessionRef string
	// Attempt is the zero-based retry attempt.
	Attempt int
}

// Answer is a runner's response.
type Answer struct {
	// Text is the raw response. The bridge parses it against the declared type.
	Text string
	// Value, when non-nil, is a pre-parsed answer that bypasses text parsing.
	// A runner with native structured output sets it.
	Value any
	// SubSessionRef identifies a sub-session the runner created, when it made
	// one. Empty means the call ran in the bridge's own session.
	SubSessionRef string
	InputTokens   int
	OutputTokens  int
	Model         string
}

// Bridge answers Verdict agentDecision nodes from a Nexus session.
type Bridge struct {
	bus     nexusengine.EventBus
	session *nexusengine.SessionWorkspace
	logger  *slog.Logger
	runner  Runner

	strategy SessionStrategy
	channel  string

	// transcriptDir is where each call's question and answer are written, so
	// the run stays forensically available after the process exits. Empty
	// disables transcript writing.
	transcriptDir string

	mu sync.Mutex
	// evaluationRef holds the shared session reference under PerEvaluation.
	// The bridge cannot see evaluation boundaries from the outside, so it
	// derives one from the first call and resets when the engine tells it to
	// via Reset.
	evaluationRef string
	callSeq       int
}

// Option configures a Bridge.
type Option func(*Bridge)

// WithBus supplies the Nexus event bus. Required: the bridge announces every
// question and answer on it, which is what makes an agent decision observable
// rather than a black box in the middle of a decision graph.
func WithBus(bus nexusengine.EventBus) Option {
	return func(b *Bridge) { b.bus = bus }
}

// WithSession supplies the session workspace the bridge belongs to.
func WithSession(s *nexusengine.SessionWorkspace) Option {
	return func(b *Bridge) { b.session = s }
}

// WithLogger supplies a logger.
func WithLogger(l *slog.Logger) Option {
	return func(b *Bridge) { b.logger = l }
}

// WithRunner replaces the answering mechanism. The default runner puts the
// question to the engine's LLM plugin with structured output; a delegate-backed
// runner turns each question into a full sub-agent run instead.
func WithRunner(r Runner) Option {
	return func(b *Bridge) { b.runner = r }
}

// WithSessionStrategy selects how invocations map onto sessions.
func WithSessionStrategy(s SessionStrategy) Option {
	return func(b *Bridge) {
		if s != "" {
			b.strategy = s
		}
	}
}

// WithChannel sets the event-name prefix the bridge publishes trace events
// under. Defaults to "verdict".
func WithChannel(prefix string) Option {
	return func(b *Bridge) { b.channel = prefix }
}

// WithTranscripts writes each call's prompt, inputs and answer under dir, so a
// decision that went wrong can be read back long after the process exited.
// Passing an empty string disables transcripts.
func WithTranscripts(dir string) Option {
	return func(b *Bridge) { b.transcriptDir = dir }
}

// NewBridge builds a Nexus-backed agent bridge.
//
// With no runner supplied it uses the engine's LLM plugin directly, which is
// the cheap path: one structured-output request per decision. Supply a
// delegate-backed runner when a decision deserves a full agent run with tools
// and a workspace.
func NewBridge(opts ...Option) (*Bridge, error) {
	b := &Bridge{
		strategy: PerDecision,
		channel:  "verdict",
		logger:   slog.Default(),
	}
	for _, o := range opts {
		o(b)
	}
	if b.bus == nil {
		return nil, fmt.Errorf("verdict/nexus: a bus is required (WithBus)")
	}
	if b.runner == nil {
		b.runner = &LLMRunner{Bus: b.bus}
	}
	if b.transcriptDir == "" && b.session != nil {
		b.transcriptDir = filepath.Join(b.session.PluginDir(PluginID), "decisions")
	}
	return b, nil
}

// Reset clears the per-evaluation session reference. A host that evaluates
// several requests through one bridge calls it between them; under any strategy
// other than PerEvaluation it does nothing.
func (b *Bridge) Reset() {
	b.mu.Lock()
	b.evaluationRef = ""
	b.mu.Unlock()
}

// Invoke implements agent.Bridge.
func (b *Bridge) Invoke(ctx context.Context, req agent.Request) (agent.Response, error) {
	callID := nexusengine.GenerateID()
	sessionRef := b.sessionRef(req)

	prompt := b.augmentPrompt(req)
	call := Call{
		DecisionID:   req.DecisionID,
		DecisionName: req.DecisionName,
		Prompt:       prompt,
		Inputs:       req.Inputs,
		OutputType:   req.OutputType,
		Schema:       jsonSchema(req.OutputType),
		SessionRef:   sessionRef,
		Attempt:      req.Attempt,
	}

	b.emit(EventAgentDecisionRequested, AgentDecisionRequested{
		CallID:       callID,
		DecisionID:   req.DecisionID,
		DecisionName: req.DecisionName,
		Prompt:       prompt,
		Inputs:       req.Inputs,
		OutputType:   describeType(req.OutputType),
		Attempt:      req.Attempt,
		SessionRef:   sessionRef,
	})

	answer, err := b.runner.Run(ctx, call)
	if err != nil {
		b.emit(EventAgentDecisionCompleted, AgentDecisionCompleted{
			CallID: callID, DecisionID: req.DecisionID, DecisionName: req.DecisionName,
			SessionRef: sessionRef, Error: err.Error(),
		})
		b.writeTranscript(callID, req, prompt, "", err)
		return agent.Response{}, err
	}

	ref := sessionRef
	if answer.SubSessionRef != "" {
		ref = answer.SubSessionRef
	}

	value := answer.Value
	if value == nil {
		value, err = parseAnswer(answer.Text, req.OutputType)
		if err != nil {
			b.emit(EventAgentDecisionCompleted, AgentDecisionCompleted{
				CallID: callID, DecisionID: req.DecisionID, DecisionName: req.DecisionName,
				SessionRef: ref, Error: err.Error(),
			})
			b.writeTranscript(callID, req, prompt, answer.Text, err)
			return agent.Response{}, err
		}
	}

	b.emit(EventAgentDecisionCompleted, AgentDecisionCompleted{
		CallID: callID, DecisionID: req.DecisionID, DecisionName: req.DecisionName,
		Value: value, SessionRef: ref,
		InputTokens: answer.InputTokens, OutputTokens: answer.OutputTokens, Model: answer.Model,
	})
	b.writeTranscript(callID, req, prompt, answer.Text, nil)

	if req.Trace != nil && req.Trace.Enabled() {
		req.Trace.Annotate("nexus_session", ref)
	}

	return agent.Response{
		Value:      value,
		SessionRef: ref,
		Tokens: agent.TokenAccounting{
			Input:  answer.InputTokens,
			Output: answer.OutputTokens,
			Total:  answer.InputTokens + answer.OutputTokens,
			Model:  answer.Model,
		},
	}, nil
}

// sessionRef derives the session reference for a call under the configured
// strategy. The reference is a URI into the Nexus session tree, which is what
// makes a decision trace navigable back to the conversation that produced it.
func (b *Bridge) sessionRef(req agent.Request) string {
	base := "nexus://sessions/unbound"
	if b.session != nil {
		base = "nexus://sessions/" + b.session.ID
	}

	// A model may pin a session explicitly; an explicit hint always wins,
	// because the modeller asked for it by name.
	if req.SessionHint != "" {
		return base + "/" + b.channel + "/" + req.SessionHint
	}

	switch b.strategy {
	case Shared:
		return base + "/" + b.channel
	case PerEvaluation:
		b.mu.Lock()
		defer b.mu.Unlock()
		if b.evaluationRef == "" {
			b.callSeq++
			b.evaluationRef = fmt.Sprintf("%s/%s/eval-%d", base, b.channel, b.callSeq)
		}
		return b.evaluationRef
	default:
		return base + "/" + b.channel + "/" + req.DecisionID
	}
}

// augmentPrompt appends the output-shape instruction to the model's own prompt.
//
// The declared type is already enforced twice — as a provider schema and by the
// engine on the way back — but a model told what is wanted in words answers
// correctly far more often than one left to infer it from a schema, and a
// rejected answer costs a whole retry.
func (b *Bridge) augmentPrompt(req agent.Request) string {
	if req.OutputType.IsAny() {
		return req.Prompt
	}
	var sb strings.Builder
	sb.WriteString(strings.TrimRight(req.Prompt, "\n"))
	sb.WriteString("\n\nAnswer with ")
	sb.WriteString(describeType(req.OutputType))
	sb.WriteString(". Return only the answer, with no explanation or preamble.")
	if req.Attempt > 0 {
		fmt.Fprintf(&sb, "\n\nA previous attempt was rejected because it did not match this shape. "+
			"This is attempt %d.", req.Attempt+1)
	}
	return sb.String()
}

func (b *Bridge) emit(eventType string, payload any) {
	if b.bus == nil {
		return
	}
	if err := b.bus.Emit(eventType, payload); err != nil {
		b.logger.Warn("verdict/nexus: emitting event failed", "event", eventType, "error", err)
	}
}

// writeTranscript records a call in the session workspace. Failures are logged
// and swallowed: losing an audit copy must never fail a decision that otherwise
// succeeded.
func (b *Bridge) writeTranscript(callID string, req agent.Request, prompt, answer string, callErr error) {
	if b.transcriptDir == "" {
		return
	}
	dir := filepath.Join(b.transcriptDir, sanitise(req.DecisionID))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		b.logger.Warn("verdict/nexus: cannot create transcript directory", "dir", dir, "error", err)
		return
	}
	record := map[string]any{
		"call_id":       callID,
		"decision_id":   req.DecisionID,
		"decision_name": req.DecisionName,
		"attempt":       req.Attempt,
		"at":            time.Now().UTC().Format(time.RFC3339Nano),
		"prompt":        prompt,
		"inputs":        req.Inputs,
		"answer":        answer,
	}
	if callErr != nil {
		record["error"] = callErr.Error()
	}
	blob, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		return
	}
	path := filepath.Join(dir, callID+".json")
	if err := os.WriteFile(path, append(blob, '\n'), 0o644); err != nil {
		b.logger.Warn("verdict/nexus: cannot write transcript", "path", path, "error", err)
	}
}

// sanitise makes a decision ID safe as a path element.
func sanitise(s string) string {
	if s == "" {
		return "unnamed"
	}
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_', r == '.':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	return b.String()
}

// LLMRunner answers a decision with a single structured-output request to the
// engine's LLM plugin. It is the default runner: cheap, synchronous, and
// sufficient for the classification-shaped questions most agentDecision nodes
// ask.
type LLMRunner struct {
	// Bus is the engine bus. Required.
	Bus nexusengine.EventBus
	// Role selects the model role ("reasoning", "balanced", "quick"). Empty
	// lets the provider's default apply.
	Role string
	// Model pins an explicit model ID, taking precedence over Role.
	Model string
	// MaxTokens bounds the response. Zero uses the provider default.
	MaxTokens int
	// Temperature overrides the provider default. A decision node wants the
	// least creative answer the model can give, so leaving this at a low value
	// is usually right.
	Temperature *float64
	// System, when set, replaces the default system message.
	System string
}

// Run implements Runner.
func (r *LLMRunner) Run(ctx context.Context, call Call) (Answer, error) {
	if r.Bus == nil {
		return Answer{}, fmt.Errorf("verdict/nexus: LLMRunner needs a bus")
	}

	system := r.System
	if system == "" {
		system = defaultSystemPrompt
	}

	req := events.LLMRequest{
		SchemaVersion: events.LLMRequestVersion,
		Role:          r.Role,
		Model:         r.Model,
		MaxTokens:     r.MaxTokens,
		Temperature:   r.Temperature,
		Messages: []events.Message{
			{Role: "system", Content: system},
			{Role: "user", Content: call.Prompt},
		},
		Metadata: map[string]any{
			"_source":              PluginID,
			"verdict_decision_id":  call.DecisionID,
			"verdict_session_ref":  call.SessionRef,
			"verdict_attempt":      call.Attempt,
			"verdict_output_shape": describeType(call.OutputType),
		},
		Tags: map[string]string{
			"source_plugin": PluginID,
			"task_kind":     "decision",
		},
	}
	if !call.OutputType.IsAny() {
		req.ResponseFormat = &events.ResponseFormat{
			Type:   "json_schema",
			Name:   "verdict_decision",
			Schema: call.Schema,
			Strict: true,
		}
	}

	resp, err := syncLLM(ctx, r.Bus, req)
	if err != nil {
		return Answer{}, fmt.Errorf("verdict/nexus: decision %q: %w", call.DecisionID, err)
	}
	return Answer{
		Text:         resp.Content,
		InputTokens:  resp.Usage.PromptTokens,
		OutputTokens: resp.Usage.CompletionTokens,
		Model:        resp.Model,
	}, nil
}

// defaultSystemPrompt frames the model's role. It is deliberately narrow: the
// node has already been given its question, its inputs and its answer shape by
// the decision model, and anything else this prompt adds is an opportunity to
// contradict them.
const defaultSystemPrompt = "You are answering one question inside a decision model. " +
	"Everything you need is in the message; do not ask for more. " +
	"Answer in exactly the shape requested and nothing else."
