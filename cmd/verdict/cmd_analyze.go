package main

import (
	"context"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/urfave/cli/v3"

	"github.com/frankbardon/verdict/pkg/analyze"
	"github.com/frankbardon/verdict/pkg/diag"
)

func cmdAnalyze() *cli.Command {
	return &cli.Command{
		Name:      "analyze",
		Aliases:   []string{"analyse"},
		Usage:     "report gaps and overlaps in a model's decision tables",
		ArgsUsage: "<model.dmn|model.vdj>",
		Description: "Probes every decision table's input space and reports the combinations no\n" +
			"rule covers (gaps) and the combinations several rules cover (overlaps),\n" +
			"together with rules an earlier rule makes unreachable.\n\n" +
			"Exit status is 2 when any error-severity finding is reported, so this is\n" +
			"usable as a CI gate.",
		Flags: []cli.Flag{
			&cli.BoolFlag{Name: "json", Usage: "emit the full report as JSON"},
			&cli.BoolFlag{Name: "strict", Usage: "treat gaps as errors, as strict mode does"},
		},
		Action: func(ctx context.Context, cmd *cli.Command) error {
			path := cmd.Args().First()
			if path == "" {
				return fmt.Errorf("analyze needs a model file")
			}
			_, m, err := openModel(cmd, path)
			if err != nil {
				return err
			}
			report := m.Analysis()
			// --strict raises gap findings to errors. The model is still loaded
			// leniently, so the full report is printed either way: a CI gate
			// that fails without saying what it found sends the reader back to
			// run the command a second time without the flag.
			diags := m.Diagnostics()
			if cmd.Bool("strict") {
				diags = raiseGapsToErrors(diags)
			}

			if cmd.Bool("json") {
				if err := writeJSON(map[string]any{
					"model":       m.ID,
					"version":     m.Version,
					"tables":      report.Tables,
					"diagnostics": diags,
				}); err != nil {
					return err
				}
			} else {
				printReport(m.ID, report, diags)
			}

			for _, d := range diags {
				if d.Severity == diag.SeverityError {
					// Reported, not returned: the output above is the answer,
					// and the status code is the signal to a caller that it
					// came with an error-severity finding attached. Returning
					// an ExitCoder rather than calling os.Exit keeps the
					// command callable from a test.
					return exitWithFindings()
				}
			}
			return nil
		},
	}
}

func printReport(modelID string, report *analyze.Report, ds []diag.Diagnostic) {
	fmt.Printf("Model %s\n\n", modelID)

	w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "DECISION\tPOLICY\tRULES\tGAPS\tOVERLAPS\tUNREACHABLE\tNOTE")
	for _, t := range report.Tables {
		note := ""
		if !t.Analysable {
			note = t.Reason
		}
		fmt.Fprintf(w, "%s\t%s\t%d\t%s\t%s\t%s\t%s\n",
			nameOrID(t.DecisionName, t.DecisionID),
			t.HitPolicy,
			t.RuleCount,
			countOrDash(len(t.Gaps), t.Analysable),
			countOrDash(len(t.Overlaps), t.Analysable),
			countOrDash(len(t.UnreachableRules), t.Analysable),
			note,
		)
	}
	w.Flush()

	for _, t := range report.Tables {
		if len(t.Gaps) == 0 && len(t.Overlaps) == 0 {
			continue
		}
		fmt.Printf("\n%s\n", nameOrID(t.DecisionName, t.DecisionID))
		for i, g := range t.Gaps {
			if i == 5 {
				fmt.Printf("  ... and %d more uncovered combinations\n", len(t.Gaps)-5)
				break
			}
			fmt.Printf("  gap:     (%s)\n", g)
		}
		for i, o := range t.Overlaps {
			if i == 5 {
				fmt.Printf("  ... and %d more overlaps\n", len(t.Overlaps)-5)
				break
			}
			agree := ""
			if !o.Agree {
				agree = " — and they disagree"
			}
			fmt.Printf("  overlap: (%s) matches %s%s\n", o.Combination, strings.Join(o.Rules, ", "), agree)
		}
	}

	errors, warnings := 0, 0
	for _, d := range ds {
		switch d.Severity {
		case diag.SeverityError:
			errors++
		case diag.SeverityWarning:
			warnings++
		}
	}
	if errors+warnings > 0 {
		fmt.Printf("\nDiagnostics\n")
		var set diag.Set
		set.Add(ds...)
		for _, d := range set.Sorted() {
			if d.Severity == diag.SeverityInfo {
				continue
			}
			fmt.Printf("  %s\n", d)
		}
	}
	fmt.Printf("\n%d error(s), %d warning(s)\n", errors, warnings)
}

func nameOrID(name, id string) string {
	if name != "" {
		return name
	}
	return id
}

func countOrDash(n int, analysable bool) string {
	if !analysable {
		return "-"
	}
	return fmt.Sprint(n)
}

// raiseGapsToErrors re-severities gap findings, which are warnings by default
// because an uncovered combination is often deliberate — a table that cannot be
// reached with that input, or one whose default is "do nothing". Under --strict
// the caller has said their model has no such holes, and a hole is a build
// failure.
func raiseGapsToErrors(ds []diag.Diagnostic) []diag.Diagnostic {
	out := make([]diag.Diagnostic, len(ds))
	copy(out, ds)
	for i := range out {
		if out[i].Code == diag.CodeTableGap {
			out[i].Severity = diag.SeverityError
		}
	}
	return out
}
