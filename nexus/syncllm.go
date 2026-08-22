package nexus

import (
	"context"

	"github.com/frankbardon/nexus/pkg/delegate"
	nexusengine "github.com/frankbardon/nexus/pkg/engine"
	"github.com/frankbardon/nexus/pkg/events"
)

// syncLLM puts a request to the engine's LLM plugin and waits for the matching
// response.
//
// Nexus already owns this pattern — request/response correlation, the
// before:llm.request veto, the no-provider-wired diagnostic — so the bridge
// borrows it rather than reimplementing the correlation logic and drifting from
// it.
func syncLLM(ctx context.Context, bus nexusengine.EventBus, req events.LLMRequest) (events.LLMResponse, error) {
	return delegate.SyncLLM(ctx, bus, req)
}
