package xml

import (
	"encoding/xml"
	"fmt"
)

// expressionHolder is the polymorphic slot for a boxed expression. DMN puts the
// expression kind in the element name, so decoding is dispatched on the local
// name of the start element.
type expressionHolder struct {
	kind string
	// exactly one of the following is set
	literal  *literalExpression
	table    *decisionTable
	invoke   *invocation
	context  *contextExpression
	list     *listExpression
	relation *relationExpression
	function *functionDefinition
	agent    *agentDecisionXML

	// unknown records an element Verdict does not implement, so the loader can
	// report a precise diagnostic instead of failing the whole document.
	unknown string
}

// UnmarshalXML dispatches on the element's local name, ignoring its namespace
// so that DMN 1.3, 1.4 and 1.5 documents all decode through the same path.
func (h *expressionHolder) UnmarshalXML(d *xml.Decoder, start xml.StartElement) error {
	h.kind = start.Name.Local
	switch start.Name.Local {
	case "literalExpression":
		h.literal = &literalExpression{}
		return d.DecodeElement(h.literal, &start)
	case "decisionTable":
		h.table = &decisionTable{}
		return d.DecodeElement(h.table, &start)
	case "invocation":
		h.invoke = &invocation{}
		return d.DecodeElement(h.invoke, &start)
	case "context":
		h.context = &contextExpression{}
		return d.DecodeElement(h.context, &start)
	case "list":
		h.list = &listExpression{}
		return d.DecodeElement(h.list, &start)
	case "relation":
		h.relation = &relationExpression{}
		return d.DecodeElement(h.relation, &start)
	case "functionDefinition":
		h.function = &functionDefinition{}
		return d.DecodeElement(h.function, &start)
	case "agentDecision":
		h.agent = &agentDecisionXML{}
		return d.DecodeElement(h.agent, &start)
	default:
		h.unknown = start.Name.Local
		return d.Skip()
	}
}

type literalExpression struct {
	ID                 string `xml:"id,attr"`
	TypeRef            string `xml:"typeRef,attr"`
	ExpressionLanguage string `xml:"expressionLanguage,attr"`
	Text               string `xml:"text"`
}

type decisionTable struct {
	ID                   string `xml:"id,attr"`
	TypeRef              string `xml:"typeRef,attr"`
	HitPolicy            string `xml:"hitPolicy,attr"`
	Aggregation          string `xml:"aggregation,attr"`
	PreferredOrientation string `xml:"preferredOrientation,attr"`
	OutputLabel          string `xml:"outputLabel,attr"`

	Inputs      []tableInput  `xml:"input"`
	Outputs     []tableOutput `xml:"output"`
	Annotations []annotation  `xml:"annotation"`
	Rules       []tableRule   `xml:"rule"`
}

type tableInput struct {
	ID              string             `xml:"id,attr"`
	Label           string             `xml:"label,attr"`
	InputExpression *literalExpression `xml:"inputExpression"`
	InputValues     *textElem          `xml:"inputValues"`
}

type tableOutput struct {
	ID                 string    `xml:"id,attr"`
	Label              string    `xml:"label,attr"`
	Name               string    `xml:"name,attr"`
	TypeRef            string    `xml:"typeRef,attr"`
	OutputValues       *textElem `xml:"outputValues"`
	DefaultOutputEntry *textElem `xml:"defaultOutputEntry"`
}

type annotation struct {
	Name string `xml:"name,attr"`
}

type tableRule struct {
	ID              string     `xml:"id,attr"`
	Description     string     `xml:"description"`
	InputEntries    []textElem `xml:"inputEntry"`
	OutputEntries   []textElem `xml:"outputEntry"`
	AnnotationEntry []textElem `xml:"annotationEntry"`
}

type invocation struct {
	ID      string `xml:"id,attr"`
	TypeRef string `xml:"typeRef,attr"`
	// Called is the nested literal expression naming the invoked BKM. It is the
	// first unnamed child, so it lands in Expr along with nothing else.
	Called   *literalExpression `xml:"literalExpression"`
	Bindings []binding          `xml:"binding"`
}

type binding struct {
	Parameter *informationItem  `xml:"parameter"`
	Value     *expressionHolder `xml:",any"`
}

type contextExpression struct {
	ID      string         `xml:"id,attr"`
	TypeRef string         `xml:"typeRef,attr"`
	Entries []contextEntry `xml:"contextEntry"`
}

type contextEntry struct {
	Variable *informationItem  `xml:"variable"`
	Value    *expressionHolder `xml:",any"`
}

type listExpression struct {
	ID       string              `xml:"id,attr"`
	TypeRef  string              `xml:"typeRef,attr"`
	Elements []*expressionHolder `xml:",any"`
}

type relationExpression struct {
	ID      string            `xml:"id,attr"`
	TypeRef string            `xml:"typeRef,attr"`
	Columns []informationItem `xml:"column"`
	Rows    []relationRow     `xml:"row"`
}

type relationRow struct {
	ID    string              `xml:"id,attr"`
	Cells []*expressionHolder `xml:",any"`
}

type functionDefinition struct {
	ID         string              `xml:"id,attr"`
	TypeRef    string              `xml:"typeRef,attr"`
	Kind       string              `xml:"kind,attr"`
	Parameters []informationItem   `xml:"formalParameter"`
	Body       []*expressionHolder `xml:",any"`
}

// agentDecisionXML is the Verdict extension element. It is namespaced under
// NSVerdict; a standard DMN tool round-tripping the document sees a typed
// extension it can preserve.
type agentDecisionXML struct {
	ID             string              `xml:"id,attr"`
	TypeRef        string              `xml:"typeRef,attr"`
	PromptTemplate string              `xml:"promptTemplate"`
	Bindings       []agentBindingXML   `xml:"inputBinding"`
	OutputType     *agentOutputTypeXML `xml:"outputType"`
	Validator      *agentValidatorXML  `xml:"validator"`
	Policy         *agentPolicyXML     `xml:"policy"`
}

type agentBindingXML struct {
	Name string `xml:"name,attr"`
	FEEL string `xml:"feel,attr"`
	Text string `xml:",chardata"`
}

type agentOutputTypeXML struct {
	TypeRef     string              `xml:"typeRef,attr"`
	Collection  bool                `xml:"isCollection,attr"`
	Enumeration *enumerationXML     `xml:"enumeration"`
	Components  []agentComponentXML `xml:"component"`
}

type enumerationXML struct {
	Values []string `xml:"value"`
}

type agentComponentXML struct {
	Name        string              `xml:"name,attr"`
	TypeRef     string              `xml:"typeRef,attr"`
	Collection  bool                `xml:"isCollection,attr"`
	Enumeration *enumerationXML     `xml:"enumeration"`
	Components  []agentComponentXML `xml:"component"`
}

type agentValidatorXML struct {
	FEEL string `xml:"feel,attr"`
	Text string `xml:",chardata"`
}

type agentPolicyXML struct {
	MaxLatency       string `xml:"maxLatency,attr"`
	MaxRetries       string `xml:"maxRetries,attr"`
	OnFailure        string `xml:"onFailure,attr"`
	FallbackDecision string `xml:"fallbackDecision,attr"`
	SessionHint      string `xml:"sessionHint,attr"`
}

func (h *expressionHolder) describe() string {
	if h == nil {
		return "<none>"
	}
	return fmt.Sprintf("<%s>", h.kind)
}
