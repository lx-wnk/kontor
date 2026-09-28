package cmdscope

import (
	"path/filepath"
	"testing"

	"github.com/lx-wnk/kontor/server/internal/claudeconfig"
	"github.com/lx-wnk/kontor/server/internal/db/ent"
	"github.com/stretchr/testify/require"
)

func TestScopes_ConfiguredDirWins(t *testing.T) {
	t.Setenv("CLAUDE_CONFIG_DIR", "/env")
	t.Cleanup(claudeconfig.SetConfigDirProvider(func() string { return "/configured" }))
	require.Equal(t, "/configured", DefaultScope("/p").ConfigDir)
	require.Equal(t, "/configured", ResolveSpawnerScope(&ent.Spawner{}, "").ConfigDir)
	require.Equal(t, "/configured", ResolveSessionScope("", "/cwd").ConfigDir)
}

func TestResolveSpawnerScope_ConfigDirFromEnvTildeExpanded(t *testing.T) {
	t.Setenv("CLAUDE_CONFIG_DIR", "/should/be/ignored")
	home := t.TempDir()
	t.Setenv("HOME", home)

	sp := &ent.Spawner{
		Command:     "claude",
		Env:         map[string]string{"CLAUDE_CONFIG_DIR": "~/.claude-work"},
		AdapterType: "claude",
	}
	got := ResolveSpawnerScope(sp, "/proj")

	require.True(t, got.Supported)
	require.Equal(t, filepath.Join(home, ".claude-work"), got.ConfigDir)
	require.Equal(t, "/proj", got.ProjectCwd)
	require.Equal(t, "claude", got.Command)
}

func TestResolveSpawnerScope_FallsBackToProcessEnv(t *testing.T) {
	t.Setenv("CLAUDE_CONFIG_DIR", "/process/cfg")
	sp := &ent.Spawner{Command: "", Env: map[string]string{}, AdapterType: ""}
	got := ResolveSpawnerScope(sp, "")

	require.True(t, got.Supported)
	require.Equal(t, "/process/cfg", got.ConfigDir)
	require.Equal(t, "claude", got.Command, "empty command defaults to claude")
}

func TestResolveSpawnerScope_NonClaudeAdapterUnsupported(t *testing.T) {
	sp := &ent.Spawner{Command: "ollama", AdapterType: "ollama"}
	got := ResolveSpawnerScope(sp, "/proj")

	require.False(t, got.Supported)
	require.Empty(t, got.Commands())
	require.Empty(t, got.Skills())
}

func TestResolveSpawnerScope_CustomCommand(t *testing.T) {
	t.Setenv("CLAUDE_CONFIG_DIR", "/c")
	sp := &ent.Spawner{Command: "my-claude-wrapper", AdapterType: "claude"}
	got := ResolveSpawnerScope(sp, "")
	require.Equal(t, "my-claude-wrapper", got.Command)
}

func TestResolveSessionScope_EmptyConfigDirUsesDefault(t *testing.T) {
	t.Setenv("CLAUDE_CONFIG_DIR", "/session/default")
	got := ResolveSessionScope("", "/cwd")
	require.True(t, got.Supported)
	require.Equal(t, "/session/default", got.ConfigDir)
	require.Equal(t, "/cwd", got.ProjectCwd)
	require.Equal(t, "claude", got.Command)
}

func TestResolveSessionScope_ExplicitConfigDir(t *testing.T) {
	got := ResolveSessionScope("/explicit/work", "/cwd")
	require.Equal(t, "/explicit/work", got.ConfigDir)
}

func TestDefaultScope_UsesHomeWhenNoEnv(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	got := DefaultScope("/p")
	require.True(t, got.Supported)
	require.Equal(t, filepath.Join(home, ".claude"), got.ConfigDir)
	require.Equal(t, "/p", got.ProjectCwd)
}
