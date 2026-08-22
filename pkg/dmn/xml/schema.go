// Package xml reads and writes DMN documents in their standard XML
// serialisation, projecting them onto the version-neutral types in
// pkg/dmn/model.
//
// The reader is namespace-tolerant by design. DMN 1.3, 1.4 and 1.5 differ only
// in their MODEL namespace URI for everything Verdict evaluates, and real
// exporters emit a mix of them, so element matching is by local name. The
// writer always emits the DMN 1.5 namespace.
package xml

import "encoding/xml"

// Namespace URIs, by DMN version. The reader accepts any of them; the writer
// emits NS15.
const (
	NS13 = "https://www.omg.org/spec/DMN/20191111/MODEL/"
	NS14 = "https://www.omg.org/spec/DMN/20211108/MODEL/"
	NS15 = "https://www.omg.org/spec/DMN/20230324/MODEL/"

	// The diagram-interchange namespaces. DMNDI is versioned alongside MODEL;
	// DC and DI are not, and have been stable since 2018.
	NSDMNDI13 = "https://www.omg.org/spec/DMN/20191111/DMNDI/"
	NSDMNDI14 = "https://www.omg.org/spec/DMN/20211108/DMNDI/"
	NSDMNDI15 = "https://www.omg.org/spec/DMN/20230324/DMNDI/"
	NSDC      = "http://www.omg.org/spec/DMN/20180521/DC/"
	NSDI      = "http://www.omg.org/spec/DMN/20180521/DI/"

	// NSVerdict is where the agentDecision extension lives.
	NSVerdict = "https://github.com/frankbardon/verdict/schema/1.0"

	// FEELLanguage is the URI DMN assigns to FEEL. An expressionLanguage
	// attribute naming anything else is reported as a diagnostic.
	FEELLanguage = "https://www.omg.org/spec/DMN/20230324/FEEL/"
)

// definitions mirrors the DMN <definitions> root.
type definitions struct {
	XMLName xml.Name `xml:"definitions"`

	ID                 string `xml:"id,attr"`
	Name               string `xml:"name,attr"`
	Namespace          string `xml:"namespace,attr"`
	ExpressionLanguage string `xml:"expressionLanguage,attr"`
	Exporter           string `xml:"exporter,attr"`
	ExporterVersion    string `xml:"exporterVersion,attr"`

	// Version and ConformanceLevel are Verdict extension attributes.
	//
	// They are read from the Verdict namespace, which is where the writer puts
	// them and the only place DMN's `tDefinitions` permits a foreign attribute
	// (its `anyAttribute` is `namespace="##other"`). The unqualified spellings
	// are still accepted, because early Verdict models wrote them that way and
	// refusing to read your own old output is not a defensible position.
	//
	// A document without them is unversioned and adopts the engine's configured
	// dialect.
	Version             string `xml:"https://github.com/frankbardon/verdict/schema/1.0 version,attr"`
	VersionAlt          string `xml:"version,attr"`
	ConformanceLevel    string `xml:"https://github.com/frankbardon/verdict/schema/1.0 conformanceLevel,attr"`
	ConformanceLevelAlt string `xml:"conformanceLevel,attr"`

	Description string `xml:"description"`

	ItemDefinitions  []itemDefinition  `xml:"itemDefinition"`
	Decisions        []decision        `xml:"decision"`
	InputData        []inputData       `xml:"inputData"`
	BKMs             []bkm             `xml:"businessKnowledgeModel"`
	KnowledgeSources []knowledgeSource `xml:"knowledgeSource"`
	DecisionServices []decisionService `xml:"decisionService"`
}

type itemDefinition struct {
	ID             string           `xml:"id,attr"`
	Name           string           `xml:"name,attr"`
	TypeRef        string           `xml:"typeRef,attr"`
	IsCollection   bool             `xml:"isCollection,attr"`
	Description    string           `xml:"description"`
	TypeRefElem    string           `xml:"typeRef"`
	AllowedValues  *textElem        `xml:"allowedValues"`
	TypeConstraint *textElem        `xml:"typeConstraint"`
	Components     []itemDefinition `xml:"itemComponent"`
	FunctionItem   *functionItem    `xml:"functionItem"`
}

type functionItem struct {
	OutputTypeRef string            `xml:"outputTypeRef,attr"`
	Parameters    []informationItem `xml:"formalParameter"`
}

type textElem struct {
	Text string `xml:"text"`
	// Chars catches implementations that put the value in the element body
	// rather than in a nested <text>.
	Chars string `xml:",chardata"`
}

// Value returns the nested <text> when present, falling back to the element's
// own character data.
func (t *textElem) Value() string {
	if t == nil {
		return ""
	}
	if t.Text != "" {
		return t.Text
	}
	return trim(t.Chars)
}

type informationItem struct {
	ID       string `xml:"id,attr"`
	Name     string `xml:"name,attr"`
	TypeRef  string `xml:"typeRef,attr"`
	Optional bool   `xml:"isOptional,attr"`
}

type requirement struct {
	ID                string    `xml:"id,attr"`
	RequiredDecision  *hrefElem `xml:"requiredDecision"`
	RequiredInput     *hrefElem `xml:"requiredInput"`
	RequiredKnowledge *hrefElem `xml:"requiredKnowledge"`
	RequiredAuthority *hrefElem `xml:"requiredAuthority"`
}

type hrefElem struct {
	Href string `xml:"href,attr"`
}

func (h *hrefElem) ref() string {
	if h == nil {
		return ""
	}
	return trimHash(h.Href)
}

type decision struct {
	ID             string `xml:"id,attr"`
	Name           string `xml:"name,attr"`
	Description    string `xml:"description"`
	Question       string `xml:"question"`
	AllowedAnswers string `xml:"allowedAnswers"`

	Variable *informationItem `xml:"variable"`

	InformationRequirements []requirement `xml:"informationRequirement"`
	KnowledgeRequirements   []requirement `xml:"knowledgeRequirement"`
	AuthorityRequirements   []requirement `xml:"authorityRequirement"`

	Extensions *extensionElements `xml:"extensionElements"`

	// Logic is the boxed expression. Every DMN expression element is caught
	// here because the named fields above claim everything else.
	Logic *expressionHolder `xml:",any"`
}

type inputData struct {
	ID          string           `xml:"id,attr"`
	Name        string           `xml:"name,attr"`
	Description string           `xml:"description"`
	Variable    *informationItem `xml:"variable"`
}

type bkm struct {
	ID          string           `xml:"id,attr"`
	Name        string           `xml:"name,attr"`
	Description string           `xml:"description"`
	Variable    *informationItem `xml:"variable"`

	KnowledgeRequirements   []requirement `xml:"knowledgeRequirement"`
	AuthorityRequirements   []requirement `xml:"authorityRequirement"`
	InformationRequirements []requirement `xml:"informationRequirement"`

	Encapsulated *functionDefinition `xml:"encapsulatedLogic"`
}

type knowledgeSource struct {
	ID          string `xml:"id,attr"`
	Name        string `xml:"name,attr"`
	Description string `xml:"description"`
	Type        string `xml:"type"`
	LocationURI string `xml:"locationURI,attr"`
	// Owner is DMN's optional pointer at the organisation unit responsible for
	// the source. It is decoded as a nested href element rather than an
	// attribute chain, which encoding/xml does not support.
	Owner *hrefElem `xml:"owner"`
}

type decisionService struct {
	ID          string           `xml:"id,attr"`
	Name        string           `xml:"name,attr"`
	Description string           `xml:"description"`
	Variable    *informationItem `xml:"variable"`

	OutputDecisions       []hrefElem `xml:"outputDecision"`
	EncapsulatedDecisions []hrefElem `xml:"encapsulatedDecision"`
	InputDecisions        []hrefElem `xml:"inputDecision"`
	InputData             []hrefElem `xml:"inputData"`
}

// extensionElements holds vendor extensions. Verdict looks for its own
// agentDecision here as well as accepting it as a direct child of a decision.
type extensionElements struct {
	Agent *agentDecisionXML `xml:"agentDecision"`
	// Raw preserves everything else so a round-trip does not silently drop
	// another vendor's extensions.
	Raw []byte `xml:",innerxml"`
}
