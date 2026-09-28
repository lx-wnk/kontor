package pipeline_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/lx-wnk/kontor/server/internal/db/ent"
	"github.com/lx-wnk/kontor/server/internal/mcp"
	"github.com/lx-wnk/kontor/server/internal/pipeline"
)

// spawnSettingsAllow runs a real SpawnStageAgent against a no-op script and
// returns permissions.allow from the settings file the spawn wrote. Going
// through the real spawn is the point: a derivation that is correct but never
// reaches the settings file leaves the agent asking for permission anyway.
func spawnSettingsAllow(t *testing.T, opts pipeline.SpawnAgentOptions) []string {
	t.Helper()
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())

	script := filepath.Join(t.TempDir(), "noop.sh")
	require.NoError(t, os.WriteFile(script, []byte("#!/bin/sh\nexit 0\n"), 0o700))

	opts.Task = &ent.Task{ID: "t-allow", Cwd: t.TempDir(), Autonomy: "manual"}
	opts.StageRun = &ent.StageRun{ID: "r-allow"}
	opts.EnableChannel = true
	opts.Spawner = &ent.Spawner{Command: script}

	result, err := pipeline.SpawnStageAgent(opts)
	require.NoError(t, err)
	t.Cleanup(result.Cleanup)
	if p, perr := os.FindProcess(result.PID); perr == nil {
		_, _ = p.Wait()
	}

	require.NotEmpty(t, result.SettingsPath, "the spawn must have written a settings file")
	data, err := os.ReadFile(result.SettingsPath)
	require.NoError(t, err)
	var parsed struct {
		Permissions struct {
			Allow []string `json:"allow"`
		} `json:"permissions"`
	}
	require.NoError(t, json.Unmarshal(data, &parsed))
	return parsed.Permissions.Allow
}

func TestSpawnStageAgent_PreApprovesTheToolsTheStageRunKeyUnlocks(t *testing.T) {
	allow := spawnSettingsAllow(t, pipeline.SpawnAgentOptions{
		TaskAPIToken: "stagerun-tok",
		MCPUrl:       "http://127.0.0.1:13120",
	})
	require.Subset(t, allow, mcp.StageRunAllowedTools(),
		"under --permission-mode default an MCP tool that is not pre-approved raises a "+
			"permission request on its first call, parking the stage run in awaiting_user")
}

func TestSpawnStageAgent_NoStageRunCredentialPreApprovesNoTaskTools(t *testing.T) {
	for name, opts := range map[string]pipeline.SpawnAgentOptions{
		"no credential minted": {MCPUrl: "http://127.0.0.1:13120"},
		"no dashboard URL":     {TaskAPIToken: "stagerun-tok"},
	} {
		t.Run(name, func(t *testing.T) {
			for _, entry := range spawnSettingsAllow(t, opts) {
				require.False(t, strings.HasPrefix(entry, "mcp__"+mcp.ServerName+"__"),
					"the spawn config carries no %s server, so nothing may pre-approve its tools, got %q",
					mcp.ServerName, entry)
			}
		})
	}
}
