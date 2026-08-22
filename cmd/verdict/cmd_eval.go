package main

import (
	"context"
	"fmt"

	"github.com/urfave/cli/v3"

	"github.com/frankbardon/verdict/pkg/diag"
	"github.com/frankbardon/verdict/pkg/verdict"
)

func cmdEval() *cli.Command {
	flags := []cli.Flag{
		&cli.StringFlag{
			Name:  "decision",
			Usage: "evaluate a single decision by id or name instead of the model's outputs",
		},
		&cli.StringFlag{
			Name:  "service",
			Usage: "evaluate a decision service by id or name",
		},
		&cli.BoolFlag{
			Name:  "trace",
			Usage: "include the execution trace in the output",
		},
		&cli.BoolFlag{
			Name:  "quiet",
			Usage: "suppress diagnostics on stderr",
		},
	}
	flags = append(flags, inputFlags()...)
	flags = append(flags, agentFlags()...)

	return &cli.Command{
		Name:      "eval",
		Usage:     "evaluate a decision model against a set of inputs",
		ArgsUsage: "<model.dmn|model.vdj>",
		Description: "Evaluates the model's top-level decisions, or the decision or service\n" +
			"named by --decision / --service, and writes the outputs as JSON on stdout.\n" +
			"Diagnostics go to stderr so stdout stays machine-readable.\n\n" +
			"Exit status is 1 when the evaluation fails and 2 when it succeeds but the\n" +
			"model reported an error-severity diagnostic.",
		Flags: flags,
		Action: func(ctx context.Context, cmd *cli.Command) error {
			path := cmd.Args().First()
			if path == "" {
				return fmt.Errorf("eval needs a model file")
			}
			engine, m, err := openModel(cmd, path)
			if err != nil {
				return err
			}
			inputs, err := readInputs(cmd)
			if err != nil {
				return err
			}

			res, err := engine.EvaluateVersion(ctx, m.ID, m.Version, buildRequest(cmd, inputs))
			if err != nil {
				// A failed evaluation still has a trace and diagnostics, and they
				// are the most useful thing a caller can be handed.
				if res != nil && !cmd.Bool("quiet") {
					printDiagnostics(res.Diagnostics, diag.SeverityWarning)
				}
				return err
			}
			if !cmd.Bool("quiet") {
				printDiagnostics(res.Diagnostics, diag.SeverityWarning)
			}

			out := map[string]any{"outputs": res.Outputs, "duration_ms": res.Duration.Milliseconds()}
			if cmd.Bool("trace") && res.Trace != nil {
				out["trace"] = res.Trace
			}
			if err := writeJSON(out); err != nil {
				return err
			}
			for _, d := range res.Diagnostics {
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

func buildRequest(cmd *cli.Command, inputs verdict.Inputs) verdict.Request {
	req := verdict.Request{Inputs: inputs}
	if d := cmd.String("decision"); d != "" {
		req.Decisions = []string{d}
	}
	req.Service = cmd.String("service")
	return req
}
