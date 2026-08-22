package vdj

import (
	"encoding/json"
	"fmt"
	"io"
	"time"

	"github.com/frankbardon/verdict/pkg/dmn/model"
)

// Marshal renders a model as indented Verdict Decision JSON.
func Marshal(defs *model.Definitions) ([]byte, error) {
	doc, err := Project(defs)
	if err != nil {
		return nil, err
	}
	return json.MarshalIndent(doc, "", "  ")
}

// Write renders a model as VDJ to w.
func Write(w io.Writer, defs *model.Definitions) error {
	b, err := Marshal(defs)
	if err != nil {
		return err
	}
	if _, err := w.Write(b); err != nil {
		return err
	}
	_, err = w.Write([]byte("\n"))
	return err
}

// Project turns a model into its VDJ document form. It is the inverse of the
// reader: `Parse(Marshal(d))` reproduces d.
func Project(defs *model.Definitions) (*Document, error) {
	if defs == nil {
		return nil, fmt.Errorf("vdj: nil definitions")
	}
	doc := &Document{
		VDJ:                Version,
		ID:                 defs.ID,
		Name:               defs.Name,
		Namespace:          defs.Namespace,
		Description:        defs.Description,
		Version:            defs.Version,
		ExpressionLanguage: defs.ExpressionLanguage,
		Exporter:           defs.Exporter,
		ExporterVersion:    defs.ExporterVersion,
	}
	if defs.Level != model.LevelUnspecified {
		doc.ConformanceLevel = defs.Level.String()
	}
	for _, it := range defs.ItemDefinitions {
		doc.ItemDefinitions = append(doc.ItemDefinitions, projectItemDefinition(it))
	}
	for _, in := range defs.InputData {
		doc.InputData = append(doc.InputData, &InputData{
			ID: in.ID, Name: in.Name, Description: in.Description,
			Variable: projectInformationItem(in.Variable),
		})
	}
	for _, d := range defs.Decisions {
		logic, err := projectExpression(d.Logic)
		if err != nil {
			return nil, fmt.Errorf("vdj: decision %q: %w", d.Name, err)
		}
		doc.Decisions = append(doc.Decisions, &Decision{
			ID: d.ID, Name: d.Name, Description: d.Description,
			Question: d.Question, AllowedAnswers: d.AllowedAnswers,
			Variable:          projectInformationItem(d.Variable),
			RequiredDecisions: d.RequiredDecisions,
			RequiredInputs:    d.RequiredInputs,
			RequiredKnowledge: d.RequiredKnowledge,
			Authorities:       d.AuthorityRequirements,
			Logic:             logic,
		})
	}
	for _, b := range defs.BKMs {
		var enc *Expression
		if b.Encapsulated != nil {
			var err error
			if enc, err = projectExpression(b.Encapsulated); err != nil {
				return nil, fmt.Errorf("vdj: business knowledge model %q: %w", b.Name, err)
			}
		}
		doc.BKMs = append(doc.BKMs, &BKM{
			ID: b.ID, Name: b.Name, Description: b.Description,
			Variable:          projectInformationItem(b.Variable),
			RequiredKnowledge: b.RequiredKnowledge,
			RequiredInputs:    b.RequiredInputs,
			RequiredDecisions: b.RequiredDecisions,
			Encapsulated:      enc,
		})
	}
	for _, k := range defs.KnowledgeSources {
		doc.KnowledgeSources = append(doc.KnowledgeSources, &KnowledgeSource{
			ID: k.ID, Name: k.Name, Description: k.Description,
			Type: k.Type, LocationURI: k.LocationURI, Owner: k.Owner,
		})
	}
	for _, s := range defs.DecisionServices {
		doc.DecisionServices = append(doc.DecisionServices, &DecisionService{
			ID: s.ID, Name: s.Name, Description: s.Description,
			Variable:              projectInformationItem(s.Variable),
			OutputDecisions:       s.OutputDecisions,
			EncapsulatedDecisions: s.EncapsulatedDecisions,
			InputDecisions:        s.InputDecisions,
			InputData:             s.InputData,
		})
	}
	return doc, nil
}

func projectItemDefinition(it *model.ItemDefinition) *ItemDefinition {
	if it == nil {
		return nil
	}
	out := &ItemDefinition{
		ID: it.ID, Name: it.Name, Description: it.Description,
		TypeRef: it.TypeRef, IsCollection: it.IsCollection,
		AllowedValues: it.AllowedValues, TypeConstraint: it.TypeConstraint,
	}
	for _, c := range it.Components {
		out.Components = append(out.Components, projectItemDefinition(c))
	}
	if it.FunctionItem != nil {
		fi := &FunctionItem{OutputType: it.FunctionItem.OutputType}
		for _, p := range it.FunctionItem.Parameters {
			fi.Parameters = append(fi.Parameters, projectInformationItem(p))
		}
		out.FunctionItem = fi
	}
	return out
}

func projectInformationItem(v *model.InformationItem) *InformationItem {
	if v == nil {
		return nil
	}
	return &InformationItem{ID: v.ID, Name: v.Name, TypeRef: v.TypeRef, Optional: v.Optional}
}

func projectExpression(e model.Expression) (*Expression, error) {
	if e == nil {
		return nil, nil
	}
	out := &Expression{Kind: string(e.Kind()), ID: e.ExprID(), TypeRef: e.ResultType()}
	switch v := e.(type) {
	case *model.LiteralExpression:
		out.Text = v.Text
		out.ExpressionLanguage = v.ExpressionLanguage

	case *model.DecisionTable:
		out.HitPolicy = string(v.HitPolicy)
		out.Aggregation = string(v.Aggregation)
		out.Orientation = string(v.Orientation)
		out.Annotations = v.Annotations
		for _, in := range v.Inputs {
			out.Inputs = append(out.Inputs, &TableInput{
				ID: in.ID, Label: in.Label, Expression: in.Expression,
				TypeRef: in.TypeRef, Values: in.Values,
			})
		}
		for _, o := range v.Outputs {
			out.Outputs = append(out.Outputs, &TableOutput{
				ID: o.ID, Label: o.Label, Name: o.Name, TypeRef: o.TypeRef,
				Values: o.Values, DefaultValue: o.DefaultValue,
			})
		}
		for _, r := range v.Rules {
			out.Rules = append(out.Rules, &TableRule{
				ID: r.ID, Description: r.Description,
				InputEntries: r.InputEntries, OutputEntries: r.OutputEntries,
				Annotations: r.Annotations,
			})
		}

	case *model.Invocation:
		out.Called = v.Called
		for _, b := range v.Bindings {
			val, err := projectExpression(b.Value)
			if err != nil {
				return nil, err
			}
			out.Bindings = append(out.Bindings, &Binding{
				Parameter: projectInformationItem(b.Parameter), Value: val,
			})
		}

	case *model.ContextExpression:
		for _, entry := range v.Entries {
			val, err := projectExpression(entry.Value)
			if err != nil {
				return nil, err
			}
			out.Entries = append(out.Entries, &ContextEntry{
				Variable: projectInformationItem(entry.Variable), Value: val,
			})
		}

	case *model.ListExpression:
		for _, el := range v.Elements {
			pe, err := projectExpression(el)
			if err != nil {
				return nil, err
			}
			out.Elements = append(out.Elements, pe)
		}

	case *model.Relation:
		for _, c := range v.Columns {
			out.Columns = append(out.Columns, projectInformationItem(c))
		}
		for _, row := range v.Rows {
			cells := make([]*Expression, 0, len(row))
			for _, cell := range row {
				pe, err := projectExpression(cell)
				if err != nil {
					return nil, err
				}
				cells = append(cells, pe)
			}
			out.Rows = append(out.Rows, cells)
		}

	case *model.FunctionDefinition:
		out.FunctionKind = string(v.FnKind)
		for _, p := range v.Parameters {
			out.Parameters = append(out.Parameters, projectInformationItem(p))
		}
		body, err := projectExpression(v.Body)
		if err != nil {
			return nil, err
		}
		out.Body = body

	case *model.AgentDecision:
		agent := &AgentDecision{
			PromptTemplate: v.PromptTemplate,
			Validator:      v.Validator,
		}
		for _, b := range v.Bindings {
			agent.Bindings = append(agent.Bindings, &AgentBinding{Name: b.Name, FEEL: b.FEEL})
		}
		if !v.OutputType.IsAny() {
			ts := projectTypeSpec(v.OutputType)
			agent.OutputType = &ts
		}
		agent.Policy = &AgentPolicy{
			MaxRetries:       v.Policy.MaxRetries,
			OnFailure:        string(v.Policy.OnFailure),
			FallbackDecision: v.Policy.FallbackDecision,
			SessionHint:      v.Policy.SessionHint,
		}
		if v.Policy.MaxLatency > 0 {
			agent.Policy.MaxLatency = ISODuration(v.Policy.MaxLatency)
		}
		out.Agent = agent

	case *model.UnknownExpression:
		out.Detail = v.Detail

	default:
		return nil, fmt.Errorf("no VDJ projection for boxed expression kind %q", e.Kind())
	}
	return out, nil
}

func projectTypeSpec(t model.TypeSpec) TypeSpec {
	out := TypeSpec{TypeRef: t.TypeRef, Enumeration: t.Enumeration, Collection: t.Collection}
	if len(t.Components) > 0 {
		out.Components = make(map[string]TypeSpec, len(t.Components))
		for k, v := range t.Components {
			out.Components[k] = projectTypeSpec(v)
		}
	}
	return out
}

// ISODuration renders a policy duration in ISO-8601 form. It is a thin alias
// for model.FormatDuration.
func ISODuration(d time.Duration) string { return model.FormatDuration(d) }
