package nexus

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/frankbardon/verdict/pkg/dmn/model"
)

// parseAnswer turns a session's raw response text into the value the declared
// output type asks for.
//
// The layering here is deliberate. Structured output is requested from the
// provider, so the common case is a JSON object with a `value` key. But a model
// asked for a risk tier frequently answers `low` — correct, useful, and not
// JSON. Accepting that is not laxity: the engine still type-checks and validates
// whatever comes out of here, so the only thing this function decides is
// *representation*. It never guesses at meaning; an answer it cannot read
// unambiguously is an error, and the node's retry and failure policy take over.
func parseAnswer(text string, t model.TypeSpec) (any, error) {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return nil, fmt.Errorf("the session returned an empty answer")
	}
	trimmed = stripCodeFence(trimmed)

	// 1. The structured-output envelope: {"value": ...}.
	var envelope struct {
		Value json.RawMessage `json:"value"`
	}
	if err := json.Unmarshal([]byte(trimmed), &envelope); err == nil && len(envelope.Value) > 0 {
		var v any
		if err := json.Unmarshal(envelope.Value, &v); err == nil {
			return v, nil
		}
	}

	// 2. A bare JSON value of the right shape. Only trusted when the declared
	//    type is structural: a bare string answer parses as JSON too, and
	//    treating `"low"` as JSON rather than as text would be the same result
	//    by a longer route.
	if t.Collection || len(t.Components) > 0 {
		var v any
		if err := json.Unmarshal([]byte(trimmed), &v); err == nil {
			return v, nil
		}
		return nil, fmt.Errorf(
			"the session answered with text where %s was required: %s",
			describeType(t), truncate(trimmed, 200))
	}

	// 3. A bare scalar. Strip the quotes a model sometimes wraps its answer in,
	//    and hand the rest to the engine's own coercion, which owns the
	//    type-specific rules.
	return strings.Trim(trimmed, "\"'` \n\t"), nil
}

// stripCodeFence removes a Markdown fence a model wrapped its JSON in.
func stripCodeFence(s string) string {
	if !strings.HasPrefix(s, "```") {
		return s
	}
	// Drop the opening fence and its optional language tag.
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[i+1:]
	} else {
		return s
	}
	if i := strings.LastIndex(s, "```"); i >= 0 {
		s = s[:i]
	}
	return strings.TrimSpace(s)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
