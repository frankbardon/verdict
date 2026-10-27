package nexus

import (
	"math"
	"time"
)

// parseConfig reads the plugin's YAML block. Nexus hands plugins a
// map[string]any, so every field is read defensively: an unparseable value
// falls back to its documented default rather than failing boot, because a
// decision plugin that refuses to start takes the whole agent down with it.
//
// The keys mirror the `verdict:` configuration block the library and the server
// read, so one vocabulary covers all three.
func parseConfig(raw map[string]any) pluginConfig {
	c := pluginConfig{
		Tracing:         "full",
		SessionStrategy: PerDecision,
		Channel:         "verdict",
		PublishTrace:    true,
	}
	if raw == nil {
		return c
	}

	c.Paths = stringSlice(raw["models"])
	if len(c.Paths) == 0 {
		c.Paths = stringSlice(raw["paths"])
	}
	if v, ok := raw["strict_mode"].(bool); ok {
		c.Strict = v
	}
	if v, ok := stringOf(raw["tracing"]); ok {
		c.Tracing = v
	}
	c.Redact = stringSlice(raw["redact_inputs"])
	if v, ok := raw["publish_trace"].(bool); ok {
		c.PublishTrace = v
	}
	if v, ok := stringOf(raw["session_strategy"]); ok {
		switch SessionStrategy(v) {
		case PerDecision, PerEvaluation, Shared:
			c.SessionStrategy = SessionStrategy(v)
		}
	}
	if v, ok := stringOf(raw["channel"]); ok {
		c.Channel = v
	}
	if v, ok := stringOf(raw["role"]); ok {
		c.Role = v
	}
	if v, ok := stringOf(raw["model"]); ok {
		c.Model = v
	}
	if v, ok := stringOf(raw["posture"]); ok {
		c.Posture = v
	}
	if v, ok := stringOf(raw["default_max_latency"]); ok {
		if d, err := time.ParseDuration(v); err == nil {
			c.DefaultMaxLatency = d
		}
	}
	// A non-positive or non-integral cap keeps the engine default rather than
	// failing boot; the engine never runs without one.
	if v, ok := positiveInt(raw["max_iteration_range"]); ok {
		c.MaxIterationRange = v
	}
	return c
}

// positiveInt reads a positive whole number, whichever numeric type the YAML
// decoder chose for it.
func positiveInt(v any) (int64, bool) {
	var n int64
	switch x := v.(type) {
	case int:
		n = int64(x)
	case int64:
		n = x
	case uint64:
		if x > math.MaxInt64 {
			return 0, false
		}
		n = int64(x)
	case float64:
		if x != math.Trunc(x) || x >= math.MaxInt64 {
			return 0, false
		}
		n = int64(x)
	default:
		return 0, false
	}
	return n, n > 0
}

func stringOf(v any) (string, bool) {
	s, ok := v.(string)
	if !ok || s == "" {
		return "", false
	}
	return s, true
}

func stringSlice(v any) []string {
	switch x := v.(type) {
	case nil:
		return nil
	case string:
		if x == "" {
			return nil
		}
		return []string{x}
	case []string:
		return x
	case []any:
		out := make([]string, 0, len(x))
		for _, e := range x {
			if s, ok := e.(string); ok && s != "" {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}
