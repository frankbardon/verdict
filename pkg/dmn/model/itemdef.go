package model

// Builtin FEEL type names, as spelled in DMN typeRef attributes. DMN 1.3+
// drops the `feel:` prefix but tools still emit it, so the resolver accepts
// both spellings.
const (
	TypeAny        = "Any"
	TypeNumber     = "number"
	TypeString     = "string"
	TypeBoolean    = "boolean"
	TypeDate       = "date"
	TypeTime       = "time"
	TypeDateTime   = "date and time"
	TypeDayTimeDur = "days and time duration"
	TypeYearMonth  = "years and months duration"
)

// ItemDefinition is a user-defined type: either a constrained simple type, a
// collection, or a structure of named components.
type ItemDefinition struct {
	ID          string
	Name        string
	Description string

	// TypeRef names the base type for a simple item definition. Empty when
	// Components is non-empty (a structure).
	TypeRef string

	// IsCollection makes the definition a list of its base type or structure.
	IsCollection bool

	// AllowedValues is a FEEL unary-test list constraining the value domain,
	// e.g. `"low","medium","high"` or `[1..10]`.
	AllowedValues string

	// TypeConstraint is DMN 1.4+'s constraint on a collection as a whole,
	// evaluated with the whole list bound to `?`.
	TypeConstraint string

	// Components are the named fields of a structure type.
	Components []*ItemDefinition

	// FunctionItem, when set, types the definition as a function signature.
	FunctionItem *FunctionItem
}

// FunctionItem types a function-valued item definition.
type FunctionItem struct {
	Parameters []*InformationItem
	OutputType string
}

// IsStructure reports whether the definition describes a record type.
func (i *ItemDefinition) IsStructure() bool { return len(i.Components) > 0 }

// TypeRegistry resolves typeRef strings against a document's item definitions.
type TypeRegistry struct {
	byName map[string]*ItemDefinition
}

// NewTypeRegistry indexes a document's item definitions by name. Nested
// component definitions are indexed under `Parent.Child` as well as their own
// bare name when unambiguous, matching how DMN tools reference them.
func NewTypeRegistry(defs []*ItemDefinition) *TypeRegistry {
	r := &TypeRegistry{byName: make(map[string]*ItemDefinition, len(defs))}
	for _, d := range defs {
		r.index("", d)
	}
	return r
}

func (r *TypeRegistry) index(prefix string, d *ItemDefinition) {
	if d == nil || d.Name == "" {
		return
	}
	qualified := d.Name
	if prefix != "" {
		qualified = prefix + "." + d.Name
	}
	r.byName[qualified] = d
	// Only claim the bare name if nothing else has; top-level definitions are
	// registered first and therefore win.
	if _, taken := r.byName[d.Name]; !taken {
		r.byName[d.Name] = d
	}
	for _, c := range d.Components {
		r.index(qualified, c)
	}
}

// Lookup resolves a typeRef, tolerating a namespace prefix such as `feel:` or
// `tns:`. It returns nil for builtin types and for unknown names.
func (r *TypeRegistry) Lookup(typeRef string) *ItemDefinition {
	if r == nil || typeRef == "" {
		return nil
	}
	if d, ok := r.byName[typeRef]; ok {
		return d
	}
	if _, local, ok := splitPrefix(typeRef); ok {
		return r.byName[local]
	}
	return nil
}

// Names lists every indexed qualified type name.
func (r *TypeRegistry) Names() []string {
	if r == nil {
		return nil
	}
	out := make([]string, 0, len(r.byName))
	for k := range r.byName {
		out = append(out, k)
	}
	return out
}

// BaseType strips any namespace prefix and reports the builtin type name a
// typeRef ultimately resolves to, following item-definition chains.
func (r *TypeRegistry) BaseType(typeRef string) string {
	seen := map[string]bool{}
	cur := stripPrefix(typeRef)
	for cur != "" && !seen[cur] {
		seen[cur] = true
		d := r.Lookup(cur)
		if d == nil {
			return cur
		}
		if d.IsStructure() {
			return cur
		}
		if d.TypeRef == "" {
			return TypeAny
		}
		cur = stripPrefix(d.TypeRef)
	}
	return cur
}

func splitPrefix(s string) (prefix, local string, ok bool) {
	for i := 0; i < len(s); i++ {
		if s[i] == ':' {
			return s[:i], s[i+1:], true
		}
	}
	return "", s, false
}

func stripPrefix(s string) string {
	_, local, _ := splitPrefix(s)
	return local
}
