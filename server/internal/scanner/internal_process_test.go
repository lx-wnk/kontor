package scanner_test

import (
	"testing"

	"github.com/lx-wnk/kontor/server/internal/scanner"
	"github.com/stretchr/testify/require"
)

func TestIsInternalProcess(t *testing.T) {
	tests := []struct {
		name string
		comm string
		want bool
	}{
		{
			name: "bg-spare grandchild",
			comm: "claude bg-spare --bg-spare /tmp/cc-daemon-501/9f2c1e4a-8b3d-4e2f-9a1c-7d6e5b4a3c2d/spare/9f2c1e4a-8b3d-4e2f-9a1c-7d6e5b4a3c2d.claim.sock",
			want: true,
		},
		{
			name: "bg-pty-host grandchild",
			comm: "claude bg-pty-host --bg-pty-host /tmp/cc-daemon-501/9f2c1e4a-8b3d-4e2f-9a1c-7d6e5b4a3c2d/spare/9f2c1e4a-8b3d-4e2f-9a1c-7d6e5b4a3c2d.pty.sock 200 50 -- claude --resume 9f2c1e4a-8b3d-4e2f-9a1c-7d6e5b4a3c2d",
			want: true,
		},
		{
			name: "daemon run",
			comm: `claude daemon run --origin transient --spawned-by {"pid":11367,"sessionId":"9f2c1e4a-8b3d-4e2f-9a1c-7d6e5b4a3c2d"}`,
			want: true,
		},
		{
			name: "daemon run behind an absolute binary path",
			comm: `/Users/alex/.local/bin/claude daemon run --origin transient --spawned-by {"pid":11367,"sessionId":"9f2c1e4a-8b3d-4e2f-9a1c-7d6e5b4a3c2d"}`,
			want: true,
		},
		{
			// The real session, as spawned by `agent-dashboard live --resume`:
			// long --mcp-config JSON pointing at the dashboard-channel MCP
			// server (see channelconfig.buildConfig), followed by --resume.
			name: "genuine interactive session with the real --mcp-config shape",
			comm: `claude --mcp-config {"mcpServers":{"dashboard-channel":{"command":"/Users/alex/.local/bin/agent-dashboard","args":["channel"]}}} --resume 9f2c1e4a-8b3d-4e2f-9a1c-7d6e5b4a3c2d`,
			want: false,
		},
		{
			name: "prompt text merely contains the word bg-spare",
			comm: "claude --resume 9f2c1e4a-8b3d-4e2f-9a1c-7d6e5b4a3c2d --prompt please avoid the bg-spare-mode setting",
			want: false,
		},
		{
			name: "prompt text contains the adjacent words daemon run",
			comm: `claude -p "check why the daemon run loop stalls"`,
			want: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, scanner.IsInternalProcess(tt.comm))
		})
	}
}
