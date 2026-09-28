package claudeconfig_test

import (
	"os"
	"path/filepath"
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
