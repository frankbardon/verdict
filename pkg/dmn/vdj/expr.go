package vdj

import (
	"strings"

	"github.com/frankbardon/verdict/pkg/diag"
	"github.com/frankbardon/verdict/pkg/dmn/model"
)

// expression projects a VDJ expression onto the model's boxed-expression types.
func (c *converter) expression(e *Expression, elemID, elemName string) model.Expression {
	if e == nil {
		return nil
	}
	switch model.Kind(e.Kind) {
	case model.KindLiteral:
		lit := model.NewLiteral(e.ID, e.TypeRef, e.Text)
		lit.ExpressionLanguage = e.ExpressionLanguage
		return lit

	case model.KindDecisionTable:
		return c.decisionTable(e, elemID, elemName)

	case model.KindInvocation:
		out := &model.Invocation{Called: e.Called}
		model.SetExpressionBase(out, e.ID, e.TypeRef)
		for _, b := range e.Bindings {
			out.Bindings = append(out.Bindings, &model.Binding{
				Parameter: c.informationItem(b.Parameter),
				Value:     c.expression(b.Value, elemID, elemName),
			})
		}
		if out.Called == "" {
			c.diags.Add(diag.Errorf(diag.CodeBadExpression,
				"invocation does not name the business knowledge model it calls").At(elemID, elemName))
		}
		return out

	case model.KindContext:
		out := &model.ContextExpression{}
		model.SetExpressionBase(out, e.ID, e.TypeRef)
		for _, entry := range e.Entries {
			out.Entries = append(out.Entries, &model.ContextEntry{
				Variable: c.informationItem(entry.Variable),
				Value:    c.expression(entry.Value, elemID, elemName),
			})
		}
		return out

	case model.KindList:
		out := &model.ListExpression{}
		model.SetExpressionBase(out, e.ID, e.TypeRef)
		for _, el := range e.Elements {
			out.Elements = append(out.Elements, c.expression(el, elemID, elemName))
		}
		return out

	case model.KindRelation:
		out := &model.Relation{}
		model.SetExpressionBase(out, e.ID, e.TypeRef)
		for _, col := range e.Columns {
			out.Columns = append(out.Columns, c.informationItem(col))
		}
		for _, row := range e.Rows {
			cells := make([]model.Expression, 0, len(row))
			for _, cell := range row {
				cells = append(cells, c.expression(cell, elemID, elemName))
			}
			out.Rows = append(out.Rows, cells)
		}
		return out

	case model.KindFunction:
		out := &model.FunctionDefinition{FnKind: functionKind(e.FunctionKind)}
		model.SetExpressionBase(out, e.ID, e.TypeRef)
		for _, p := range e.Parameters {
			out.Parameters = append(out.Parameters, c.informationItem(p))
		}
		out.Body = c.expression(e.Body, elemID, elemName)
		switch out.FnKind {
		case model.FunctionJava:
			c.diags.Add(diag.Warnf(diag.CodeJavaBinding,
				"Java-bound function is preserved but not executed; calls return null").At(elemID, elemName))
		case model.FunctionPMML:
			c.diags.Add(diag.Warnf(diag.CodePMMLBinding,
				"PMML function reference is preserved but not executed; calls return null").At(elemID, elemName))
		}
		return out

	case model.KindAgent:
		return c.agentDecision(e, elemID, elemName)

	default:
		c.diags.Add(diag.Warnf(diag.CodeUnsupportedExpression,
			"boxed expression kind %q is not implemented; it evaluates to null", e.Kind).At(elemID, elemName))
		out := &model.UnknownExpression{Detail: e.Kind}
		model.SetExpressionBase(out, e.ID, e.TypeRef)
		return out
	}
}

func (c *converter) decisionTable(e *Expression, elemID, elemName string) model.Expression {
	policy, agg, err := model.ParseHitPolicy(strings.ToUpper(strings.TrimSpace(e.HitPolicy)))
	if err != nil {
		c.diags.Add(diag.Warnf(diag.CodeUnknownHitPolicy, "%v; defaulting to UNIQUE", err).At(elemID, elemName))
		policy, agg = model.HitUnique, model.AggNone
	}
	if a := strings.ToUpper(strings.TrimSpace(e.Aggregation)); a != "" {
		switch a {
		case "SUM":
			agg = model.AggSum
		case "MIN":
			agg = model.AggMin
		case "MAX":
			agg = model.AggMax
		case "COUNT":
			agg = model.AggCount
		default:
			c.diags.Add(diag.Warnf(diag.CodeUnknownHitPolicy,
				"unknown COLLECT aggregation %q; collecting without aggregation", e.Aggregation).At(elemID, elemName))
		}
	}
	out := &model.DecisionTable{
		HitPolicy:   policy,
		Aggregation: agg,
		Orientation: orientation(e.Orientation),
		Annotations: e.Annotations,
	}
	model.SetExpressionBase(out, e.ID, e.TypeRef)

	for _, in := range e.Inputs {
		out.Inputs = append(out.Inputs, &model.TableInput{
			ID: in.ID, Label: in.Label, Expression: in.Expression, TypeRef: in.TypeRef, Values: in.Values,
		})
	}
	for _, o := range e.Outputs {
		out.Outputs = append(out.Outputs, &model.TableOutput{
			ID: o.ID, Label: o.Label, Name: o.Name, TypeRef: o.TypeRef,
			Values: o.Values, DefaultValue: o.DefaultValue,
		})
	}
	for i, r := range e.Rules {
		rule := &model.TableRule{
			ID: r.ID, Description: r.Description, Index: i,
			InputEntries: r.InputEntries, OutputEntries: r.OutputEntries, Annotations: r.Annotations,
		}
		if rule.ID == "" {
			rule.ID = elemID + "_rule_" + itoa(i+1)
		}
		if len(rule.InputEntries) != len(out.Inputs) {
			c.diags.Add(diag.Errorf(diag.CodeArityMismatch,
				"rule %d has %d `when` entries but the table declares %d input clauses",
				i+1, len(rule.InputEntries), len(out.Inputs)).At(elemID, elemName).With("rule", i+1))
		}
		if len(rule.OutputEntries) != len(out.Outputs) {
			c.diags.Add(diag.Errorf(diag.CodeArityMismatch,
				"rule %d has %d `then` entries but the table declares %d output clauses",
				i+1, len(rule.OutputEntries), len(out.Outputs)).At(elemID, elemName).With("rule", i+1))
		}
		out.Rules = append(out.Rules, rule)
	}
	return out
}

func (c *converter) agentDecision(e *Expression, elemID, elemName string) model.Expression {
	a := e.Agent
	if a == nil {
		c.diags.Add(diag.Errorf(diag.CodeBadExpression,
			"agentDecision expression has no `agent` payload").At(elemID, elemName))
		return &model.UnknownExpression{Detail: e.Kind}
	}
	out := &model.AgentDecision{
		PromptTemplate: a.PromptTemplate,
		Validator:      a.Validator,
		Policy:         model.AgentPolicy{OnFailure: model.FailError},
	}
	model.SetExpressionBase(out, e.ID, e.TypeRef)

	if strings.TrimSpace(out.PromptTemplate) == "" {
		c.diags.Add(diag.Errorf(diag.CodeBadTemplate,
			"agent decision has an empty prompt template").At(elemID, elemName))
	}
	seen := map[string]bool{}
	for i, b := range a.Bindings {
		if b.Name == "" || b.FEEL == "" {
			c.diags.Add(diag.Errorf(diag.CodeBadExpression,
				"agent decision input binding %d needs both a name and a FEEL expression", i+1).At(elemID, elemName))
			continue
		}
		if seen[b.Name] {
			c.diags.Add(diag.Errorf(diag.CodeDuplicateID,
				"agent decision binds %q more than once", b.Name).At(elemID, elemName))
			continue
		}
		seen[b.Name] = true
		out.Bindings = append(out.Bindings, &model.AgentInputBinding{Name: b.Name, FEEL: b.FEEL})
	}
	if a.OutputType != nil {
		out.OutputType = typeSpec(*a.OutputType)
	}
	if a.Policy != nil {
		if a.Policy.MaxLatency != "" {
			d, err := model.ParseDuration(a.Policy.MaxLatency)
			if err != nil {
				c.diags.Add(diag.Warnf(diag.CodeBadExpression,
					"agent policy max_latency %q is not an ISO-8601 duration: %v; using the engine default",
					a.Policy.MaxLatency, err).At(elemID, elemName))
			} else {
				out.Policy.MaxLatency = d
			}
		}
		if a.Policy.MaxRetries < 0 {
			c.diags.Add(diag.Warnf(diag.CodeBadExpression,
				"agent policy max_retries is negative; using 0").At(elemID, elemName))
		} else {
			out.Policy.MaxRetries = a.Policy.MaxRetries
		}
		if fp, ok := model.ParseFailurePolicy(a.Policy.OnFailure); ok {
			out.Policy.OnFailure = fp
		} else {
			c.diags.Add(diag.Warnf(diag.CodeBadExpression,
				"agent policy on_failure %q is not one of error|null|fallback; using error",
				a.Policy.OnFailure).At(elemID, elemName))
		}
		out.Policy.FallbackDecision = a.Policy.FallbackDecision
		out.Policy.SessionHint = a.Policy.SessionHint
		if out.Policy.OnFailure == model.FailFallback && out.Policy.FallbackDecision == "" {
			c.diags.Add(diag.Errorf(diag.CodeBadExpression,
				"agent policy declares on_failure \"fallback\" but names no fallback_decision").At(elemID, elemName))
		}
	}
	return out
}

func typeSpec(t TypeSpec) model.TypeSpec {
	out := model.TypeSpec{TypeRef: t.TypeRef, Enumeration: t.Enumeration, Collection: t.Collection}
	if len(t.Components) > 0 {
		out.Components = make(map[string]model.TypeSpec, len(t.Components))
		for k, v := range t.Components {
			out.Components[k] = typeSpec(v)
		}
	}
	return out
}

func orientation(s string) model.Orientation {
	switch strings.TrimSpace(s) {
	case string(model.RulesAsColumns):
		return model.RulesAsColumns
	case string(model.CrossTable):
		return model.CrossTable
	default:
		return model.RulesAsRows
	}
}

func functionKind(s string) model.FunctionKind {
	switch strings.ToUpper(strings.TrimSpace(s)) {
	case "JAVA":
		return model.FunctionJava
	case "PMML":
		return model.FunctionPMML
	default:
		return model.FunctionFEEL
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}
