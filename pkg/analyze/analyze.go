// Package analyze is Verdict's static analysis over a prepared model: it finds
// decision-table gaps (input combinations no rule covers) and overlaps (input
// combinations several rules cover), plus the structural problems that make a
// table suspect regardless of its data.
//
// Gap and overlap detection is what turns a decision table from a program into
// a specification you can reason about, and it is the feature users of mature
// DMN engines reach for first. Verdict runs it at load time so a table with a
// hole is caught before it reaches production, not by the one request that
// falls through it.
package analyze

import (
	"strings"

	"github.com/frankbardon/verdict/pkg/diag"
	"github.com/frankbardon/verdict/pkg/dmn/model"
	"github.com/frankbardon/verdict/pkg/eval"
)

// Options configure an analysis run.
type Options struct {
	// Strict raises gap findings from warnings to errors, so the engine's
	// strict mode refuses to load a table with a hole.
	Strict bool
	// MaxCombinations bounds the cross-product the gap search explores. A table
	// whose input domains multiply out beyond this is reported as unanalysed
	// rather than hanging the loader.
	MaxCombinations int
}

const defaultMaxCombinations = 200_000

// Report is the outcome of analysing a model.
type Report struct {
	// ModelID is the analysed model.
	ModelID string
	// Tables holds one entry per decision table, in document order.
	Tables []TableReport
	// Diagnostics are the findings, ready to merge into the model's diagnostic
	// surface.
	Diagnostics []diag.Diagnostic
}

// TableReport is the analysis of one decision table.
type TableReport struct {
	DecisionID   string
	DecisionName string
	HitPolicy    string
	RuleCount    int

	// Gaps are input combinations no rule matches. Only populated when every
	// input clause has a finite, enumerable domain.
	Gaps []Combination
	// Overlaps are input combinations more than one rule matches.
	Overlaps []Overlap
	// UnreachableRules are rules that can never fire because an earlier rule
	// subsumes them under a first-match policy.
	UnreachableRules []string

	// Analysable reports whether the input domains were enumerable. When false,
	// Gaps and Overlaps are empty and Reason explains why.
	Analysable bool
	Reason     string
}

// Combination is one point in a table's input space.
type Combination struct {
	// Values are the sampled values, parallel to the table's input clauses,
	// rendered as their FEEL source text.
	Values []string
}

func (c Combination) String() string { return strings.Join(c.Values, ", ") }

// Overlap is an input combination matched by more than one rule.
type Overlap struct {
	Combination Combination
	// Rules are the IDs of the rules that match, in rule order.
	Rules []string
	// Agree reports whether the overlapping rules produce the same output. An
	// overlap where every rule agrees is harmless under ANY and merely
	// redundant elsewhere.
	Agree bool
}

// Analyze runs the static analysis over a prepared program.
func Analyze(p *eval.Program, opts Options) *Report {
	if opts.MaxCombinations <= 0 {
		opts.MaxCombinations = defaultMaxCombinations
	}
	r := &Report{ModelID: p.Defs.ID}
	for _, d := range p.Defs.Decisions {
		t, ok := d.Logic.(*model.DecisionTable)
		if !ok {
			continue
		}
		tr := analyzeTable(p, d, t, opts)
		r.Tables = append(r.Tables, tr)
		r.Diagnostics = append(r.Diagnostics, tr.diagnostics(p.Defs.ID, t, opts)...)
	}
	return r
}

// Sorted returns the report's diagnostics in a stable order.
func (r *Report) Sorted() []diag.Diagnostic {
	var ds diag.Set
	ds.Add(r.Diagnostics...)
	return ds.Sorted()
}

// Table returns the report for a decision, if it has a decision table.
func (r *Report) Table(decisionID string) (TableReport, bool) {
	for _, t := range r.Tables {
		if t.DecisionID == decisionID {
			return t, true
		}
	}
	return TableReport{}, false
}

func (tr TableReport) diagnostics(modelID string, t *model.DecisionTable, opts Options) []diag.Diagnostic {
	var out []diag.Diagnostic
	at := func(d diag.Diagnostic) diag.Diagnostic {
		return d.At(tr.DecisionID, tr.DecisionName).In(modelID)
	}

	if !tr.Analysable && tr.Reason != "" {
		out = append(out, at(diag.Infof(diag.CodeTableGap,
			"decision table was not checked for gaps or overlaps: %s", tr.Reason)))
	}

	// A gap only matters when nothing else covers the hole. A table with a
	// default output entry has declared what happens outside its rules, so the
	// hole is intentional.
	if len(tr.Gaps) > 0 && !hasDefault(t) {
		sev := diag.Warnf
		if opts.Strict {
			sev = diag.Errorf
		}
		out = append(out, at(sev(diag.CodeTableGap,
			"decision table has %d uncovered input combination(s), for example (%s), and declares no default output",
			len(tr.Gaps), tr.Gaps[0]).With("gaps", combinationStrings(tr.Gaps))))
	}

	for _, o := range tr.Overlaps {
		switch t.HitPolicy {
		case model.HitUnique, "":
			out = append(out, at(diag.Errorf(diag.CodeTableOverlap,
				"hit policy UNIQUE forbids overlap, but rules %s all match (%s)",
				strings.Join(o.Rules, ", "), o.Combination).
				With("rules", o.Rules).With("combination", o.Combination.Values)))
		case model.HitAny:
			if o.Agree {
				continue
			}
			out = append(out, at(diag.Errorf(diag.CodeTableOverlap,
				"hit policy ANY requires overlapping rules to agree, but rules %s disagree on (%s)",
				strings.Join(o.Rules, ", "), o.Combination).
				With("rules", o.Rules).With("combination", o.Combination.Values)))
		default:
			out = append(out, at(diag.Infof(diag.CodeTableOverlap,
				"rules %s overlap on (%s); hit policy %s resolves it",
				strings.Join(o.Rules, ", "), o.Combination, t.HitPolicy).
				With("rules", o.Rules).With("combination", o.Combination.Values)))
		}
	}

	for _, id := range tr.UnreachableRules {
		out = append(out, at(diag.Warnf(diag.CodeUnreachableRule,
			"rule %s can never fire: an earlier rule matches everything it matches", id).With("rule", id)))
	}
	return out
}

func hasDefault(t *model.DecisionTable) bool {
	for _, o := range t.Outputs {
		if strings.TrimSpace(o.DefaultValue) != "" {
			return true
		}
	}
	// A collecting policy has a well-defined answer for "no rule matched" — the
	// empty list — so a gap is not a hole.
	return !t.HitPolicy.SingleHit()
}

func combinationStrings(cs []Combination) []string {
	out := make([]string, 0, len(cs))
	for _, c := range cs {
		out = append(out, c.String())
	}
	return out
}
