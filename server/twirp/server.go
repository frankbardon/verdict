// Package twirp implements the Verdict RPC surface.
//
// The server is a thin adapter over pkg/verdict: it decodes an envelope,
// calls the library, and encodes the result. No decision logic lives here, and
// the library has no dependency on this package — the server is optional, and a
// Go service embeds the engine directly instead.
package twirp

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/twitchtv/twirp"

	"github.com/frankbardon/verdict/pkg/diag"
	"github.com/frankbardon/verdict/pkg/explain"
	"github.com/frankbardon/verdict/pkg/verdict"
)

// Server implements the Engine service.
type Server struct {
	engine *verdict.Engine

	// modelRoot bounds LoadModel's `path` field. Empty forbids loading by path
	// entirely, which is the default: a server that will read any file the
	// caller names is a file-disclosure primitive, not a decision engine.
	modelRoot string

	// traces retains recent traces so a caller can fetch one after the fact
	// without having asked for it inline.
	traces *traceStore
}

// Option configures a Server.
type Option func(*Server)

// WithModelRoot permits LoadModel to read files, confined to root. Requests
// naming a path outside it are refused.
func WithModelRoot(root string) Option {
	return func(s *Server) { s.modelRoot = root }
}

// WithTraceRetention sets how many traces are retained for GetTrace. Zero
// disables retention, so traces are only ever returned inline.
func WithTraceRetention(n int) Option {
	return func(s *Server) { s.traces = newTraceStore(n) }
}

// NewServer builds a server over an engine.
func NewServer(engine *verdict.Engine, opts ...Option) *Server {
	s := &Server{engine: engine, traces: newTraceStore(defaultTraceRetention)}
	for _, o := range opts {
		o(s)
	}
	return s
}

const defaultTraceRetention = 256

// LoadModel implements the Engine service.
func (s *Server) LoadModel(ctx context.Context, req *LoadModelRequest) (*LoadModelResponse, error) {
	var source verdict.ModelSource
	switch {
	case req.GetSource() != "":
		source = verdict.FromBytes([]byte(req.GetSource()))
	case req.GetPath() != "":
		path, err := s.resolvePath(req.GetPath())
		if err != nil {
			return nil, err
		}
		source = verdict.FromFile(path)
	default:
		return nil, twirp.RequiredArgumentError("source")
	}

	m, err := s.engine.LoadModel(source)
	if err != nil {
		return nil, twirp.InvalidArgumentError("source", err.Error())
	}
	return &LoadModelResponse{Model: projectModel(m)}, nil
}

// resolvePath confines a path request to the configured model root.
func (s *Server) resolvePath(requested string) (string, error) {
	if s.modelRoot == "" {
		return "", twirp.NewError(twirp.PermissionDenied,
			"this server does not load models by path; send the document in `source` instead")
	}
	root, err := filepath.Abs(s.modelRoot)
	if err != nil {
		return "", twirp.InternalErrorWith(err)
	}
	full, err := filepath.Abs(filepath.Join(root, filepath.Clean("/"+requested)))
	if err != nil {
		return "", twirp.InvalidArgumentError("path", err.Error())
	}
	// Compare with a trailing separator so `/models-secret` cannot pass a
	// prefix check against `/models`.
	if full != root && !strings.HasPrefix(full, root+string(os.PathSeparator)) {
		return "", twirp.InvalidArgumentError("path", "resolves outside the server's model root")
	}
	return full, nil
}

// ListModels implements the Engine service.
func (s *Server) ListModels(ctx context.Context, _ *ListModelsRequest) (*ListModelsResponse, error) {
	models := s.engine.ListModels()
	out := &ListModelsResponse{Models: make([]*Model, 0, len(models))}
	for _, m := range models {
		out.Models = append(out.Models, projectModel(m))
	}
	return out, nil
}

// Evaluate implements the Engine service.
func (s *Server) Evaluate(ctx context.Context, req *EvaluateRequest) (*EvaluateResponse, error) {
	inputs, err := decodeInputs(req.GetInputsJson())
	if err != nil {
		return nil, err
	}
	return s.evaluate(ctx, req.GetModelId(), req.GetVersion(), verdict.Request{Inputs: inputs},
		req.GetIncludeTrace())
}

// EvaluateDecision implements the Engine service.
func (s *Server) EvaluateDecision(ctx context.Context, req *EvaluateDecisionRequest) (*EvaluateResponse, error) {
	if req.GetDecisionId() == "" {
		return nil, twirp.RequiredArgumentError("decision_id")
	}
	inputs, err := decodeInputs(req.GetInputsJson())
	if err != nil {
		return nil, err
	}
	return s.evaluate(ctx, req.GetModelId(), req.GetVersion(),
		verdict.Request{Decisions: []string{req.GetDecisionId()}, Inputs: inputs}, req.GetIncludeTrace())
}

// EvaluateService implements the Engine service.
func (s *Server) EvaluateService(ctx context.Context, req *EvaluateServiceRequest) (*EvaluateResponse, error) {
	if req.GetServiceId() == "" {
		return nil, twirp.RequiredArgumentError("service_id")
	}
	inputs, err := decodeInputs(req.GetInputsJson())
	if err != nil {
		return nil, err
	}
	return s.evaluate(ctx, req.GetModelId(), req.GetVersion(),
		verdict.Request{Service: req.GetServiceId(), Inputs: inputs}, req.GetIncludeTrace())
}

func (s *Server) evaluate(ctx context.Context, modelID, version string, req verdict.Request, inline bool) (*EvaluateResponse, error) {
	if modelID == "" {
		return nil, twirp.RequiredArgumentError("model_id")
	}
	res, err := s.engine.EvaluateVersion(ctx, modelID, version, req)
	if err != nil {
		// A failed evaluation still carries its trace and diagnostics, and they
		// are the fastest route to the cause — so they travel with the error
		// rather than being discarded.
		terr := twirp.NewError(twirp.FailedPrecondition, err.Error())
		if res != nil {
			if blob, mErr := json.Marshal(res.Diagnostics); mErr == nil {
				terr = terr.WithMeta("diagnostics", string(blob))
			}
			if res.Trace != nil {
				if id := s.traces.put(res.Trace); id != "" {
					terr = terr.WithMeta("trace_id", id)
				}
			}
		}
		return nil, terr
	}

	out := &EvaluateResponse{
		Diagnostics: projectDiagnostics(res.Diagnostics),
		DurationNs:  res.Duration.Nanoseconds(),
	}
	blob, err := json.Marshal(res.Outputs)
	if err != nil {
		return nil, twirp.InternalErrorWith(fmt.Errorf("encoding outputs: %w", err))
	}
	out.OutputsJson = string(blob)

	if res.Trace != nil {
		out.TraceId = s.traces.put(res.Trace)
		if inline {
			traceBlob, err := json.Marshal(res.Trace)
			if err != nil {
				return nil, twirp.InternalErrorWith(fmt.Errorf("encoding trace: %w", err))
			}
			out.TraceJson = string(traceBlob)
		}
	}
	return out, nil
}

// GetTrace implements the Engine service.
func (s *Server) GetTrace(ctx context.Context, req *GetTraceRequest) (*GetTraceResponse, error) {
	if req.GetTraceId() == "" {
		return nil, twirp.RequiredArgumentError("trace_id")
	}
	t, ok := s.traces.get(req.GetTraceId())
	if !ok {
		return nil, twirp.NotFoundError("no retained trace with that id")
	}
	blob, err := json.Marshal(t)
	if err != nil {
		return nil, twirp.InternalErrorWith(err)
	}
	return &GetTraceResponse{TraceJson: string(blob)}, nil
}

// Analyze implements the Engine service.
func (s *Server) Analyze(ctx context.Context, req *AnalyzeRequest) (*AnalyzeResponse, error) {
	m, err := s.model(req.GetModelId(), req.GetVersion())
	if err != nil {
		return nil, err
	}
	blob, err := json.Marshal(m.Analysis())
	if err != nil {
		return nil, twirp.InternalErrorWith(err)
	}
	return &AnalyzeResponse{
		ReportJson:  string(blob),
		Diagnostics: projectDiagnostics(m.Diagnostics()),
	}, nil
}

// Explain implements the Engine service.
func (s *Server) Explain(ctx context.Context, req *ExplainRequest) (*ExplainResponse, error) {
	m, err := s.model(req.GetModelId(), req.GetVersion())
	if err != nil {
		return nil, err
	}
	if req.GetDecisionId() == "" {
		return explainModel(m)
	}
	slice, err := explain.Decision(m.Definitions(), m.Graph(), m.Analysis(), req.GetDecisionId())
	if err != nil {
		return nil, twirp.NotFoundError(err.Error())
	}
	blob, err := json.Marshal(slice)
	if err != nil {
		return nil, twirp.InternalErrorWith(err)
	}
	return &ExplainResponse{SliceJson: string(blob), Text: slice.Text()}, nil
}

// explainModel answers an Explain with no decision named: an index of what the
// model offers, which is what a caller meeting a model for the first time needs.
func explainModel(m *verdict.Model) (*ExplainResponse, error) {
	type entry struct {
		ID       string `json:"id"`
		Name     string `json:"name,omitempty"`
		Kind     string `json:"kind"`
		Question string `json:"question,omitempty"`
		TopLevel bool   `json:"top_level,omitempty"`
	}
	top := map[string]bool{}
	for _, id := range m.TopLevelDecisions() {
		top[id] = true
	}
	var index []entry
	var text strings.Builder
	fmt.Fprintf(&text, "%s", m.ID)
	if m.Version != "" {
		fmt.Fprintf(&text, " version %s", m.Version)
	}
	text.WriteString("\n\nDecisions\n")
	for _, d := range m.Decisions() {
		index = append(index, entry{
			ID: d.ID, Name: d.Name, Kind: "decision", Question: d.Question, TopLevel: top[d.ID],
		})
		marker := " "
		if top[d.ID] {
			marker = "*"
		}
		fmt.Fprintf(&text, "  %s %-28s %s\n", marker, d.ID, d.Question)
	}
	if len(m.Services()) > 0 {
		text.WriteString("\nDecision services\n")
		for _, svc := range m.Services() {
			index = append(index, entry{ID: svc.ID, Name: svc.Name, Kind: "decisionService"})
			fmt.Fprintf(&text, "    %-28s outputs: %v\n", svc.ID, svc.OutputDecisions)
		}
	}
	text.WriteString("\n* marks a top-level decision: nothing else in the model depends on it.\n")

	blob, err := json.Marshal(map[string]any{"model": m.ID, "version": m.Version, "elements": index})
	if err != nil {
		return nil, twirp.InternalErrorWith(err)
	}
	return &ExplainResponse{SliceJson: string(blob), Text: text.String()}, nil
}

func (s *Server) model(modelID, version string) (*verdict.Model, error) {
	if modelID == "" {
		return nil, twirp.RequiredArgumentError("model_id")
	}
	m, ok := s.engine.ModelVersion(modelID, version)
	if !ok {
		return nil, twirp.NotFoundError(fmt.Sprintf("no model %q is loaded", modelID))
	}
	return m, nil
}

func decodeInputs(raw string) (verdict.Inputs, error) {
	if strings.TrimSpace(raw) == "" {
		return verdict.Inputs{}, nil
	}
	var inputs verdict.Inputs
	if err := json.Unmarshal([]byte(raw), &inputs); err != nil {
		return nil, twirp.InvalidArgumentError("inputs_json", "must be a JSON object: "+err.Error())
	}
	return inputs, nil
}

func projectModel(m *verdict.Model) *Model {
	out := &Model{
		Id: m.ID, Name: m.Name, Version: m.Version, Hash: m.Hash,
		Diagnostics:         projectDiagnostics(m.Diagnostics()),
		TopLevelDecisionIds: m.TopLevelDecisions(),
	}
	for _, d := range m.Decisions() {
		out.DecisionIds = append(out.DecisionIds, d.ID)
	}
	for _, s := range m.Services() {
		out.ServiceIds = append(out.ServiceIds, s.ID)
	}
	return out
}

func projectDiagnostics(ds []diag.Diagnostic) []*Diagnostic {
	if len(ds) == 0 {
		return nil
	}
	out := make([]*Diagnostic, 0, len(ds))
	for _, d := range ds {
		pd := &Diagnostic{
			Code:        string(d.Code),
			Severity:    projectSeverity(d.Severity),
			Message:     d.Message,
			ModelId:     d.ModelID,
			ElementId:   d.ElementID,
			ElementName: d.ElementName,
		}
		if len(d.Detail) > 0 {
			if blob, err := json.Marshal(d.Detail); err == nil {
				pd.DetailJson = string(blob)
			}
		}
		out = append(out, pd)
	}
	return out
}

func projectSeverity(s diag.Severity) Severity {
	switch s {
	case diag.SeverityError:
		return Severity_SEVERITY_ERROR
	case diag.SeverityWarning:
		return Severity_SEVERITY_WARNING
	case diag.SeverityInfo:
		return Severity_SEVERITY_INFO
	default:
		return Severity_SEVERITY_UNSPECIFIED
	}
}

// traceStore retains recent traces in a bounded ring, so GetTrace works without
// unbounded memory growth. A trace that has aged out is a 404, not a leak.
type traceStore struct {
	mu    sync.Mutex
	cap   int
	byID  map[string]any
	order []string
	seq   uint64
}

func newTraceStore(capacity int) *traceStore {
	if capacity <= 0 {
		return &traceStore{cap: 0}
	}
	return &traceStore{cap: capacity, byID: map[string]any{}}
}

func (s *traceStore) put(t any) string {
	if s == nil || s.cap == 0 {
		return ""
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.seq++
	id := fmt.Sprintf("trace-%d", s.seq)
	s.byID[id] = t
	s.order = append(s.order, id)
	for len(s.order) > s.cap {
		delete(s.byID, s.order[0])
		s.order = s.order[1:]
	}
	return id
}

func (s *traceStore) get(id string) (any, bool) {
	if s == nil || s.cap == 0 {
		return nil, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.byID[id]
	return t, ok
}
