package channelconfig_test

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/lx-wnk/kontor/server/internal/channelconfig"
	"github.com/stretchr/testify/require"
)

func TestWriteTempConfig_ReturnsFilePath(t *testing.T) {
	binaryPath := "/usr/local/bin/agent-dashboard"
	path, err := channelconfig.WriteTempConfig(binaryPath, nil, nil)
	require.NoError(t, err)
	require.NotEmpty(t, path)
	t.Cleanup(func() { _ = os.Remove(path) })
}

func TestWriteTempConfig_FileExists(t *testing.T) {
	binaryPath := "/usr/local/bin/agent-dashboard"
	path, err := channelconfig.WriteTempConfig(binaryPath, nil, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.Remove(path) })

	_, statErr := os.Stat(path)
	require.NoError(t, statErr, "returned file path must exist on disk")
}

func TestWriteTempConfig_FilePermissions(t *testing.T) {
	binaryPath := "/usr/local/bin/agent-dashboard"
	path, err := channelconfig.WriteTempConfig(binaryPath, nil, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.Remove(path) })

	info, err := os.Stat(path)
	require.NoError(t, err)
	// File must not be world-readable (permissions & 0o077 == 0).
	mode := info.Mode().Perm()
	require.Equal(t, os.FileMode(0), mode&0o077,
		"file must not be group- or world-readable, got %o", mode)
}

func TestWriteTempConfig_JSONContract(t *testing.T) {
	binaryPath := "/usr/local/bin/agent-dashboard"
	path, err := channelconfig.WriteTempConfig(binaryPath, nil, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.Remove(path) })

	data, err := os.ReadFile(path)
	require.NoError(t, err)

	var cfg struct {
		MCPServers map[string]struct {
			Command string   `json:"command"`
			Args    []string `json:"args"`
		} `json:"mcpServers"`
	}
	require.NoError(t, json.Unmarshal(data, &cfg), "file must contain valid JSON")

	entry, ok := cfg.MCPServers["dashboard-channel"]
	require.True(t, ok, "mcpServers must contain 'dashboard-channel' key")
	require.Equal(t, binaryPath, entry.Command,
		"dashboard-channel.command must equal the provided binary path")
}

func TestConfigJSON_ValidJSON(t *testing.T) {
	binaryPath := "/usr/local/bin/agent-dashboard"
	s, err := channelconfig.ConfigJSON(binaryPath, nil)
	require.NoError(t, err)
	require.True(t, json.Valid([]byte(s)), "ConfigJSON must return valid JSON")
}

func TestConfigJSON_ContainsBinaryPath(t *testing.T) {
	binaryPath := "/usr/local/bin/agent-dashboard"
	s, err := channelconfig.ConfigJSON(binaryPath, nil)
	require.NoError(t, err)

	var cfg struct {
		MCPServers map[string]struct {
			Command string   `json:"command"`
			Args    []string `json:"args"`
		} `json:"mcpServers"`
	}
	require.NoError(t, json.Unmarshal([]byte(s), &cfg))
	entry, ok := cfg.MCPServers["dashboard-channel"]
	require.True(t, ok, "mcpServers must contain 'dashboard-channel' key")
	require.Equal(t, binaryPath, entry.Command)
}

func TestConfigJSON_ContainsChannelArg(t *testing.T) {
	binaryPath := "/usr/local/bin/agent-dashboard"
	s, err := channelconfig.ConfigJSON(binaryPath, nil)
	require.NoError(t, err)

	var cfg struct {
		MCPServers map[string]struct {
			Command string   `json:"command"`
			Args    []string `json:"args"`
		} `json:"mcpServers"`
	}
	require.NoError(t, json.Unmarshal([]byte(s), &cfg))
	entry := cfg.MCPServers["dashboard-channel"]
	require.Contains(t, entry.Args, "channel", "args must contain 'channel'")
}

func TestConfigJSON_MatchesWriteTempConfig(t *testing.T) {
	// Both ConfigJSON and WriteTempConfig must produce structurally identical configs.
	binaryPath := "/usr/local/bin/agent-dashboard"

	jsonStr, err := channelconfig.ConfigJSON(binaryPath, nil)
	require.NoError(t, err)

	filePath, err := channelconfig.WriteTempConfig(binaryPath, nil, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.Remove(filePath) })

	fileData, err := os.ReadFile(filePath)
	require.NoError(t, err)

	type entry struct {
		Command string   `json:"command"`
		Args    []string `json:"args"`
	}
	type config struct {
		MCPServers map[string]entry `json:"mcpServers"`
	}

	var fromJSON, fromFile config
	require.NoError(t, json.Unmarshal([]byte(jsonStr), &fromJSON))
	require.NoError(t, json.Unmarshal(fileData, &fromFile))
	require.Equal(t, fromFile, fromJSON, "ConfigJSON and WriteTempConfig must produce identical configs")
}

func TestWriteTempConfig_MultipleCalls(t *testing.T) {
	// Each call must produce a distinct temp file (no collisions).
	binaryPath := "/usr/local/bin/agent-dashboard"
	path1, err := channelconfig.WriteTempConfig(binaryPath, nil, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.Remove(path1) })

	path2, err := channelconfig.WriteTempConfig(binaryPath, nil, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.Remove(path2) })

	require.NotEqual(t, path1, path2, "successive calls must return distinct file paths")
}

func TestConfigJSON_NilTaskAPIIsUnchanged(t *testing.T) {
	got, err := channelconfig.ConfigJSON("/bin/agent-dashboard", nil)
	require.NoError(t, err)
	require.Equal(t,
		`{"mcpServers":{"dashboard-channel":{"command":"/bin/agent-dashboard","args":["channel"]}}}`,
		got)
}

func TestConfigJSON_TaskAPIAddsASecondServer(t *testing.T) {
	got, err := channelconfig.ConfigJSON("/bin/agent-dashboard", &channelconfig.TaskAPI{
		URL: "http://127.0.0.1:13120/api/mcp", Token: "mcp_deadbeef",
	})
	require.NoError(t, err)

	var parsed struct {
		MCPServers map[string]struct {
			Command string            `json:"command"`
			Type    string            `json:"type"`
			URL     string            `json:"url"`
			Headers map[string]string `json:"headers"`
		} `json:"mcpServers"`
	}
	require.NoError(t, json.Unmarshal([]byte(got), &parsed))
	require.Len(t, parsed.MCPServers, 2)

	tasks := parsed.MCPServers["dashboard-tasks"]
	require.Equal(t, "http", tasks.Type)
	require.Equal(t, "http://127.0.0.1:13120/api/mcp", tasks.URL)
	require.Equal(t, "Bearer mcp_deadbeef", tasks.Headers["Authorization"])
	require.Equal(t, "/bin/agent-dashboard", parsed.MCPServers["dashboard-channel"].Command)
}

// A half-filled TaskAPI would write a server entry the agent cannot use and,
// worse, one that looks configured. Refuse it instead.
func TestConfigJSON_TaskAPIWithoutTokenIsRefused(t *testing.T) {
	_, err := channelconfig.ConfigJSON("/bin/agent-dashboard", &channelconfig.TaskAPI{URL: "http://127.0.0.1:13120/api/mcp"})
	require.Error(t, err)
}

func TestConfigJSON_TaskAPIWithoutURLIsRefused(t *testing.T) {
	_, err := channelconfig.ConfigJSON("/bin/agent-dashboard", &channelconfig.TaskAPI{Token: "mcp_deadbeef"})
	require.Error(t, err)
}
