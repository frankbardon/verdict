package eval

import (
	"context"
	"fmt"
	"strings"

	"github.com/frankbardon/verdict/pkg/diag"
	"github.com/frankbardon/verdict/pkg/dmn/model"
	"github.com/frankbardon/verdict/pkg/feel"
	"github.com/frankbardon/verdict/pkg/trace"
)

// compiledTable is a decision table with every cell parsed. Compiling once at
// load time keeps evaluation to expression evaluation and comparison.
type compiledTable struct {
	src *model.DecisionTable

	// inputs are the compiled input-clause expressions, parallel to src.Inputs.
	inputs []*feel.Expr
	// rules[i][j] is rule i's unary test for input clause j. A nil test is "-".
	rules [][]*feel.UnaryTest
	// outputs[i][j] is rule i's expression for output clause j.
	outputs [][]*feel.Expr
	// defaults are the compiled defaultOutputEntry expressions, parallel to
	// src.Outputs. A nil entry means the clause has no default.
	defaults []*feel.Expr
	// outputValues[j] holds the ordered allowed values of output clause j,
	// which PRIORITY and OUTPUT ORDER rank by. Nil when the clause declares none.
	outputValues [][]*feel.Expr
	// singleOutput is true when the table has exactly one unnamed output
	// clause, in which case the result is the value itself rather than a context.
	singleOutput bool
}

func (p *Program) prepareTable(ds *diag.Set, elemID, elemName string, t *model.DecisionTable) {
	ct := &compiledTable{src: t}
	// DMN 1.5 §8.3: a table with a single output clause produces that clause's
	// value directly; only a multi-output table produces a context keyed by
	// output name. The clause's `name` is a label in the single-output case,
	// not a wrapper key.
	ct.singleOutput = len(t.Outputs) == 1

	for i, in := range t.Inputs {
		x := p.compileText(ds, elemID, elemName, in.Expression)
		if x == nil && in.Expression != "" {
			// compileText already reported the parse failure.
			_ = i
		}
		ct.inputs = append(ct.inputs, x)
	}

	for _, out := range t.Outputs {
		ct.defaults = append(ct.defaults, p.compileText(ds, elemID, elemName, out.DefaultValue))
		ct.outputValues = append(ct.outputValues, p.compileValueList(ds, elemID, elemName, out.Values))
	}

	needsOrder := t.HitPolicy == model.HitPriority || t.HitPolicy == model.HitOutputOrder
	if needsOrder {
		hasAny := false
		for _, vs := range ct.outputValues {
			if len(vs) > 0 {
				hasAny = true
				break
			}
		}
		if !hasAny {
			ds.Add(diag.Errorf(diag.CodeMissingOutputSet,
				"hit policy %s ranks outputs by the outputValues list, but no output clause declares one",
				t.HitPolicy).At(elemID, elemName).In(p.Defs.ID))
		}
	}

	for _, r := range t.Rules {
		tests := make([]*feel.UnaryTest, len(t.Inputs))
		for j := range t.Inputs {
			if j >= len(r.InputEntries) {
				continue // arity mismatch already reported; treat as "-"
			}
			ut, err := p.cfg.FEEL.CompileUnaryTest(r.InputEntries[j])
			if err != nil {
				ds.Add(diag.Errorf(diag.CodeBadExpression,
					"rule %d, input %d: %v", r.Index+1, j+1, err).
					At(elemID, elemName).In(p.Defs.ID).With("rule", r.Index+1))
				continue
			}
			tests[j] = ut
		}
		ct.rules = append(ct.rules, tests)

		outs := make([]*feel.Expr, len(t.Outputs))
		for j := range t.Outputs {
			if j >= len(r.OutputEntries) {
				continue
			}
			outs[j] = p.compileText(ds, elemID, elemName, r.OutputEntries[j])
		}
		ct.outputs = append(ct.outputs, outs)
	}
	p.tables[t] = ct
}

// compileValueList parses an outputValues or inputValues list — a comma
// separated list of FEEL expressions — into its elements, preserving order.
func (p *Program) compileValueList(ds *diag.Set, elemID, elemName, list string) []*feel.Expr {
	if list == "" {
		return nil
	}
	parts := feel.SplitList(list)
	out := make([]*feel.Expr, 0, len(parts))
	for _, part := range parts {
		if x := p.compileText(ds, elemID, elemName, part); x != nil {
			out = append(out, x)
		}
	}
	return out
}

// evalTable evaluates a decision table under its hit policy.
func (p *Program) evalTable(ec *Context, t *model.DecisionTable) (any, error) {
	ct := p.tables[t]
	if ct == nil {
		return nil, diag.Errorf(diag.CodeExpressionError,
			"decision table was not prepared; this is a Verdict bug").At(t.ExprID(), "")
	}

	// 1. Evaluate every input clause exactly once. DMN is explicit that an input
	//    expression is evaluated once per table, not once per rule, which matters
	//    when the expression has a cost or invokes a BKM.
	inputValues := make([]any, len(ct.inputs))
	traceInputs := make(map[string]any, len(ct.inputs))
	for i, x := range ct.inputs {
		v, err := ec.eval(x)
		if err != nil {
			return nil, err
		}
		inputValues[i] = v
		traceInputs[inputLabel(t, i)] = feel.ToGo(v)
	}
	ec.trace.Annotate(trace.AnnInputValues, traceInputs)
	ec.trace.Annotate(trace.AnnHitPolicy, t.HitPolicy.Shorthand(t.Aggregation))
	ec.trace.Annotate(trace.AnnRuleCount, len(ct.rules))

	// 2. Find the matching rules in rule order.
	var matches []int
	for i, tests := range ct.rules {
		ok, err := p.ruleMatches(ec, tests, inputValues)
		if err != nil {
			return nil, fmt.Errorf("rule %d: %w", i+1, err)
		}
		if !ok {
			continue
		}
		matches = append(matches, i)
		// FIRST stops at the first hit; every other policy needs the full set,
		// either to detect ambiguity or to collect.
		if t.HitPolicy == model.HitFirst {
			break
		}
	}
	ec.trace.Annotate(trace.AnnMatchedRules, ruleIDs(t, matches))

	// 3. No match: fall back to the declared defaults, or to null.
	if len(matches) == 0 {
		return p.tableDefault(ec, ct)
	}

	// 4. Apply the hit policy.
	switch t.HitPolicy {
	case model.HitUnique, "":
		if len(matches) > 1 {
			return nil, diag.Errorf(diag.CodeMultipleHits,
				"hit policy UNIQUE requires exactly one matching rule, but %d matched (%v)",
				len(matches), ruleIDs(t, matches)).
				At(t.ExprID(), "").With("matched_rules", ruleIDs(t, matches))
		}
		return p.ruleOutput(ec, ct, matches[0])
	case model.HitFirst:
		return p.ruleOutput(ec, ct, matches[0])
	case model.HitAny:
		return p.anyHit(ec, ct, matches)
	case model.HitPriority:
		return p.priorityHit(ec, ct, matches)
	case model.HitRuleOrder:
		return p.collectAll(ec, ct, matches)
	case model.HitOutputOrder:
		return p.outputOrderHit(ec, ct, matches)
	case model.HitCollect:
		return p.collectHit(ec, ct, matches)
	default:
		return nil, diag.Errorf(diag.CodeUnknownHitPolicy,
			"hit policy %q is not implemented", t.HitPolicy).At(t.ExprID(), "")
	}
}

func (p *Program) ruleMatches(ec *Context, tests []*feel.UnaryTest, inputs []any) (bool, error) {
	for j, test := range tests {
		if j >= len(inputs) {
			continue
		}
		ok, err := ec.prog.cfg.FEEL.EvalUnaryTest(test, inputs[j], ec.vars)
		if err != nil {
			return false, err
		}
		if !ok {
			return false, nil
		}
	}
	return true, nil
}

// ruleOutput evaluates one rule's output entries and shapes them per the
// table's output arity.
func (p *Program) ruleOutput(ec *Context, ct *compiledTable, rule int) (any, error) {
	if ct.singleOutput {
		return ec.eval(ct.outputs[rule][0])
	}
	out := make(map[string]any, len(ct.src.Outputs))
	for j, clause := range ct.src.Outputs {
		v, err := ec.eval(ct.outputs[rule][j])
		if err != nil {
			return nil, err
		}
		out[outputName(clause, j)] = v
	}
	return out, nil
}

// tableDefault produces the value of a table no rule matched.
func (p *Program) tableDefault(ec *Context, ct *compiledTable) (any, error) {
	hasDefault := false
	for _, d := range ct.defaults {
		if d != nil {
			hasDefault = true
			break
		}
	}
	// A collecting policy with no match returns an empty list, not null: the
	// collection of nothing is the empty collection.
	if !hasDefault && !ct.src.HitPolicy.SingleHit() {
		if ct.src.HitPolicy == model.HitCollect && ct.src.Aggregation == model.AggCount {
			return feel.FromGo(0), nil
		}
		if ct.src.HitPolicy == model.HitCollect && ct.src.Aggregation != model.AggNone {
			return feel.Null, nil
		}
		return []any{}, nil
	}
	if !hasDefault {
		ec.diagnose(diag.Warnf(diag.CodeNoRuleMatched,
			"no rule matched and the table declares no default output; the decision is null"))
		return feel.Null, nil
	}
	ec.trace.Annotate(trace.AnnDefaulted, true)
	if ct.singleOutput {
		return ec.eval(ct.defaults[0])
	}
	out := make(map[string]any, len(ct.src.Outputs))
	for j, clause := range ct.src.Outputs {
		v, err := ec.eval(ct.defaults[j])
		if err != nil {
			return nil, err
		}
		out[outputName(clause, j)] = v
	}
	return out, nil
}

// anyHit implements ANY: several rules may match, but they must agree.
func (p *Program) anyHit(ec *Context, ct *compiledTable, matches []int) (any, error) {
	first, err := p.ruleOutput(ec, ct, matches[0])
	if err != nil {
		return nil, err
	}
	for _, r := range matches[1:] {
		v, err := p.ruleOutput(ec, ct, r)
		if err != nil {
			return nil, err
		}
		if !feel.Equal(first, v) {
			return nil, diag.Errorf(diag.CodeInconsistentAny,
				"hit policy ANY requires every matching rule to agree, but rules %v produced different outputs",
				ruleIDs(ct.src, matches)).At(ct.src.ExprID(), "").With("matched_rules", ruleIDs(ct.src, matches))
		}
	}
	return first, nil
}

// priorityHit implements PRIORITY: the single output whose value ranks highest
// in the first output clause's outputValues order.
func (p *Program) priorityHit(ec *Context, ct *compiledTable, matches []int) (any, error) {
	ranked, err := p.rankMatches(ec, ct, matches)
	if err != nil {
		return nil, err
	}
	return p.ruleOutput(ec, ct, ranked[0])
}

// outputOrderHit implements OUTPUT ORDER: every match, sorted by the
// outputValues order.
func (p *Program) outputOrderHit(ec *Context, ct *compiledTable, matches []int) (any, error) {
	ranked, err := p.rankMatches(ec, ct, matches)
	if err != nil {
		return nil, err
	}
	return p.collectAll(ec, ct, ranked)
}

// rankMatches orders matching rules by the outputValues precedence of their
// output values, most-preferred first. Rules whose value is absent from the
// list rank last, in rule order, so an incompletely enumerated table degrades
// predictably rather than erroring.
func (p *Program) rankMatches(ec *Context, ct *compiledTable, matches []int) ([]int, error) {
	// The ranking clause is the first output clause that declares values.
	clause := -1
	for j, vs := range ct.outputValues {
		if len(vs) > 0 {
			clause = j
			break
		}
	}
	if clause < 0 {
		// Reported at load time; rank by rule order so evaluation still returns.
		return matches, nil
	}
	order := make([]any, 0, len(ct.outputValues[clause]))
	for _, x := range ct.outputValues[clause] {
		v, err := ec.eval(x)
		if err != nil {
			return nil, err
		}
		order = append(order, v)
	}
	rankOf := func(v any) int {
		for i, o := range order {
			if feel.Equal(o, v) {
				return i
			}
		}
		return len(order)
	}

	type scored struct {
		rule int
		rank int
	}
	scoredMatches := make([]scored, 0, len(matches))
	for _, r := range matches {
		v, err := ec.eval(ct.outputs[r][clause])
		if err != nil {
			return nil, err
		}
		scoredMatches = append(scoredMatches, scored{rule: r, rank: rankOf(v)})
	}
	// Stable insertion sort: the match count in a decision table is small, and
	// stability preserves rule order within a rank.
	for i := 1; i < len(scoredMatches); i++ {
		for j := i; j > 0 && scoredMatches[j].rank < scoredMatches[j-1].rank; j-- {
			scoredMatches[j], scoredMatches[j-1] = scoredMatches[j-1], scoredMatches[j]
		}
	}
	out := make([]int, len(scoredMatches))
	for i, s := range scoredMatches {
		out[i] = s.rule
	}
	return out, nil
}

// collectAll returns the outputs of the given rules as a list, in the order
// given. It implements RULE ORDER and backs OUTPUT ORDER.
func (p *Program) collectAll(ec *Context, ct *compiledTable, rules []int) (any, error) {
	out := make([]any, 0, len(rules))
	for _, r := range rules {
		v, err := p.ruleOutput(ec, ct, r)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, nil
}

// collectHit implements COLLECT and its aggregators.
func (p *Program) collectHit(ec *Context, ct *compiledTable, matches []int) (any, error) {
	list, err := p.collectAll(ec, ct, matches)
	if err != nil {
		return nil, err
	}
	values := list.([]any)
	if ct.src.Aggregation != model.AggNone {
		ec.trace.Annotate(trace.AnnAggregation, string(ct.src.Aggregation))
	}
	switch ct.src.Aggregation {
	case model.AggNone:
		return values, nil
	case model.AggCount:
		return feel.FromGo(len(values)), nil
	case model.AggSum, model.AggMin, model.AggMax:
		return aggregate(ct.src, values)
	default:
		return values, nil
	}
}

// aggregate folds a collected list. DMN restricts SUM, MIN and MAX to
// single-output tables of comparable values; a non-conforming table is an
// error rather than a silent null, because a silently wrong total is worse than
// a loud failure.
func aggregate(t *model.DecisionTable, values []any) (any, error) {
	if len(values) == 0 {
		return feel.Null, nil
	}
	switch t.Aggregation {
	case model.AggSum:
		sum := feel.FromGo(0)
		for _, v := range values {
			next, err := feel.Add(sum, v)
			if err != nil {
				return nil, diag.Errorf(diag.CodeTypeViolation,
					"COLLECT SUM requires numeric outputs: %v", err).At(t.ExprID(), "")
			}
			sum = next
		}
		return sum, nil
	case model.AggMin, model.AggMax:
		best := values[0]
		for _, v := range values[1:] {
			c, ok := feel.Compare(v, best)
			if !ok {
				return nil, diag.Errorf(diag.CodeTypeViolation,
					"COLLECT %s requires comparable outputs, but the table produced %s and %s",
					t.Aggregation, feel.TypeName(best), feel.TypeName(v)).At(t.ExprID(), "")
			}
			if (t.Aggregation == model.AggMin && c < 0) || (t.Aggregation == model.AggMax && c > 0) {
				best = v
			}
		}
		return best, nil
	}
	return values, nil
}

// inputLabel names an input clause for the trace: its label, else its
// expression, else its position.
func inputLabel(t *model.DecisionTable, i int) string {
	in := t.Inputs[i]
	if in.Label != "" {
		return in.Label
	}
	if in.Expression != "" {
		return in.Expression
	}
	return fmt.Sprintf("input_%d", i+1)
}

// outputName names an output clause when the table has more than one.
func outputName(o *model.TableOutput, i int) string {
	if o.Name != "" {
		return o.Name
	}
	if o.Label != "" {
		return o.Label
	}
	return fmt.Sprintf("output_%d", i+1)
}

func ruleIDs(t *model.DecisionTable, matches []int) []string {
	out := make([]string, 0, len(matches))
	for _, m := range matches {
		if m < len(t.Rules) {
			out = append(out, t.Rules[m].ID)
		}
	}
	return out
}

// Matcher exposes a prepared decision table's rule-matching machinery to the
// static analyser.
//
// Analysis runs the *same* unary tests through the *same* evaluator that
// evaluation uses, rather than reimplementing DMN's matching rules over the
// table's source text. That is what keeps the gap and overlap report honest:
// it cannot drift from what the engine will actually do at runtime.
type Matcher struct {
	prog *Program
	ct   *compiledTable
	ec   *Context
}

// Matcher builds a matcher for a prepared decision table.
func (p *Program) Matcher(t *model.DecisionTable) (*Matcher, bool) {
	ct := p.tables[t]
	if ct == nil {
		return nil, false
	}
	return &Matcher{prog: p, ct: ct, ec: p.detachedContext()}, true
}

// RuleCount reports how many rules the table has.
func (m *Matcher) RuleCount() int { return len(m.ct.rules) }

// InputCount reports how many input clauses the table has.
func (m *Matcher) InputCount() int { return len(m.ct.inputs) }

// Test returns the compiled unary test at (rule, clause), or nil for "-".
func (m *Matcher) Test(rule, clause int) *feel.UnaryTest {
	if rule < 0 || rule >= len(m.ct.rules) || clause < 0 || clause >= len(m.ct.rules[rule]) {
		return nil
	}
	return m.ct.rules[rule][clause]
}

// OutputExprs returns a rule's compiled output expressions.
func (m *Matcher) OutputExprs(rule int) []*feel.Expr {
	if rule < 0 || rule >= len(m.ct.outputs) {
		return nil
	}
	return m.ct.outputs[rule]
}

// InputValues returns the compiled allowed-value list of an input clause, which
// the analyser uses as that clause's domain when the modeller declared one.
func (m *Matcher) InputValues(clause int) string {
	if clause < 0 || clause >= len(m.ct.src.Inputs) {
		return ""
	}
	return m.ct.src.Inputs[clause].Values
}

// Admits reports whether a value is inside the domain an input clause declares
// through its allowed-value list, and whether there was a decidable list to ask.
//
// The analyser probes each boundary and the points either side of it, which for
// a range-valued list means probing just outside the declared domain. A value
// the modeller has said cannot occur is not a gap, so those probes are dropped
// rather than reported — otherwise a table that covers its whole declared
// domain could never be gap-free, and --strict would be unusable.
func (m *Matcher) Admits(clause int, value any) (admits, decidable bool) {
	list := m.InputValues(clause)
	if strings.TrimSpace(list) == "" {
		return true, false
	}
	test, err := m.prog.cfg.FEEL.CompileUnaryTest(list)
	if err != nil || test == nil {
		return true, false
	}
	ok, err := m.prog.cfg.FEEL.EvalUnaryTest(test, value, nil)
	if err != nil {
		return true, false
	}
	return ok, true
}

// Compile parses an expression with the program's dialect, so the analyser can
// interpret a declared inputValues list with the same grammar the table uses.
func (m *Matcher) Compile(text string) (*feel.Expr, error) {
	return m.prog.cfg.FEEL.Compile(text)
}

// Match reports which rules match a point in the table's input space, in rule
// order. Values are FEEL values parallel to the input clauses.
func (m *Matcher) Match(values []any) ([]int, error) {
	var out []int
	for i, tests := range m.ct.rules {
		ok, err := m.prog.ruleMatches(m.ec, tests, values)
		if err != nil {
			return nil, fmt.Errorf("rule %d: %w", i+1, err)
		}
		if ok {
			out = append(out, i)
		}
	}
	return out, nil
}

// EvalStatic evaluates an expression with no model variables in scope. It
// returns ok=false when the expression depends on runtime data, which is the
// analyser's signal that it cannot decide the question statically.
func (m *Matcher) EvalStatic(x *feel.Expr) (any, bool) {
	if x == nil {
		return feel.Null, true
	}
	v, err := m.prog.cfg.FEEL.Eval(x, nil)
	if err != nil {
		return nil, false
	}
	return v, true
}

// detachedContext builds a Context with no bindings, for static evaluation.
func (p *Program) detachedContext() *Context {
	return &Context{
		ctx:    context.Background(),
		prog:   p,
		vars:   map[string]any{},
		trace:  noTrace{},
		shared: &evalState{},
	}
}

// noTrace discards trace writes during static analysis.
type noTrace struct{}

func (noTrace) Annotate(string, any) {}
func (noTrace) Enabled() bool        { return false }
func (noTrace) Child(string, string) (trace.Writer, func(any, error)) {
	return noTrace{}, func(any, error) {}
}
