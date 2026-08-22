package nexus

import (
	"sort"
	"strings"

	"github.com/frankbardon/verdict/pkg/dmn/model"
)

// jsonSchema renders an agentDecision's declared output type as the JSON Schema
// a provider's structured-output mode understands.
//
// This is the point of the whole design. Verdict already knows exactly what
// shape the decision graph will accept, so the model is constrained at the
// provider rather than corrected afterwards: an enumerated tier becomes an
// `enum`, a structured answer becomes an object with `required` fields. The
// engine still validates what comes back — a schema is a strong hint, not a
// guarantee — but constraining first makes the retry path rare instead of
// routine.
//
// The result is always wrapped in an object with a single `value` property,
// because providers require a top-level object even when the decision's answer
// is a bare string.
func jsonSchema(t model.TypeSpec) map[string]any {
	return map[string]any{
		"type":                 "object",
		"properties":           map[string]any{"value": typeSchema(t)},
		"required":             []string{"value"},
		"additionalProperties": false,
	}
}

func typeSchema(t model.TypeSpec) map[string]any {
	if t.Collection {
		elem := t
		elem.Collection = false
		return map[string]any{"type": "array", "items": typeSchema(elem)}
	}
	if len(t.Enumeration) > 0 {
		return map[string]any{"type": "string", "enum": t.Enumeration}
	}
	if len(t.Components) > 0 {
		props := make(map[string]any, len(t.Components))
		required := make([]string, 0, len(t.Components))
		for name, comp := range t.Components {
			props[name] = typeSchema(comp)
			required = append(required, name)
		}
		sort.Strings(required)
		return map[string]any{
			"type":                 "object",
			"properties":           props,
			"required":             required,
			"additionalProperties": false,
		}
	}
	switch baseType(t.TypeRef) {
	case "number":
		return map[string]any{"type": "number"}
	case "boolean":
		return map[string]any{"type": "boolean"}
	case "string":
		return map[string]any{"type": "string"}
	case "date":
		return map[string]any{"type": "string", "format": "date"}
	case "time":
		return map[string]any{"type": "string", "format": "time"}
	case "date and time":
		return map[string]any{"type": "string", "format": "date-time"}
	case "days and time duration", "years and months duration":
		return map[string]any{"type": "string", "format": "duration"}
	default:
		// A user-defined item definition the bridge cannot see the shape of.
		// Leaving the schema open is honest: the engine's own type check and
		// the model's FEEL validator remain the enforcement point.
		return map[string]any{}
	}
}

func baseType(typeRef string) string {
	t := strings.TrimSpace(typeRef)
	if i := strings.LastIndexByte(t, ':'); i >= 0 {
		t = t[i+1:]
	}
	switch strings.ToLower(t) {
	case "number", "integer", "long", "double", "decimal":
		return "number"
	case "boolean", "bool":
		return "boolean"
	case "string", "text":
		return "string"
	case "date":
		return "date"
	case "time":
		return "time"
	case "date and time", "datetime":
		return "date and time"
	case "days and time duration", "daytimeduration":
		return "days and time duration"
	case "years and months duration", "yearmonthduration":
		return "years and months duration"
	default:
		return ""
	}
}

// describeType renders a type spec as the one-line description that goes into
// the prompt, so a model that ignores the schema still knows what is wanted.
func describeType(t model.TypeSpec) string {
	switch {
	case t.Collection:
		elem := t
		elem.Collection = false
		return "a list of " + describeType(elem)
	case len(t.Enumeration) > 0:
		return "exactly one of: " + strings.Join(t.Enumeration, ", ")
	case len(t.Components) > 0:
		keys := make([]string, 0, len(t.Components))
		for k := range t.Components {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		parts := make([]string, 0, len(keys))
		for _, k := range keys {
			parts = append(parts, k+" ("+describeType(t.Components[k])+")")
		}
		return "an object with the fields " + strings.Join(parts, ", ")
	case t.TypeRef != "":
		return "a value of type " + t.TypeRef
	default:
		return "any value"
	}
}
