package agentbroadcast

import (
	"context"
	"testing"

	sdk "github.com/lx-wnk/kontor/sdk"
	"github.com/lx-wnk/kontor/server/internal/hookstore"
)

func TestHookEventEnricher_SetsEventsForMatchingSession(t *testing.T) {
	store := hookstore.New(10, 0)
	store.Record("sess-1", sdk.HookEvent{Type: "PostToolUse", Tool: "Read"})

	enrich := NewHookEventEnricher(store)
	agents := []sdk.Agent{{SessionID: "sess-1"}, {SessionID: "sess-2"}}
	enrich(context.Background(), agents)

	if len(agents[0].RecentHookEvents) != 1 || agents[0].RecentHookEvents[0].Tool != "Read" {
		t.Errorf("sess-1 events = %+v, want one Read event", agents[0].RecentHookEvents)
	}
	if agents[1].RecentHookEvents != nil {
		t.Errorf("sess-2 (no events) should stay nil, got %+v", agents[1].RecentHookEvents)
	}
}

func TestNewHookEventEnricher_NilStoreReturnsNil(t *testing.T) {
	if NewHookEventEnricher(nil) != nil {
		t.Error("NewHookEventEnricher(nil) should return nil so it composes away")
	}
}
