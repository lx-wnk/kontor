package merger_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	sdk "github.com/lx-wnk/kontor/sdk"
	"github.com/lx-wnk/kontor/server/internal/api/agents"
	"github.com/lx-wnk/kontor/server/internal/merger"
	"github.com/lx-wnk/kontor/server/internal/testsupport/fakespawn"
)

// findBySession returns the agent with the given session ID, or false.
func findBySession(agentsList []sdk.Agent, sessionID string) (sdk.Agent, bool) {
	for _, a := range agentsList {
		if a.SessionID == sessionID {
			return a, true
		}
	}
	return sdk.Agent{}, false
}

// TestFinishedAgentFullLifecycle exercises the whole controllable-agent
// lifecycle end to end: live → finished (after exit) → dismissed via the real
// HTTP handler → gone from GetAgents.
func TestFinishedAgentFullLifecycle(t *testing.T) {
	fs := fakespawn.New(t)

	m := merger.New(merger.WithScanFn(fs.ScanFn()))

	ag := fs.Spawn(fakespawn.SpawnOpts{})

	// Live: agent is scanned and exposes its channel.
	live, err := m.GetAgents(context.Background(), merger.GetAgentsOpts{})
	require.NoError(t, err)
	liveAgent, ok := findBySession(live, ag.SessionID)
	require.True(t, ok, "live agent must be emitted")
	assert.NotEqual(t, sdk.AgentStatusFinished, liveAgent.Status, "agent must be live")
	assert.True(t, liveAgent.ChannelAvailable, "live agent must expose its channel")

	// Exit → finished: process gone, reconstructed from JSONL + discovery file.
	fs.Exit(ag.PID)
	afterExit, err := m.GetAgents(context.Background(), merger.GetAgentsOpts{})
	require.NoError(t, err)
	finishedAgent, ok := findBySession(afterExit, ag.SessionID)
	require.True(t, ok, "finished agent must be emitted after exit")
	assert.Equal(t, sdk.AgentStatusFinished, finishedAgent.Status)
	assert.Equal(t, ag.PID, finishedAgent.PID)
	assert.True(t, finishedAgent.ChannelAvailable, "finished agent must still expose its channel")

	// Dismiss via the real HTTP handler: forgets the agent in the tracker (and
	// cleans up any leftover discovery file).
	h := agents.NewSpawnHandler(nil)
	h.SetAgentDismisser(m)
	r := chi.NewRouter()
	r.Delete("/api/agents/{pid}/channel", h.DismissChannel)
	req := httptest.NewRequest(http.MethodDelete, "/api/agents/"+strconv.Itoa(ag.PID)+"/channel", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	require.Equal(t, http.StatusNoContent, rec.Code, "dismiss must succeed")

	_, statErr := os.Stat(fs.DiscoveryPath(ag.PID))
	assert.True(t, os.IsNotExist(statErr), "discovery file must be removed")

	// Gone: the agent is no longer emitted.
	afterDismiss, err := m.GetAgents(context.Background(), merger.GetAgentsOpts{})
	require.NoError(t, err)
	_, ok = findBySession(afterDismiss, ag.SessionID)
	assert.False(t, ok, "dismissed agent must not be emitted anymore")
}
