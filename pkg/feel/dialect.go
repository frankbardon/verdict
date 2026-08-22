package feel

import pfeel "github.com/frankbardon/verdict/pkg/feel/internal/dialect"

// violatesSFEEL reports the first Conformance-Level-2 violation in an AST, or
// "" when the tree is valid S-FEEL.
//
// S-FEEL (DMN 1.5 §10.3.4) is the subset a Level 2 engine must support: number,
// string, boolean, date/time and duration literals; arithmetic, comparison and
// range operators; conjunction and disjunction; list literals; and qualified
// names. Iteration (`for`/`some`/`every`), conditionals (`if`) and inline
// function definitions belong to full FEEL only.
//
// The embedded parser hides the argument list of a FunCall behind an unexported
// type, so an offending construct nested inside a call argument — `f(for x in
// [1] return x)` — is not reachable from here. That is a deliberate limit: the
// check exists to keep Level 2 models honest about the constructs a Level 2
// consumer would choke on at the top level of a cell, not to be a security
// boundary. Full FEEL models are unaffected.
func violatesSFEEL(n pfeel.Node) string {
	switch v := n.(type) {
	case nil:
		return ""
	case *pfeel.ForExpr:
		return "`for` iteration is a FEEL construct; S-FEEL admits only simple expressions"
	case *pfeel.SomeExpr:
		return "`some` quantification is a FEEL construct; S-FEEL admits only simple expressions"
	case *pfeel.EveryExpr:
		return "`every` quantification is a FEEL construct; S-FEEL admits only simple expressions"
	case *pfeel.IfExpr:
		return "`if` is a FEEL construct; S-FEEL admits only simple expressions"
	case *pfeel.FunDef:
		return "inline function definitions are a FEEL construct; S-FEEL admits only simple expressions"
	case *pfeel.Binop:
		if r := violatesSFEEL(v.Left); r != "" {
			return r
		}
		return violatesSFEEL(v.Right)
	case *pfeel.DotOp:
		return violatesSFEEL(v.Left)
	case *pfeel.RangeNode:
		if r := violatesSFEEL(v.Start); r != "" {
			return r
		}
		return violatesSFEEL(v.End)
	case *pfeel.ArrayNode:
		return violatesSFEELAll(v.Elements)
	case *pfeel.ExprList:
		return violatesSFEELAll(v.Elements)
	case *pfeel.MultiTests:
		return violatesSFEELAll(v.Elements)
	case *pfeel.MapNode:
		// A context literal is not part of S-FEEL's value grammar, but every
		// exporter that emits one also declares Level 3, so rejecting it here
		// would only fire on hand-written models. Treated as a Level 3 construct.
		return "context literals are a FEEL construct; S-FEEL admits only simple expressions"
	case *pfeel.FunCall:
		return violatesSFEEL(v.FunRef)
	default:
		// Literals, temporals and variable references are all valid S-FEEL.
		return ""
	}
}

func violatesSFEELAll(nodes []pfeel.Node) string {
	for _, n := range nodes {
		if r := violatesSFEEL(n); r != "" {
			return r
		}
	}
	return ""
}
