package tools

import (
	"testing"

	mcp "github.com/lx-wnk/kontor/server/internal/mcp"
	"github.com/stretchr/testify/require"
)

func TestRegisterReadTools_AllToolsPresent(t *testing.T) {
	registry := mcp.ToolRegistry{}
	RegisterReadTools(registry, ReadDeps{})
	for _, name := range []string{
		"list_tasks",
		"get_task",
		"list_stage_runs",
		"list_audit",
		"list_permission_requests",
	} {
		require.Contains(t, registry, name, "tool %s should be registered", name)
	}
}
