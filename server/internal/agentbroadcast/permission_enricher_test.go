package agentbroadcast_test

import (
	"context"
	"testing"

	sdk "github.com/lx-wnk/kontor/sdk"
	"github.com/lx-wnk/kontor/server/internal/agentbroadcast"
)

type fakeBridge struct {
	pending      map[string][]sdk.PendingPermission
	atTerminal   map[string]bool
	terminalTool map[string]string
	armed        map[string]bool
	swept        int
}

func (f *fakeBridge) StateForSession(sessionID string) ([]sdk.PendingPermission, bool, string, bool) {
	return f.pending[sessionID], f.atTerminal[sessionID], f.terminalTool[sessionID], f.armed[sessionID]
}

func (f *fakeBridge) SweepExpired() { f.swept++ }

func strPtr(s string) *string { return &s }

func TestPermissionBridgeEnricherAttachesHeldRequests(t *testing.T) {
	bridge := &fakeBridge{
		pending: map[string][]sdk.PendingPermission{
			"s1": {{ID: "r1", Tool: "Bash", Pattern: strPtr("npm publish")}},
		},
	}
	agents := []sdk.Agent{{SessionID: "s1"}, {SessionID: "s2"}}

	agentbroadcast.NewPermissionBridgeEnricher(bridge)(context.Background(), agents)

	if len(agents[0].HeldPermissions) != 1 || agents[0].HeldPermissions[0].ID != "r1" {
		t.Fatalf("agent s1 pending = %+v, want the held request", agents[0].HeldPermissions)
	}
	if len(agents[1].HeldPermissions) != 0 {
		t.Fatalf("agent s2 picked up %d requests belonging to another session", len(agents[1].HeldPermissions))
	}
}

// A hook hold must never be merged into PendingPermissions: those are
// database-backed pipeline rows that resolve through a different endpoint, and
// sharing the slice let a hook id ride along in the pipeline's bulk-resolve
// payload.
func TestPermissionBridgeEnricherKeepsTheTwoOriginsApart(t *testing.T) {
	bridge := &fakeBridge{
		pending: map[string][]sdk.PendingPermission{
			"s1": {{ID: "hook-1", Tool: "Bash"}},
		},
	}
	agents := []sdk.Agent{{
		SessionID:          "s1",
		PendingPermissions: []sdk.PendingPermission{{ID: "pipeline-1", Tool: "Edit"}},
	}}

	agentbroadcast.NewPermissionBridgeEnricher(bridge)(context.Background(), agents)

	if len(agents[0].PendingPermissions) != 1 || agents[0].PendingPermissions[0].ID != "pipeline-1" {
		t.Fatalf("the pipeline list was touched: %+v", agents[0].PendingPermissions)
	}
	if len(agents[0].HeldPermissions) != 1 || agents[0].HeldPermissions[0].ID != "hook-1" {
		t.Fatalf("held = %+v, want exactly the hook request", agents[0].HeldPermissions)
	}
}

// Expiry is driven from the tick rather than from a read, so a session nobody
// polls still gets its notice cleaned up.
func TestPermissionBridgeEnricherSweepsOncePerTick(t *testing.T) {
	bridge := &fakeBridge{}
	agents := []sdk.Agent{{SessionID: "s1"}, {SessionID: "s2"}}

	enrich := agentbroadcast.NewPermissionBridgeEnricher(bridge)
	enrich(context.Background(), agents)
	enrich(context.Background(), agents)

	if bridge.swept != 2 {
		t.Fatalf("swept %d times for 2 ticks, want 2", bridge.swept)
	}
}

func TestPermissionBridgeEnricherReportsArmedState(t *testing.T) {
	bridge := &fakeBridge{armed: map[string]bool{"s1": true}}
	agents := []sdk.Agent{{SessionID: "s1"}, {SessionID: "s2"}}

	agentbroadcast.NewPermissionBridgeEnricher(bridge)(context.Background(), agents)

	if !agents[0].PermissionBridgeArmed {
		t.Fatal("s1 is armed but was not reported as such")
	}
	if agents[1].PermissionBridgeArmed {
		t.Fatal("s2 was reported armed without being armed")
	}
}

func TestPermissionBridgeEnricherFlagsTheTerminalPrompt(t *testing.T) {
	bridge := &fakeBridge{atTerminal: map[string]bool{"s1": true}}
	agents := []sdk.Agent{{SessionID: "s1"}, {SessionID: "s2"}}

	agentbroadcast.NewPermissionBridgeEnricher(bridge)(context.Background(), agents)

	if !agents[0].AwaitingTerminalPermission {
		t.Fatal("s1 is waiting at its terminal but was not flagged")
	}
	if agents[1].AwaitingTerminalPermission {
		t.Fatal("s2 was flagged without a notice of its own")
	}
}

// The no-hooks path must compose away rather than annotate nothing at cost.
func TestPermissionBridgeEnricherIsNilWithoutABridge(t *testing.T) {
	if agentbroadcast.NewPermissionBridgeEnricher(nil) != nil {
		t.Fatal("a nil bridge produced a non-nil enricher")
	}
}

// An agent with no session id has nothing to match on and must be left alone.
func TestPermissionBridgeEnricherSkipsSessionlessAgents(t *testing.T) {
	bridge := &fakeBridge{
		pending:    map[string][]sdk.PendingPermission{"": {{ID: "r1"}}},
		atTerminal: map[string]bool{"": true},
	}
	agents := []sdk.Agent{{SessionID: ""}}

	agentbroadcast.NewPermissionBridgeEnricher(bridge)(context.Background(), agents)

	if len(agents[0].HeldPermissions) != 0 || agents[0].AwaitingTerminalPermission {
		t.Fatalf("a sessionless agent was annotated: %+v", agents[0])
	}
}

// The client may only offer a standing grant for the call the prompt is about,
// so the tool id the bridge correlated has to reach the agent.
func TestPermissionBridgeEnricherCarriesTheTerminalToolUse(t *testing.T) {
	bridge := &fakeBridge{
		atTerminal:   map[string]bool{"s1": true},
		terminalTool: map[string]string{"s1": "toolu_9"},
	}
	agents := []sdk.Agent{{SessionID: "s1"}, {SessionID: "s2"}}

	agentbroadcast.NewPermissionBridgeEnricher(bridge)(context.Background(), agents)

	if agents[0].TerminalPermissionToolUseID != "toolu_9" {
		t.Fatalf("terminalPermissionToolUseId = %q, want toolu_9", agents[0].TerminalPermissionToolUseID)
	}
	if agents[1].TerminalPermissionToolUseID != "" {
		t.Fatalf("agent s2 picked up %q from another session", agents[1].TerminalPermissionToolUseID)
	}
}
