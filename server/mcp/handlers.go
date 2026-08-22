package mcp

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/frankbardon/verdict/pkg/analyze"
	"github.com/frankbardon/verdict/pkg/diag"
	"github.com/frankbardon/verdict/pkg/explain"
	"github.com/frankbardon/verdict/pkg/trace"
	"github.com/frankbardon/verdict/pkg/verdict"
)

// The typed input and output structs below are the tool contract. Every field
// carries a jsonschema description tag, because an agent picks a tool and fills
// its arguments from these descriptions alone.
//
// Output shapes are kept flat and JSON-native — no Verdict types leak across
// the boundary — so a client needs nothing but a JSON decoder to read them.

// ListInput selects nothing; the engine lists everything it has.
type ListInput struct{}

// ListOutput is the model index.
type ListOutput struct {
	Models []ModelSummary `json:"models" jsonschema:"the decision models this engine has loaded"`
}

// ModelSummary describes one loaded model.
type ModelSummary struct {
	ID        string   `json:"id" jsonschema:"the model identifier to pass as model_id"`
	Name      string   `json:"name,omitempty" jsonschema:"the model's human-readable name"`
	Version   string   `json:"version,omitempty" jsonschema:"the model version, empty when unversioned"`
	Decisions []string `json:"decisions" jsonschema:"every decision this model defines"`
	TopLevel  []string `json:"top_level_decisions" jsonschema:"the decisions nothing else depends on: the model's outputs"`
	Services  []string `json:"services,omitempty" jsonschema:"the decision services this model exposes"`
	Inputs    []string `json:"inputs" jsonschema:"the input-data names a caller must supply"`
	Errors    int      `json:"error_count" jsonschema:"how many error-severity diagnostics this model reported at load"`
	Warnings  int      `json:"warning_count" jsonschema:"how many warnings this model reported at load"`
}

// ListModels implements verdict_list_models.
func ListModels(_ context.Context, e *verdict.Engine, _ ListInput) (ListOutput, error) {
	models := e.ListModels()
	out := ListOutput{Models: make([]ModelSummary, 0, len(models))}
	for _, m := range models {
		s := ModelSummary{
			ID: m.ID, Name: m.Name, Version: m.Version,
			TopLevel:  m.TopLevelDecisions(),
			Decisions: []string{},
			Inputs:    []string{},
		}
		for _, d := range m.Decisions() {
			s.Decisions = append(s.Decisions, d.ID)
		}
		for _, svc := range m.Services() {
			s.Services = append(s.Services, svc.ID)
		}
		for _, in := range m.Definitions().InputData {
			s.Inputs = append(s.Inputs, in.InputName())
		}
		sort.Strings(s.Inputs)
		s.Errors, s.Warnings = countDiagnostics(m.Diagnostics())
		out.Models = append(out.Models, s)
	}
	return out, nil
}

// EvaluateInput evaluates a model's top-level decisions.
type EvaluateInput struct {
	ModelID      string         `json:"model_id" jsonschema:"the model to evaluate; use verdict_list_models to discover it"`
	Version      string         `json:"version,omitempty" jsonschema:"pin a model version; omit for the most recently loaded"`
	Inputs       map[string]any `json:"inputs" jsonschema:"the model's input-data values, keyed by input name; verdict_explain lists them"`
	IncludeTrace bool           `json:"include_trace,omitempty" jsonschema:"return the full execution trace as well as the outputs"`
}

// EvaluateOutput is the result of an evaluation.
type EvaluateOutput struct {
	Outputs     map[string]any `json:"outputs" jsonschema:"the evaluated decisions' values, keyed by decision output name"`
	Summary     []string       `json:"summary" jsonschema:"one line per decision that fired, in evaluation order"`
	Diagnostics []string       `json:"diagnostics,omitempty" jsonschema:"warnings and errors reported during this evaluation"`
	Trace       any            `json:"trace,omitempty" jsonschema:"the full execution trace, present when include_trace was set"`
	DurationMS  int64          `json:"duration_ms" jsonschema:"wall time of the evaluation in milliseconds"`
}

// Evaluate implements verdict_evaluate.
func Evaluate(ctx context.Context, e *verdict.Engine, in EvaluateInput) (EvaluateOutput, error) {
	return runEvaluation(ctx, e, in.ModelID, in.Version,
		verdict.Request{Inputs: in.Inputs}, in.IncludeTrace)
}

// EvaluateDecisionInput evaluates one decision or service.
type EvaluateDecisionInput struct {
	ModelID      string         `json:"model_id" jsonschema:"the model to evaluate"`
	Version      string         `json:"version,omitempty" jsonschema:"pin a model version; omit for the most recently loaded"`
	DecisionID   string         `json:"decision_id,omitempty" jsonschema:"the decision to evaluate, by id or name; supply this or service_id"`
	ServiceID    string         `json:"service_id,omitempty" jsonschema:"the decision service to evaluate, by id or name; supply this or decision_id"`
	Inputs       map[string]any `json:"inputs" jsonschema:"the model's input-data values, keyed by input name"`
	IncludeTrace bool           `json:"include_trace,omitempty" jsonschema:"return the full execution trace as well as the outputs"`
}

// EvaluateDecision implements verdict_evaluate_decision.
func EvaluateDecision(ctx context.Context, e *verdict.Engine, in EvaluateDecisionInput) (EvaluateOutput, error) {
	if in.DecisionID == "" && in.ServiceID == "" {
		return EvaluateOutput{}, fmt.Errorf("supply either decision_id or service_id")
	}
	if in.DecisionID != "" && in.ServiceID != "" {
		return EvaluateOutput{}, fmt.Errorf("supply decision_id or service_id, not both")
	}
	req := verdict.Request{Inputs: in.Inputs, Service: in.ServiceID}
	if in.DecisionID != "" {
		req.Decisions = []string{in.DecisionID}
	}
	return runEvaluation(ctx, e, in.ModelID, in.Version, req, in.IncludeTrace)
}

func runEvaluation(ctx context.Context, e *verdict.Engine, modelID, version string, req verdict.Request, includeTrace bool) (EvaluateOutput, error) {
	if modelID == "" {
		return EvaluateOutput{}, fmt.Errorf("model_id is required; call verdict_list_models to discover it")
	}
	res, err := e.EvaluateVersion(ctx, modelID, version, req)
	if err != nil {
		// The diagnostics are usually the answer to "why did that fail", so they
		// travel with the error rather than being dropped.
		if res != nil && len(res.Diagnostics) > 0 {
			return EvaluateOutput{}, fmt.Errorf("%w\n%s", err, joinLines(renderDiagnostics(res.Diagnostics)))
		}
		return EvaluateOutput{}, err
	}
	out := EvaluateOutput{
		Outputs:     res.Outputs,
		Summary:     summarise(res.Trace),
		Diagnostics: renderDiagnostics(res.Diagnostics),
		DurationMS:  res.Duration.Milliseconds(),
	}
	if includeTrace && res.Trace != nil {
		out.Trace = res.Trace
	}
	return out, nil
}

// summarise renders the trace as one line per decision. An agent reading a
// result wants to know which rules fired without being handed the whole trace
// document, and this is that middle ground.
func summarise(t *trace.Trace) []string {
	if t == nil || t.Root == nil {
		return nil
	}
	var out []string
	for _, n := range t.Root.Children {
		line := fmt.Sprintf("%s [%s]", nameOf(n), n.NodeKind)
		if rules, ok := n.Annotations[trace.AnnMatchedRules].([]string); ok && len(rules) > 0 {
			line += fmt.Sprintf(" matched %v", rules)
		}
		if ref, ok := n.Annotations[trace.AnnSessionRef].(string); ok && ref != "" {
			line += fmt.Sprintf(" via %s", ref)
		}
		if used, ok := n.Annotations[trace.AnnFallbackUsed].(bool); ok && used {
			line += " (fell back)"
		}
		if n.Error != "" {
			line += " ✗ " + n.Error
		}
		if n.Output != nil {
			line += fmt.Sprintf(" → %v", n.Output)
		}
		out = append(out, line)
	}
	return out
}

func nameOf(n *trace.Node) string {
	if n.DecisionName != "" {
		return n.DecisionName
	}
	return n.DecisionID
}

// ExplainInput selects what to explain.
type ExplainInput struct {
	ModelID    string `json:"model_id" jsonschema:"the model to describe"`
	Version    string `json:"version,omitempty" jsonschema:"pin a model version; omit for the most recently loaded"`
	DecisionID string `json:"decision_id,omitempty" jsonschema:"the decision to describe; omit to list the model's decisions and services"`
}

// ExplainOutput is a decision's description.
type ExplainOutput struct {
	Text  string `json:"text" jsonschema:"the explanation as prose: read this first"`
	Slice any    `json:"slice,omitempty" jsonschema:"the same explanation as structured data"`
}

// Explain implements verdict_explain.
func Explain(_ context.Context, e *verdict.Engine, in ExplainInput) (ExplainOutput, error) {
	m, err := resolveModel(e, in.ModelID, in.Version)
	if err != nil {
		return ExplainOutput{}, err
	}
	if in.DecisionID == "" {
		summary, err := ListModels(context.Background(), e, ListInput{})
		if err != nil {
			return ExplainOutput{}, err
		}
		return ExplainOutput{Text: modelIndexText(m), Slice: summary}, nil
	}
	slice, err := explain.Decision(m.Definitions(), m.Graph(), m.Analysis(), in.DecisionID)
	if err != nil {
		return ExplainOutput{}, err
	}
	return ExplainOutput{Text: slice.Text(), Slice: slice}, nil
}

// modelIndexText renders the model index an agent reads when it has not been
// told which decision it wants.
func modelIndexText(m *verdict.Model) string {
	var b strings.Builder
	b.WriteString(m.ID)
	if m.Version != "" {
		fmt.Fprintf(&b, " version %s", m.Version)
	}
	b.WriteString("\n\nDecisions\n")

	top := map[string]bool{}
	for _, id := range m.TopLevelDecisions() {
		top[id] = true
	}
	for _, d := range m.Decisions() {
		marker := " "
		if top[d.ID] {
			marker = "*"
		}
		fmt.Fprintf(&b, "  %s %-28s %s\n", marker, d.ID, d.Question)
	}
	if len(m.Services()) > 0 {
		b.WriteString("\nDecision services\n")
		for _, s := range m.Services() {
			fmt.Fprintf(&b, "    %-28s outputs: %v\n", s.ID, s.OutputDecisions)
		}
	}
	b.WriteString("\n* marks a top-level decision: nothing else in the model depends on it.\n")
	return b.String()
}

// AnalyzeInput selects a model to analyse.
type AnalyzeInput struct {
	ModelID string `json:"model_id" jsonschema:"the model to analyse"`
	Version string `json:"version,omitempty" jsonschema:"pin a model version; omit for the most recently loaded"`
}

// AnalyzeOutput is the gap and overlap report.
type AnalyzeOutput struct {
	Tables      []TableFinding `json:"tables" jsonschema:"one entry per decision table"`
	Diagnostics []string       `json:"diagnostics,omitempty" jsonschema:"the findings as human-readable lines"`
	Errors      int            `json:"error_count" jsonschema:"how many findings are error-severity"`
	Warnings    int            `json:"warning_count" jsonschema:"how many findings are warnings"`
}

// TableFinding is one decision table's analysis.
type TableFinding struct {
	Decision    string   `json:"decision" jsonschema:"the decision this table belongs to"`
	HitPolicy   string   `json:"hit_policy" jsonschema:"the table's hit policy in shorthand: U, A, P, F, C, C+, C<, C>, C#, R or O"`
	Rules       int      `json:"rules" jsonschema:"how many rules the table has"`
	Analysable  bool     `json:"analysable" jsonschema:"false when the table's inputs have no enumerable domain"`
	Reason      string   `json:"reason,omitempty" jsonschema:"why the table could not be analysed"`
	Gaps        []string `json:"gaps,omitempty" jsonschema:"input combinations no rule covers"`
	Overlaps    []string `json:"overlaps,omitempty" jsonschema:"input combinations several rules cover"`
	Unreachable []string `json:"unreachable_rules,omitempty" jsonschema:"rules an earlier rule makes unreachable"`
}

// Analyze implements verdict_analyze.
func Analyze(_ context.Context, e *verdict.Engine, in AnalyzeInput) (AnalyzeOutput, error) {
	m, err := resolveModel(e, in.ModelID, in.Version)
	if err != nil {
		return AnalyzeOutput{}, err
	}
	report := m.Analysis()
	out := AnalyzeOutput{Tables: make([]TableFinding, 0, len(report.Tables))}
	for _, t := range report.Tables {
		f := TableFinding{
			Decision:  firstNonEmpty(t.DecisionName, t.DecisionID),
			HitPolicy: t.HitPolicy, Rules: t.RuleCount,
			Analysable: t.Analysable, Reason: t.Reason,
			Unreachable: t.UnreachableRules,
		}
		for _, g := range t.Gaps {
			f.Gaps = append(f.Gaps, g.String())
		}
		for _, o := range t.Overlaps {
			f.Overlaps = append(f.Overlaps, fmt.Sprintf("(%s) matches %v", o.Combination, o.Rules))
		}
		out.Tables = append(out.Tables, f)
	}
	out.Diagnostics = renderDiagnostics(m.Diagnostics())
	out.Errors, out.Warnings = countDiagnostics(m.Diagnostics())
	return out, nil
}

// LoadInput registers a model.
type LoadInput struct {
	Source string `json:"source" jsonschema:"the model document: DMN 1.5 XML or Verdict Decision JSON"`
}

// LoadOutput reports what was registered.
type LoadOutput struct {
	Model       ModelSummary `json:"model" jsonschema:"the registered model"`
	Diagnostics []string     `json:"diagnostics,omitempty" jsonschema:"findings from parsing and static analysis"`
}

// LoadModel implements verdict_load_model.
func LoadModel(_ context.Context, e *verdict.Engine, in LoadInput) (LoadOutput, error) {
	if in.Source == "" {
		return LoadOutput{}, fmt.Errorf("source is required")
	}
	m, err := e.LoadModel(verdict.FromBytes([]byte(in.Source)))
	if err != nil {
		return LoadOutput{}, err
	}
	listed, err := ListModels(context.Background(), e, ListInput{})
	if err != nil {
		return LoadOutput{}, err
	}
	out := LoadOutput{Diagnostics: renderDiagnostics(m.Diagnostics())}
	for _, s := range listed.Models {
		if s.ID == m.ID && s.Version == m.Version {
			out.Model = s
			break
		}
	}
	return out, nil
}

func resolveModel(e *verdict.Engine, modelID, version string) (*verdict.Model, error) {
	if modelID == "" {
		return nil, fmt.Errorf("model_id is required; call verdict_list_models to discover it")
	}
	m, ok := e.ModelVersion(modelID, version)
	if !ok {
		return nil, fmt.Errorf("no model %q is loaded", modelID)
	}
	return m, nil
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

func joinLines(ss []string) string {
	out := ""
	for i, s := range ss {
		if i > 0 {
			out += "\n"
		}
		out += s
	}
	return out
}

func firstNonEmpty(vs ...string) string {
	for _, v := range vs {
		if v != "" {
			return v
		}
	}
	return ""
}

var _ = analyze.Report{}
