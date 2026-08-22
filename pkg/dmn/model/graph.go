package model

import (
	"fmt"
	"sort"
)

// Graph is an index over a Definitions document: every DRG element by ID and by
// name, plus the dependency edges between them. Building a Graph is the point
// at which dangling requirements and cycles are detected.
type Graph struct {
	defs *Definitions

	byID   map[string]DRGElement
	byName map[string]DRGElement

	// requires maps element ID to the IDs it depends on, deduplicated and with
	// dangling references dropped (they are reported as diagnostics instead).
	requires map[string][]string
	// dependents is the reverse edge set.
	dependents map[string][]string

	types *TypeRegistry

	// order is a deterministic topological order over all elements.
	order []string
}

// BuildGraph indexes a document and computes its topological order. It returns
// the graph together with any dangling-requirement or duplicate-ID problems; a
// cycle is returned as an error because no evaluation order exists.
func BuildGraph(defs *Definitions) (*Graph, []GraphProblem, error) {
	g := &Graph{
		defs:       defs,
		byID:       map[string]DRGElement{},
		byName:     map[string]DRGElement{},
		requires:   map[string][]string{},
		dependents: map[string][]string{},
		types:      NewTypeRegistry(defs.ItemDefinitions),
	}

	var problems []GraphProblem
	add := func(e DRGElement) {
		id := e.ElementID()
		if id == "" {
			problems = append(problems, GraphProblem{Kind: ProblemMissingID, ElementName: e.ElementName()})
			return
		}
		if prev, dup := g.byID[id]; dup {
			problems = append(problems, GraphProblem{Kind: ProblemDuplicateID, ElementID: id, ElementName: prev.ElementName()})
			return
		}
		g.byID[id] = e
		if n := e.ElementName(); n != "" {
			if _, taken := g.byName[n]; !taken {
				g.byName[n] = e
			}
		}
	}
	for _, d := range defs.Decisions {
		add(d)
	}
	for _, i := range defs.InputData {
		add(i)
	}
	for _, b := range defs.BKMs {
		add(b)
	}
	for _, k := range defs.KnowledgeSources {
		add(k)
	}
	for _, s := range defs.DecisionServices {
		add(s)
	}

	// Resolve edges. A requirement may reference an element by ID (the normal
	// case) or by name (some exporters emit hrefs that resolve to names).
	for id, e := range g.byID {
		seen := map[string]bool{}
		for _, ref := range e.Requires() {
			target, ok := g.resolveRef(ref)
			if !ok {
				problems = append(problems, GraphProblem{
					Kind: ProblemDangling, ElementID: id, ElementName: e.ElementName(), Ref: ref,
				})
				continue
			}
			tid := target.ElementID()
			if tid == id || seen[tid] {
				continue
			}
			seen[tid] = true
			g.requires[id] = append(g.requires[id], tid)
			g.dependents[tid] = append(g.dependents[tid], id)
		}
		sort.Strings(g.requires[id])
	}
	for k := range g.dependents {
		sort.Strings(g.dependents[k])
	}

	order, err := g.topoSort()
	if err != nil {
		return nil, problems, err
	}
	g.order = order
	return g, problems, nil
}

// ProblemKind classifies a graph construction problem.
type ProblemKind string

const (
	ProblemDangling    ProblemKind = "dangling"
	ProblemDuplicateID ProblemKind = "duplicate_id"
	ProblemMissingID   ProblemKind = "missing_id"
)

// GraphProblem is a non-fatal structural problem found while indexing.
type GraphProblem struct {
	Kind        ProblemKind
	ElementID   string
	ElementName string
	Ref         string
}

// resolveRef accepts an ID, a `#id` href, or an element name.
func (g *Graph) resolveRef(ref string) (DRGElement, bool) {
	if ref == "" {
		return nil, false
	}
	if ref[0] == '#' {
		ref = ref[1:]
	}
	if e, ok := g.byID[ref]; ok {
		return e, true
	}
	if e, ok := g.byName[ref]; ok {
		return e, true
	}
	return nil, false
}

// Resolve looks an element up by ID, href or name.
func (g *Graph) Resolve(ref string) (DRGElement, bool) { return g.resolveRef(ref) }

// Definitions returns the indexed document.
func (g *Graph) Definitions() *Definitions { return g.defs }

// Types returns the document's type registry.
func (g *Graph) Types() *TypeRegistry { return g.types }

// Element returns the element with the given ID.
func (g *Graph) Element(id string) (DRGElement, bool) {
	e, ok := g.byID[id]
	return e, ok
}

// Decision resolves a decision by ID or name.
func (g *Graph) Decision(ref string) (*Decision, bool) {
	e, ok := g.resolveRef(ref)
	if !ok {
		return nil, false
	}
	d, ok := e.(*Decision)
	return d, ok
}

// BKM resolves a business knowledge model by ID or name.
func (g *Graph) BKM(ref string) (*BusinessKnowledgeModel, bool) {
	e, ok := g.resolveRef(ref)
	if !ok {
		return nil, false
	}
	b, ok := e.(*BusinessKnowledgeModel)
	return b, ok
}

// Service resolves a decision service by ID or name.
func (g *Graph) Service(ref string) (*DecisionService, bool) {
	e, ok := g.resolveRef(ref)
	if !ok {
		return nil, false
	}
	s, ok := e.(*DecisionService)
	return s, ok
}

// RequiredBy lists the IDs of elements that depend on id.
func (g *Graph) RequiredBy(id string) []string { return g.dependents[id] }

// Requires lists the IDs id depends on.
func (g *Graph) Requires(id string) []string { return g.requires[id] }

// Order returns a deterministic topological order of every element ID:
// dependencies always precede their dependents.
func (g *Graph) Order() []string { return g.order }

// Slice returns the transitive dependency closure of the given element IDs, in
// topological order and including the roots themselves. This is the evaluation
// plan for a decision or a decision service.
func (g *Graph) Slice(roots ...string) ([]string, error) {
	want := map[string]bool{}
	var visit func(string) error
	var stack []string
	onStack := map[string]bool{}
	visit = func(id string) error {
		if want[id] {
			return nil
		}
		if onStack[id] {
			return fmt.Errorf("cycle in decision requirements: %v", append(append([]string{}, stack...), id))
		}
		onStack[id] = true
		stack = append(stack, id)
		for _, dep := range g.requires[id] {
			if err := visit(dep); err != nil {
				return err
			}
		}
		stack = stack[:len(stack)-1]
		onStack[id] = false
		want[id] = true
		return nil
	}
	for _, r := range roots {
		e, ok := g.resolveRef(r)
		if !ok {
			return nil, fmt.Errorf("unknown DRG element %q", r)
		}
		if err := visit(e.ElementID()); err != nil {
			return nil, err
		}
	}
	out := make([]string, 0, len(want))
	for _, id := range g.order {
		if want[id] {
			out = append(out, id)
		}
	}
	return out, nil
}

// Layers groups the topological order into levels where every element in a
// level depends only on earlier levels. Independent siblings share a level, so
// the evaluator can run a whole level concurrently.
func (g *Graph) Layers(ids []string) [][]string {
	inSlice := make(map[string]bool, len(ids))
	for _, id := range ids {
		inSlice[id] = true
	}
	depth := make(map[string]int, len(ids))
	var layers [][]string
	for _, id := range ids { // ids is already topologically ordered
		d := 0
		for _, dep := range g.requires[id] {
			if !inSlice[dep] {
				continue
			}
			if depth[dep]+1 > d {
				d = depth[dep] + 1
			}
		}
		depth[id] = d
		for len(layers) <= d {
			layers = append(layers, nil)
		}
		layers[d] = append(layers[d], id)
	}
	return layers
}

// topoSort produces a deterministic topological order: Kahn's algorithm with
// ready nodes drained in sorted-ID order so repeated loads agree.
func (g *Graph) topoSort() ([]string, error) {
	indeg := make(map[string]int, len(g.byID))
	ids := make([]string, 0, len(g.byID))
	for id := range g.byID {
		ids = append(ids, id)
		indeg[id] = len(g.requires[id])
	}
	sort.Strings(ids)

	ready := make([]string, 0, len(ids))
	for _, id := range ids {
		if indeg[id] == 0 {
			ready = append(ready, id)
		}
	}
	out := make([]string, 0, len(ids))
	for len(ready) > 0 {
		sort.Strings(ready)
		id := ready[0]
		ready = ready[1:]
		out = append(out, id)
		for _, dep := range g.dependents[id] {
			indeg[dep]--
			if indeg[dep] == 0 {
				ready = append(ready, dep)
			}
		}
	}
	if len(out) != len(ids) {
		var stuck []string
		for _, id := range ids {
			if indeg[id] > 0 {
				stuck = append(stuck, id)
			}
		}
		return nil, fmt.Errorf("decision requirement graph is cyclic; unresolvable elements: %v", stuck)
	}
	return out, nil
}
