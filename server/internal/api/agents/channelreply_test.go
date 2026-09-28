package agents

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lx-wnk/kontor/server/internal/db/ent"
	"github.com/lx-wnk/kontor/server/internal/db/repo"
	mcp "github.com/lx-wnk/kontor/server/internal/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeApiKeyRepo implements just enough of repo.ApiKeyRepo for the Post auth tests.
type fakeApiKeyRepo struct {
	repo.ApiKeyRepo
	getByHash func(hash string) (*ent.ApiKey, error)
}

func (f *fakeApiKeyRepo) GetByHash(_ context.Context, hash string) (*ent.ApiKey, error) {
	return f.getByHash(hash)
}

func postReplyReq(t *testing.T, authHeader string) *http.Request {
	t.Helper()
	body := `{"parentPid":1234,"message":"hi","timestamp":"2026-06-09T10:00:00Z"}`
	req := httptest.NewRequest(http.MethodPost, "/api/channel-reply", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if authHeader != "" {
		req.Header.Set("Authorization", authHeader)
	}
	return req
}

func TestChannelReplyPost_ValidToken_Stores(t *testing.T) {
	const raw = "mcp_valid_token"
	var seenHash string
	store := NewReplyStore()
	keys := &fakeApiKeyRepo{getByHash: func(h string) (*ent.ApiKey, error) {
		seenHash = h
		return &ent.ApiKey{}, nil
	}}
	h := NewChannelReplyHandler(store, keys, nil)

	rec := httptest.NewRecorder()
	h.Post(rec, postReplyReq(t, "Bearer "+raw))

	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, mcp.HashToken(raw), seenHash, "handler must look up the SHA-256 hash of the bearer token")
	require.Len(t, store.Since(1234, ""), 1)
}

func TestChannelReplyPost_BadToken_401(t *testing.T) {
	store := NewReplyStore()
	keys := &fakeApiKeyRepo{getByHash: func(string) (*ent.ApiKey, error) {
		return nil, errors.New("not found")
	}}
	h := NewChannelReplyHandler(store, keys, nil)

	rec := httptest.NewRecorder()
	h.Post(rec, postReplyReq(t, "Bearer wrong"))

	require.Equal(t, http.StatusUnauthorized, rec.Code)
	assert.Empty(t, store.Since(1234, ""), "reply must not be stored on auth failure")
}

func TestChannelReplyPost_NoBearer_401(t *testing.T) {
	store := NewReplyStore()
	keys := &fakeApiKeyRepo{getByHash: func(string) (*ent.ApiKey, error) {
		return nil, errors.New("not found")
	}}
	h := NewChannelReplyHandler(store, keys, nil)

	rec := httptest.NewRecorder()
	h.Post(rec, postReplyReq(t, ""))

	require.Equal(t, http.StatusUnauthorized, rec.Code)
}

func TestChannelReplyPost_NilRepo_401(t *testing.T) {
	store := NewReplyStore()
	h := NewChannelReplyHandler(store, nil, nil)

	rec := httptest.NewRecorder()
	h.Post(rec, postReplyReq(t, "Bearer anything"))

	require.Equal(t, http.StatusUnauthorized, rec.Code)
}

func TestReplyStore_StoreAndRetrieve(t *testing.T) {
	s := NewReplyStore()
	ts := time.Now().UTC().Format(time.RFC3339)

	s.Add(1234, "hello world", ts)

	replies := s.Since(1234, "")
	require.Len(t, replies, 1)
	assert.Equal(t, "hello world", replies[0].Message)
	assert.Equal(t, ts, replies[0].Timestamp)
}

func TestReplyStore_SinceEmptyReturnsAll(t *testing.T) {
	s := NewReplyStore()
	now := time.Now().UTC()

	for i := 0; i < 5; i++ {
		ts := now.Add(time.Duration(i) * time.Second).Format(time.RFC3339)
		s.Add(42, fmt.Sprintf("msg-%d", i), ts)
	}

	replies := s.Since(42, "")
	assert.Len(t, replies, 5)
}

func TestReplyStore_SinceFiltersOlderEntries(t *testing.T) {
	s := NewReplyStore()
	base := time.Now().UTC().Truncate(time.Second)

	// Add entries at t+0, t+1, t+2.
	for i := 0; i < 3; i++ {
		ts := base.Add(time.Duration(i) * time.Second).Format(time.RFC3339)
		s.Add(99, fmt.Sprintf("msg-%d", i), ts)
	}

	// Request replies since t+0 — should return only t+1 and t+2.
	sinceStr := base.Format(time.RFC3339)
	replies := s.Since(99, sinceStr)
	assert.Len(t, replies, 2)
	assert.Equal(t, "msg-1", replies[0].Message)
	assert.Equal(t, "msg-2", replies[1].Message)
}

func TestReplyStore_RingBufferWrapsOldestEvicted(t *testing.T) {
	s := NewReplyStore()
	pid := 7

	// Add maxRepliesPerPID + 5 entries; the first 5 should be evicted.
	total := maxRepliesPerPID + 5
	for i := 0; i < total; i++ {
		ts := time.Now().UTC().Format(time.RFC3339)
		s.Add(pid, fmt.Sprintf("msg-%d", i), ts)
	}

	replies := s.Since(pid, "")
	require.Len(t, replies, maxRepliesPerPID, "ring buffer should cap at maxRepliesPerPID")

	// The oldest evicted messages are msg-0 through msg-4; the first kept is msg-5.
	assert.Equal(t, "msg-5", replies[0].Message)
	assert.Equal(t, fmt.Sprintf("msg-%d", total-1), replies[len(replies)-1].Message)
}

func TestReplyStore_RingBufferExactCapacity(t *testing.T) {
	s := NewReplyStore()
	pid := 8

	for i := 0; i < maxRepliesPerPID; i++ {
		s.Add(pid, fmt.Sprintf("msg-%d", i), time.Now().UTC().Format(time.RFC3339))
	}

	replies := s.Since(pid, "")
	assert.Len(t, replies, maxRepliesPerPID, "ring buffer should hold exactly maxRepliesPerPID entries")
}

func TestReplyStore_UnknownPidReturnsEmpty(t *testing.T) {
	s := NewReplyStore()
	replies := s.Since(9999, "")
	assert.NotNil(t, replies)
	assert.Empty(t, replies)
}

func TestReplyStore_SeparateStorePerPid(t *testing.T) {
	s := NewReplyStore()
	ts := time.Now().UTC().Format(time.RFC3339)

	s.Add(1, "pid1-msg", ts)
	s.Add(2, "pid2-msg", ts)

	replies1 := s.Since(1, "")
	replies2 := s.Since(2, "")

	require.Len(t, replies1, 1)
	require.Len(t, replies2, 1)
	assert.Equal(t, "pid1-msg", replies1[0].Message)
	assert.Equal(t, "pid2-msg", replies2[0].Message)
}

func TestReplyStore_ConcurrentWritesSafe(t *testing.T) {
	s := NewReplyStore()
	pid := 55

	var wg sync.WaitGroup
	goroutines := 50
	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		i := i
		go func() {
			defer wg.Done()
			ts := time.Now().UTC().Format(time.RFC3339)
			s.Add(pid, fmt.Sprintf("concurrent-msg-%d", i), ts)
		}()
	}
	wg.Wait()

	replies := s.Since(pid, "")
	// Expect at most maxRepliesPerPID entries; all goroutines wrote successfully.
	assert.LessOrEqual(t, len(replies), maxRepliesPerPID)
	assert.Greater(t, len(replies), 0)
}

type fakeStageRuns struct {
	pid *int
	err error
}

func (f fakeStageRuns) GetBySessionID(_ context.Context, _ string) (*ent.StageRun, error) {
	if f.err != nil {
		return nil, f.err
	}
	return &ent.StageRun{Pid: f.pid}, nil
}

func TestGetReplies_ResolvesSessionIDToPID(t *testing.T) {
	store := NewReplyStore()
	pid := 4242
	store.Add(pid, "hello from agent", time.Now().UTC().Format(time.RFC3339))

	h := NewChannelReplyHandler(store, nil, fakeStageRuns{pid: &pid})
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/agents/{sessionId}/replies", h.GetReplies)

	req := httptest.NewRequest(http.MethodGet, "/api/agents/sess-abc-123/replies", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	var body struct {
		Replies []Reply `json:"replies"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	require.Len(t, body.Replies, 1)
	require.Equal(t, "hello from agent", body.Replies[0].Message)
}

func TestGetReplies_NoStageRunPID_ReturnsEmpty(t *testing.T) {
	h := NewChannelReplyHandler(NewReplyStore(), nil, fakeStageRuns{pid: nil})
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/agents/{sessionId}/replies", h.GetReplies)

	req := httptest.NewRequest(http.MethodGet, "/api/agents/unknown/replies", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	var body struct {
		Replies []Reply `json:"replies"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	require.Empty(t, body.Replies)
}
