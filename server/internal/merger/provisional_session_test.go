package merger_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lx-wnk/agent-dashboard/sdk"
	"github.com/lx-wnk/agent-dashboard/server/internal/merger"
	"github.com/lx-wnk/agent-dashboard/server/internal/scanner"
)

// TestGetAgents_SessionNamedButNotYetOnDisk covers the window a dashboard-spawned
// agent lives in before its first turn closes: Claude has written no transcript,
// so session resolution fails, and the process used to be dropped from the list
// entirely. That hid exactly the session worth seeing — one whose opening move is
// an AskUserQuestion, which the pty broker is already holding with nobody to hang
// it on. The process names its own session on its command line, so it is not
// anonymous; it is only unwritten.
func TestGetAgents_SessionNamedButNotYetOnDisk(t *testing.T) {
	const sessionID = "df5c2147-5385-4e2e-b8ee-0204f9eac500"

	cases := []struct {
		name     string
		proc     scanner.ProcessInfo
		wantID   string
		wantNone bool
	}{
		{
			name: "a claude process that names its session is listed before it writes",
			proc: scanner.ProcessInfo{
				PID: 4711, CWD: "/some/project", Uptime: 12,
				Command: "/usr/local/bin/claude --session-id " + sessionID + " --permission-mode plan do the thing",
			},
			wantID: sessionID,
		},
		{
			name: "--session-id=value is the same claim",
			proc: scanner.ProcessInfo{
				PID: 4712, CWD: "/some/project", Uptime: 12,
				Command: "claude --session-id=" + sessionID,
			},
			wantID: sessionID,
		},
		{
			name: "a process that names no session stays out of the list",
			proc: scanner.ProcessInfo{
				PID: 4713, CWD: "/some/project", Uptime: 12,
				Command: "/usr/local/bin/claude daemon run --origin transient",
			},
			wantNone: true,
		},
		{
			name: "--session-id is a Claude flag, so another provider does not qualify",
			proc: scanner.ProcessInfo{
				PID: 4714, CWD: "/some/project", Uptime: 12,
				Provider: sdk.ProviderCodex,
				Command:  "codex --session-id " + sessionID,
			},
			wantNone: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// An empty home has no transcripts, so resolution fails for every case
			// and the fallback is the only thing that can produce an agent.
			t.Setenv("HOME", t.TempDir())

			m := merger.New(merger.WithScanFn(fixedScanFn([]scanner.ProcessInfo{tc.proc})))
			agents, err := m.GetAgents(context.Background(), merger.GetAgentsOpts{})
			require.NoError(t, err)

			if tc.wantNone {
				assert.Empty(t, agents)
				return
			}
			require.Len(t, agents, 1)
			assert.Equal(t, tc.wantID, agents[0].SessionID)
			assert.Equal(t, tc.proc.PID, agents[0].PID)
			assert.True(t, agents[0].Working, "the turn it is waiting inside has not closed")
		})
	}
}

// TestGetAgents_TwoProcessesNamingOneSession proves the fallback respects the
// claim set: a session already taken by a resolved process is not handed out a
// second time, which would draw one session as two agents.
func TestGetAgents_TwoProcessesNamingOneSession(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	const sessionID = "11111111-2222-3333-4444-555555555555"

	m := merger.New(merger.WithScanFn(fixedScanFn([]scanner.ProcessInfo{
		{PID: 5001, CWD: "/some/project", Uptime: 10, Command: "claude --session-id " + sessionID},
		{PID: 5002, CWD: "/some/project", Uptime: 20, Command: "claude --session-id " + sessionID},
	})))

	agents, err := m.GetAgents(context.Background(), merger.GetAgentsOpts{})
	require.NoError(t, err)
	require.Len(t, agents, 1, "one session is one agent, whichever process claimed it first")
	assert.Equal(t, sessionID, agents[0].SessionID)
}
