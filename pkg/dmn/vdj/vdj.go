// Package vdj implements Verdict Decision JSON: a lossless JSON projection of
// the DMN model, for tooling that would rather not touch XML.
//
// VDJ is a projection, not a second source of truth. Every construct maps
// one-to-one onto the DMN model, boxed expressions are a discriminated union on
// `kind`, and a document round-trips through XML and back unchanged. Anything
// DMN can express and Verdict can evaluate, VDJ can carry.
package vdj

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"

	"github.com/frankbardon/verdict/pkg/diag"
	"github.com/frankbardon/verdict/pkg/dmn/model"
)

// Version is the VDJ schema version written into every document.
const Version = "1.0"

// Document is a VDJ file.
type Document struct {
	VDJ                string `json:"vdj"`
	ID                 string `json:"id"`
	Name               string `json:"name,omitempty"`
	Namespace          string `json:"namespace,omitempty"`
	Description        string `json:"description,omitempty"`
	Version            string `json:"version,omitempty"`
	ConformanceLevel   string `json:"conformance_level,omitempty"`
	ExpressionLanguage string `json:"expression_language,omitempty"`
	Exporter           string `json:"exporter,omitempty"`
	ExporterVersion    string `json:"exporter_version,omitempty"`

	ItemDefinitions  []*ItemDefinition  `json:"item_definitions,omitempty"`
	InputData        []*InputData       `json:"input_data,omitempty"`
	Decisions        []*Decision        `json:"decisions,omitempty"`
	BKMs             []*BKM             `json:"business_knowledge_models,omitempty"`
	KnowledgeSources []*KnowledgeSource `json:"knowledge_sources,omitempty"`
	DecisionServices []*DecisionService `json:"decision_services,omitempty"`
}

// ItemDefinition is a user-defined type.
type ItemDefinition struct {
	ID             string            `json:"id,omitempty"`
	Name           string            `json:"name"`
	Description    string            `json:"description,omitempty"`
	TypeRef        string            `json:"type_ref,omitempty"`
	IsCollection   bool              `json:"is_collection,omitempty"`
	AllowedValues  string            `json:"allowed_values,omitempty"`
	TypeConstraint string            `json:"type_constraint,omitempty"`
	Components     []*ItemDefinition `json:"components,omitempty"`
	FunctionItem   *FunctionItem     `json:"function_item,omitempty"`
}

// FunctionItem types a function-valued item definition.
type FunctionItem struct {
	Parameters []*InformationItem `json:"parameters,omitempty"`
	OutputType string             `json:"output_type,omitempty"`
}

// InformationItem is a typed name.
type InformationItem struct {
	ID       string `json:"id,omitempty"`
	Name     string `json:"name"`
	TypeRef  string `json:"type_ref,omitempty"`
	Optional bool   `json:"optional,omitempty"`
}

// InputData is a named external input.
type InputData struct {
	ID          string           `json:"id,omitempty"`
	Name        string           `json:"name"`
	Description string           `json:"description,omitempty"`
	Variable    *InformationItem `json:"variable,omitempty"`
}

// Decision is a DRG decision node.
type Decision struct {
	ID             string           `json:"id,omitempty"`
	Name           string           `json:"name"`
	Description    string           `json:"description,omitempty"`
	Question       string           `json:"question,omitempty"`
	AllowedAnswers string           `json:"allowed_answers,omitempty"`
	Variable       *InformationItem `json:"variable,omitempty"`

	RequiredDecisions []string `json:"required_decisions,omitempty"`
	RequiredInputs    []string `json:"required_inputs,omitempty"`
	RequiredKnowledge []string `json:"required_knowledge,omitempty"`
	Authorities       []string `json:"authority_requirements,omitempty"`

	Logic *Expression `json:"logic,omitempty"`
}

// BKM is a business knowledge model.
type BKM struct {
	ID          string           `json:"id,omitempty"`
	Name        string           `json:"name"`
	Description string           `json:"description,omitempty"`
	Variable    *InformationItem `json:"variable,omitempty"`

	RequiredKnowledge []string `json:"required_knowledge,omitempty"`
	RequiredInputs    []string `json:"required_inputs,omitempty"`
	RequiredDecisions []string `json:"required_decisions,omitempty"`

	Encapsulated *Expression `json:"encapsulated_logic,omitempty"`
}

// KnowledgeSource is a documentation pointer to an external authority.
type KnowledgeSource struct {
	ID          string `json:"id,omitempty"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Type        string `json:"type,omitempty"`
	LocationURI string `json:"location_uri,omitempty"`
	Owner       string `json:"owner,omitempty"`
}

// DecisionService is a named callable subset of the DRG.
type DecisionService struct {
	ID          string           `json:"id,omitempty"`
	Name        string           `json:"name"`
	Description string           `json:"description,omitempty"`
	Variable    *InformationItem `json:"variable,omitempty"`

	OutputDecisions       []string `json:"output_decisions,omitempty"`
	EncapsulatedDecisions []string `json:"encapsulated_decisions,omitempty"`
	InputDecisions        []string `json:"input_decisions,omitempty"`
	InputData             []string `json:"input_data,omitempty"`
}

// Expression is the discriminated union of boxed expressions. Exactly one of
// the payload fields is set, selected by Kind.
type Expression struct {
	Kind    string `json:"kind"`
	ID      string `json:"id,omitempty"`
	TypeRef string `json:"type_ref,omitempty"`

	// literalExpression
	Text               string `json:"text,omitempty"`
	ExpressionLanguage string `json:"expression_language,omitempty"`

	// decisionTable
	HitPolicy   string         `json:"hit_policy,omitempty"`
	Aggregation string         `json:"aggregation,omitempty"`
	Orientation string         `json:"orientation,omitempty"`
	Inputs      []*TableInput  `json:"inputs,omitempty"`
	Outputs     []*TableOutput `json:"outputs,omitempty"`
	Rules       []*TableRule   `json:"rules,omitempty"`
	Annotations []string       `json:"annotations,omitempty"`

	// invocation
	Called   string     `json:"called,omitempty"`
	Bindings []*Binding `json:"bindings,omitempty"`

	// context
	Entries []*ContextEntry `json:"entries,omitempty"`

	// list
	Elements []*Expression `json:"elements,omitempty"`

	// relation
	Columns []*InformationItem `json:"columns,omitempty"`
	Rows    [][]*Expression    `json:"rows,omitempty"`

	// functionDefinition
	Parameters   []*InformationItem `json:"parameters,omitempty"`
	Body         *Expression        `json:"body,omitempty"`
	FunctionKind string             `json:"function_kind,omitempty"`

	// agentDecision
	Agent *AgentDecision `json:"agent,omitempty"`

	// unknown
	Detail string `json:"detail,omitempty"`
}

// TableInput is one input clause.
type TableInput struct {
	ID         string `json:"id,omitempty"`
	Label      string `json:"label,omitempty"`
	Expression string `json:"expression"`
	TypeRef    string `json:"type_ref,omitempty"`
	Values     string `json:"values,omitempty"`
}

// TableOutput is one output clause.
type TableOutput struct {
	ID           string `json:"id,omitempty"`
	Label        string `json:"label,omitempty"`
	Name         string `json:"name,omitempty"`
	TypeRef      string `json:"type_ref,omitempty"`
	Values       string `json:"values,omitempty"`
	DefaultValue string `json:"default_value,omitempty"`
}

// TableRule is one rule.
type TableRule struct {
	ID            string   `json:"id,omitempty"`
	Description   string   `json:"description,omitempty"`
	InputEntries  []string `json:"when"`
	OutputEntries []string `json:"then"`
	Annotations   []string `json:"annotations,omitempty"`
}

// Binding is one named parameter of an invocation.
type Binding struct {
	Parameter *InformationItem `json:"parameter,omitempty"`
	Value     *Expression      `json:"value,omitempty"`
}

// ContextEntry is one named entry of a boxed context.
type ContextEntry struct {
	Variable *InformationItem `json:"variable,omitempty"`
	Value    *Expression      `json:"value,omitempty"`
}

// AgentDecision is the Verdict extension node.
type AgentDecision struct {
	PromptTemplate string          `json:"prompt_template"`
	Bindings       []*AgentBinding `json:"input_bindings,omitempty"`
	OutputType     *TypeSpec       `json:"output_type,omitempty"`
	Validator      string          `json:"validator,omitempty"`
	Policy         *AgentPolicy    `json:"policy,omitempty"`
}

// AgentBinding binds a prompt input to a FEEL expression.
type AgentBinding struct {
	Name string `json:"name"`
	FEEL string `json:"feel"`
}

// TypeSpec describes an agent decision's declared return shape.
type TypeSpec struct {
	TypeRef     string              `json:"type_ref,omitempty"`
	Enumeration []string            `json:"enumeration,omitempty"`
	Collection  bool                `json:"collection,omitempty"`
	Components  map[string]TypeSpec `json:"components,omitempty"`
}

// AgentPolicy bounds an agent invocation. MaxLatency is an ISO-8601 duration,
// matching the XML attribute.
type AgentPolicy struct {
	MaxLatency       string `json:"max_latency,omitempty"`
	MaxRetries       int    `json:"max_retries,omitempty"`
	OnFailure        string `json:"on_failure,omitempty"`
	FallbackDecision string `json:"fallback_decision,omitempty"`
	SessionHint      string `json:"session_hint,omitempty"`
}

// Parse reads a VDJ document and projects it onto the model.
func Parse(raw []byte) (*model.Definitions, []diag.Diagnostic, error) {
	var doc Document
	raw = trimBOM(raw)
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&doc); err != nil {
		// A document with an unrecognised field is more likely to be a newer VDJ
		// version than a mistake, so retry leniently and report it rather than
		// refusing to load.
		var lenient Document
		if err2 := json.Unmarshal(raw, &lenient); err2 != nil {
			return nil, nil, fmt.Errorf("vdj: parsing document: %w", err2)
		}
		doc = lenient
		defs, ds := (&converter{}).convert(&doc, raw)
		ds = append(ds, diag.Warnf(diag.CodeUnsupportedExpression,
			"document contains fields this version of Verdict does not recognise: %v", err))
		return defs, ds, nil
	}
	defs, ds := (&converter{}).convert(&doc, raw)
	return defs, ds, nil
}

type converter struct{ diags diag.Set }

func (c *converter) convert(doc *Document, raw []byte) (*model.Definitions, []diag.Diagnostic) {
	defs := &model.Definitions{
		ID:                 firstNonEmpty(doc.ID, doc.Name),
		Name:               doc.Name,
		Namespace:          doc.Namespace,
		Description:        doc.Description,
		Version:            doc.Version,
		ExpressionLanguage: doc.ExpressionLanguage,
		Exporter:           doc.Exporter,
		ExporterVersion:    doc.ExporterVersion,
	}
	if lvl, err := model.ParseConformanceLevel(doc.ConformanceLevel); err != nil {
		c.diags.Add(diag.Warnf(diag.CodeForeignLanguage, "%v; falling back to the engine default", err))
	} else {
		defs.Level = lvl
	}

	for _, it := range doc.ItemDefinitions {
		defs.ItemDefinitions = append(defs.ItemDefinitions, c.itemDefinition(it))
	}
	for _, in := range doc.InputData {
		defs.InputData = append(defs.InputData, &model.InputData{
			ID:          firstNonEmpty(in.ID, in.Name),
			Name:        in.Name,
			Description: in.Description,
			Variable:    c.informationItem(in.Variable),
		})
	}
	for _, d := range doc.Decisions {
		id := firstNonEmpty(d.ID, d.Name)
		dec := &model.Decision{
			ID:                    id,
			Name:                  d.Name,
			Description:           d.Description,
			Question:              d.Question,
			AllowedAnswers:        d.AllowedAnswers,
			Variable:              c.informationItem(d.Variable),
			RequiredDecisions:     d.RequiredDecisions,
			RequiredInputs:        d.RequiredInputs,
			RequiredKnowledge:     d.RequiredKnowledge,
			AuthorityRequirements: d.Authorities,
		}
		if d.Logic == nil {
			c.diags.Add(diag.Warnf(diag.CodeMissingLogic,
				"decision has no decision logic; it will evaluate to null").At(id, d.Name))
		} else {
			dec.Logic = c.expression(d.Logic, id, d.Name)
		}
		defs.Decisions = append(defs.Decisions, dec)
	}
	for _, b := range doc.BKMs {
		id := firstNonEmpty(b.ID, b.Name)
		bkm := &model.BusinessKnowledgeModel{
			ID:                id,
			Name:              b.Name,
			Description:       b.Description,
			Variable:          c.informationItem(b.Variable),
			RequiredKnowledge: b.RequiredKnowledge,
			RequiredInputs:    b.RequiredInputs,
			RequiredDecisions: b.RequiredDecisions,
		}
		if b.Encapsulated != nil {
			fn, _ := c.expression(b.Encapsulated, id, b.Name).(*model.FunctionDefinition)
			bkm.Encapsulated = fn
		}
		defs.BKMs = append(defs.BKMs, bkm)
	}
	for _, k := range doc.KnowledgeSources {
		defs.KnowledgeSources = append(defs.KnowledgeSources, &model.KnowledgeSource{
			ID: firstNonEmpty(k.ID, k.Name), Name: k.Name, Description: k.Description,
			Type: k.Type, LocationURI: k.LocationURI, Owner: k.Owner,
		})
	}
	for _, s := range doc.DecisionServices {
		defs.DecisionServices = append(defs.DecisionServices, &model.DecisionService{
			ID: firstNonEmpty(s.ID, s.Name), Name: s.Name, Description: s.Description,
			Variable:              c.informationItem(s.Variable),
			OutputDecisions:       s.OutputDecisions,
			EncapsulatedDecisions: s.EncapsulatedDecisions,
			InputDecisions:        s.InputDecisions,
			InputData:             s.InputData,
		})
	}

	sum := sha256.Sum256(raw)
	defs.Hash = hex.EncodeToString(sum[:])
	return defs, c.diags.All()
}

func (c *converter) itemDefinition(it *ItemDefinition) *model.ItemDefinition {
	if it == nil {
		return nil
	}
	out := &model.ItemDefinition{
		ID: firstNonEmpty(it.ID, it.Name), Name: it.Name, Description: it.Description,
		TypeRef: it.TypeRef, IsCollection: it.IsCollection,
		AllowedValues: it.AllowedValues, TypeConstraint: it.TypeConstraint,
	}
	for _, comp := range it.Components {
		out.Components = append(out.Components, c.itemDefinition(comp))
	}
	if it.FunctionItem != nil {
		fi := &model.FunctionItem{OutputType: it.FunctionItem.OutputType}
		for _, p := range it.FunctionItem.Parameters {
			fi.Parameters = append(fi.Parameters, c.informationItem(p))
		}
		out.FunctionItem = fi
	}
	return out
}

func (c *converter) informationItem(v *InformationItem) *model.InformationItem {
	if v == nil {
		return nil
	}
	return &model.InformationItem{ID: v.ID, Name: v.Name, TypeRef: v.TypeRef, Optional: v.Optional}
}

func firstNonEmpty(vs ...string) string {
	for _, v := range vs {
		if v != "" {
			return v
		}
	}
	return ""
}

// trimBOM strips a UTF-8 byte-order mark so a document written by an editor
// that adds one still decodes.
func trimBOM(raw []byte) []byte {
	if len(raw) >= 3 && raw[0] == 0xEF && raw[1] == 0xBB && raw[2] == 0xBF {
		return raw[3:]
	}
	return raw
}
