// Command verdict is the command-line front end to the Verdict decision engine.
//
// It is a thin adapter: every subcommand parses flags, constructs library
// objects and formats their output. No decision logic lives here.
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/urfave/cli/v3"
)

// version is stamped at build time with -ldflags "-X main.version=...".
var version = "dev"

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	cmd := &cli.Command{
		Name:                  "verdict",
		Usage:                 "evaluate, analyse and explain DMN decision models",
		Version:               version,
		EnableShellCompletion: true,
		Description: "Verdict is a DMN 1.5 decision engine. It evaluates decision models,\n" +
			"reports the gaps and overlaps in their decision tables, and explains what\n" +
			"a decision depends on and how it decides.\n\n" +
			"It is also the server: `verdict serve` exposes the same engine over Twirp\n" +
			"and MCP, and `verdict mcp` serves MCP on stdio for a client that launches\n" +
			"Verdict as a subprocess. One binary — the subcommand chooses the surface.",
		// cli's default handler exits the process itself when a command returns
		// an ExitCoder, which would bypass the reporting below and make every
		// command untestable in-process. Exit codes are decided in one place:
		// here.
		ExitErrHandler: func(context.Context, *cli.Command, error) {},
		Flags: []cli.Flag{
			&cli.StringFlag{
				Name:    "config",
				Aliases: []string{"c"},
				Usage:   "path to a Verdict YAML configuration file",
				Sources: cli.EnvVars("VERDICT_CONFIG"),
			},
		},
		Commands: []*cli.Command{
			cmdEval(),
			cmdAnalyze(),
			cmdExplain(),
			cmdConvert(),
			cmdTrace(),
			cmdSchema(),
			cmdServe(),
			cmdMCP(),
		},
	}

	if err := cmd.Run(ctx, os.Args); err != nil {
		// An ExitCoder carries a status a caller is meant to branch on — exit 2
		// means "it worked, and it reported an error-severity finding" — so it
		// is not an error message to print, only a code to exit with.
		var coded cli.ExitCoder
		if errors.As(err, &coded) {
			if msg := err.Error(); msg != "" {
				fmt.Fprintln(os.Stderr, msg)
			}
			os.Exit(coded.ExitCode())
		}
		fmt.Fprintf(os.Stderr, "verdict: %v\n", strings.TrimPrefix(err.Error(), "verdict: "))
		os.Exit(1)
	}
}
