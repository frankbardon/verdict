// Package explain renders a decision in human-readable form: what it depends
// on, how it decides, and what its rules say.
//
// It exists as a library rather than as CLI formatting because the same
// rendering is what the MCP `verdict_explain` tool returns. An agent asking
// "what does this decision do?" and an engineer running `verdict explain`
// should get the same answer.
package explain

import (
	"fmt"
	"sort"
	"strings"

	"github.com/frankbardon/verdict/pkg/analyze"
	"github.com/frankbardon/verdict/pkg/dmn/model"
)

// Slice is the DRG neighbourhood of one decision, with its logic described.
type Slice struct {
	ModelID   string `json:"model_id"`
	ModelName string `json:"model_name,omitempty"`

	DecisionID   string `json:"decision_id"`
	DecisionName string `json:"decision_name,omitempty"`
	Question     string `json:"question,omitempty"`
	Answers      string `json:"allowed_answers,omitempty"`
	Description  string `json:"description,omitempty"`
	OutputName   string `json:"output_name"`
	OutputType   string `json:"output_type,omitempty"`

	// RequiredInputs are the input-data elements this decision reads, directly
	// or through the decisions it depends on.
	RequiredInputs []Item `json:"required_inputs,omitempty"`
	// RequiredDecisions are the decisions this one depends on directly.
	RequiredDecisions []Item `json:"required_decisions,omitempty"`
	// RequiredKnowledge are the business knowledge models it may invoke.
	RequiredKnowledge []Item `json:"required_knowledge,omitempty"`
	// Authorities are the knowledge sources cited as the decision's authority.
	Authorities []Item `json:"authorities,omitempty"`

	// Logic describes the boxed expression.
	Logic Logic `json:"logic"`

	// Analysis is the decision table's gap/overlap summary, when it has one.
	Analysis *TableSummary `json:"analysis,omitempty"`
}

// Item is a named DRG element reference.
type Item struct {
	ID       string `json:"id"`
	Name     string `json:"name,omitempty"`
	TypeRef  string `json:"type_ref,omitempty"`
	Kind     string `json:"kind"`
	Question string `json:"question,omitempty"`
}

// Logic describes a decision's boxed expression.
type Logic struct {
	Kind string `json:"kind"`
	// Summary is a one-line description.
	Summary string `json:"summary"`
	// Expression is the FEEL source, for a literal expression.
	Expression string `json:"expression,omitempty"`
	// Table is present for a decision table.
	Table *Table `json:"table,omitempty"`
	// Agent is present for an agentDecision.
	Agent *Agent `json:"agent,omitempty"`
}

// Table renders a decision table.
type Table struct {
	HitPolicy string   `json:"hit_policy"`
	Inputs    []Clause `json:"inputs"`
	Outputs   []Clause `json:"outputs"`
	Rules     []Rule   `json:"rules"`
}

// Clause is one input or output clause.
type Clause struct {
	Label      string `json:"label,omitempty"`
	Expression string `json:"expression,omitempty"`
	Name       string `json:"name,omitempty"`
	TypeRef    string `json:"type_ref,omitempty"`
	Values     string `json:"values,omitempty"`
	Default    string `json:"default,omitempty"`
}

// Rule is one row of a decision table.
type Rule struct {
	ID          string   `json:"id"`
	When        []string `json:"when"`
	Then        []string `json:"then"`
	Annotations []string `json:"annotations,omitempty"`
}

// Agent renders an agentDecision.
type Agent struct {
	Prompt      string            `json:"prompt_template"`
	Bindings    map[string]string `json:"input_bindings,omitempty"`
	OutputType  string            `json:"output_type,omitempty"`
	Validator   string            `json:"validator,omitempty"`
	MaxLatency  string            `json:"max_latency,omitempty"`
	MaxRetries  int               `json:"max_retries,omitempty"`
	OnFailure   string            `json:"on_failure,omitempty"`
	FallbackTo  string            `json:"fallback_decision,omitempty"`
	SessionHint string            `json:"session_hint,omitempty"`
}

// TableSummary is the decision's static-analysis result.
type TableSummary struct {
	Analysable       bool     `json:"analysable"`
	Reason           string   `json:"reason,omitempty"`
	Gaps             []string `json:"gaps,omitempty"`
	Overlaps         []string `json:"overlaps,omitempty"`
	UnreachableRules []string `json:"unreachable_rules,omitempty"`
}

// Decision builds the slice for one decision.
func Decision(defs *model.Definitions, graph *model.Graph, report *analyze.Report, ref string) (*Slice, error) {
	d, ok := graph.Decision(ref)
	if !ok {
		return nil, fmt.Errorf("explain: model %q has no decision %q", defs.ID, ref)
	}
	s := &Slice{
		ModelID:      defs.ID,
		ModelName:    defs.Name,
		DecisionID:   d.ID,
		DecisionName: d.Name,
		Question:     d.Question,
		Answers:      d.AllowedAnswers,
		Description:  d.Description,
		OutputName:   d.OutputName(),
	}
	if d.Variable != nil {
		s.OutputType = d.Variable.TypeRef
	}

	s.RequiredInputs = items(graph, transitiveInputs(graph, d))
	s.RequiredDecisions = items(graph, d.RequiredDecisions)
	s.RequiredKnowledge = items(graph, d.RequiredKnowledge)
	s.Authorities = items(graph, d.AuthorityRequirements)
	s.Logic = describe(d.Logic)

	if report != nil {
		if t, ok := report.Table(d.ID); ok {
			s.Analysis = &TableSummary{
				Analysable:       t.Analysable,
				Reason:           t.Reason,
				UnreachableRules: t.UnreachableRules,
			}
			for _, g := range t.Gaps {
				s.Analysis.Gaps = append(s.Analysis.Gaps, g.String())
			}
			for _, o := range t.Overlaps {
				s.Analysis.Overlaps = append(s.Analysis.Overlaps,
					fmt.Sprintf("(%s) matches %s", o.Combination, strings.Join(o.Rules, ", ")))
			}
		}
	}
	return s, nil
}

// transitiveInputs collects every input-data element the decision reads, either
// directly or through the decisions it depends on. That is what a caller
// actually needs to supply, which is the question `explain` is usually asked.
func transitiveInputs(graph *model.Graph, d *model.Decision) []string {
	slice, err := graph.Slice(d.ID)
	if err != nil {
		return d.RequiredInputs
	}
	var out []string
	for _, id := range slice {
		if e, ok := graph.Element(id); ok {
			if _, isInput := e.(*model.InputData); isInput {
				out = append(out, id)
			}
		}
	}
	sort.Strings(out)
	return out
}

func items(graph *model.Graph, refs []string) []Item {
	out := make([]Item, 0, len(refs))
	for _, ref := range refs {
		e, ok := graph.Resolve(ref)
		if !ok {
			out = append(out, Item{ID: ref, Kind: "unresolved"})
			continue
		}
		it := Item{ID: e.ElementID(), Name: e.ElementName()}
		switch v := e.(type) {
		case *model.InputData:
			it.Kind = "inputData"
			it.Name = v.InputName()
			if v.Variable != nil {
				it.TypeRef = v.Variable.TypeRef
			}
		case *model.Decision:
			it.Kind = "decision"
			it.Question = v.Question
			if v.Variable != nil {
				it.TypeRef = v.Variable.TypeRef
			}
		case *model.BusinessKnowledgeModel:
			it.Kind = "businessKnowledgeModel"
		case *model.KnowledgeSource:
			it.Kind = "knowledgeSource"
		case *model.DecisionService:
			it.Kind = "decisionService"
		}
		out = append(out, it)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func describe(x model.Expression) Logic {
	if x == nil {
		return Logic{Kind: "none", Summary: "this decision has no logic and evaluates to null"}
	}
	l := Logic{Kind: string(x.Kind())}
	switch v := x.(type) {
	case *model.LiteralExpression:
		l.Expression = v.Text
		l.Summary = "evaluates a FEEL expression"

	case *model.DecisionTable:
		l.Summary = fmt.Sprintf("a %d-rule decision table under hit policy %s",
			len(v.Rules), v.HitPolicy.Shorthand(v.Aggregation))
		t := &Table{HitPolicy: v.HitPolicy.Shorthand(v.Aggregation)}
		for _, in := range v.Inputs {
			t.Inputs = append(t.Inputs, Clause{
				Label: in.Label, Expression: in.Expression, TypeRef: in.TypeRef, Values: in.Values,
			})
		}
		for _, o := range v.Outputs {
			t.Outputs = append(t.Outputs, Clause{
				Label: o.Label, Name: o.Name, TypeRef: o.TypeRef, Values: o.Values, Default: o.DefaultValue,
			})
		}
		for _, r := range v.Rules {
			t.Rules = append(t.Rules, Rule{
				ID: r.ID, When: r.InputEntries, Then: r.OutputEntries, Annotations: r.Annotations,
			})
		}
		l.Table = t

	case *model.Invocation:
		l.Summary = fmt.Sprintf("invokes the business knowledge model %q", v.Called)

	case *model.ContextExpression:
		names := make([]string, 0, len(v.Entries))
		for _, e := range v.Entries {
			if e.Variable != nil && e.Variable.Name != "" {
				names = append(names, e.Variable.Name)
			}
		}
		l.Summary = fmt.Sprintf("a context of %d entries (%s)", len(v.Entries), strings.Join(names, ", "))

	case *model.ListExpression:
		l.Summary = fmt.Sprintf("a list of %d expressions", len(v.Elements))

	case *model.Relation:
		l.Summary = fmt.Sprintf("a relation of %d rows over %d columns", len(v.Rows), len(v.Columns))

	case *model.FunctionDefinition:
		l.Summary = fmt.Sprintf("a %s function of %d parameters", v.FnKind, len(v.Parameters))

	case *model.AgentDecision:
		l.Summary = "delegates to an agent, with a declared output type and failure policy"
		a := &Agent{
			Prompt:      v.PromptTemplate,
			Validator:   v.Validator,
			MaxRetries:  v.Policy.MaxRetries,
			OnFailure:   string(v.Policy.OnFailure),
			FallbackTo:  v.Policy.FallbackDecision,
			SessionHint: v.Policy.SessionHint,
		}
		if v.Policy.MaxLatency > 0 {
			a.MaxLatency = model.FormatDuration(v.Policy.MaxLatency)
		}
		if len(v.Bindings) > 0 {
			a.Bindings = make(map[string]string, len(v.Bindings))
			for _, b := range v.Bindings {
				a.Bindings[b.Name] = b.FEEL
			}
		}
		a.OutputType = describeTypeSpec(v.OutputType)
		l.Agent = a

	case *model.UnknownExpression:
		l.Summary = fmt.Sprintf("an unsupported boxed expression (%s); it evaluates to null", v.Detail)
	}
	return l
}

func describeTypeSpec(t model.TypeSpec) string {
	if t.IsAny() {
		return "Any"
	}
	var b strings.Builder
	if len(t.Enumeration) > 0 {
		b.WriteString("one of " + strings.Join(t.Enumeration, ", "))
	} else if len(t.Components) > 0 {
		keys := make([]string, 0, len(t.Components))
		for k := range t.Components {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		parts := make([]string, 0, len(keys))
		for _, k := range keys {
			parts = append(parts, k+": "+describeTypeSpec(t.Components[k]))
		}
		b.WriteString("{" + strings.Join(parts, ", ") + "}")
	} else if t.TypeRef != "" {
		b.WriteString(t.TypeRef)
	} else {
		b.WriteString("Any")
	}
	if t.Collection {
		return "list of " + b.String()
	}
	return b.String()
}

// Text renders a slice as the prose an engineer or an agent reads.
func (s *Slice) Text() string {
	var b strings.Builder
	title := s.DecisionName
	if title == "" {
		title = s.DecisionID
	}
	fmt.Fprintf(&b, "%s  (%s)\n", title, s.DecisionID)
	fmt.Fprintf(&b, "%s\n", strings.Repeat("=", len(title)+len(s.DecisionID)+4))
	if s.Question != "" {
		fmt.Fprintf(&b, "\nQuestion: %s\n", s.Question)
	}
	if s.Answers != "" {
		fmt.Fprintf(&b, "Answers:  %s\n", s.Answers)
	}
	if s.Description != "" {
		fmt.Fprintf(&b, "\n%s\n", strings.TrimSpace(s.Description))
	}
	fmt.Fprintf(&b, "\nBinds:    %s", s.OutputName)
	if s.OutputType != "" {
		fmt.Fprintf(&b, " : %s", s.OutputType)
	}
	b.WriteString("\n")

	writeItems(&b, "Reads inputs", s.RequiredInputs)
	writeItems(&b, "Depends on decisions", s.RequiredDecisions)
	writeItems(&b, "May invoke", s.RequiredKnowledge)
	writeItems(&b, "Cites authority", s.Authorities)

	fmt.Fprintf(&b, "\nLogic: %s\n", s.Logic.Summary)
	switch {
	case s.Logic.Expression != "":
		fmt.Fprintf(&b, "\n    %s\n", s.Logic.Expression)
	case s.Logic.Table != nil:
		writeTable(&b, s.Logic.Table)
	case s.Logic.Agent != nil:
		writeAgent(&b, s.Logic.Agent)
	}

	if s.Analysis != nil {
		b.WriteString("\nAnalysis\n")
		if !s.Analysis.Analysable {
			fmt.Fprintf(&b, "    not checked: %s\n", s.Analysis.Reason)
		} else {
			fmt.Fprintf(&b, "    %d gap(s), %d overlap(s), %d unreachable rule(s)\n",
				len(s.Analysis.Gaps), len(s.Analysis.Overlaps), len(s.Analysis.UnreachableRules))
			for _, g := range s.Analysis.Gaps {
				fmt.Fprintf(&b, "    gap:     (%s)\n", g)
			}
			for _, o := range s.Analysis.Overlaps {
				fmt.Fprintf(&b, "    overlap: %s\n", o)
			}
		}
	}
	return b.String()
}

func writeItems(b *strings.Builder, label string, items []Item) {
	if len(items) == 0 {
		return
	}
	fmt.Fprintf(b, "\n%s:\n", label)
	for _, it := range items {
		name := it.Name
		if name == "" {
			name = it.ID
		}
		fmt.Fprintf(b, "    %s", name)
		if it.TypeRef != "" {
			fmt.Fprintf(b, " : %s", it.TypeRef)
		}
		if it.Question != "" {
			fmt.Fprintf(b, "  — %s", it.Question)
		}
		b.WriteString("\n")
	}
}

func writeTable(b *strings.Builder, t *Table) {
	b.WriteString("\n")
	header := []string{"RULE"}
	for _, in := range t.Inputs {
		label := in.Label
		if label == "" {
			label = in.Expression
		}
		header = append(header, label)
	}
	for i, o := range t.Outputs {
		name := o.Name
		if name == "" {
			name = o.Label
		}
		if name == "" {
			name = fmt.Sprintf("output_%d", i+1)
		}
		header = append(header, "→ "+name)
	}

	rows := [][]string{header}
	for _, r := range t.Rules {
		row := []string{r.ID}
		row = append(row, padTo(r.When, len(t.Inputs))...)
		row = append(row, padTo(r.Then, len(t.Outputs))...)
		rows = append(rows, row)
	}
	writeAligned(b, rows, "    ")

	for i, o := range t.Outputs {
		if o.Values != "" {
			fmt.Fprintf(b, "\n    output %d allowed values: %s", i+1, o.Values)
		}
		if o.Default != "" {
			fmt.Fprintf(b, "\n    output %d default:        %s", i+1, o.Default)
		}
	}
	b.WriteString("\n")
}

func writeAgent(b *strings.Builder, a *Agent) {
	if len(a.Bindings) > 0 {
		b.WriteString("\n    Inputs the agent sees (and nothing else):\n")
		keys := make([]string, 0, len(a.Bindings))
		for k := range a.Bindings {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			fmt.Fprintf(b, "        %s = %s\n", k, a.Bindings[k])
		}
	}
	fmt.Fprintf(b, "\n    Must answer: %s\n", a.OutputType)
	if a.Validator != "" {
		fmt.Fprintf(b, "    Validator:   %s\n", a.Validator)
	}
	fmt.Fprintf(b, "    On failure:  %s", a.OnFailure)
	if a.FallbackTo != "" {
		fmt.Fprintf(b, " to %s", a.FallbackTo)
	}
	b.WriteString("\n")
	if a.MaxLatency != "" {
		fmt.Fprintf(b, "    Budget:      %s, %d retr%s\n", a.MaxLatency, a.MaxRetries,
			plural(a.MaxRetries, "y", "ies"))
	}
	fmt.Fprintf(b, "\n    Prompt template:\n")
	for _, line := range strings.Split(strings.TrimSpace(a.Prompt), "\n") {
		fmt.Fprintf(b, "        %s\n", line)
	}
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

func padTo(vs []string, n int) []string {
	out := make([]string, n)
	for i := range out {
		if i < len(vs) {
			out[i] = vs[i]
			if out[i] == "" {
				out[i] = "-"
			}
			continue
		}
		out[i] = "-"
	}
	return out
}

// writeAligned prints a grid with columns padded to their widest cell.
func writeAligned(b *strings.Builder, rows [][]string, indent string) {
	if len(rows) == 0 {
		return
	}
	widths := make([]int, len(rows[0]))
	for _, row := range rows {
		for i, cell := range row {
			if i < len(widths) && len(cell) > widths[i] {
				widths[i] = len(cell)
			}
		}
	}
	for _, row := range rows {
		b.WriteString(indent)
		for i, cell := range row {
			b.WriteString(cell)
			if i < len(row)-1 {
				b.WriteString(strings.Repeat(" ", widths[i]-len(cell)+2))
			}
		}
		b.WriteString("\n")
	}
}
