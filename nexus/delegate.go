package nexus

import (
	"context"
	"fmt"

	"github.com/frankbardon/nexus/pkg/delegate"
)

// DelegateRunner answers a decision by running a full Nexus sub-agent session:
// a posture, a workspace, tool access, a budget, and a journal.
//
// This is what "a Nexus session as evaluator" actually buys over a bare model
// call. The same agentDecision node, the same declared output type and the same
// failure policy, but the thing on the other end can read files, call tools and
// take several turns before it answers. Swapping LLMRunner for this one is a
// wiring change, not a model change — which is the point of keeping the bridge
// thin.
type DelegateRunner struct {
	// Runtime executes the sub-agent. Required.
	Runtime *delegate.Runtime
	// Posture names the registered AgentPosture the sub-agent adopts. Required:
	// a posture is where the sub-agent's tools, prompt and budget are declared,
	// and a decision that runs without one is unbounded.
	Posture string
	// ParentDepth seeds the recursion depth so a decision graph reached from a
	// sub-agent cannot spawn an unbounded tower of sessions.
	ParentDepth int
	// Overrides tighten the posture's default budget for decision work
	// specifically. A classification does not need the token budget a research
	// task does.
	Overrides delegate.Overrides
}

// Run implements Runner.
func (r *DelegateRunner) Run(ctx context.Context, call Call) (Answer, error) {
	if r.Runtime == nil || r.Posture == "" {
		return Answer{}, fmt.Errorf(
			"verdict/nexus: DelegateRunner needs both a Runtime and a Posture")
	}

	out, err := r.Runtime.Run(ctx, delegate.Input{
		Posture: r.Posture,
		Task:    call.Prompt,
		Context: map[string]any{
			"decision_id":   call.DecisionID,
			"decision_name": call.DecisionName,
			"inputs":        call.Inputs,
			"answer_shape":  describeType(call.OutputType),
			"attempt":       call.Attempt,
		},
		ParentDepth: r.ParentDepth,
		Overrides:   r.Overrides,
	})
	if err != nil {
		return Answer{}, fmt.Errorf("verdict/nexus: decision %q: %w", call.DecisionID, err)
	}

	// A partial result is a real answer that ran out of budget. Verdict's own
	// type check decides whether it is usable; refusing it here would discard
	// work the failure policy might well have accepted.
	switch out.Status {
	case delegate.StatusSuccess, delegate.StatusCacheHit, delegate.StatusPartial:
	default:
		reason := out.Error
		if reason == "" {
			reason = string(out.Status)
		}
		return Answer{}, fmt.Errorf("verdict/nexus: decision %q sub-agent %s: %s",
			call.DecisionID, out.Status, reason)
	}

	return Answer{
		Text:          out.Result,
		SubSessionRef: "nexus://sessions/" + out.SubSessionID,
		InputTokens:   0,
		OutputTokens:  out.TokensUsed,
	}, nil
}
