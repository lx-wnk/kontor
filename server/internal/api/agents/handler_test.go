package agents_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lx-wnk/kontor/sdk"
	"github.com/lx-wnk/kontor/server/internal/api/agents"
	"github.com/lx-wnk/kontor/server/internal/sse"
)

// syncRecorder is an http.ResponseWriter+http.Flusher safe for concurrent
// use: the SSE handler writes from its own goroutine while the test polls
// the buffered body from the main goroutine, which httptest.ResponseRecorder
// does not support (its embedded bytes.Buffer has no internal locking).
type syncRecorder struct {
	mu     sync.Mutex
	header http.Header
	body   bytes.Buffer
}

func newSyncRecorder() *syncRecorder {
	return &syncRecorder{header: make(http.Header)}
}

func (r *syncRecorder) Header() http.Header { return r.header }

func (r *syncRecorder) Write(b []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.body.Write(b)
}

func (r *syncRecorder) WriteHeader(int) {}

func (r *syncRecorder) Flush() {}

func (r *syncRecorder) Len() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.body.Len()
}

func (r *syncRecorder) String() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.body.String()
}

func TestAgentHandler_List_ReturnsJSON(t *testing.T) {
	want := []sdk.Agent{{SessionID: "abc", Status: sdk.AgentStatusActive}}
	getAgents := func(_ context.Context) ([]sdk.Agent, error) { return want, nil }
	broadcaster := sse.NewBroadcaster()
	h := agents.NewHandler(getAgents, broadcaster)

	req := httptest.NewRequest(http.MethodGet, "/api/agents", nil)
	rec := httptest.NewRecorder()
	require.NoError(t, h.List(rec, req))

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "application/json", rec.Header().Get("Content-Type"))

	var got []sdk.Agent
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&got))
	require.Len(t, got, 1)
	assert.Equal(t, "abc", got[0].SessionID)
}

func TestAgentHandler_List_EmptyReturnsArray(t *testing.T) {
	getAgents := func(_ context.Context) ([]sdk.Agent, error) { return nil, nil }
	broadcaster := sse.NewBroadcaster()
	h := agents.NewHandler(getAgents, broadcaster)

	req := httptest.NewRequest(http.MethodGet, "/api/agents", nil)
	rec := httptest.NewRecorder()
	require.NoError(t, h.List(rec, req))

	assert.Equal(t, http.StatusOK, rec.Code)
	// Body must be a JSON array [], not null.
	var got []sdk.Agent
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&got))
	assert.NotNil(t, got, "handler must return [] not null when no agents exist")
	assert.Empty(t, got)
}

// TestAgentHandler_List_UsesLastFrame_NotScan confirms List serves the
// broadcaster's last frame instead of calling getAgents once a frame has been
// broadcast (PERF-LOW2).
func TestAgentHandler_List_UsesLastFrame_NotScan(t *testing.T) {
	var scanCalls atomic.Int32
	getAgents := func(_ context.Context) ([]sdk.Agent, error) {
		scanCalls.Add(1)
		return []sdk.Agent{{SessionID: "scanned", Status: sdk.AgentStatusActive}}, nil
	}
	broadcaster := sse.NewBroadcaster()
	broadcaster.Broadcast([]byte(`{"agents":[{"sessionId":"cached","status":"active"}],"trend":[]}`))
	h := agents.NewHandler(getAgents, broadcaster)

	req := httptest.NewRequest(http.MethodGet, "/api/agents", nil)
	rec := httptest.NewRecorder()
	require.NoError(t, h.List(rec, req))

	var got []sdk.Agent
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&got))
	require.Len(t, got, 1)
	assert.Equal(t, "cached", got[0].SessionID)
	assert.Equal(t, int32(0), scanCalls.Load(), "getAgents must not be called when a last frame is cached")
}

// TestAgentHandler_List_FallsBackToScan_BeforeFirstTick confirms List calls
// getAgents when no frame has ever been broadcast (cold start / fresh
// install, before the broadcast loop's first tick). (PERF-LOW2)
func TestAgentHandler_List_FallsBackToScan_BeforeFirstTick(t *testing.T) {
	var scanCalls atomic.Int32
	getAgents := func(_ context.Context) ([]sdk.Agent, error) {
		scanCalls.Add(1)
		return []sdk.Agent{{SessionID: "scanned", Status: sdk.AgentStatusActive}}, nil
	}
	broadcaster := sse.NewBroadcaster()
	h := agents.NewHandler(getAgents, broadcaster)

	req := httptest.NewRequest(http.MethodGet, "/api/agents", nil)
	rec := httptest.NewRecorder()
	require.NoError(t, h.List(rec, req))

	var got []sdk.Agent
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&got))
	require.Len(t, got, 1)
	assert.Equal(t, "scanned", got[0].SessionID)
	assert.Equal(t, int32(1), scanCalls.Load(), "getAgents must be called as a fallback when no frame is cached yet")
}

// TestAgentHandler_Stream_InitialSend_UsesLastFrame confirms the SSE initial
// send reuses the broadcaster's last frame verbatim and does not call
// getAgents when a frame has already been broadcast. (PERF-LOW2)
func TestAgentHandler_Stream_InitialSend_UsesLastFrame(t *testing.T) {
	var scanCalls atomic.Int32
	getAgents := func(_ context.Context) ([]sdk.Agent, error) {
		scanCalls.Add(1)
		return []sdk.Agent{{SessionID: "scanned", Status: sdk.AgentStatusActive}}, nil
	}
	broadcaster := sse.NewBroadcaster()
	frame := []byte(`{"agents":[{"sessionId":"cached","status":"active"}],"trend":[]}`)
	broadcaster.Broadcast(frame)
	h := agents.NewHandler(getAgents, broadcaster)

	ctx, cancel := context.WithCancel(context.Background())
	req := httptest.NewRequest(http.MethodGet, "/api/agents/stream", nil).WithContext(ctx)
	rec := newSyncRecorder()

	done := make(chan struct{})
	go func() {
		h.Stream(rec, req)
		close(done)
	}()

	require.Eventually(t, func() bool {
		return rec.Len() > 0
	}, time.Second, time.Millisecond, "initial frame was not written")
	cancel()
	<-done

	assert.Equal(t, "data: "+string(frame)+"\n\n", rec.String())
	assert.Equal(t, int32(0), scanCalls.Load(), "getAgents must not be called when a last frame is cached")
}

// TestAgentHandler_Stream_InitialSend_FallsBackToScan confirms the SSE
// initial send calls getAgents when no frame has ever been broadcast (cold
// start / fresh install, before the broadcast loop's first tick). (PERF-LOW2)
func TestAgentHandler_Stream_InitialSend_FallsBackToScan(t *testing.T) {
	var scanCalls atomic.Int32
	getAgents := func(_ context.Context) ([]sdk.Agent, error) {
		scanCalls.Add(1)
		return []sdk.Agent{{SessionID: "scanned", Status: sdk.AgentStatusActive}}, nil
	}
	broadcaster := sse.NewBroadcaster()
	h := agents.NewHandler(getAgents, broadcaster)

	ctx, cancel := context.WithCancel(context.Background())
	req := httptest.NewRequest(http.MethodGet, "/api/agents/stream", nil).WithContext(ctx)
	rec := newSyncRecorder()

	done := make(chan struct{})
	go func() {
		h.Stream(rec, req)
		close(done)
	}()

	require.Eventually(t, func() bool {
		return rec.Len() > 0
	}, time.Second, time.Millisecond, "initial frame was not written")
	cancel()
	<-done

	assert.Contains(t, rec.String(), "scanned")
	assert.Equal(t, int32(1), scanCalls.Load(), "getAgents must be called as a fallback when no frame is cached yet")
}
