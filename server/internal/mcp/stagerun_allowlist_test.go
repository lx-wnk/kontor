package mcp_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/lx-wnk/kontor/server/internal/mcp"
)

const toolPrefix = "mcp__" + mcp.ServerName + "__"

// TestStageRunAllowedTools_MatchesTheScopeSet is the anti-drift property: the
// allow list is the projection of StageRunScopes through ToolScopeMap, so
// adding or removing a scope moves the list with it and no hand-written entry
// can survive a scope it no longer belongs to.
func TestStageRunAllowedTools_MatchesTheScopeSet(t *testing.T) {
	granted := mcp.ResolveScopes(mcp.StageRunScopes)

	got := make(map[string]bool)
	for _, entry := range mcp.StageRunAllowedTools() {
		require.True(t, strings.HasPrefix(entry, toolPrefix),
			"every entry must name the dashboard-tasks server, got %q", entry)
		got[strings.TrimPrefix(entry, toolPrefix)] = true
	}

	for tool, scope := range mcp.ToolScopeMap {
		require.Equal(t, granted[scope], got[tool],
			"tool %q is unlocked by scope %q — presence in the allow list must follow the scope set", tool, scope)
	}
}

// TestStageRunAllowedTools_CoversEveryScopeTheKeyCarries fails if a scope in
// StageRunScopes contributes no callable tool: the key would then carry a
// scope the agent can never use, which means the two lists have drifted.
func TestStageRunAllowedTools_CoversEveryScopeTheKeyCarries(t *testing.T) {
	covered := make(map[string]bool)
	for _, entry := range mcp.StageRunAllowedTools() {
		covered[mcp.ToolScopeMap[strings.TrimPrefix(entry, toolPrefix)]] = true
	}
	for _, scope := range mcp.StageRunScopes {
		require.True(t, covered[scope],
			"scope %q is on the key but no allow entry renders a tool for it", scope)
	}
}

// TestStageRunAllowedTools_OmitsTheEscalationScopes names the two scopes
// StageRunScopes deliberately withholds. With a pipeline:control entry an
// agent approves its own spec and resolves its own permission requests; with a
// tasks:write entry it widens its own permissions through manage_task. An
// allow entry for either hands that capability straight back.
func TestStageRunAllowedTools_OmitsTheEscalationScopes(t *testing.T) {
	allow := mcp.StageRunAllowedTools()
	for _, tool := range []string{
		"approve_spec", "grant_permission", "resolve_permission_request", "advance_task",
		"manage_task", "create_task", "delete_task", "update_task",
		"create_api_key", "revoke_api_key",
	} {
		require.NotContains(t, allow, toolPrefix+tool,
			"%q is gated by scope %q, which a stage-run key must never carry", tool, mcp.ToolScopeMap[tool])
	}
}
