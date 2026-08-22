package analyze

import (
	"fmt"
	"strings"
	"time"

	"github.com/frankbardon/verdict/pkg/eval"
	vfeel "github.com/frankbardon/verdict/pkg/feel"
)

// sample is one probe value for an input clause, carrying both the FEEL value
// the matcher tests and a human-readable rendering for the report.
type sample struct {
	text  string
	value any
}

func samplesOf(ls []vfeel.Literal) []sample {
	out := make([]sample, 0, len(ls))
	for _, l := range ls {
		out = append(out, sample{text: l.Text, value: l.Value})
	}
	return out
}

// domain is the finite set of probe values an input clause is explored with.
//
// Exhaustively checking a decision table is impossible in general: an input of
// type number has an infinite domain. What makes the check tractable, and what
// every practical DMN analyser does, is that a rule set built from unary tests
// partitions its inputs at a finite number of boundaries. Probing each declared
// value, each range endpoint, the points immediately either side of every
// endpoint, and one value outside everything the table mentions is enough to
// detect any gap or overlap a table's own vocabulary can express.
type domain struct {
	samples []sample
	// reason explains why a clause could not be sampled, when samples is empty.
	reason string
}

// buildDomain derives the probe set for one input clause.
func buildDomain(m *eval.Matcher, clause int) domain {
	// A declared inputValues list is the modeller's own statement of the
	// domain, so it is authoritative when present.
	if list := m.InputValues(clause); strings.TrimSpace(list) != "" {
		if d, ok := domainFromValueList(m, clause, list); ok {
			return d
		}
	}

	// Otherwise, mine the rules' own tests for the boundaries they mention.
	var literals []sample
	sawTest := false
	for rule := 0; rule < m.RuleCount(); rule++ {
		t := m.Test(rule, clause)
		if t.MatchesAnything() {
			continue
		}
		sawTest = true
		literals = append(literals, samplesOf(t.Literals())...)
	}
	if !sawTest {
		// Every rule ignores this clause. One arbitrary probe is enough: no
		// rule's behaviour varies along this axis.
		return domain{samples: []sample{{text: "any", value: vfeel.Null}}}
	}
	if len(literals) == 0 {
		return domain{reason: fmt.Sprintf(
			"input clause %d tests values that are not decidable statically", clause+1)}
	}
	return domain{samples: expand(literals)}
}

func domainFromValueList(m *eval.Matcher, clause int, list string) (domain, bool) {
	parts := vfeel.SplitList(list)
	var literals []sample
	for _, part := range parts {
		x, err := m.Compile(part)
		if err != nil {
			return domain{}, false
		}
		literals = append(literals, samplesOf(x.Literals())...)
	}
	if len(literals) == 0 {
		return domain{}, false
	}
	// A declared list is closed: values outside it are not legal inputs, so the
	// probe set is the list itself plus range interiors, with no "outside"
	// sentinel. That is what lets a fully enumerated table be gap-free.
	//
	// expandWithin probes either side of every boundary, which for a range like
	// [0..50] means probing -1 and 51. Those are outside the domain the
	// modeller declared, and reporting them as gaps would make a table that
	// covers its whole declared domain impossible to close — so they are
	// filtered back out here, against the same list, evaluated by the same
	// engine.
	return domain{samples: dedupe(clip(m, clause, expandWithin(literals)))}, true
}

// expand turns the literals a clause mentions into a probe set: every literal,
// the points immediately either side of each numeric or temporal literal, and
// one value outside everything mentioned.
func expand(literals []sample) []sample {
	out := expandWithin(literals)

	// A value no rule mentions, to expose the "everything else" gap.
	switch kindOfSamples(literals) {
	case "number":
		lo := minNumber(literals)
		out = append(out, sample{text: "below " + lo.text, value: vfeel.NumberOffset(lo.value, -1)})
	case "string":
		out = append(out, sample{text: outsideStringText, value: outsideStringValue})
	case "date", "time", "date and time":
		if t, ok := earliestTemporal(literals); ok {
			out = append(out, sample{
				text:  "before " + t.text,
				value: vfeel.TemporalOffset(t.value, -24*time.Hour),
			})
		}
	}
	return dedupe(out)
}

// outsideStringValue is a probe no realistic string literal equals, used to
// expose the gap a table leaves for unenumerated string inputs.
const (
	outsideStringValue = "(any other value)"
	outsideStringText  = `"(any other value)"`
)

// expandWithin produces the interior probe set: the literals themselves plus
// the immediate neighbours of numeric and temporal boundaries, which is what
// distinguishes `[1..10]` from `[1..10)`.
func expandWithin(literals []sample) []sample {
	out := make([]sample, 0, len(literals)*3)
	for _, l := range literals {
		out = append(out, l)
		switch {
		case vfeel.IsNumber(l.value):
			out = append(out,
				sample{text: l.text + "-1", value: vfeel.NumberOffset(l.value, -1)},
				sample{text: l.text + "+1", value: vfeel.NumberOffset(l.value, 1)})
		case vfeel.IsTemporal(l.value):
			out = append(out,
				sample{text: l.text + "-1d", value: vfeel.TemporalOffset(l.value, -24*time.Hour)},
				sample{text: l.text + "+1d", value: vfeel.TemporalOffset(l.value, 24*time.Hour)})
		}
	}
	return out
}

// clip drops probes the clause's declared value list does not admit.
func clip(m *eval.Matcher, clause int, samples []sample) []sample {
	out := make([]sample, 0, len(samples))
	for _, s := range samples {
		admits, decidable := m.Admits(clause, s.value)
		if decidable && !admits {
			continue
		}
		out = append(out, s)
	}
	// If clipping removed everything the list is not usable as a domain after
	// all; fall back to the unclipped set rather than declaring the clause
	// unanalysable.
	if len(out) == 0 {
		return samples
	}
	return out
}

func kindOfSamples(ls []sample) string {
	for _, l := range ls {
		if n := vfeel.TypeName(l.value); n != "null" {
			return n
		}
	}
	return "null"
}

func minNumber(ls []sample) sample {
	var best sample
	found := false
	for _, l := range ls {
		if !vfeel.IsNumber(l.value) {
			continue
		}
		if !found {
			best, found = l, true
			continue
		}
		if c, ok := vfeel.Compare(l.value, best.value); ok && c < 0 {
			best = l
		}
	}
	return best
}

func earliestTemporal(ls []sample) (sample, bool) {
	var best sample
	found := false
	for _, l := range ls {
		if !vfeel.IsTemporal(l.value) {
			continue
		}
		if !found {
			best, found = l, true
			continue
		}
		if c, ok := vfeel.Compare(l.value, best.value); ok && c < 0 {
			best = l
		}
	}
	return best, found
}

func dedupe(ls []sample) []sample {
	out := make([]sample, 0, len(ls))
	for _, l := range ls {
		seen := false
		for _, o := range out {
			if vfeel.Equal(o.value, l.value) {
				seen = true
				break
			}
		}
		if !seen {
			out = append(out, l)
		}
	}
	return out
}
