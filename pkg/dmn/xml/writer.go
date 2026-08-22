package xml

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"io"
	"strconv"

	"github.com/frankbardon/verdict/pkg/dmn/model"
)

// Options configure the writer.
type Options struct {
	// Namespace is the DMN MODEL namespace to emit: NS13 (the default), NS14 or
	// NS15.
	//
	// The default is 1.3, not the newest version Verdict can read, and that is a
	// deliberate interoperability choice rather than conservatism. Camunda
	// Modeler and dmn-js — between them, most of the DMN authoring in the world —
	// read 1.3 and reject a 1.5 namespace outright. A model Verdict writes is
	// worth little if the tool the modeller uses will not open it.
	Namespace string

	// Diagram emits auto-laid-out DMNDI so the document opens as a diagram
	// rather than as an empty canvas. On by default; dmn-js has nothing to draw
	// without it.
	Diagram bool
}

func (o *Options) defaults() {
	if o.Namespace == "" {
		o.Namespace = NS13
	}
}

// DefaultOptions returns the writer defaults.
func DefaultOptions() Options { return Options{Namespace: NS13, Diagram: true} }

// Marshal renders a model as DMN XML using the default options.
func Marshal(defs *model.Definitions) ([]byte, error) {
	return MarshalWith(defs, DefaultOptions())
}

// MarshalWith renders a model as DMN XML.
func MarshalWith(defs *model.Definitions, opts Options) ([]byte, error) {
	var buf bytes.Buffer
	if err := WriteWith(&buf, defs, opts); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// Write renders a model as DMN XML to w using the default options.
func Write(w io.Writer, defs *model.Definitions) error {
	return WriteWith(w, defs, DefaultOptions())
}

// WriteWith renders a model as DMN XML to w.
//
// Two things are worth knowing about the output. Verdict's own metadata —
// the model version and its conformance level — is written as attributes in
// the Verdict namespace, because DMN's `tDefinitions` declares no such
// attributes and only `##other`-namespaced ones are admitted by its
// `anyAttribute`. And `agentDecision` is written inside `<extensionElements>`
// rather than in the decision-logic slot, because that slot accepts only the
// DMN `expression` substitution group: a foreign element there makes the whole
// document fail schema validation, which is the opposite of degrading
// gracefully.
func WriteWith(w io.Writer, defs *model.Definitions, opts Options) error {
	if defs == nil {
		return fmt.Errorf("dmn: nil definitions")
	}
	opts.defaults()
	if _, err := io.WriteString(w, xml.Header); err != nil {
		return err
	}
	e := &encoder{enc: xml.NewEncoder(w), opts: opts}
	e.enc.Indent("", "  ")

	rootAttrs := []xml.Attr{
		attr("xmlns", opts.Namespace),
		attr("xmlns:verdict", NSVerdict),
		attr("id", defs.ID),
		attr("name", defs.Name),
		attr("namespace", defs.Namespace),
		attr("expressionLanguage", defs.ExpressionLanguage),
		attr("exporter", defs.Exporter),
		attr("exporterVersion", defs.ExporterVersion),
		// Verdict's own metadata lives in Verdict's namespace: DMN's
		// tDefinitions admits foreign attributes, but only namespaced ones.
		attr("verdict:version", defs.Version),
	}
	if opts.Diagram {
		rootAttrs = append(rootAttrs,
			attr("xmlns:dmndi", dmndiNamespace(opts.Namespace)),
			attr("xmlns:dc", NSDC),
			attr("xmlns:di", NSDI))
	}
	if defs.Level != model.LevelUnspecified {
		rootAttrs = append(rootAttrs, attr("verdict:conformanceLevel", defs.Level.String()))
	}

	e.open("definitions", rootAttrs...)
	e.text("description", defs.Description)

	// DMN's schema fixes the order of a definitions' children; emitting them in
	// declaration order keeps the output loadable by strict validators.
	for _, it := range defs.ItemDefinitions {
		e.itemDefinition("itemDefinition", it)
	}
	for _, in := range defs.InputData {
		e.open("inputData", attr("id", in.ID), attr("name", in.Name))
		e.text("description", in.Description)
		e.informationItem("variable", in.Variable)
		e.close()
	}
	for _, b := range defs.BKMs {
		e.bkm(b)
	}
	for _, d := range defs.Decisions {
		e.decision(d)
	}
	for _, k := range defs.KnowledgeSources {
		// tKnowledgeSource orders its children description, authorityRequirement,
		// type, owner.
		e.open("knowledgeSource", attr("id", k.ID), attr("name", k.Name), attr("locationURI", k.LocationURI))
		e.text("description", k.Description)
		e.text("type", k.Type)
		e.href("owner", k.Owner)
		e.close()
	}
	for _, s := range defs.DecisionServices {
		e.decisionService(s)
	}
	if opts.Diagram {
		e.diagram(defs)
	}
	e.close()

	if e.err != nil {
		return e.err
	}
	if err := e.enc.Flush(); err != nil {
		return err
	}
	_, err := io.WriteString(w, "\n")
	return err
}

// encoder wraps xml.Encoder with its own element stack and a sticky error, so
// the writer below reads as a description of the document rather than as error
// plumbing.
type encoder struct {
	enc   *xml.Encoder
	opts  Options
	stack []string
	err   error
}

// open writes a start element, dropping attributes whose value is empty.
func (e *encoder) open(name string, attrs ...xml.Attr) {
	if e.err != nil {
		return
	}
	kept := make([]xml.Attr, 0, len(attrs))
	for _, a := range attrs {
		if a.Value != "" {
			kept = append(kept, a)
		}
	}
	e.stack = append(e.stack, name)
	e.err = e.enc.EncodeToken(xml.StartElement{Name: xml.Name{Local: name}, Attr: kept})
}

func (e *encoder) close() {
	if len(e.stack) == 0 {
		return
	}
	name := e.stack[len(e.stack)-1]
	e.stack = e.stack[:len(e.stack)-1]
	if e.err != nil {
		return
	}
	e.err = e.enc.EncodeToken(xml.EndElement{Name: xml.Name{Local: name}})
}

// text writes a complete element with character data, skipping empty values.
func (e *encoder) text(name, value string) {
	if e.err != nil || value == "" {
		return
	}
	e.open(name)
	e.chars(value)
	e.close()
}

func (e *encoder) chars(s string) {
	if e.err != nil {
		return
	}
	e.err = e.enc.EncodeToken(xml.CharData(s))
}

// empty writes a self-contained element with attributes and no children.
func (e *encoder) empty(name string, attrs ...xml.Attr) {
	e.open(name, attrs...)
	e.close()
}

func (e *encoder) informationItem(name string, v *model.InformationItem) {
	if v == nil {
		return
	}
	e.empty(name, attr("id", v.ID), attr("name", v.Name), attr("typeRef", v.TypeRef))
}

func (e *encoder) href(name, ref string) {
	if ref == "" {
		return
	}
	e.empty(name, attr("href", "#"+ref))
}

func (e *encoder) itemDefinition(elemName string, it *model.ItemDefinition) {
	if it == nil {
		return
	}
	attrs := []xml.Attr{attr("id", it.ID), attr("name", it.Name)}
	if it.IsCollection {
		attrs = append(attrs, attr("isCollection", "true"))
	}
	e.open(elemName, attrs...)
	e.text("description", it.Description)

	// DMN 1.3 has no typeConstraint — it arrived in 1.4 — so when targeting 1.3
	// it is preserved in the Verdict namespace instead of being dropped. A
	// constraint silently lost on a round-trip is a rule silently lost.
	if it.TypeConstraint != "" && e.opts.Namespace == NS13 {
		e.open("extensionElements")
		e.text("verdict:typeConstraint", it.TypeConstraint)
		e.close()
	}

	// tItemDefinition is an xsd:choice: a definition is *either* a constrained
	// simple type, *or* a structure, *or* a function signature. Emitting more
	// than one branch produces a document no DMN tool will load.
	switch {
	case len(it.Components) > 0:
		for _, c := range it.Components {
			e.itemDefinition("itemComponent", c)
		}
	case it.FunctionItem != nil:
		e.open("functionItem", attr("outputTypeRef", it.FunctionItem.OutputType))
		for _, p := range it.FunctionItem.Parameters {
			e.informationItem("formalParameter", p)
		}
		e.close()
	default:
		// typeRef is required in this branch; an unconstrained definition is
		// still an Any-typed one.
		typeRef := it.TypeRef
		if typeRef == "" {
			typeRef = model.TypeAny
		}
		e.text("typeRef", typeRef)
		if it.AllowedValues != "" {
			e.open("allowedValues")
			e.text("text", it.AllowedValues)
			e.close()
		}
		if it.TypeConstraint != "" && e.opts.Namespace != NS13 {
			e.open("typeConstraint")
			e.text("text", it.TypeConstraint)
			e.close()
		}
	}
	e.close()
}

func (e *encoder) decision(d *model.Decision) {
	e.open("decision", attr("id", d.ID), attr("name", d.Name))
	e.text("description", d.Description)

	// tDMNElement puts extensionElements immediately after description, so an
	// agent decision is written here rather than in the decision-logic slot at
	// the end. The slot only accepts the DMN expression substitution group.
	if agent, ok := d.Logic.(*model.AgentDecision); ok {
		e.open("extensionElements")
		e.agentDecision(agent)
		e.close()
	}

	e.text("question", d.Question)
	e.text("allowedAnswers", d.AllowedAnswers)
	e.informationItem("variable", d.Variable)

	// Requirement elements carry generated IDs so the diagram's edges have
	// something to reference; see requirementID.
	for i, ref := range d.RequiredInputs {
		e.open("informationRequirement", attr("id", requirementID(d.ID, reqInput, i)))
		e.href("requiredInput", ref)
		e.close()
	}
	for i, ref := range d.RequiredDecisions {
		e.open("informationRequirement", attr("id", requirementID(d.ID, reqDecision, i)))
		e.href("requiredDecision", ref)
		e.close()
	}
	for i, ref := range d.RequiredKnowledge {
		e.open("knowledgeRequirement", attr("id", requirementID(d.ID, reqKnowledge, i)))
		e.href("requiredKnowledge", ref)
		e.close()
	}
	for i, ref := range d.AuthorityRequirements {
		e.open("authorityRequirement", attr("id", requirementID(d.ID, reqAuthority, i)))
		e.href("requiredAuthority", ref)
		e.close()
	}
	// The agent decision was already written into extensionElements above.
	if _, isAgent := d.Logic.(*model.AgentDecision); !isAgent {
		e.expression(d.Logic)
	}
	e.close()
}

func (e *encoder) bkm(b *model.BusinessKnowledgeModel) {
	e.open("businessKnowledgeModel", attr("id", b.ID), attr("name", b.Name))
	e.text("description", b.Description)
	e.informationItem("variable", b.Variable)
	for i, ref := range b.RequiredInputs {
		e.open("informationRequirement", attr("id", requirementID(b.ID, reqInput, i)))
		e.href("requiredInput", ref)
		e.close()
	}
	for i, ref := range b.RequiredDecisions {
		e.open("informationRequirement", attr("id", requirementID(b.ID, reqDecision, i)))
		e.href("requiredDecision", ref)
		e.close()
	}
	for i, ref := range b.RequiredKnowledge {
		e.open("knowledgeRequirement", attr("id", requirementID(b.ID, reqKnowledge, i)))
		e.href("requiredKnowledge", ref)
		e.close()
	}
	if b.Encapsulated != nil {
		e.functionDefinition("encapsulatedLogic", b.Encapsulated)
	}
	e.close()
}

func (e *encoder) decisionService(s *model.DecisionService) {
	e.open("decisionService", attr("id", s.ID), attr("name", s.Name))
	e.text("description", s.Description)
	e.informationItem("variable", s.Variable)
	for _, ref := range s.OutputDecisions {
		e.href("outputDecision", ref)
	}
	for _, ref := range s.EncapsulatedDecisions {
		e.href("encapsulatedDecision", ref)
	}
	for _, ref := range s.InputDecisions {
		e.href("inputDecision", ref)
	}
	for _, ref := range s.InputData {
		e.href("inputData", ref)
	}
	e.close()
}

// expression dispatches a boxed expression onto its DMN element.
func (e *encoder) expression(x model.Expression) {
	if x == nil {
		return
	}
	switch v := x.(type) {
	case *model.LiteralExpression:
		e.literal("literalExpression", v)
	case *model.DecisionTable:
		e.decisionTable(v)
	case *model.Invocation:
		e.invocation(v)
	case *model.ContextExpression:
		e.contextExpr(v)
	case *model.ListExpression:
		e.open("list", attr("id", v.ID), attr("typeRef", v.TypeRef))
		for _, el := range v.Elements {
			e.expression(el)
		}
		e.close()
	case *model.Relation:
		e.relation(v)
	case *model.FunctionDefinition:
		e.functionDefinition("functionDefinition", v)
	case *model.AgentDecision:
		e.agentDecision(v)
	case *model.UnknownExpression:
		// Preserved as a comment rather than silently dropped, so a reader can
		// see that something was here and what it was called.
		if e.err == nil {
			e.err = e.enc.EncodeToken(xml.Comment(
				fmt.Sprintf(" verdict: unsupported boxed expression <%s> was not written back ", v.Detail)))
		}
	}
}

func (e *encoder) literal(name string, v *model.LiteralExpression) {
	e.open(name, attr("id", v.ID), attr("typeRef", v.TypeRef),
		attr("expressionLanguage", v.ExpressionLanguage))
	e.text("text", v.Text)
	e.close()
}

func (e *encoder) decisionTable(t *model.DecisionTable) {
	attrs := []xml.Attr{
		attr("id", t.ID),
		attr("typeRef", t.TypeRef),
		attr("hitPolicy", string(t.HitPolicy)),
		attr("aggregation", string(t.Aggregation)),
		attr("preferredOrientation", string(t.Orientation)),
	}
	e.open("decisionTable", attrs...)
	for _, in := range t.Inputs {
		e.open("input", attr("id", in.ID), attr("label", in.Label))
		e.open("inputExpression", attr("typeRef", in.TypeRef))
		e.text("text", in.Expression)
		e.close()
		if in.Values != "" {
			e.open("inputValues")
			e.text("text", in.Values)
			e.close()
		}
		e.close()
	}
	for _, o := range t.Outputs {
		e.open("output", attr("id", o.ID), attr("label", o.Label),
			attr("name", o.Name), attr("typeRef", o.TypeRef))
		if o.Values != "" {
			e.open("outputValues")
			e.text("text", o.Values)
			e.close()
		}
		if o.DefaultValue != "" {
			e.open("defaultOutputEntry")
			e.text("text", o.DefaultValue)
			e.close()
		}
		e.close()
	}
	for _, a := range t.Annotations {
		e.empty("annotation", attr("name", a))
	}
	for _, r := range t.Rules {
		e.open("rule", attr("id", r.ID))
		e.text("description", r.Description)
		for _, in := range r.InputEntries {
			e.open("inputEntry")
			e.text("text", in)
			e.close()
		}
		for _, out := range r.OutputEntries {
			e.open("outputEntry")
			e.text("text", out)
			e.close()
		}
		for _, a := range r.Annotations {
			e.open("annotationEntry")
			e.text("text", a)
			e.close()
		}
		e.close()
	}
	e.close()
}

func (e *encoder) invocation(v *model.Invocation) {
	e.open("invocation", attr("id", v.ID), attr("typeRef", v.TypeRef))
	// DMN encodes the callee as a nested literal expression whose text is the
	// business knowledge model's name.
	e.open("literalExpression")
	e.text("text", v.Called)
	e.close()
	for _, b := range v.Bindings {
		e.open("binding")
		e.informationItem("parameter", b.Parameter)
		e.expression(b.Value)
		e.close()
	}
	e.close()
}

func (e *encoder) contextExpr(v *model.ContextExpression) {
	e.open("context", attr("id", v.ID), attr("typeRef", v.TypeRef))
	for _, entry := range v.Entries {
		e.open("contextEntry")
		e.informationItem("variable", entry.Variable)
		e.expression(entry.Value)
		e.close()
	}
	e.close()
}

func (e *encoder) relation(v *model.Relation) {
	e.open("relation", attr("id", v.ID), attr("typeRef", v.TypeRef))
	for _, c := range v.Columns {
		e.informationItem("column", c)
	}
	for _, row := range v.Rows {
		e.open("row")
		for _, cell := range row {
			e.expression(cell)
		}
		e.close()
	}
	e.close()
}

func (e *encoder) functionDefinition(name string, v *model.FunctionDefinition) {
	attrs := []xml.Attr{attr("id", v.ID), attr("typeRef", v.TypeRef)}
	if v.FnKind != "" && v.FnKind != model.FunctionFEEL {
		attrs = append(attrs, attr("kind", string(v.FnKind)))
	}
	e.open(name, attrs...)
	for _, p := range v.Parameters {
		e.informationItem("formalParameter", p)
	}
	e.expression(v.Body)
	e.close()
}

func (e *encoder) agentDecision(a *model.AgentDecision) {
	e.open("verdict:agentDecision", attr("id", a.ID), attr("typeRef", a.TypeRef))
	e.text("verdict:promptTemplate", a.PromptTemplate)
	for _, b := range a.Bindings {
		e.empty("verdict:inputBinding", attr("name", b.Name), attr("feel", b.FEEL))
	}
	if !a.OutputType.IsAny() {
		e.typeSpec("verdict:outputType", a.OutputType, "")
	}
	if a.Validator != "" {
		e.empty("verdict:validator", attr("feel", a.Validator))
	}
	policy := []xml.Attr{
		attr("onFailure", string(a.Policy.OnFailure)),
		attr("fallbackDecision", a.Policy.FallbackDecision),
		attr("sessionHint", a.Policy.SessionHint),
	}
	if a.Policy.MaxLatency > 0 {
		policy = append(policy, attr("maxLatency", model.FormatDuration(a.Policy.MaxLatency)))
	}
	if a.Policy.MaxRetries > 0 {
		policy = append(policy, attr("maxRetries", strconv.Itoa(a.Policy.MaxRetries)))
	}
	e.empty("verdict:policy", policy...)
	e.close()
}

// typeSpec writes an agent output type. name is the element to use and
// componentName, when non-empty, is written as the component's `name`
// attribute.
func (e *encoder) typeSpec(name string, t model.TypeSpec, componentName string) {
	attrs := []xml.Attr{attr("typeRef", t.TypeRef)}
	if componentName != "" {
		attrs = append([]xml.Attr{attr("name", componentName)}, attrs...)
	}
	if t.Collection {
		attrs = append(attrs, attr("isCollection", "true"))
	}
	e.open(name, attrs...)
	if len(t.Enumeration) > 0 {
		e.open("verdict:enumeration")
		for _, v := range t.Enumeration {
			e.text("verdict:value", v)
		}
		e.close()
	}
	for _, key := range sortedKeys(t.Components) {
		e.typeSpec("verdict:component", t.Components[key], key)
	}
	e.close()
}

func sortedKeys(m map[string]model.TypeSpec) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	// Deterministic output matters: a model written twice must be byte-identical
	// so that content addressing and version control both behave.
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j] < out[j-1]; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}

func attr(name, value string) xml.Attr {
	return xml.Attr{Name: xml.Name{Local: name}, Value: value}
}
