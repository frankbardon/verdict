// Package skill embeds Verdict's agent skill pack, so the guidance an agent is
// given always matches the version of the library it is talking to.
//
// A skill pack that lives in a separate repository drifts: the engine gains a
// hit policy, the guidance keeps describing the old set, and an agent confidently
// writes a model the engine rejects. Shipping it in the module makes the two
// impossible to version apart.
package skill

import _ "embed"

//go:embed SKILL.md
var markdown string

// Markdown returns the skill pack.
func Markdown() string { return markdown }

// Name is the skill's identifier.
const Name = "verdict"
