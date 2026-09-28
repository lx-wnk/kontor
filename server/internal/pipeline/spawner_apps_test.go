package pipeline

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/lx-wnk/kontor/server/internal/db/ent"
	"github.com/lx-wnk/kontor/server/internal/mcpapps"
)

func TestSpawnToolLists_RendersApplicationDecisions(t *testing.T) {
	opts := SpawnAgentOptions{
		Task: &ent.Task{Autonomy: "spec_gated"},
		Applications: mcpapps.RunApplications{
			Allow:          []string{"mcp__mail__search"},
			Deny:           []string{"mcp__mail__send"},
			CatalogueTools: map[string]bool{"mcp__mail__search": true, "mcp__mail__send": true, "mcp__mail__draft": true},
		},
	}
	allow, deny := spawnToolLists(opts, false)
	require.Contains(t, allow, "mcp__mail__search")
	require.Contains(t, deny, "mcp__mail__send", "a deny grant must reach --disallowedTools")
	require.NotContains(t, allow, "mcp__mail__draft", "allow-all autonomy does not allow application tools")
	require.NotContains(t, allow, "mcp__mail__send")
}

func TestBuildAllowList_TaskPermissionForACatalogueToolSurvives(t *testing.T) {
	granted := &ent.TaskPermission{ID: "p1", Tool: "mcp__mail__search", Granted: true}
	unknown := &ent.TaskPermission{ID: "p2", Tool: "mcp__other__thing", Granted: true}
	allow := BuildAllowList("manual", []*ent.TaskPermission{granted, unknown}, false, false,
		map[string]bool{"mcp__mail__search": true})
	require.Contains(t, allow, "mcp__mail__search")
	require.NotContains(t, allow, "mcp__other__thing", "a tool outside any catalogue stays ungrantable")
}
