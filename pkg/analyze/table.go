package analyze

import (
	"fmt"

	"github.com/frankbardon/verdict/pkg/dmn/model"
	"github.com/frankbardon/verdict/pkg/eval"
	vfeel "github.com/frankbardon/verdict/pkg/feel"
)

// analyzeTable probes one decision table's input space for gaps and overlaps.
func analyzeTable(p *eval.Program, d *model.Decision, t *model.DecisionTable, opts Options) TableReport {
	tr := TableReport{
		DecisionID:   d.ID,
		DecisionName: d.Name,
		HitPolicy:    t.HitPolicy.Shorthand(t.Aggregation),
		RuleCount:    len(t.Rules),
	}

	m, ok := p.Matcher(t)
	if !ok {
		tr.Reason = "the table was not prepared"
		return tr
	}
	if m.RuleCount() == 0 || m.InputCount() == 0 {
		tr.Reason = "the table has no rules or no input clauses"
		return tr
	}

	domains := make([]domain, m.InputCount())
	total := 1
	for i := range domains {
		domains[i] = buildDomain(m, i)
		if len(domains[i].samples) == 0 {
			tr.Reason = domains[i].reason
			if tr.Reason == "" {
				tr.Reason = fmt.Sprintf("input clause %d has no enumerable domain", i+1)
			}
			return tr
		}
		total *= len(domains[i].samples)
		if total > opts.MaxCombinations {
			tr.Reason = fmt.Sprintf(
				"the table's input space exceeds the %d-combination analysis budget", opts.MaxCombinations)
			return tr
		}
	}
	tr.Analysable = true

	// Walk the cross-product of the per-clause probe sets.
	idx := make([]int, len(domains))
	values := make([]any, len(domains))
	texts := make([]string, len(domains))

	// matchedBy[rule] collects the combinations a rule matches, used afterwards
	// to find rules an earlier rule completely subsumes.
	matchedBy := make([]map[int]bool, m.RuleCount())
	for i := range matchedBy {
		matchedBy[i] = map[int]bool{}
	}

	combination := 0
	for {
		for i := range domains {
			values[i] = domains[i].samples[idx[i]].value
			texts[i] = domains[i].samples[idx[i]].text
		}
		matches, err := m.Match(values)
		if err != nil {
			// A rule whose test cannot be decided without runtime data makes the
			// whole table unanalysable rather than producing a misleading report.
			tr.Analysable = false
			tr.Gaps, tr.Overlaps = nil, nil
			tr.Reason = fmt.Sprintf("a rule could not be evaluated statically: %v", err)
			return tr
		}
		combo := Combination{Values: append([]string(nil), texts...)}
		switch {
		case len(matches) == 0:
			tr.Gaps = append(tr.Gaps, combo)
		case len(matches) > 1:
			tr.Overlaps = append(tr.Overlaps, Overlap{
				Combination: combo,
				Rules:       ruleIDsOf(t, matches),
				Agree:       outputsAgree(m, matches),
			})
		}
		for _, r := range matches {
			matchedBy[r][combination] = true
		}
		combination++

		// Odometer increment over the probe sets.
		carry := len(domains) - 1
		for carry >= 0 {
			idx[carry]++
			if idx[carry] < len(domains[carry].samples) {
				break
			}
			idx[carry] = 0
			carry--
		}
		if carry < 0 {
			break
		}
	}

	tr.UnreachableRules = unreachableRules(t, matchedBy)
	return tr
}

// outputsAgree reports whether every overlapping rule produces the same output.
// Rules whose outputs depend on runtime data cannot be compared statically, and
// are reported as agreeing so that the analyser never fabricates a conflict it
// cannot prove.
func outputsAgree(m *eval.Matcher, rules []int) bool {
	var first []any
	for i, r := range rules {
		exprs := m.OutputExprs(r)
		vals := make([]any, len(exprs))
		for j, x := range exprs {
			v, ok := m.EvalStatic(x)
			if !ok {
				return true
			}
			vals[j] = v
		}
		if i == 0 {
			first = vals
			continue
		}
		if len(vals) != len(first) {
			return false
		}
		for j := range vals {
			if !vfeel.Equal(vals[j], first[j]) {
				return false
			}
		}
	}
	return true
}

// unreachableRules finds rules that can never be the deciding one because an
// earlier rule already covers every combination they match.
//
// The check applies to FIRST only. Under FIRST, "earlier" means rule order, so
// subsumption by any earlier rule is exactly unreachability. PRIORITY looks
// similar and is not: it ranks matches by their position in the output clause's
// outputValues list, so a rule late in the table can still outrank one above it,
// and applying the rule-order test there would report rules as dead that are in
// fact the deciding ones. Every other policy collects all its matches, where a
// subsumed rule still contributes.
func unreachableRules(t *model.DecisionTable, matchedBy []map[int]bool) []string {
	if t.HitPolicy != model.HitFirst {
		return nil
	}
	var out []string
	for r := 1; r < len(matchedBy); r++ {
		if len(matchedBy[r]) == 0 {
			continue
		}
		covered := true
		for combo := range matchedBy[r] {
			seenEarlier := false
			for e := 0; e < r; e++ {
				if matchedBy[e][combo] {
					seenEarlier = true
					break
				}
			}
			if !seenEarlier {
				covered = false
				break
			}
		}
		if covered && r < len(t.Rules) {
			out = append(out, t.Rules[r].ID)
		}
	}
	return out
}

func ruleIDsOf(t *model.DecisionTable, matches []int) []string {
	out := make([]string, 0, len(matches))
	for _, m := range matches {
		if m < len(t.Rules) {
			out = append(out, t.Rules[m].ID)
		}
	}
	return out
}
