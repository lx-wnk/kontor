package channelconfig_test

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/lx-wnk/kontor/server/internal/channelconfig"
)

// readServers parses a written MCP config back into its server map.
func readServers(t *testing.T, path string) map[string]struct {
	Env     map[string]string `json:"env"`
	Headers map[string]string `json:"headers"`
} {
	t.Helper()
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	var parsed struct {
		MCPServers map[string]struct {
			Env     map[string]string `json:"env"`
			Headers map[string]string `json:"headers"`
		} `json:"mcpServers"`
	}
	require.NoError(t, json.Unmarshal(data, &parsed))
	return parsed.MCPServers
}

// TestWriteTempConfig_ChannelGetsTheStageRunCredential pins the credential the
// bridge authenticates with.
//
// The bridge posts set_stage_output, request_permission and dashboard_reply back
// to the dashboard using DASHBOARD_MCP_TOKEN. Before this it inherited whatever
// the operator had set globally, and an install that set nothing had every
// callback answered `HTTP 401 unauthorized` — visible only in the agent's own
// transcript, never in the dashboard. Handing it the stage run's own key makes
// the credential short-lived, attributable and revoked with the run.
func TestWriteTempConfig_ChannelGetsTheStageRunCredential(t *testing.T) {
	path, err := channelconfig.WriteTempConfig("/bin/echo",
		&channelconfig.TaskAPI{URL: "http://127.0.0.1:13120/api/mcp", Token: "stage-run-token"}, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.Remove(path) })

	servers := readServers(t, path)
	channel, ok := servers[channelconfig.ChannelServerName]
	require.True(t, ok, "the channel server must be in the config")
	require.Equal(t, "stage-run-token", channel.Env[channelconfig.EnvMCPToken],
		"without this the bridge falls back to a global env var that may not exist, and every callback 401s")
}

// TestWriteTempConfig_NoTaskAPI_LeavesTheEnvironmentAlone: with no per-run key
// to hand over, the bridge must keep inheriting whatever the operator set, not
// receive an empty token that would fail closed where it used to work.
func TestWriteTempConfig_NoTaskAPI_LeavesTheEnvironmentAlone(t *testing.T) {
	path, err := channelconfig.WriteTempConfig("/bin/echo", nil, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.Remove(path) })

	servers := readServers(t, path)
	channel := servers[channelconfig.ChannelServerName]
	require.Empty(t, channel.Env[channelconfig.EnvMCPToken],
		"an absent per-run key must leave the inherited environment untouched")
}
