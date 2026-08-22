package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/urfave/cli/v3"

	"github.com/frankbardon/verdict/pkg/trace"
)

func cmdTrace() *cli.Command {
	return &cli.Command{
		Name:      "trace",
		Usage:     "render a saved execution trace as a readable tree",
		ArgsUsage: "[trace.json]",
		Description: "Reads a trace produced by `verdict eval --trace` (or by the server's\n" +
			"GetTrace endpoint) and prints it as an indented tree: which decisions fired,\n" +
			"what each was given, what it produced, which rules matched, and what an\n" +
			"agent decision was asked and answered.\n\n" +
			"With no file argument, reads the trace from standard input.",
		Flags: []cli.Flag{
			&cli.BoolFlag{Name: "values", Usage: "show each node's inputs and output"},
		},
		Action: func(ctx context.Context, cmd *cli.Command) error {
			var raw []byte
			var err error
			if path := cmd.Args().First(); path != "" {
				raw, err = os.ReadFile(path)
			} else {
				raw, err = io.ReadAll(os.Stdin)
			}
			if err != nil {
				return fmt.Errorf("reading trace: %w", err)
			}

			// `verdict eval --trace` nests the trace under a "trace" key; accept
			// both that envelope and a bare trace document.
			var envelope struct {
				Trace *trace.Trace `json:"trace"`
			}
			if err := json.Unmarshal(raw, &envelope); err == nil && envelope.Trace != nil {
				return renderTrace(envelope.Trace, cmd.Bool("values"))
			}
			var t trace.Trace
			if err := json.Unmarshal(raw, &t); err != nil {
				return fmt.Errorf("input is not a Verdict trace: %w", err)
			}
			return renderTrace(&t, cmd.Bool("values"))
		},
	}
}

func renderTrace(t *trace.Trace, values bool) error {
	if t == nil || t.Root == nil {
		return fmt.Errorf("trace is empty")
	}
	fmt.Printf("%s", t.ModelID)
	if t.Entry != "" {
		fmt.Printf(" — %s", t.Entry)
	}
	fmt.Printf("\n%s  %s\n\n", t.StartedAt.Format(time.RFC3339), t.Duration.Round(time.Microsecond))
	renderNode(t.Root, "", true, true, values)
	return nil
}

// renderNode prints one node and its subtree. The root prints flush left with
// no branch glyph; every other node hangs off its parent.
func renderNode(n *trace.Node, prefix string, last, root, values bool) {
	branch := ""
	if !root {
		branch = "├── "
		if last {
			branch = "└── "
		}
	}

	name := n.DecisionName
	if name == "" {
		name = n.DecisionID
	}
	status := ""
	if n.Error != "" {
		status = "  ✗ " + firstLine(n.Error)
	}
	fmt.Printf("%s%s%s  [%s]  %s%s\n",
		prefix, branch, name, n.NodeKind, n.Duration.Round(time.Microsecond), status)

	childPrefix := prefix
	if !root {
		if last {
			childPrefix += "    "
		} else {
			childPrefix += "\u2502   "
		}
	}

	// Annotations and values belong to this node, so they are indented under it
	// rather than aligned with its children.
	detail := childPrefix
	if !root {
		detail += "    "
	}
	for _, key := range annotationOrder {
		v, ok := n.Annotations[key]
		if !ok {
			continue
		}
		fmt.Printf("%s%s: %s\n", detail, key, compact(v))
	}
	if values {
		if len(n.Inputs) > 0 {
			fmt.Printf("%sinputs: %s\n", detail, compact(n.Inputs))
		}
		if n.Output != nil {
			fmt.Printf("%soutput: %s\n", detail, compact(n.Output))
		}
	}

	for i, c := range n.Children {
		renderNode(c, childPrefix, i == len(n.Children)-1, false, values)
	}
}

// annotationOrder fixes the order annotations print in, so two traces of the
// same model are diffable.
var annotationOrder = []string{
	trace.AnnHitPolicy,
	trace.AnnAggregation,
	trace.AnnMatchedRules,
	trace.AnnDefaulted,
	trace.AnnBKM,
	trace.AnnSessionRef,
	trace.AnnAttempts,
	trace.AnnTokens,
	trace.AnnFallbackUsed,
	trace.AnnFallbackFrom,
	trace.AnnError,
}

func compact(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprint(v)
	}
	s := string(b)
	if len(s) > 160 {
		return s[:160] + "…"
	}
	return s
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}
