package claudeconfig_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lx-wnk/kontor/server/internal/claudeconfig"
	"github.com/stretchr/testify/require"
)

func writeUserConfig(t *testing.T, body string) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", dir)
	if body != "" {
		require.NoError(t, os.WriteFile(filepath.Join(dir, ".claude.json"), []byte(body), 0o600))
	}
}

func TestJSONPath_HonorsConfigDir(t *testing.T) {
	t.Setenv("CLAUDE_CONFIG_DIR", "/tmp/some-claude-root")
	path, err := claudeconfig.JSONPath()
	require.NoError(t, err)
	require.Equal(t, "/tmp/some-claude-root/.claude.json", path)
}

func TestUserMCPServers_AbsentFileIsNotAnError(t *testing.T) {
	writeUserConfig(t, "")
	servers, err := claudeconfig.UserMCPServers()
	require.NoError(t, err, "a user who never ran `claude mcp add` is not an error case")
	require.Empty(t, servers)
}

func TestUserMCPServers_MalformedFileReportsErrorAndNoServers(t *testing.T) {
	writeUserConfig(t, "{ this is not json")
	servers, err := claudeconfig.UserMCPServers()
	require.Error(t, err)
	require.Nil(t, servers, "a caller ignoring the error must still get a usable empty map")
}

func TestUserMCPServers_ReturnsRawEntries(t *testing.T) {
	writeUserConfig(t, `{"mcpServers":{"context7":{"type":"http","url":"https://ctx7.example/mcp"}},"other":1}`)
	servers, err := claudeconfig.UserMCPServers()
	require.NoError(t, err)
	require.Contains(t, servers, "context7")
	require.JSONEq(t, `{"type":"http","url":"https://ctx7.example/mcp"}`, string(servers["context7"]))
}

func TestConfigDir_ProviderTakesPrecedence(t *testing.T) {
	t.Cleanup(claudeconfig.SetConfigDirProvider(func() string {
		return "/custom/claude-dir"
	}))
	t.Setenv("CLAUDE_CONFIG_DIR", "/env-dir")

	dir := claudeconfig.ConfigDir()
	require.Equal(t, "/custom/claude-dir", dir)
}

func TestConfigDir_EnvFallback(t *testing.T) {
	t.Cleanup(claudeconfig.SetConfigDirProvider(func() string { return "" }))
	t.Setenv("CLAUDE_CONFIG_DIR", "/env-dir")

	dir := claudeconfig.ConfigDir()
	require.Equal(t, "/env-dir", dir)
}

func TestConfigDir_DefaultHome(t *testing.T) {
	t.Cleanup(claudeconfig.SetConfigDirProvider(nil))
	t.Setenv("CLAUDE_CONFIG_DIR", "")

	dir := claudeconfig.ConfigDir()
	require.True(t, strings.HasSuffix(dir, "/.claude"), "expected dir to end with /.claude, got %s", dir)
}

// JSONPath default must be ~/.claude.json (home level), not ~/.claude/.claude.json.
// ConfigDir returns ~/.claude for session scanning; .claude.json is its sibling in home.
func TestJSONPath_DefaultIsHomeLevel(t *testing.T) {
	t.Cleanup(claudeconfig.SetConfigDirProvider(nil))
	t.Setenv("CLAUDE_CONFIG_DIR", "")

	path, err := claudeconfig.JSONPath()
	require.NoError(t, err)
	require.True(t, strings.HasSuffix(path, "/.claude.json"), "expected path to end with /.claude.json, got %s", path)
	require.False(t, strings.Contains(path, "/.claude/.claude.json"), "path must not nest .claude.json inside .claude dir, got %s", path)
}

func TestJSONPath_ProviderTakesPrecedence(t *testing.T) {
	t.Cleanup(claudeconfig.SetConfigDirProvider(func() string { return "/custom/claude-dir" }))
	t.Setenv("CLAUDE_CONFIG_DIR", "/env-dir")

	path, err := claudeconfig.JSONPath()
	require.NoError(t, err)
	require.Equal(t, "/custom/claude-dir/.claude.json", path)
}

func TestSessionDir(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Cleanup(claudeconfig.SetConfigDirProvider(func() string { return "/configured" }))

	require.Equal(t, "/own", claudeconfig.SessionDir("/own", true), "a dir read from the process wins")
	require.Equal(t, filepath.Join(home, ".claude"), claudeconfig.SessionDir("", true),
		"read and unset runs on the CLI default, not the server's configured dir")
	require.Equal(t, "/configured", claudeconfig.SessionDir("", false),
		"an unreadable env falls back to the server's configured dir")
}
