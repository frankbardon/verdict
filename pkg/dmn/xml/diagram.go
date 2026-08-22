package xml

import (
	"fmt"

	"github.com/frankbardon/verdict/pkg/dmn/model"
)

// dmndiNamespace maps a MODEL namespace onto its matching DMNDI namespace. The
// two are versioned together, and mixing them produces a document that
// validates against neither.
func dmndiNamespace(modelNS string) string {
	switch modelNS {
	case NS15:
		return NSDMNDI15
	case NS14:
		return NSDMNDI14
	default:
		return NSDMNDI13
	}
}

// Layout constants, in the units dmn-js uses. The shape sizes match what
// Camunda Modeler creates for a new element, so a Verdict-written diagram opens
// looking like a hand-drawn one rather than like something a machine produced.
const (
	shapeWidth  = 180.0
	shapeHeight = 80.0
	inputWidth  = 125.0
	inputHeight = 45.0
	gapX        = 60.0
	gapY        = 100.0
	originX     = 160.0
	originY     = 80.0
)

// diagram writes an auto-laid-out DMNDI block.
//
// Without diagram interchange a DMN document is schema-valid but opens as an
// empty canvas in every editor built on dmn-js, which is most of them. Deriving
// a layout from the DRG costs little and is the difference between a model a
// modeller can look at and a file they have to take on faith.
//
// The layout is a simple layered one: requirements point upward, so a
// dependency sits below the decision that needs it, and each dependency layer
// is a centred row. It will not win a prize, but it is readable and — because
// it is derived from the graph rather than remembered — it stays correct when
// the model changes.
func (e *encoder) diagram(defs *model.Definitions) {
	if e.err != nil {
		return
	}
	graph, _, err := model.BuildGraph(defs)
	if err != nil {
		// A cyclic model has no layered layout. It is still worth writing the
		// rest of the document, so the diagram is simply omitted.
		return
	}

	placed, edges := layout(defs, graph)
	if len(placed) == 0 {
		return
	}

	e.open("dmndi:DMNDI")
	e.open("dmndi:DMNDiagram",
		attr("id", "DMNDiagram_"+sanitiseID(defs.ID)),
		attr("name", defs.Name))

	for _, p := range placed {
		e.open("dmndi:DMNShape",
			attr("id", "DMNShape_"+sanitiseID(p.id)),
			attr("dmnElementRef", p.id))
		e.empty("dc:Bounds",
			attr("x", num(p.x)), attr("y", num(p.y)),
			attr("width", num(p.w)), attr("height", num(p.h)))
		e.close()
	}
	for _, ed := range edges {
		e.open("dmndi:DMNEdge",
			attr("id", "DMNEdge_"+sanitiseID(ed.from)+"_"+sanitiseID(ed.to)),
			attr("dmnElementRef", ed.requirementID))
		e.empty("di:waypoint", attr("x", num(ed.x1)), attr("y", num(ed.y1)))
		e.empty("di:waypoint", attr("x", num(ed.x2)), attr("y", num(ed.y2)))
		e.close()
	}

	e.close() // DMNDiagram
	e.close() // DMNDI
}

// shape is one placed DRG element.
type shape struct {
	id           string
	x, y, w, h   float64
	centreX, top float64
	bottom       float64
}

// edge is one placed information/knowledge requirement.
type edge struct {
	from, to      string
	requirementID string
	x1, y1        float64
	x2, y2        float64
}

// layout places every DRG element that has a visual representation.
func layout(defs *model.Definitions, graph *model.Graph) ([]shape, []edge) {
	// Decision services are drawn as containers in DMN and need a divider line
	// and nested shapes to look right; laying one out badly is worse than
	// leaving it to the editor, which will place it on first edit.
	drawable := map[string]bool{}
	for _, d := range defs.Decisions {
		drawable[d.ID] = true
	}
	for _, i := range defs.InputData {
		drawable[i.ID] = true
	}
	for _, b := range defs.BKMs {
		drawable[b.ID] = true
	}
	for _, k := range defs.KnowledgeSources {
		drawable[k.ID] = true
	}

	var ids []string
	for _, id := range graph.Order() {
		if drawable[id] {
			ids = append(ids, id)
		}
	}
	if len(ids) == 0 {
		return nil, nil
	}

	// Depth 0 is the leaves — input data and knowledge sources — and depth
	// increases toward the model's outputs. DMN draws requirements pointing
	// upward, so a higher depth is a smaller y.
	layers := graph.Layers(ids)
	maxDepth := len(layers) - 1

	// Shapes are collected by value and indexed by position only once the slice
	// has stopped growing. Taking &placed[i] while appending hands out pointers
	// into a backing array that the next append can replace, so the edges would
	// be laid out against stale coordinates.
	var placed []shape
	for depth, layer := range layers {
		// Widths differ between input data and decisions, so a row's total width
		// is measured before it is centred.
		total := 0.0
		for i, id := range layer {
			total += widthOf(graph, id)
			if i > 0 {
				total += gapX
			}
		}
		x := originX - total/2
		y := originY + float64(maxDepth-depth)*(shapeHeight+gapY)

		for _, id := range layer {
			w, h := widthOf(graph, id), heightOf(graph, id)
			// Centre shapes of differing heights on the row's midline.
			rowY := y + (shapeHeight-h)/2
			s := shape{
				id: id, x: x, y: rowY, w: w, h: h,
				centreX: x + w/2, top: rowY, bottom: rowY + h,
			}
			placed = append(placed, s)
			x += w + gapX
		}
	}

	// Centring each row around a fixed origin lets rows drift negative when one
	// is wider than another. Editors cope with negative coordinates, but a
	// diagram that opens off-canvas looks broken, so the whole drawing is
	// translated into the positive quadrant and snapped to whole units.
	normalise(placed)

	byID := make(map[string]*shape, len(placed))
	for i := range placed {
		byID[placed[i].id] = &placed[i]
	}

	var edges []edge
	for _, e := range requirementEdges(defs) {
		from, okF := byID[e.from]
		to, okT := byID[e.to]
		if !okF || !okT {
			continue
		}
		edges = append(edges, edge{
			from: e.from, to: e.to, requirementID: e.requirementID,
			// From the top of the dependency to the bottom of the dependent.
			x1: from.centreX, y1: from.top,
			x2: to.centreX, y2: to.bottom,
		})
	}
	return placed, edges
}

// normalise translates the layout into the positive quadrant and rounds every
// coordinate to a whole unit.
func normalise(shapes []shape) {
	if len(shapes) == 0 {
		return
	}
	minX, minY := shapes[0].x, shapes[0].y
	for _, s := range shapes {
		if s.x < minX {
			minX = s.x
		}
		if s.y < minY {
			minY = s.y
		}
	}
	dx, dy := originX-minX, originY-minY
	for i := range shapes {
		s := &shapes[i]
		s.x = roundUnit(s.x + dx)
		s.y = roundUnit(s.y + dy)
		s.centreX = roundUnit(s.x + s.w/2)
		s.top = s.y
		s.bottom = s.y + s.h
	}
}

func roundUnit(v float64) float64 {
	if v < 0 {
		return -float64(int64(-v + 0.5))
	}
	return float64(int64(v + 0.5))
}

// requirementRef is one edge of the DRG, with the ID of the requirement element
// that declares it.
type requirementRef struct {
	from, to      string
	requirementID string
}

// requirementID names a requirement element deterministically.
//
// DMN's requirement elements need IDs because DMNDI edges reference them, and
// the model type does not carry the ones a source document may have had. Both
// the requirement writer and the diagram generator call this, so the edge and
// the element it points at cannot drift apart.
func requirementID(ownerID, kind string, index int) string {
	return fmt.Sprintf("%s_%s_%d", sanitiseID(ownerID), kind, index+1)
}

// Requirement kinds, as they appear in a generated requirement ID.
const (
	reqInput     = "ir"
	reqDecision  = "dr"
	reqKnowledge = "kr"
	reqAuthority = "ar"
)

// requirementEdges lists the DRG's edges. The requirement element's own ID is
// carried because DMNDI references it, not the elements it joins.
func requirementEdges(defs *model.Definitions) []requirementRef {
	var out []requirementRef
	add := func(owner string, kind string, refs []string) {
		for i, ref := range refs {
			out = append(out, requirementRef{
				from: ref, to: owner, requirementID: requirementID(owner, kind, i),
			})
		}
	}
	for _, d := range defs.Decisions {
		add(d.ID, reqInput, d.RequiredInputs)
		add(d.ID, reqDecision, d.RequiredDecisions)
		add(d.ID, reqKnowledge, d.RequiredKnowledge)
		add(d.ID, reqAuthority, d.AuthorityRequirements)
	}
	for _, b := range defs.BKMs {
		add(b.ID, reqInput, b.RequiredInputs)
		add(b.ID, reqDecision, b.RequiredDecisions)
		add(b.ID, reqKnowledge, b.RequiredKnowledge)
	}
	return out
}

func widthOf(graph *model.Graph, id string) float64 {
	if isInputData(graph, id) {
		return inputWidth
	}
	return shapeWidth
}

func heightOf(graph *model.Graph, id string) float64 {
	if isInputData(graph, id) {
		return inputHeight
	}
	return shapeHeight
}

func isInputData(graph *model.Graph, id string) bool {
	e, ok := graph.Element(id)
	if !ok {
		return false
	}
	_, isInput := e.(*model.InputData)
	return isInput
}

// num renders a coordinate. DMNDI coordinates are xsd:double; whole numbers are
// written without a decimal point so the output matches what editors produce.
func num(v float64) string {
	if v == float64(int64(v)) {
		return fmt.Sprintf("%d", int64(v))
	}
	return fmt.Sprintf("%g", v)
}

// sanitiseID makes an element ID safe as part of another xsd:ID.
func sanitiseID(s string) string {
	if s == "" {
		return "x"
	}
	out := make([]rune, 0, len(s))
	for i, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r == '_':
			out = append(out, r)
		case i > 0 && (r >= '0' && r <= '9' || r == '-' || r == '.'):
			out = append(out, r)
		default:
			out = append(out, '_')
		}
	}
	if out[0] >= '0' && out[0] <= '9' {
		return "_" + string(out)
	}
	return string(out)
}
