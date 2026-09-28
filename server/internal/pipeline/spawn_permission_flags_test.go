package pipeline_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/lx-wnk/kontor/server/internal/channelconfig"
	"github.com/lx-wnk/kontor/server/internal/pipeline"
)

// flagValues returns the arguments following the first occurrence of flag, up
// to the next argument that itself looks like a flag.
func flagValues(argv []string, flag string) []string {
	for i, a := range argv {
		if a != flag {
			continue
		}
		var out []string
		for _, v := range argv[i+1:] {
			if len(v) > 1 && v[0] == '-' {
				break
			}
			out = append(out, v)
		}
		return out
	}
	return nil
}

// TestSpawnArgs_CarryPermissionsAsFlags is the guard on the one mechanism that
// actually binds a stage agent's permissions.
//
// The settings file is not that mechanism. Claude Code applies
// permissions.allow from .claude/settings.json only in a workspace that has
// been trusted, and a stage worktree is created fresh, so it never has been —
// the CLI discards every entry and says so. Measured in an untrusted directory
// on 2026-09-05: a settings file allowing Write left Write blocked, while
// --allowedTools Write let the identical call through.
//
// So an argv without these flags means every per-task permission is inert,
// which is invisible from inside the dashboard: it records the grant, the agent
// never receives it, and the failure surfaces much later as a denied tool call.
func TestSpawnArgs_CarryPermissionsAsFlags(t *testing.T) {
	argv, _ := spawnRecording(t, "", pipeline.SpawnAgentOptions{Prompt: "do the thing"})

	allowed := flagValues(argv, "--allowedTools")
	require.NotEmpty(t, allowed,
		"without --allowedTools the agent gets no permissions at all: the settings file is ignored in an untrusted worktree")

	require.Contains(t, allowed, "mcp__"+channelconfig.ServerName+"__"+channelconfig.ToolSetStageOutput,
		"the stage-output tool must be permitted on the command line, or the agent's submission is denied")
	require.Contains(t, allowed, "mcp__"+channelconfig.ServerName+"__"+channelconfig.ToolRequestPermission,
		"an agent that cannot request permissions cannot recover from a missing one either")
}

// TestSpawnArgs_CarryTheDenyListAsAFlag covers the other half. BuildDenyList
// exists to stop git push on an allow-all autonomy; routed through the settings
// file it was as inert as the allow entries, which made the block decorative in
// exactly the configuration that needs it most.
func TestSpawnArgs_CarryTheDenyListAsAFlag(t *testing.T) {
	argv, _ := spawnRecording(t, "", pipeline.SpawnAgentOptions{Prompt: "do the thing", AllowGitPush: false})

	denied := flagValues(argv, "--disallowedTools")
	require.NotEmpty(t, denied, "the deny list must reach the spawn as --disallowedTools")
	require.Contains(t, denied, "Bash(git push:*)",
		"git push is denied by default even under allow-all autonomy, and only a flag enforces it")
}
