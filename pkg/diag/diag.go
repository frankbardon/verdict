// Package diag carries the diagnostic surface shared by the loader, the static
// analyzer and the evaluator. It has no dependencies on any other Verdict
// package so every layer can report through it.
package diag

import (
	"fmt"
	"sort"
	"strings"
)

// Severity ranks a diagnostic. Errors block loading in strict mode and are
// always surfaced; warnings and infos are advisory.
type Severity string

const (
	SeverityError   Severity = "error"
	SeverityWarning Severity = "warning"
	SeverityInfo    Severity = "info"
)

// Code is a stable machine-readable identifier. Codes never change meaning;
// retired codes are kept in the catalogue with a `retired` note so that logs
// and dashboards referring to them stay interpretable.
type Code string

const (
	// Loader / parse.
	CodeUnsupportedExpression Code = "VERDICT_LOAD_001" // boxed expression kind Verdict cannot evaluate
	CodeUnknownHitPolicy      Code = "VERDICT_LOAD_002"
	CodeMissingLogic          Code = "VERDICT_LOAD_003" // decision has no decision logic
	CodeDanglingRequirement   Code = "VERDICT_LOAD_004" // requirement points at an unknown element
	CodeCycle                 Code = "VERDICT_LOAD_005" // DRG is not acyclic
	CodeDuplicateID           Code = "VERDICT_LOAD_006"
	CodeJavaBinding           Code = "VERDICT_LOAD_007" // Java-bound BKM parsed but not executable
	CodePMMLBinding           Code = "VERDICT_LOAD_008" // PMML function reference, deferred to v2
	CodeForeignLanguage       Code = "VERDICT_LOAD_009" // expressionLanguage is not FEEL
	CodeDialectViolation      Code = "VERDICT_LOAD_010" // FEEL-only construct in an S-FEEL model
	CodeArityMismatch         Code = "VERDICT_LOAD_011" // rule entry count differs from clause count
	CodeBadTemplate           Code = "VERDICT_LOAD_012" // agentDecision prompt template does not parse
	CodeBadExpression         Code = "VERDICT_LOAD_013" // FEEL text does not parse
	CodeUnknownService        Code = "VERDICT_LOAD_014"

	// Static analysis.
	CodeTableGap         Code = "VERDICT_ANALYZE_001"
	CodeTableOverlap     Code = "VERDICT_ANALYZE_002"
	CodeMissingOutputSet Code = "VERDICT_ANALYZE_003" // PRIORITY/OUTPUT ORDER without outputValues
	CodeUnreachableRule  Code = "VERDICT_ANALYZE_004"
	CodeUnusedInput      Code = "VERDICT_ANALYZE_005"

	// Evaluation.
	CodeNoRuleMatched     Code = "VERDICT_EVAL_001"
	CodeMultipleHits      Code = "VERDICT_EVAL_002" // >1 match under UNIQUE
	CodeInconsistentAny   Code = "VERDICT_EVAL_003" // disagreeing outputs under ANY
	CodeMissingInput      Code = "VERDICT_EVAL_004"
	CodeExpressionError   Code = "VERDICT_EVAL_005"
	CodeTypeViolation     Code = "VERDICT_EVAL_006"
	CodeAgentFailure      Code = "VERDICT_EVAL_007"
	CodeAgentTimeout      Code = "VERDICT_EVAL_008"
	CodeAgentValidation   Code = "VERDICT_EVAL_009"
	CodeFallbackUsed      Code = "VERDICT_EVAL_010"
	CodeStubEvaluated     Code = "VERDICT_EVAL_011" // Java BKM or unknown expression yielded null
	CodeRecursionExceeded Code = "VERDICT_EVAL_012"
)

// Diagnostic is one reported problem, anchored to a model element where
// possible.
type Diagnostic struct {
	Code     Code     `json:"code"`
	Severity Severity `json:"severity"`
	Message  string   `json:"message"`

	// ModelID, ElementID and ElementName locate the diagnostic in the DRG.
	ModelID     string `json:"model_id,omitempty"`
	ElementID   string `json:"element_id,omitempty"`
	ElementName string `json:"element_name,omitempty"`

	// Detail carries code-specific structured context, for example the matching
	// rule indexes of an overlap or the uncovered input combination of a gap.
	Detail map[string]any `json:"detail,omitempty"`
}

func (d Diagnostic) String() string {
	var b strings.Builder
	fmt.Fprintf(&b, "[%s] %s: %s", d.Severity, d.Code, d.Message)
	if d.ElementName != "" {
		fmt.Fprintf(&b, " (%s)", d.ElementName)
	} else if d.ElementID != "" {
		fmt.Fprintf(&b, " (%s)", d.ElementID)
	}
	return b.String()
}

// Error reports a diagnostic as a Go error, so an error-severity diagnostic can
// be returned directly from a loader or evaluator.
func (d Diagnostic) Error() string { return d.String() }

// Errorf builds an error-severity diagnostic.
func Errorf(code Code, format string, args ...any) Diagnostic {
	return Diagnostic{Code: code, Severity: SeverityError, Message: fmt.Sprintf(format, args...)}
}

// Warnf builds a warning-severity diagnostic.
func Warnf(code Code, format string, args ...any) Diagnostic {
	return Diagnostic{Code: code, Severity: SeverityWarning, Message: fmt.Sprintf(format, args...)}
}

// Infof builds an info-severity diagnostic.
func Infof(code Code, format string, args ...any) Diagnostic {
	return Diagnostic{Code: code, Severity: SeverityInfo, Message: fmt.Sprintf(format, args...)}
}

// At anchors a diagnostic to a DRG element.
func (d Diagnostic) At(elementID, elementName string) Diagnostic {
	d.ElementID, d.ElementName = elementID, elementName
	return d
}

// In anchors a diagnostic to a model.
func (d Diagnostic) In(modelID string) Diagnostic {
	d.ModelID = modelID
	return d
}

// With attaches one structured detail key.
func (d Diagnostic) With(key string, value any) Diagnostic {
	if d.Detail == nil {
		d.Detail = map[string]any{}
	} else {
		clone := make(map[string]any, len(d.Detail)+1)
		for k, v := range d.Detail {
			clone[k] = v
		}
		d.Detail = clone
	}
	d.Detail[key] = value
	return d
}

// Set is an ordered, append-only diagnostic accumulator.
type Set struct {
	items []Diagnostic
}

// Add appends diagnostics to the set.
func (s *Set) Add(ds ...Diagnostic) {
	s.items = append(s.items, ds...)
}

// All returns the accumulated diagnostics in insertion order.
func (s *Set) All() []Diagnostic {
	if s == nil {
		return nil
	}
	return s.items
}

// Errors returns only the error-severity diagnostics.
func (s *Set) Errors() []Diagnostic {
	if s == nil {
		return nil
	}
	var out []Diagnostic
	for _, d := range s.items {
		if d.Severity == SeverityError {
			out = append(out, d)
		}
	}
	return out
}

// HasErrors reports whether any error-severity diagnostic was recorded.
func (s *Set) HasErrors() bool { return len(s.Errors()) > 0 }

// Err collapses the error-severity diagnostics into a single error, or nil.
func (s *Set) Err() error {
	errs := s.Errors()
	if len(errs) == 0 {
		return nil
	}
	if len(errs) == 1 {
		return errs[0]
	}
	msgs := make([]string, len(errs))
	for i, e := range errs {
		msgs[i] = e.String()
	}
	return fmt.Errorf("%d errors:\n  %s", len(errs), strings.Join(msgs, "\n  "))
}

// Sorted returns the diagnostics ordered by severity then code then element, so
// CLI output and golden tests are stable regardless of traversal order.
func (s *Set) Sorted() []Diagnostic {
	out := append([]Diagnostic(nil), s.All()...)
	rank := map[Severity]int{SeverityError: 0, SeverityWarning: 1, SeverityInfo: 2}
	sort.SliceStable(out, func(i, j int) bool {
		if rank[out[i].Severity] != rank[out[j].Severity] {
			return rank[out[i].Severity] < rank[out[j].Severity]
		}
		if out[i].Code != out[j].Code {
			return out[i].Code < out[j].Code
		}
		return out[i].ElementID < out[j].ElementID
	})
	return out
}
