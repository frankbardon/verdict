package config

import (
	"testing"
	"time"
)

func TestDefaultsSurviveAPartialFile(t *testing.T) {
	cfg, err := Parse([]byte(`
verdict:
  feel:
    dialect: s-feel
  tracing:
    mode: summary
    redact_inputs: ["Applicant.ssn"]
`))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if cfg.FEEL.Dialect != "s-feel" {
		t.Errorf("dialect = %q, want s-feel", cfg.FEEL.Dialect)
	}
	if cfg.Tracing.Mode != "summary" {
		t.Errorf("tracing mode = %q, want summary", cfg.Tracing.Mode)
	}
	// Everything the file did not mention keeps its documented default.
	if cfg.FEEL.Timezone != "UTC" {
		t.Errorf("timezone = %q, want the UTC default", cfg.FEEL.Timezone)
	}
	if got := cfg.Evaluation.DefaultMaxLatency.Duration(); got != 30*time.Second {
		t.Errorf("default max latency = %v, want the 30s default", got)
	}
	if cfg.Evaluation.ParallelIndependentDecisions == nil || !*cfg.Evaluation.ParallelIndependentDecisions {
		t.Error("parallel evaluation lost its default of true")
	}
	if cfg.Server.Listen != ":7430" {
		t.Errorf("listen = %q, want the :7430 default", cfg.Server.Listen)
	}
}

func TestExplicitFalseIsHonoured(t *testing.T) {
	cfg, err := Parse([]byte(`
verdict:
  evaluation:
    parallel_independent_decisions: false
  server:
    mcp_enabled: false
`))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if cfg.Evaluation.ParallelIndependentDecisions == nil || *cfg.Evaluation.ParallelIndependentDecisions {
		t.Error("an explicit `false` was overwritten by the default `true`")
	}
	if cfg.Server.MCPEnabled == nil || *cfg.Server.MCPEnabled {
		t.Error("an explicit mcp_enabled: false was overwritten")
	}
}

func TestUnnestedDocumentIsAccepted(t *testing.T) {
	cfg, err := Parse([]byte("feel:\n  dialect: s-feel\n"))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if cfg.FEEL.Dialect != "s-feel" {
		t.Errorf("dialect = %q, want s-feel from an unnested document", cfg.FEEL.Dialect)
	}
}

func TestDurationAcceptsStringsAndSeconds(t *testing.T) {
	cfg, err := Parse([]byte("verdict:\n  evaluation:\n    default_max_latency: 90s\n"))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if got := cfg.Evaluation.DefaultMaxLatency.Duration(); got != 90*time.Second {
		t.Errorf("duration from string = %v, want 90s", got)
	}
	cfg, err = Parse([]byte("verdict:\n  evaluation:\n    default_max_latency: 45\n"))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if got := cfg.Evaluation.DefaultMaxLatency.Duration(); got != 45*time.Second {
		t.Errorf("duration from number = %v, want 45s", got)
	}
}

func TestValidationRejectsNonsense(t *testing.T) {
	bad := []string{
		"verdict:\n  feel:\n    dialect: klingon\n",
		"verdict:\n  tracing:\n    mode: verbose\n",
		"verdict:\n  feel:\n    timezone: Mars/Olympus\n",
		"verdict:\n  agent:\n    nexus:\n      session_strategy: telepathic\n",
	}
	for _, src := range bad {
		if _, err := Parse([]byte(src)); err == nil {
			t.Errorf("Parse accepted invalid configuration:\n%s", src)
		}
	}
}

func TestOptionsReflectTheConfiguration(t *testing.T) {
	cfg, err := Parse([]byte(`
verdict:
  feel:
    dialect: s-feel
  evaluation:
    strict_mode: true
`))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if got := len(cfg.Options()); got == 0 {
		t.Fatal("Options produced nothing")
	}
}
