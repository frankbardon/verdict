// Package config loads Verdict's YAML configuration and turns it into engine
// options.
//
// Every behaviour DMN specifies a default for uses that default. Every
// behaviour outside the spec has a documented Verdict default here, and can be
// overridden. The zero value of Config is the documented default configuration,
// so a program that never reads a file gets the same behaviour as one that
// reads an empty one.
package config

import (
	"fmt"
	"os"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/frankbardon/verdict/pkg/trace"
	"github.com/frankbardon/verdict/pkg/verdict"
)

// File is the top-level YAML document. Configuration is nested under a
// `verdict:` key so a Verdict block can live inside a host application's own
// configuration file without collision.
type File struct {
	Verdict Config `yaml:"verdict"`
}

// Config is the Verdict configuration block.
type Config struct {
	FEEL       FEEL       `yaml:"feel"`
	Evaluation Evaluation `yaml:"evaluation"`
	Tracing    Tracing    `yaml:"tracing"`
	Agent      Agent      `yaml:"agent"`
	Models     Models     `yaml:"models"`
	Server     Server     `yaml:"server"`
}

// FEEL configures the expression language.
type FEEL struct {
	// Dialect is "feel" (Conformance Level 3, the default) or "s-feel"
	// (Conformance Level 2). A model that declares its own conformance level
	// overrides this for itself.
	Dialect string `yaml:"dialect"`
	// Timezone is the location `now()`, `today()` and date arithmetic resolve
	// against. Defaults to UTC, because a decision that means something
	// different depending on where it ran is not auditable.
	Timezone string `yaml:"timezone"`
}

// Evaluation configures the evaluator.
type Evaluation struct {
	// StrictMode refuses to load a model whose static analysis reports an
	// error: an ambiguous hit policy, an uncovered gap under a non-defaulted
	// policy, an expression that does not compile.
	StrictMode bool `yaml:"strict_mode"`
	// DefaultMaxLatency bounds an agent decision that declares no policy of its
	// own. Defaults to 30s; zero in the file means unbounded.
	DefaultMaxLatency Duration `yaml:"default_max_latency"`
	// ParallelIndependentDecisions evaluates independent decisions in the same
	// dependency layer concurrently. Defaults to true.
	ParallelIndependentDecisions *bool `yaml:"parallel_independent_decisions"`
	// Memoize caches referentially transparent nodes within one evaluation.
	// Agent decisions are never memoised.
	Memoize bool `yaml:"memoize"`
	// MaxDepth bounds recursion through decision services. Defaults to 32.
	MaxDepth int `yaml:"max_depth"`
}

// Tracing configures the execution record.
type Tracing struct {
	// Mode is "off", "summary" or "full". Defaults to full: the trace is the
	// deliverable, so recording it is the default rather than an opt-in.
	Mode string `yaml:"mode"`
	// RedactInputs names bindings whose values are replaced with a marker in
	// the trace.
	RedactInputs []string `yaml:"redact_inputs"`
}

// Agent configures agentDecision evaluation.
type Agent struct {
	// Bridge names the bridge to use: "nexus", "http", "mock", or a name
	// registered by the host application. An empty value means the host wires
	// the bridge in code, which is the library-first path.
	Bridge string `yaml:"bridge"`
	// HTTP configures the generic HTTP bridge.
	HTTP AgentHTTP `yaml:"http"`
	// Nexus configures the Nexus-backed bridge. The Verdict core never imports
	// Nexus; these settings are read by the separate verdict/nexus module.
	Nexus AgentNexus `yaml:"nexus"`
}

// AgentHTTP configures the generic HTTP/JSON bridge.
type AgentHTTP struct {
	Endpoint string            `yaml:"endpoint"`
	Headers  map[string]string `yaml:"headers"`
	Timeout  Duration          `yaml:"timeout"`
}

// AgentNexus configures the Nexus-backed bridge.
type AgentNexus struct {
	// SessionStrategy is "per-decision" (the default), "per-evaluation" or
	// "shared". Per-decision gives each agent node its own isolated session;
	// per-evaluation shares one session across an evaluation, trading isolation
	// for shared context; shared reuses a single long-lived session.
	SessionStrategy string `yaml:"session_strategy"`
	// SessionRetention is how long a session's workspace is kept for forensic
	// replay. Empty means the host's own retention policy applies.
	SessionRetention Duration `yaml:"session_retention"`
	// Channel is the event-bus channel prefix Verdict publishes trace events on.
	Channel string `yaml:"channel"`
}

// Models configures model loading.
type Models struct {
	// Paths are files or directories to load at startup. Directories are
	// scanned non-recursively for *.dmn, *.xml, *.json and *.vdj.
	//
	// There is deliberately no hot-reload option. Loading is content-addressed
	// and idempotent, so a host that wants reloading can watch files and call
	// LoadModel itself — and a decision model changing under a running service
	// is a deploy event that should go through whatever gate deploys go
	// through, not a convenience the engine grants silently.
	Paths []string `yaml:"paths"`
}

// Server configures `verdict serve`. It is ignored by the library.
type Server struct {
	Listen     string `yaml:"listen"`
	TwirpPath  string `yaml:"twirp_path"`
	MCPEnabled *bool  `yaml:"mcp_enabled"`
	// ReadTimeout and WriteTimeout bound a single HTTP request.
	ReadTimeout  Duration `yaml:"read_timeout"`
	WriteTimeout Duration `yaml:"write_timeout"`
}

// Duration is a time.Duration that unmarshals from a Go duration string
// ("30s", "5m") or from a plain number of seconds.
type Duration time.Duration

// UnmarshalYAML implements yaml.Unmarshaler.
//
// The node's YAML tag, not a trial decode, decides how to read it: a scalar
// `45` decodes into a Go string just as happily as into a float, so trying
// string first would reject every numeric duration.
func (d *Duration) UnmarshalYAML(node *yaml.Node) error {
	switch node.Tag {
	case "!!int", "!!float":
		var secs float64
		if err := node.Decode(&secs); err != nil {
			return err
		}
		*d = Duration(time.Duration(secs * float64(time.Second)))
		return nil
	case "!!null":
		return nil
	}
	var s string
	if err := node.Decode(&s); err != nil {
		return fmt.Errorf("a duration must be a string like \"30s\" or a number of seconds")
	}
	if strings.TrimSpace(s) == "" {
		return nil
	}
	parsed, err := time.ParseDuration(s)
	if err != nil {
		return fmt.Errorf("%q is not a duration: %w", s, err)
	}
	*d = Duration(parsed)
	return nil
}

// MarshalYAML implements yaml.Marshaler.
func (d Duration) MarshalYAML() (any, error) { return time.Duration(d).String(), nil }

// Duration returns the value as a time.Duration.
func (d Duration) Duration() time.Duration { return time.Duration(d) }

// Default returns the documented default configuration.
func Default() Config {
	parallel := true
	mcp := true
	return Config{
		FEEL: FEEL{Dialect: "feel", Timezone: "UTC"},
		Evaluation: Evaluation{
			DefaultMaxLatency:            Duration(30 * time.Second),
			ParallelIndependentDecisions: &parallel,
			MaxDepth:                     32,
		},
		Tracing: Tracing{Mode: "full"},
		Agent:   Agent{Nexus: AgentNexus{SessionStrategy: "per-decision", Channel: "verdict"}},
		Server: Server{
			Listen:       ":7430",
			TwirpPath:    "/twirp",
			MCPEnabled:   &mcp,
			ReadTimeout:  Duration(30 * time.Second),
			WriteTimeout: Duration(60 * time.Second),
		},
	}
}

// Load reads a YAML file and merges it over the defaults.
func Load(path string) (Config, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return Config{}, fmt.Errorf("config: reading %s: %w", path, err)
	}
	return Parse(raw)
}

// Parse merges a YAML document over the defaults.
//
// A document may either nest its settings under a `verdict:` key or supply them
// at the top level. Both are accepted because a standalone server config file
// has no reason to nest, while a block embedded in a host application's config
// does.
func Parse(raw []byte) (Config, error) {
	cfg := Default()
	var file File
	if err := yaml.Unmarshal(raw, &file); err != nil {
		return Config{}, fmt.Errorf("config: %w", err)
	}
	if !isZero(file.Verdict) {
		merge(&cfg, file.Verdict)
		return cfg, cfg.Validate()
	}
	var flat Config
	if err := yaml.Unmarshal(raw, &flat); err != nil {
		return Config{}, fmt.Errorf("config: %w", err)
	}
	merge(&cfg, flat)
	return cfg, cfg.Validate()
}

// Validate reports configuration that cannot be honoured.
func Validate(c Config) error { return c.Validate() }

// Validate reports configuration that cannot be honoured.
func (c Config) Validate() error {
	switch strings.ToLower(c.FEEL.Dialect) {
	case "feel", "s-feel", "":
	default:
		return fmt.Errorf("config: feel.dialect must be \"feel\" or \"s-feel\", got %q", c.FEEL.Dialect)
	}
	if _, ok := trace.ParseMode(c.Tracing.Mode); !ok {
		return fmt.Errorf("config: tracing.mode must be off, summary or full, got %q", c.Tracing.Mode)
	}
	if c.FEEL.Timezone != "" {
		if _, err := time.LoadLocation(c.FEEL.Timezone); err != nil {
			return fmt.Errorf("config: feel.timezone %q is not a known location: %w", c.FEEL.Timezone, err)
		}
	}
	switch c.Agent.Nexus.SessionStrategy {
	case "", "per-decision", "per-evaluation", "shared":
	default:
		return fmt.Errorf(
			"config: agent.nexus.session_strategy must be per-decision, per-evaluation or shared, got %q",
			c.Agent.Nexus.SessionStrategy)
	}
	if c.Evaluation.DefaultMaxLatency < 0 {
		return fmt.Errorf("config: evaluation.default_max_latency cannot be negative")
	}
	return nil
}

// Options turns a configuration into engine options. The agent bridge is not
// among them: bridges are constructed by the host, because the library must not
// depend on any particular one.
func (c Config) Options() []verdict.Option {
	opts := []verdict.Option{
		verdict.WithStrictMode(c.Evaluation.StrictMode),
		verdict.WithDefaultMaxLatency(c.Evaluation.DefaultMaxLatency.Duration()),
		verdict.WithMemoization(c.Evaluation.Memoize),
	}
	if strings.EqualFold(c.FEEL.Dialect, "s-feel") {
		opts = append(opts, verdict.WithFEELDialect(verdict.DialectSFEEL))
	}
	if mode, ok := trace.ParseMode(c.Tracing.Mode); ok {
		opts = append(opts, verdict.WithTracing(mode))
	}
	if len(c.Tracing.RedactInputs) > 0 {
		opts = append(opts, verdict.WithRedactedInputs(c.Tracing.RedactInputs...))
	}
	if c.Evaluation.ParallelIndependentDecisions != nil {
		opts = append(opts, verdict.WithParallelDecisions(*c.Evaluation.ParallelIndependentDecisions))
	}
	if c.Evaluation.MaxDepth > 0 {
		opts = append(opts, verdict.WithMaxDepth(c.Evaluation.MaxDepth))
	}
	if c.FEEL.Timezone != "" {
		if loc, err := time.LoadLocation(c.FEEL.Timezone); err == nil {
			opts = append(opts, verdict.WithClock(func() time.Time { return time.Now().In(loc) }))
		}
	}
	return opts
}
