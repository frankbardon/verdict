package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/urfave/cli/v3"

	"github.com/frankbardon/verdict/pkg/config"
	"github.com/frankbardon/verdict/pkg/diag"
	"github.com/frankbardon/verdict/pkg/verdict"
	"github.com/frankbardon/verdict/server"
)

func cmdServe() *cli.Command {
	return &cli.Command{
		Name:  "serve",
		Usage: "serve decision models over Twirp and MCP",
		Description: "Loads the models named on the command line or in the configuration, then\n" +
			"serves them over Twirp at --twirp-path, MCP at /mcp, and health at /healthz.\n\n" +
			"Models are loaded at startup and the process refuses to start if any of them\n" +
			"fails to load — a decision server that is up but missing a model is worse than\n" +
			"one that is down, because callers get a 404 they may not check.\n\n" +
			"For an MCP client that launches Verdict as a subprocess, use `verdict mcp`\n" +
			"instead: same tools and resources, over stdio, without a listener.",
		Flags: append(serveFlags(), agentFlags()...),
		Action: func(ctx context.Context, cmd *cli.Command) error {
			logger := newLogger(cmd.String("log-level"), os.Stderr)

			engine, cfg, err := buildServingEngine(cmd)
			if err != nil {
				return err
			}
			if err := loadServingModels(cmd, cfg, engine, logger); err != nil {
				return err
			}

			srv, err := server.New(engine, serverOptions(cmd, cfg, logger))
			if err != nil {
				return err
			}
			return srv.ListenAndServe(ctx)
		},
	}
}

func cmdMCP() *cli.Command {
	return &cli.Command{
		Name:  "mcp",
		Usage: "serve the MCP surface over stdin and stdout",
		Description: "Runs Verdict as an MCP server on stdio, which is how an MCP client that\n" +
			"launches it as a subprocess expects to talk to it. It exposes the same tools\n" +
			"and resources as `verdict serve` does at /mcp.\n\n" +
			"Stdout carries protocol frames and nothing else — diagnostics and logs go to\n" +
			"stderr — so this is not a command to pipe into anything but an MCP client.\n\n" +
			"Configure a client with:\n\n" +
			"  command: verdict\n" +
			"  args:    [\"mcp\", \"--model\", \"/path/to/model.dmn\"]",
		Flags: append(mcpFlags(), agentFlags()...),
		Action: func(ctx context.Context, cmd *cli.Command) error {
			// Logs go to stderr: stdout is the transport, and one stray line on
			// it corrupts the session rather than merely looking untidy.
			logger := newLogger(cmd.String("log-level"), os.Stderr)

			engine, cfg, err := buildServingEngine(cmd)
			if err != nil {
				return err
			}
			if err := loadServingModels(cmd, cfg, engine, logger); err != nil {
				return err
			}
			return server.ServeStdio(ctx, engine, serverOptions(cmd, cfg, logger))
		},
	}
}

// serveFlags are the HTTP server's own flags. modelFlags and agentFlags are
// shared with `verdict mcp`, which needs the models and the bridge but has no
// listener, no Twirp path and no health endpoint.
func serveFlags() []cli.Flag {
	return append(modelFlags(),
		&cli.StringFlag{
			Name:    "listen",
			Aliases: []string{"l"},
			Usage:   "address to bind",
			Sources: cli.EnvVars("VERDICT_LISTEN"),
		},
		&cli.StringFlag{Name: "twirp-path", Usage: "path prefix for the Twirp endpoint"},
		&cli.BoolFlag{Name: "no-mcp", Usage: "disable the MCP endpoint"},
	)
}

func mcpFlags() []cli.Flag { return modelFlags() }

// modelFlags are what both serving commands need: which models to load, how
// strictly, and what an MCP client is allowed to do to the registry.
func modelFlags() []cli.Flag {
	return []cli.Flag{
		&cli.StringSliceFlag{
			Name:    "model",
			Aliases: []string{"m"},
			Usage:   "model file or directory to load (repeatable)",
		},
		&cli.BoolFlag{
			Name: "mcp-allow-load",
			Usage: "expose the model-loading MCP tool; off by default because an agent " +
				"that can load models can shadow the ones you deployed",
		},
		&cli.StringFlag{
			Name:  "model-root",
			Usage: "directory model loading may read paths from; unset forbids loading by path",
		},
		&cli.BoolFlag{Name: "strict", Usage: "refuse to load a model with error-severity findings"},
		&cli.StringFlag{Name: "log-level", Value: "info", Usage: "debug, info, warn or error"},
	}
}

// buildServingEngine resolves the configuration and constructs the engine both
// serving commands run on.
func buildServingEngine(cmd *cli.Command) (*verdict.Engine, config.Config, error) {
	cfg, err := loadConfig(cmd)
	if err != nil {
		return nil, config.Config{}, err
	}
	if cmd.Bool("strict") {
		cfg.Evaluation.StrictMode = true
	}

	opts := cfg.Options()
	bridge, err := buildBridge(cmd, cfg)
	if err != nil {
		return nil, config.Config{}, err
	}
	if bridge != nil {
		opts = append(opts, verdict.WithAgentBridge(bridge))
	}

	engine, err := verdict.NewEngine(opts...)
	if err != nil {
		return nil, config.Config{}, err
	}
	return engine, cfg, nil
}

// loadServingModels loads every model named on the command line or in the
// configuration, and fails if there are none: a decision server with nothing to
// decide is a misconfiguration, not a valid idle state.
func loadServingModels(cmd *cli.Command, cfg config.Config, engine *verdict.Engine, logger *slog.Logger) error {
	paths := append([]string{}, cfg.Models.Paths...)
	paths = append(paths, cmd.StringSlice("model")...)
	files, err := expandModelPaths(paths)
	if err != nil {
		return err
	}
	if len(files) == 0 {
		return fmt.Errorf("no models to serve: pass --model or set verdict.models.paths")
	}
	for _, f := range files {
		m, err := engine.LoadModel(verdict.FromFile(f))
		if err != nil {
			return fmt.Errorf("loading %s: %w", f, err)
		}
		errs, warns := countDiagnostics(m.Diagnostics())
		logger.Info("model loaded",
			"model", m.ID, "version", m.Version, "file", f,
			"decisions", len(m.Decisions()), "errors", errs, "warnings", warns)
		for _, d := range m.Diagnostics() {
			if d.Severity == diag.SeverityError {
				logger.Error("model diagnostic", "model", m.ID, "diagnostic", d.String())
			}
		}
	}
	return nil
}

// serverOptions projects the flags and configuration onto the server's options.
// `verdict mcp` ignores the HTTP fields; it shares this so the two commands
// cannot disagree about the MCP surface they expose.
func serverOptions(cmd *cli.Command, cfg config.Config, logger *slog.Logger) server.Options {
	return server.Options{
		Listen:       firstNonEmpty(cmd.String("listen"), cfg.Server.Listen),
		TwirpPath:    firstNonEmpty(cmd.String("twirp-path"), cfg.Server.TwirpPath),
		MCPEnabled:   mcpEnabled(cmd, cfg),
		MCPAllowLoad: cmd.Bool("mcp-allow-load"),
		ModelRoot:    cmd.String("model-root"),
		ReadTimeout:  cfg.Server.ReadTimeout.Duration(),
		WriteTimeout: cfg.Server.WriteTimeout.Duration(),
		Version:      version,
		Logger:       logger,
	}
}

func mcpEnabled(cmd *cli.Command, cfg config.Config) bool {
	if cmd.Bool("no-mcp") {
		return false
	}
	if cfg.Server.MCPEnabled != nil {
		return *cfg.Server.MCPEnabled
	}
	return true
}

// expandModelPaths turns files and directories into model files. Directories
// are scanned non-recursively, and the result is sorted so a restart loads the
// same models in the same order.
func expandModelPaths(paths []string) ([]string, error) {
	var out []string
	for _, p := range paths {
		info, err := os.Stat(p)
		if err != nil {
			return nil, fmt.Errorf("model path %s: %w", p, err)
		}
		if !info.IsDir() {
			out = append(out, p)
			continue
		}
		entries, err := os.ReadDir(p)
		if err != nil {
			return nil, err
		}
		for _, e := range entries {
			if e.IsDir() {
				continue
			}
			switch strings.ToLower(filepath.Ext(e.Name())) {
			case ".dmn", ".xml", ".json", ".vdj":
				out = append(out, filepath.Join(p, e.Name()))
			}
		}
	}
	sort.Strings(out)
	return out, nil
}

func countDiagnostics(ds []diag.Diagnostic) (errors, warnings int) {
	for _, d := range ds {
		switch d.Severity {
		case diag.SeverityError:
			errors++
		case diag.SeverityWarning:
			warnings++
		}
	}
	return errors, warnings
}

func firstNonEmpty(vs ...string) string {
	for _, v := range vs {
		if v != "" {
			return v
		}
	}
	return ""
}

// newLogger writes to w — always stderr, so that `verdict mcp` keeps stdout
// clean for the protocol and every other subcommand keeps it clean for JSON.
func newLogger(level string, w *os.File) *slog.Logger {
	var lvl slog.Level
	switch strings.ToLower(level) {
	case "debug":
		lvl = slog.LevelDebug
	case "warn":
		lvl = slog.LevelWarn
	case "error":
		lvl = slog.LevelError
	default:
		lvl = slog.LevelInfo
	}
	return slog.New(slog.NewTextHandler(w, &slog.HandlerOptions{Level: lvl}))
}
