package vdj

import (
	"encoding/json"
	"reflect"
	"sort"

	"github.com/frankbardon/verdict/pkg/dmn/model"
)

const (
	// SchemaDialect is the JSON Schema draft the VDJ contract targets.
	SchemaDialect = "https://json-schema.org/draft/2020-12/schema"

	// SchemaID is the schema's canonical identifier. It is also the URL the
	// documentation workflow publishes the file to, so the `$id` doubles as a
	// retrieval URI: a validator that resolves it gets the real document.
	SchemaID = "https://frankbardon.github.io/verdict/vdj-schema.json"
)

// BuildSchema returns the JSON Schema (draft 2020-12) describing a Verdict
// Decision JSON document.
//
// The schema is generated, never hand-maintained, from three sources:
//
//  1. Reflection over the VDJ document structs in this package. They are the
//     same types Parse decodes into, so a renamed field, a new field or a
//     changed `omitempty` shows up as a golden diff and cannot ship silently.
//  2. The model vocabulary registry (model.All*), which supplies every closed
//     enum — boxed-expression kinds, hit policies, aggregations, orientations,
//     function kinds, agent failure policies, conformance levels. Adding a hit
//     policy to the engine therefore changes the published contract in the same
//     commit.
//  3. A hand-written discrimination table for Expression, the one shape
//     reflection cannot express. VDJ's boxed expression is a flat union: every
//     kind's payload fields sit side by side on one object, selected by `kind`.
//     expressionFields records which fields belong to which kind, and the
//     generator turns that into an `allOf` of "if kind is K, the other kinds'
//     fields are forbidden" clauses — so a decisionTable carrying `elements`,
//     or a literalExpression carrying `rules`, fails validation rather than
//     being quietly ignored by the reader.
//
// The output marshals deterministically: every object is a map (encoding/json
// sorts keys) and every enum, `required` list and generated `allOf` is sorted,
// so the golden is stable across runs and Go versions.
//
// Two deliberate boundaries, documented in docs/src/vdj/schema.md:
//
//   - `hit_policy` enumerates the canonical uppercase spellings and the
//     single-cell shorthands. The reader additionally trims and upper-cases,
//     so it accepts `collect` where the schema does not. The schema describes
//     what a writer should emit, not the full tolerance of the reader.
//   - Verdict's reader accepts a document carrying unknown fields, reporting a
//     diagnostic rather than refusing to load, because a newer VDJ version is
//     the likelier explanation. The schema is stricter
//     (`additionalProperties: false`), because catching a mistyped field in a
//     hand-written document is most of what a schema is for.
func BuildSchema() json.RawMessage {
	b := newSchemaBuilder()
	root := map[string]any{
		"$schema": SchemaDialect,
		"$id":     SchemaID,
		"title":   "Verdict Decision JSON",
		"description": "Verdict Decision JSON (VDJ) " + Version + ": a lossless JSON projection of a DMN " +
			"decision model. Every construct maps one-to-one onto the DMN model, and a document " +
			"round-trips through DMN XML and back unchanged.",
		"$ref":  "#/$defs/" + b.register(reflect.TypeFor[Document]()),
		"$defs": b.defs,
	}
	out, err := json.MarshalIndent(root, "", "  ")
	if err != nil {
		// The input is composed entirely of marshalable maps, slices and
		// strings, so a failure here is a programming error rather than a
		// runtime condition a caller could act on.
		panic("vdj: BuildSchema marshal: " + err.Error())
	}
	return out
}

// expressionFields records which Expression properties belong to which boxed
// expression kind. Properties in no row — the shared header, see
// expressionHeaderFields — are permitted for every kind.
//
// A new boxed-expression kind adds a row here; a new field on an existing kind
// extends one. TestSchemaCoversEveryExpressionField fails if a field on the
// Expression struct appears in no row and is not a header field, so a field
// cannot be added to the union and then forgotten here.
var expressionFields = map[model.Kind][]string{
	model.KindLiteral:       {"text", "expression_language"},
	model.KindDecisionTable: {"hit_policy", "aggregation", "orientation", "inputs", "outputs", "rules", "annotations"},
	model.KindInvocation:    {"called", "bindings"},
	model.KindContext:       {"entries"},
	model.KindList:          {"elements"},
	model.KindRelation:      {"columns", "rows"},
	model.KindFunction:      {"parameters", "body", "function_kind"},
	model.KindAgent:         {"agent"},
	model.KindUnknown:       {"detail"},
}

// expressionHeaderFields are the properties every kind carries.
var expressionHeaderFields = []string{"kind", "id", "type_ref"}

// requiredByKind names the fields whose absence the reader reports as a load
// error rather than tolerating. It is deliberately short: the schema requires
// what the engine will not run without and no more, so a model still being
// written validates while a broken one does not.
var requiredByKind = map[model.Kind][]string{
	model.KindInvocation: {"called"},
	model.KindAgent:      {"agent"},
}

// enumOverrides injects the registry-backed enums onto fields that are plain
// strings in Go. VDJ types its discriminants as strings rather than as the
// model's named types — the projection deliberately owns its own wire types —
// so an enum cannot be attached by reflecting on the field type. The key is
// "<def name>.<json property>".
func enumOverrides() map[string][]string {
	return map[string][]string{
		"Document.conformance_level": model.AllConformanceLevels(),
		"Expression.kind":            stringify(model.AllKinds()),
		"Expression.hit_policy":      hitPolicyValues(),
		"Expression.aggregation":     stringify(model.AllAggregations()),
		"Expression.orientation":     stringify(model.AllOrientations()),
		"Expression.function_kind":   stringify(model.AllFunctionKinds()),
		"AgentPolicy.on_failure":     stringify(model.AllFailurePolicies()),
	}
}

// hitPolicyValues is the accepted spelling set for a table's hit policy: the
// long DMN names and the single-cell shorthands a modeller writes in the
// table's corner.
func hitPolicyValues() []string {
	out := stringify(model.AllHitPolicies())
	out = append(out, model.AllHitPolicyShorthands()...)
	sort.Strings(out)
	return out
}

// descriptions annotate the properties whose meaning is not obvious from the
// name. Keyed like enumOverrides, "<def name>.<json property>".
func descriptions() map[string]string {
	return map[string]string{
		"Document.vdj":                           "VDJ format version. The current version is " + Version + ".",
		"Document.id":                            "Stable identifier for the model. Verdict falls back to `name` when it is absent.",
		"Document.namespace":                     "The DMN namespace this model's elements belong to.",
		"Document.conformance_level":             "The DMN conformance level to evaluate at: `s-feel` restricts expressions to S-FEEL, `feel` admits the full dialect. Omit to take the engine default.",
		"Document.expression_language":           "Default expression language URI for the document's literal expressions.",
		"Expression.kind":                        "Selects which boxed expression this is, and therefore which of the payload properties may be present.",
		"Expression.type_ref":                    "The type of the value this expression produces: a FEEL builtin or the name of an item definition.",
		"Expression.text":                        "literalExpression: the FEEL expression source.",
		"Expression.hit_policy":                  "decisionTable: how multiple matching rules are resolved. Accepts the long DMN spelling (`COLLECT`) or the single-cell shorthand (`C+`).",
		"Expression.aggregation":                 "decisionTable: the COLLECT aggregator, when the hit policy is COLLECT and the shorthand did not already name one.",
		"Expression.orientation":                 "decisionTable: presentation only. The engine evaluates every orientation identically.",
		"Expression.annotations":                 "decisionTable: the annotation column headers. Each rule's `annotations` align with these positionally.",
		"Expression.called":                      "invocation: the name of the business knowledge model being invoked.",
		"Expression.rows":                        "relation: rows of cells, each row aligned positionally with `columns`.",
		"Expression.function_kind":               "functionDefinition: the body's binding. `Java` and `PMML` bodies are preserved but not executed; calling one returns null.",
		"Expression.detail":                      "unknown: what the unrecognised logic was, preserved so a round trip does not lose it.",
		"TableInput.expression":                  "The FEEL expression producing the value each rule's `when` entry is tested against.",
		"TableInput.values":                      "Optional FEEL list constraining the input's domain. The analyser uses it to decide what counts as a gap.",
		"TableOutput.name":                       "The output's name. Required when the table has more than one output; a single-output table returns the value itself, not a record.",
		"TableOutput.values":                     "Optional FEEL list of permitted outputs. Under PRIORITY and OUTPUT ORDER this list is also the ranking, most preferred first.",
		"TableRule.when":                         "One unary test per input clause, positionally aligned. `-` matches anything.",
		"TableRule.then":                         "One FEEL expression per output clause, positionally aligned.",
		"AgentDecision.prompt_template":          "A Go text/template rendered against the bound inputs. Template errors are reported at load time.",
		"AgentDecision.input_bindings":           "The only values the agent sees. The model context is never passed through; each binding names a value and the FEEL expression producing it.",
		"AgentDecision.validator":                "Optional FEEL expression evaluated with the coerced response bound to `value`. Anything but true is a failure.",
		"AgentBinding.feel":                      "FEEL expression evaluated against the decision's own context to produce this binding's value.",
		"TypeSpec.type_ref":                      "A FEEL builtin type or an item definition name. Empty means Any.",
		"TypeSpec.enumeration":                   "Restricts the response to this set of strings.",
		"TypeSpec.components":                    "Types a structured response, keyed by field name.",
		"AgentPolicy.max_latency":                "ISO-8601 duration bounding one invocation including its retries. Omit to take the engine default.",
		"AgentPolicy.max_retries":                "Additional attempts after the first.",
		"AgentPolicy.on_failure":                 "What to do when the agent fails to produce a conforming value within its budget.",
		"AgentPolicy.fallback_decision":          "The decision evaluated instead, when `on_failure` is `fallback`.",
		"AgentPolicy.session_hint":               "Passed through to the bridge. A bridge that reuses sessions may key on it.",
		"ItemDefinition.allowed_values":          "FEEL unary tests constraining the type's values.",
		"ItemDefinition.type_constraint":         "DMN 1.4+ type constraint, preserved through round trips.",
		"DecisionService.output_decisions":       "The decisions whose values the service returns.",
		"DecisionService.encapsulated_decisions": "Decisions evaluated inside the service but not returned.",
	}
}

// stringify converts a slice of ~string constants to a sorted []string. Sorting
// makes the generated enum stable against a reordering of the const block.
func stringify[T ~string](in []T) []string {
	out := make([]string, len(in))
	for i, v := range in {
		out[i] = string(v)
	}
	sort.Strings(out)
	return out
}

type schemaBuilder struct {
	defs  map[string]any
	enums map[string][]string
	descs map[string]string
}

func newSchemaBuilder() *schemaBuilder {
	return &schemaBuilder{
		defs:  map[string]any{},
		enums: enumOverrides(),
		descs: descriptions(),
	}
}

// register ensures a $def exists for the named type t and returns its def name.
// The name is reserved before recursing so a cyclic type graph — an Expression
// whose body is an Expression — resolves to a $ref rather than looping.
func (b *schemaBuilder) register(t reflect.Type) string {
	name := t.Name()
	if _, ok := b.defs[name]; ok {
		return name
	}
	b.defs[name] = true // placeholder; breaks the recursion cycle
	b.defs[name] = b.defFor(name, t)
	return name
}

func (b *schemaBuilder) defFor(name string, t reflect.Type) any {
	if t == reflect.TypeFor[Expression]() {
		return b.expressionDef(name, t)
	}
	if t.Kind() == reflect.Struct {
		return b.structSchema(name, t)
	}
	return b.schemaFor(name, t, true)
}

// expressionDef builds the discriminated union. The reflected object shape is
// the union of every kind's fields; the generated allOf narrows it to the one
// kind the `kind` property selects.
func (b *schemaBuilder) expressionDef(name string, t reflect.Type) any {
	schema := b.structSchema(name, t).(map[string]any)
	schema["description"] = "A boxed expression. `kind` selects the payload: the properties belonging to other kinds are forbidden."

	props := schema["properties"].(map[string]any)
	header := map[string]bool{}
	for _, f := range expressionHeaderFields {
		header[f] = true
	}

	kinds := make([]string, 0, len(expressionFields))
	for k := range expressionFields {
		kinds = append(kinds, string(k))
	}
	sort.Strings(kinds)

	var allOf []any
	for _, kind := range kinds {
		own := map[string]bool{}
		for _, f := range expressionFields[model.Kind(kind)] {
			own[f] = true
		}
		forbidden := map[string]any{}
		for prop := range props {
			if !own[prop] && !header[prop] {
				forbidden[prop] = false
			}
		}
		then := map[string]any{}
		if len(forbidden) > 0 {
			then["properties"] = forbidden
		}
		if req := requiredByKind[model.Kind(kind)]; len(req) > 0 {
			sorted := append([]string(nil), req...)
			sort.Strings(sorted)
			then["required"] = sorted
		}
		allOf = append(allOf, map[string]any{
			"if": map[string]any{
				"properties": map[string]any{"kind": map[string]any{"const": kind}},
				"required":   []string{"kind"},
			},
			"then": then,
		})
	}
	schema["allOf"] = allOf
	return schema
}

// schemaFor returns an inline schema or a $ref for a field type. inlineNamed
// short-circuits the named-type-to-$ref rule so defFor can expand a named
// type's own underlying shape.
func (b *schemaBuilder) schemaFor(defName string, t reflect.Type, inlineNamed bool) any {
	// A pointer marshals as its element or as null; schema-wise it is its
	// element, and optionality is carried by `required` instead.
	if t.Kind() == reflect.Pointer {
		return b.schemaFor(defName, t.Elem(), inlineNamed)
	}

	if !inlineNamed && t.Name() != "" && t.PkgPath() != "" && t.Kind() == reflect.Struct {
		return map[string]any{"$ref": "#/$defs/" + b.register(t)}
	}

	switch t.Kind() {
	case reflect.Bool:
		return map[string]any{"type": "boolean"}
	case reflect.String:
		return map[string]any{"type": "string"}
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return map[string]any{"type": "integer"}
	case reflect.Float32, reflect.Float64:
		return map[string]any{"type": "number"}
	case reflect.Interface:
		return true // any
	case reflect.Slice, reflect.Array:
		// A nil slice without omitempty marshals as JSON null, so an array
		// permits null; encoding/json never emits null for an omitempty slice.
		return map[string]any{
			"type":  []any{"array", "null"},
			"items": b.schemaFor(defName, t.Elem(), false),
		}
	case reflect.Map:
		return map[string]any{
			"type":                 "object",
			"additionalProperties": b.schemaFor(defName, t.Elem(), false),
		}
	case reflect.Struct:
		// An anonymous struct has no def name; expand it in place.
		return b.structSchema(defName, t)
	default:
		return true
	}
}

// structSchema reflects a struct into an object schema, honouring json tags,
// omitempty (optional) and "-" (skipped), and applying the enum and description
// overrides registered for defName.
func (b *schemaBuilder) structSchema(defName string, t reflect.Type) any {
	props := map[string]any{}
	var required []string

	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if f.PkgPath != "" { // unexported
			continue
		}
		name, opts, skip := jsonFieldName(f)
		if skip {
			continue
		}
		if f.Anonymous && f.Type.Kind() == reflect.Struct && name == f.Name {
			// An untagged embedded struct is flattened by encoding/json, so
			// flatten its properties here too.
			emb := b.structSchema(defName, f.Type).(map[string]any)
			for k, v := range emb["properties"].(map[string]any) {
				props[k] = v
			}
			if er, ok := emb["required"].([]string); ok {
				required = append(required, er...)
			}
			continue
		}

		schema := b.schemaFor(defName, f.Type, false)
		key := defName + "." + name
		if m, ok := schema.(map[string]any); ok {
			if vals, ok := b.enums[key]; ok {
				anyVals := make([]any, len(vals))
				for i, v := range vals {
					anyVals[i] = v
				}
				m["enum"] = anyVals
			}
			if d, ok := b.descs[key]; ok {
				m["description"] = d
			}
		}
		props[name] = schema

		if !opts["omitempty"] && !opts["omitzero"] {
			required = append(required, name)
		}
	}

	schema := map[string]any{
		"type":                 "object",
		"properties":           props,
		"additionalProperties": false,
	}
	if len(required) > 0 {
		sort.Strings(required)
		schema["required"] = required
	}
	return schema
}

// jsonFieldName replicates encoding/json's field-name resolution: the tag name
// (or the Go field name when untagged), the option set, and whether the field
// is skipped.
func jsonFieldName(f reflect.StructField) (name string, opts map[string]bool, skip bool) {
	tag := f.Tag.Get("json")
	opts = map[string]bool{}
	if tag == "-" {
		return "", opts, true
	}
	name = f.Name
	if tag != "" {
		parts := splitComma(tag)
		if parts[0] != "" {
			name = parts[0]
		}
		for _, o := range parts[1:] {
			opts[o] = true
		}
	}
	return name, opts, false
}

func splitComma(s string) []string {
	var out []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == ',' {
			out = append(out, s[start:i])
			start = i + 1
		}
	}
	return append(out, s[start:])
}
