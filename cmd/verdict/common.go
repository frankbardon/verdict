package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/urfave/cli/v3"

	"github.com/frankbardon/verdict/pkg/agent"
	agenthttp "github.com/frankbardon/verdict/pkg/agent/http"
	"github.com/frankbardon/verdict/pkg/agent/mock"
	"github.com/frankbardon/verdict/pkg/config"
	"github.com/frankbardon/verdict/pkg/diag"
	"github.com/frankbardon/verdict/pkg/verdict"
)

// loadConfig reads the configuration named by the global --config flag, or
// returns the documented defaults when none was given.
func loadConfig(cmd *cli.Command) (config.Config, error) {
	path := cmd.Root().String("config")
	if path == "" {
		return config.Default(), nil
	}
	return config.Load(path)
}

// openModel builds an engine from the configuration and loads one model file.
func openModel(cmd *cli.Command, path string) (*verdict.Engine, *verdict.Model, error) {
	cfg, err := loadConfig(cmd)
	if err != nil {
		return nil, nil, err
	}
	opts := cfg.Options()

	bridge, err := buildBridge(cmd, cfg)
	if err != nil {
		return nil, nil, err
	}
	if bridge != nil {
		opts = append(opts, verdict.WithAgentBridge(bridge))
	}

	engine, err := verdict.NewEngine(opts...)
	if err != nil {
		return nil, nil, err
	}
	m, err := engine.LoadModel(verdict.FromFile(path))
	if err != nil {
		return nil, nil, err
	}
	return engine, m, nil
}

// buildBridge constructs the agent bridge the configuration asks for.
//
// The CLI deliberately cannot construct the Nexus bridge: that lives in a
// separate module so the core never depends on Nexus. A configuration naming it
// is an instruction to a host that has imported it, and the CLI says so rather
// than silently evaluating agent decisions against nothing.
func buildBridge(cmd *cli.Command, cfg config.Config) (agent.Bridge, error) {
	name := cfg.Agent.Bridge
	if override := cmd.String("bridge"); override != "" {
		name = override
	}
	switch name {
	case "", "none":
		return nil, nil
	case "mock":
		answers := cmd.StringSlice("agent-answer")
		opts := make([]mock.Option, 0, len(answers))
		for _, a := range answers {
			decision, value, ok := strings.Cut(a, "=")
			if !ok {
				return nil, fmt.Errorf("--agent-answer must be DECISION=VALUE, got %q", a)
			}
			opts = append(opts, mock.WithAnswer(decision, decodeScalar(value)))
		}
		return mock.New(opts...), nil
	case "http":
		endpoint := cfg.Agent.HTTP.Endpoint
		if override := cmd.String("agent-endpoint"); override != "" {
			endpoint = override
		}
		if endpoint == "" {
			return nil, fmt.Errorf("agent bridge \"http\" needs agent.http.endpoint or --agent-endpoint")
		}
		opts := make([]agenthttp.Option, 0, len(cfg.Agent.HTTP.Headers))
		for k, v := range cfg.Agent.HTTP.Headers {
			opts = append(opts, agenthttp.WithHeader(k, v))
		}
		return agenthttp.New(endpoint, opts...), nil
	case "nexus":
		return nil, fmt.Errorf(
			"agent bridge %q lives in the separate github.com/frankbardon/verdict/nexus module and "+
				"is wired in by a host program that imports it; the CLI can use \"mock\" or \"http\"", name)
	default:
		return nil, fmt.Errorf("unknown agent bridge %q (want mock, http or none)", name)
	}
}

// agentFlags are shared by the subcommands that may evaluate an agent decision.
func agentFlags() []cli.Flag {
	return []cli.Flag{
		&cli.StringFlag{
			Name:  "bridge",
			Usage: "agent bridge to use: mock, http or none (overrides the configuration)",
		},
		&cli.StringFlag{
			Name:  "agent-endpoint",
			Usage: "endpoint for the http agent bridge",
		},
		&cli.StringSliceFlag{
			Name:  "agent-answer",
			Usage: "canned answer for the mock bridge, as DECISION=VALUE (repeatable)",
		},
	}
}

// decodeScalar interprets a command-line value as JSON when it parses as JSON,
// and as a plain string otherwise, so `--agent-answer tier=low` and
// `--agent-answer score=0.8` both do the obvious thing.
func decodeScalar(s string) any {
	var v any
	if err := json.Unmarshal([]byte(s), &v); err == nil {
		return v
	}
	return s
}

// readInputs loads the evaluation payload from a file, from a literal JSON
// string, or from standard input.
func readInputs(cmd *cli.Command) (verdict.Inputs, error) {
	var raw []byte
	switch {
	case cmd.String("input") != "":
		b, err := os.ReadFile(cmd.String("input"))
		if err != nil {
			return nil, fmt.Errorf("reading inputs: %w", err)
		}
		raw = b
	case cmd.String("data") != "":
		raw = []byte(cmd.String("data"))
	default:
		return verdict.Inputs{}, nil
	}
	var inputs verdict.Inputs
	if err := json.Unmarshal(raw, &inputs); err != nil {
		return nil, fmt.Errorf("inputs must be a JSON object: %w", err)
	}
	return inputs, nil
}

// inputFlags are shared by the subcommands that take an evaluation payload.
func inputFlags() []cli.Flag {
	return []cli.Flag{
		&cli.StringFlag{
			Name:    "input",
			Aliases: []string{"i"},
			Usage:   "path to a JSON file of input values",
		},
		&cli.StringFlag{
			Name:    "data",
			Aliases: []string{"d"},
			Usage:   "input values as a literal JSON object",
		},
	}
}

// printDiagnostics writes a model's findings to stderr, so that piping stdout
// to a JSON consumer still surfaces them to a human.
func printDiagnostics(ds []diag.Diagnostic, minSeverity diag.Severity) {
	rank := map[diag.Severity]int{diag.SeverityError: 0, diag.SeverityWarning: 1, diag.SeverityInfo: 2}
	for _, d := range ds {
		if rank[d.Severity] > rank[minSeverity] {
			continue
		}
		fmt.Fprintln(os.Stderr, d.String())
	}
}

func writeJSON(v any) error {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

// exitStatusFindings is the status `eval` and `analyze` return when the command
// itself succeeded but the model reported an error-severity diagnostic. It is
// distinct from 1 — the command failed — so a CI step can tell "the model is
// broken" from "the tool is broken".
const exitStatusFindings = 2

// exitWithFindings returns that status with no message. The findings have
// already been printed; a second line saying "error" adds nothing.
func exitWithFindings() error { return cli.Exit("", exitStatusFindings) }
