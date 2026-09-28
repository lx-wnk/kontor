// server/internal/agentbroadcast/hook_enricher.go
package agentbroadcast

import (
	"context"

	sdk "github.com/lx-wnk/kontor/sdk"
	"github.com/lx-wnk/kontor/server/internal/hookstore"
	"github.com/lx-wnk/kontor/server/internal/merger"
)

// NewHookEventEnricher returns a merger.Enricher that annotates each agent with
// the recent lifecycle-hook events recorded for its session. It is the read side
// of the opt-in hook receiver: the same store the hooks API writes to is read
// here so per-event granularity surfaces on the matching agent.
//
// A nil store returns a nil enricher (composes away via merger.ChainEnrichers),
// and a session with no live events leaves RecentHookEvents unset — keeping SSE
// payloads byte-identical for clients without a hook installed.
func NewHookEventEnricher(store *hookstore.Store) merger.Enricher {
	if store == nil {
		return nil
	}
	return func(_ context.Context, agents []sdk.Agent) {
		for i := range agents {
			sid := agents[i].SessionID
			if sid == "" {
				continue
			}
			if events := store.Recent(sid); len(events) > 0 {
				agents[i].RecentHookEvents = events
			}
		}
	}
}
