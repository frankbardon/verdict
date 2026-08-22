package main

import (
	"context"
	"fmt"

	"github.com/urfave/cli/v3"

	"github.com/frankbardon/verdict/pkg/explain"
	"github.com/frankbardon/verdict/pkg/verdict"
)

func cmdExplain() *cli.Command {
	return &cli.Command{
		Name:      "explain",
		Usage:     "describe what a decision depends on and how it decides",
		ArgsUsage: "<model.dmn|model.vdj> [decision]",
		Description: "Prints the DRG slice for a decision: the inputs a caller must supply, the\n" +
			"decisions it builds on, the knowledge it may invoke, and its decision logic\n" +
			"rendered in full — the rules of a decision table, or the bindings, output\n" +
			"type and failure policy of an agent decision.\n\n" +
			"With no decision named, lists the model's decisions and services.",
		Flags: []cli.Flag{
			&cli.BoolFlag{Name: "json", Usage: "emit the slice as JSON"},
		},
		Action: func(ctx context.Context, cmd *cli.Command) error {
			path := cmd.Args().First()
			if path == "" {
				return fmt.Errorf("explain needs a model file")
			}
			_, m, err := openModel(cmd, path)
			if err != nil {
				return err
			}

			ref := cmd.Args().Get(1)
			if ref == "" {
				return listModel(cmd, m)
			}
			slice, err := explain.Decision(m.Definitions(), m.Graph(), m.Analysis(), ref)
			if err != nil {
				return err
			}
			if cmd.Bool("json") {
				return writeJSON(slice)
			}
			fmt.Print(slice.Text())
			return nil
		},
	}
}

// listModel prints the model's decisions and services when none was named, so
// `verdict explain model.dmn` is a useful first command rather than an error.
func listModel(cmd *cli.Command, m *verdict.Model) error {
	if cmd.Bool("json") {
		type entry struct {
			ID       string `json:"id"`
			Name     string `json:"name,omitempty"`
			Kind     string `json:"kind"`
			Question string `json:"question,omitempty"`
			TopLevel bool   `json:"top_level,omitempty"`
		}
		top := map[string]bool{}
		for _, id := range m.TopLevelDecisions() {
			top[id] = true
		}
		var out []entry
		for _, d := range m.Decisions() {
			out = append(out, entry{
				ID: d.ID, Name: d.Name, Kind: "decision", Question: d.Question, TopLevel: top[d.ID],
			})
		}
		for _, s := range m.Services() {
			out = append(out, entry{ID: s.ID, Name: s.Name, Kind: "decisionService"})
		}
		return writeJSON(map[string]any{"model": m.ID, "version": m.Version, "elements": out})
	}

	fmt.Printf("%s", m.ID)
	if m.Version != "" {
		fmt.Printf("  version %s", m.Version)
	}
	fmt.Printf("\n\n")

	top := map[string]bool{}
	for _, id := range m.TopLevelDecisions() {
		top[id] = true
	}
	fmt.Println("Decisions")
	for _, d := range m.Decisions() {
		marker := " "
		if top[d.ID] {
			marker = "*"
		}
		fmt.Printf("  %s %-28s %s\n", marker, d.ID, d.Question)
	}
	if len(m.Services()) > 0 {
		fmt.Println("\nDecision services")
		for _, s := range m.Services() {
			fmt.Printf("    %-28s outputs: %v\n", s.ID, s.OutputDecisions)
		}
	}
	fmt.Printf("\n* marks a top-level decision: nothing else in the model depends on it.\n")
	fmt.Printf("Run `verdict explain <model> <decision>` for a decision's full slice.\n")
	return nil
}
