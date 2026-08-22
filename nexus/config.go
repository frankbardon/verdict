package nexus

import (
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
	return c
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
