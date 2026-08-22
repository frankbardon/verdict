package xml

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/xml"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/frankbardon/verdict/pkg/diag"
	"github.com/frankbardon/verdict/pkg/dmn/model"
)

// Read parses a DMN document and projects it onto the version-neutral model.
//
// Parsing is deliberately forgiving about content it does not understand: an
// unimplemented boxed expression or a Java-bound BKM becomes a stub plus a
// diagnostic rather than a hard failure, so one unsupported corner of a large
// model does not make the other ninety-nine decisions unreachable. Structural
// problems that make evaluation impossible — malformed XML, a cyclic DRG — are
// returned as errors.
func Read(r io.Reader) (*model.Definitions, []diag.Diagnostic, error) {
	raw, err := io.ReadAll(r)
	if err != nil {
		return nil, nil, fmt.Errorf("dmn: reading document: %w", err)
	}
	return Parse(raw)
}

// Parse is Read over an in-memory document.
func Parse(raw []byte) (*model.Definitions, []diag.Diagnostic, error) {
	var doc definitions
	dec := xml.NewDecoder(strings.NewReader(string(raw)))
	// DMN documents in the wild are UTF-8; a CharsetReader that simply passes
	// bytes through keeps a mislabelled encoding declaration from aborting the
	// parse of an otherwise valid document.
	dec.CharsetReader = func(_ string, input io.Reader) (io.Reader, error) { return input, nil }
	if err := dec.Decode(&doc); err != nil {
		return nil, nil, fmt.Errorf("dmn: parsing document: %w", err)
	}

	c := &converter{}
	defs := c.definitions(&doc)

	sum := sha256.Sum256(raw)
	defs.Hash = hex.EncodeToString(sum[:])

	return defs, c.diags.All(), nil
}

// converter accumulates diagnostics while projecting the XML tree.
type converter struct {
	diags diag.Set
	// docLanguage is the document-level expressionLanguage, used to decide
	// whether a nested expression's language override is meaningful.
	docLanguage string
}

func (c *converter) definitions(d *definitions) *model.Definitions {
	c.docLanguage = d.ExpressionLanguage

	out := &model.Definitions{
		ID:                 orDefault(d.ID, d.Name),
		Name:               d.Name,
		Namespace:          d.Namespace,
		Description:        trim(d.Description),
		Version:            firstNonEmpty(d.Version, d.VersionAlt),
		ExpressionLanguage: d.ExpressionLanguage,
		Exporter:           d.Exporter,
		ExporterVersion:    d.ExporterVersion,
	}
	if lvl, err := model.ParseConformanceLevel(firstNonEmpty(d.ConformanceLevel, d.ConformanceLevelAlt)); err != nil {
		c.diags.Add(diag.Warnf(diag.CodeForeignLanguage, "%v; falling back to the engine default", err))
	} else {
		out.Level = lvl
	}
	if d.ExpressionLanguage != "" && !isFEELLanguage(d.ExpressionLanguage) {
		c.diags.Add(diag.Warnf(diag.CodeForeignLanguage,
			"document declares expressionLanguage %q; Verdict evaluates FEEL only", d.ExpressionLanguage))
	}

	for i := range d.ItemDefinitions {
		out.ItemDefinitions = append(out.ItemDefinitions, c.itemDefinition(&d.ItemDefinitions[i]))
	}
	for i := range d.InputData {
		in := &d.InputData[i]
		out.InputData = append(out.InputData, &model.InputData{
			ID:          orDefault(in.ID, in.Name),
			Name:        in.Name,
			Description: trim(in.Description),
			Variable:    c.informationItem(in.Variable),
		})
	}
	for i := range d.Decisions {
		out.Decisions = append(out.Decisions, c.decision(&d.Decisions[i]))
	}
	for i := range d.BKMs {
		out.BKMs = append(out.BKMs, c.bkm(&d.BKMs[i]))
	}
	for i := range d.KnowledgeSources {
		ks := &d.KnowledgeSources[i]
		out.KnowledgeSources = append(out.KnowledgeSources, &model.KnowledgeSource{
			ID:          orDefault(ks.ID, ks.Name),
			Name:        ks.Name,
			Description: trim(ks.Description),
			Type:        trim(ks.Type),
			LocationURI: ks.LocationURI,
			Owner:       ks.Owner.ref(),
		})
	}
	for i := range d.DecisionServices {
		s := &d.DecisionServices[i]
		out.DecisionServices = append(out.DecisionServices, &model.DecisionService{
			ID:                    orDefault(s.ID, s.Name),
			Name:                  s.Name,
			Description:           trim(s.Description),
			Variable:              c.informationItem(s.Variable),
			OutputDecisions:       refs(s.OutputDecisions),
			EncapsulatedDecisions: refs(s.EncapsulatedDecisions),
			InputDecisions:        refs(s.InputDecisions),
			InputData:             refs(s.InputData),
		})
	}
	return out
}

func (c *converter) itemDefinition(d *itemDefinition) *model.ItemDefinition {
	out := &model.ItemDefinition{
		ID:             orDefault(d.ID, d.Name),
		Name:           d.Name,
		Description:    trim(d.Description),
		TypeRef:        firstNonEmpty(d.TypeRef, trim(d.TypeRefElem)),
		IsCollection:   d.IsCollection,
		AllowedValues:  d.AllowedValues.Value(),
		TypeConstraint: d.TypeConstraint.Value(),
	}
	for i := range d.Components {
		out.Components = append(out.Components, c.itemDefinition(&d.Components[i]))
	}
	if d.FunctionItem != nil {
		fi := &model.FunctionItem{OutputType: d.FunctionItem.OutputTypeRef}
		for i := range d.FunctionItem.Parameters {
			fi.Parameters = append(fi.Parameters, c.informationItem(&d.FunctionItem.Parameters[i]))
		}
		out.FunctionItem = fi
	}
	return out
}

func (c *converter) informationItem(v *informationItem) *model.InformationItem {
	if v == nil {
		return nil
	}
	return &model.InformationItem{
		ID:       v.ID,
		Name:     v.Name,
		TypeRef:  v.TypeRef,
		Optional: v.Optional,
	}
}

func (c *converter) decision(d *decision) *model.Decision {
	id := orDefault(d.ID, d.Name)
	out := &model.Decision{
		ID:             id,
		Name:           d.Name,
		Description:    trim(d.Description),
		Question:       trim(d.Question),
		AllowedAnswers: trim(d.AllowedAnswers),
		Variable:       c.informationItem(d.Variable),
	}
	for _, r := range d.InformationRequirements {
		if ref := r.RequiredDecision.ref(); ref != "" {
			out.RequiredDecisions = append(out.RequiredDecisions, ref)
		}
		if ref := r.RequiredInput.ref(); ref != "" {
			out.RequiredInputs = append(out.RequiredInputs, ref)
		}
	}
	for _, r := range d.KnowledgeRequirements {
		if ref := r.RequiredKnowledge.ref(); ref != "" {
			out.RequiredKnowledge = append(out.RequiredKnowledge, ref)
		}
	}
	for _, r := range d.AuthorityRequirements {
		if ref := r.RequiredAuthority.ref(); ref != "" {
			out.AuthorityRequirements = append(out.AuthorityRequirements, ref)
		}
	}

	switch {
	case d.Logic != nil:
		out.Logic = c.expression(d.Logic, id, d.Name)
	case d.Extensions != nil && d.Extensions.Agent != nil:
		// An agentDecision may also be carried inside <extensionElements>, which
		// is where a standard DMN editor will move it when it round-trips a
		// model it does not understand.
		out.Logic = c.agentDecision(d.Extensions.Agent, id, d.Name)
	default:
		c.diags.Add(diag.Warnf(diag.CodeMissingLogic,
			"decision has no decision logic; it will evaluate to null").At(id, d.Name))
	}
	return out
}

func (c *converter) bkm(b *bkm) *model.BusinessKnowledgeModel {
	id := orDefault(b.ID, b.Name)
	out := &model.BusinessKnowledgeModel{
		ID:          id,
		Name:        b.Name,
		Description: trim(b.Description),
		Variable:    c.informationItem(b.Variable),
	}
	for _, r := range b.KnowledgeRequirements {
		if ref := r.RequiredKnowledge.ref(); ref != "" {
			out.RequiredKnowledge = append(out.RequiredKnowledge, ref)
		}
	}
	for _, r := range b.InformationRequirements {
		if ref := r.RequiredInput.ref(); ref != "" {
			out.RequiredInputs = append(out.RequiredInputs, ref)
		}
		if ref := r.RequiredDecision.ref(); ref != "" {
			out.RequiredDecisions = append(out.RequiredDecisions, ref)
		}
	}
	if b.Encapsulated == nil {
		c.diags.Add(diag.Warnf(diag.CodeMissingLogic,
			"business knowledge model has no encapsulated logic; calls to it return null").At(id, b.Name))
		return out
	}
	fn, _ := c.functionDefinition(b.Encapsulated, id, b.Name).(*model.FunctionDefinition)
	out.Encapsulated = fn
	return out
}

// expression dispatches a decoded expression holder onto the model types.
// elemID and elemName locate diagnostics at the owning DRG element.
func (c *converter) expression(h *expressionHolder, elemID, elemName string) model.Expression {
	if h == nil {
		return nil
	}
	switch {
	case h.literal != nil:
		return c.literal(h.literal, elemID, elemName)
	case h.table != nil:
		return c.decisionTable(h.table, elemID, elemName)
	case h.invoke != nil:
		return c.invocation(h.invoke, elemID, elemName)
	case h.context != nil:
		return c.context(h.context, elemID, elemName)
	case h.list != nil:
		return c.list(h.list, elemID, elemName)
	case h.relation != nil:
		return c.relation(h.relation, elemID, elemName)
	case h.function != nil:
		return c.functionDefinition(h.function, elemID, elemName)
	case h.agent != nil:
		return c.agentDecision(h.agent, elemID, elemName)
	default:
		c.diags.Add(diag.Warnf(diag.CodeUnsupportedExpression,
			"boxed expression %s is not implemented; it evaluates to null", h.describe()).At(elemID, elemName))
		return &model.UnknownExpression{Detail: h.kind}
	}
}

func (c *converter) literal(l *literalExpression, elemID, elemName string) model.Expression {
	if l.ExpressionLanguage != "" && !isFEELLanguage(l.ExpressionLanguage) {
		c.diags.Add(diag.Warnf(diag.CodeForeignLanguage,
			"literal expression declares expressionLanguage %q; Verdict evaluates FEEL only",
			l.ExpressionLanguage).At(elemID, elemName))
	}
	e := model.NewLiteral(l.ID, l.TypeRef, trim(l.Text))
	e.ExpressionLanguage = l.ExpressionLanguage
	return e
}

func (c *converter) decisionTable(t *decisionTable, elemID, elemName string) model.Expression {
	policy, agg, err := model.ParseHitPolicy(strings.ToUpper(trim(t.HitPolicy)))
	if err != nil {
		c.diags.Add(diag.Warnf(diag.CodeUnknownHitPolicy,
			"%v; defaulting to UNIQUE", err).At(elemID, elemName))
		policy, agg = model.HitUnique, model.AggNone
	}
	// DMN carries the COLLECT aggregator in its own attribute; the shorthand
	// spelling (C+, C<) is accepted above for hand-written tables.
	if a := strings.ToUpper(trim(t.Aggregation)); a != "" {
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
				"unknown COLLECT aggregation %q; collecting without aggregation", t.Aggregation).At(elemID, elemName))
		}
	}

	out := &model.DecisionTable{
		HitPolicy:   policy,
		Aggregation: agg,
		Orientation: orientation(t.PreferredOrientation),
	}
	setBase(out, t.ID, t.TypeRef)

	for i := range t.Inputs {
		in := &t.Inputs[i]
		ti := &model.TableInput{
			ID:     in.ID,
			Label:  in.Label,
			Values: in.InputValues.Value(),
		}
		if in.InputExpression != nil {
			ti.Expression = trim(in.InputExpression.Text)
			ti.TypeRef = in.InputExpression.TypeRef
		}
		if ti.Expression == "" {
			c.diags.Add(diag.Errorf(diag.CodeBadExpression,
				"decision table input clause %d has no input expression", i+1).At(elemID, elemName))
		}
		out.Inputs = append(out.Inputs, ti)
	}
	for i := range t.Outputs {
		o := &t.Outputs[i]
		out.Outputs = append(out.Outputs, &model.TableOutput{
			ID:           o.ID,
			Label:        firstNonEmpty(o.Label, t.OutputLabel),
			Name:         o.Name,
			TypeRef:      o.TypeRef,
			Values:       o.OutputValues.Value(),
			DefaultValue: o.DefaultOutputEntry.Value(),
		})
	}
	// A single-output table may omit the output name; DMN then binds the result
	// directly to the decision's variable.
	if len(out.Outputs) == 0 {
		c.diags.Add(diag.Errorf(diag.CodeBadExpression,
			"decision table has no output clause").At(elemID, elemName))
	}
	for _, a := range t.Annotations {
		out.Annotations = append(out.Annotations, a.Name)
	}

	for i := range t.Rules {
		r := &t.Rules[i]
		rule := &model.TableRule{
			ID:          orDefault(r.ID, fmt.Sprintf("%s_rule_%d", elemID, i+1)),
			Description: trim(r.Description),
			Index:       i,
		}
		for j := range r.InputEntries {
			rule.InputEntries = append(rule.InputEntries, r.InputEntries[j].Value())
		}
		for j := range r.OutputEntries {
			rule.OutputEntries = append(rule.OutputEntries, r.OutputEntries[j].Value())
		}
		for j := range r.AnnotationEntry {
			rule.Annotations = append(rule.Annotations, r.AnnotationEntry[j].Value())
		}
		if len(rule.InputEntries) != len(out.Inputs) {
			c.diags.Add(diag.Errorf(diag.CodeArityMismatch,
				"rule %d has %d input entries but the table declares %d input clauses",
				i+1, len(rule.InputEntries), len(out.Inputs)).At(elemID, elemName).With("rule", i+1))
		}
		if len(rule.OutputEntries) != len(out.Outputs) {
			c.diags.Add(diag.Errorf(diag.CodeArityMismatch,
				"rule %d has %d output entries but the table declares %d output clauses",
				i+1, len(rule.OutputEntries), len(out.Outputs)).At(elemID, elemName).With("rule", i+1))
		}
		out.Rules = append(out.Rules, rule)
	}
	return out
}

func (c *converter) invocation(v *invocation, elemID, elemName string) model.Expression {
	out := &model.Invocation{}
	setBase(out, v.ID, v.TypeRef)
	if v.Called != nil {
		out.Called = trim(v.Called.Text)
	}
	if out.Called == "" {
		c.diags.Add(diag.Errorf(diag.CodeBadExpression,
			"invocation does not name the business knowledge model it calls").At(elemID, elemName))
	}
	for i := range v.Bindings {
		b := &v.Bindings[i]
		out.Bindings = append(out.Bindings, &model.Binding{
			Parameter: c.informationItem(b.Parameter),
			Value:     c.expression(b.Value, elemID, elemName),
		})
	}
	return out
}

func (c *converter) context(x *contextExpression, elemID, elemName string) model.Expression {
	out := &model.ContextExpression{}
	setBase(out, x.ID, x.TypeRef)
	for i := range x.Entries {
		e := &x.Entries[i]
		out.Entries = append(out.Entries, &model.ContextEntry{
			Variable: c.informationItem(e.Variable),
			Value:    c.expression(e.Value, elemID, elemName),
		})
	}
	return out
}

func (c *converter) list(l *listExpression, elemID, elemName string) model.Expression {
	out := &model.ListExpression{}
	setBase(out, l.ID, l.TypeRef)
	for _, e := range l.Elements {
		out.Elements = append(out.Elements, c.expression(e, elemID, elemName))
	}
	return out
}

func (c *converter) relation(r *relationExpression, elemID, elemName string) model.Expression {
	out := &model.Relation{}
	setBase(out, r.ID, r.TypeRef)
	for i := range r.Columns {
		out.Columns = append(out.Columns, c.informationItem(&r.Columns[i]))
	}
	for i := range r.Rows {
		row := make([]model.Expression, 0, len(r.Rows[i].Cells))
		for _, cell := range r.Rows[i].Cells {
			row = append(row, c.expression(cell, elemID, elemName))
		}
		if len(row) != len(out.Columns) {
			c.diags.Add(diag.Warnf(diag.CodeArityMismatch,
				"relation row %d has %d cells but the relation declares %d columns; missing cells are null",
				i+1, len(row), len(out.Columns)).At(elemID, elemName))
		}
		out.Rows = append(out.Rows, row)
	}
	return out
}

func (c *converter) functionDefinition(f *functionDefinition, elemID, elemName string) model.Expression {
	out := &model.FunctionDefinition{FnKind: functionKind(f.Kind)}
	setBase(out, f.ID, f.TypeRef)
	for i := range f.Parameters {
		out.Parameters = append(out.Parameters, c.informationItem(&f.Parameters[i]))
	}
	for _, b := range f.Body {
		out.Body = c.expression(b, elemID, elemName)
		break
	}
	switch out.FnKind {
	case model.FunctionJava:
		c.diags.Add(diag.Warnf(diag.CodeJavaBinding,
			"Java-bound function is preserved but not executed; calls return null").At(elemID, elemName))
	case model.FunctionPMML:
		c.diags.Add(diag.Warnf(diag.CodePMMLBinding,
			"PMML function reference is preserved but not executed; calls return null").At(elemID, elemName))
	}
	return out
}

func (c *converter) agentDecision(a *agentDecisionXML, elemID, elemName string) model.Expression {
	out := &model.AgentDecision{PromptTemplate: trim(a.PromptTemplate)}
	setBase(out, a.ID, a.TypeRef)

	if out.PromptTemplate == "" {
		c.diags.Add(diag.Errorf(diag.CodeBadTemplate,
			"agent decision has an empty prompt template").At(elemID, elemName))
	}
	seen := map[string]bool{}
	for i := range a.Bindings {
		b := &a.Bindings[i]
		expr := firstNonEmpty(b.FEEL, trim(b.Text))
		if b.Name == "" || expr == "" {
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
		out.Bindings = append(out.Bindings, &model.AgentInputBinding{Name: b.Name, FEEL: expr})
	}
	if a.OutputType != nil {
		out.OutputType = agentTypeSpec(a.OutputType)
	}
	if a.Validator != nil {
		out.Validator = firstNonEmpty(a.Validator.FEEL, trim(a.Validator.Text))
	}
	out.Policy = c.agentPolicy(a.Policy, elemID, elemName)
	return out
}

func (c *converter) agentPolicy(p *agentPolicyXML, elemID, elemName string) model.AgentPolicy {
	out := model.AgentPolicy{OnFailure: model.FailError}
	if p == nil {
		return out
	}
	if p.MaxLatency != "" {
		d, err := ParseISODuration(p.MaxLatency)
		if err != nil {
			c.diags.Add(diag.Warnf(diag.CodeBadExpression,
				"agent policy maxLatency %q is not an ISO-8601 duration: %v; using the engine default",
				p.MaxLatency, err).At(elemID, elemName))
		} else {
			out.MaxLatency = d
		}
	}
	if p.MaxRetries != "" {
		n, err := strconv.Atoi(strings.TrimSpace(p.MaxRetries))
		if err != nil || n < 0 {
			c.diags.Add(diag.Warnf(diag.CodeBadExpression,
				"agent policy maxRetries %q is not a non-negative integer; using 0", p.MaxRetries).At(elemID, elemName))
		} else {
			out.MaxRetries = n
		}
	}
	if fp, ok := model.ParseFailurePolicy(strings.TrimSpace(p.OnFailure)); ok {
		out.OnFailure = fp
	} else {
		c.diags.Add(diag.Warnf(diag.CodeBadExpression,
			"agent policy onFailure %q is not one of error|null|fallback; using error", p.OnFailure).At(elemID, elemName))
	}
	out.FallbackDecision = strings.TrimSpace(p.FallbackDecision)
	out.SessionHint = strings.TrimSpace(p.SessionHint)
	if out.OnFailure == model.FailFallback && out.FallbackDecision == "" {
		c.diags.Add(diag.Errorf(diag.CodeBadExpression,
			"agent policy declares onFailure=\"fallback\" but names no fallbackDecision").At(elemID, elemName))
	}
	return out
}

func agentTypeSpec(t *agentOutputTypeXML) model.TypeSpec {
	spec := model.TypeSpec{TypeRef: t.TypeRef, Collection: t.Collection}
	if t.Enumeration != nil {
		for _, v := range t.Enumeration.Values {
			spec.Enumeration = append(spec.Enumeration, trim(v))
		}
	}
	if len(t.Components) > 0 {
		spec.Components = map[string]model.TypeSpec{}
		for i := range t.Components {
			spec.Components[t.Components[i].Name] = agentComponentSpec(&t.Components[i])
		}
	}
	return spec
}

func agentComponentSpec(c *agentComponentXML) model.TypeSpec {
	spec := model.TypeSpec{TypeRef: c.TypeRef, Collection: c.Collection}
	if c.Enumeration != nil {
		for _, v := range c.Enumeration.Values {
			spec.Enumeration = append(spec.Enumeration, trim(v))
		}
	}
	if len(c.Components) > 0 {
		spec.Components = map[string]model.TypeSpec{}
		for i := range c.Components {
			spec.Components[c.Components[i].Name] = agentComponentSpec(&c.Components[i])
		}
	}
	return spec
}

// ---- small helpers --------------------------------------------------------

// setBase writes the shared ID and typeRef of a boxed expression.
func setBase(e model.Expression, id, typeRef string) {
	model.SetExpressionBase(e, id, typeRef)
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

func isFEELLanguage(s string) bool {
	l := strings.ToLower(strings.TrimSpace(s))
	return l == "" || l == "feel" || strings.Contains(l, "/feel") || strings.HasSuffix(l, "feel/")
}

func refs(hs []hrefElem) []string {
	out := make([]string, 0, len(hs))
	for i := range hs {
		if r := hs[i].ref(); r != "" {
			out = append(out, r)
		}
	}
	return out
}

func trim(s string) string { return strings.TrimSpace(s) }

// trimHash strips the fragment marker and any document part from an href, so
// both "#id" and "model.dmn#id" resolve to "id".
func trimHash(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.LastIndexByte(s, '#'); i >= 0 {
		return s[i+1:]
	}
	return s
}

func orDefault(v, fallback string) string {
	if strings.TrimSpace(v) != "" {
		return v
	}
	return fallback
}

func firstNonEmpty(vs ...string) string {
	for _, v := range vs {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

// ParseISODuration parses an ISO-8601 duration policy attribute. It is a thin
// alias for model.ParseDuration, kept here because the XML attribute is where
// callers meet the format.
func ParseISODuration(s string) (time.Duration, error) { return model.ParseDuration(s) }
