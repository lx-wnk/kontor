package history_test

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	apihistory "github.com/lx-wnk/kontor/server/internal/api/history"
	"github.com/lx-wnk/kontor/server/internal/auth"
	"github.com/lx-wnk/kontor/server/internal/db/ent"
	"github.com/lx-wnk/kontor/server/internal/db/repo"
	histsvc "github.com/lx-wnk/kontor/server/internal/history"
)

const testJWTSecret = "test-history-secret"

// withAuth returns a copy of req with a valid JWT cookie for user "test-user".
func withAuth(t *testing.T, req *http.Request) *http.Request {
	t.Helper()
	token, err := auth.SignJWT(auth.JWTPayload{Sub: "test-user", Login: "tester"}, testJWTSecret, 3600)
	require.NoError(t, err)
	req.AddCookie(&http.Cookie{Name: "auth_token", Value: token})
	return req
}

// newTestRouter builds a chi router with RequireAuth + the history handler.
func newTestRouter(imp *histsvc.Importer) http.Handler {
	h := apihistory.NewHandler(imp)
	r := chi.NewRouter()
	r.Group(func(r chi.Router) {
		r.Use(auth.RequireAuth(testJWTSecret))
		h.Mount(r)
	})
	return r
}

// noopRepo succeeds immediately without doing anything.
type noopRepo struct{}

func (n *noopRepo) Upsert(_ context.Context, _ []repo.AgentCostRow) error { return nil }
func (n *noopRepo) ListByTimeRange(_ context.Context, _, _ time.Time) ([]*ent.AgentCostTrend, error) {
	return nil, nil
}
func (n *noopRepo) ListSourceMtimes(_ context.Context) (map[string]int64, error) {
	return map[string]int64{}, nil
}

// TestHandler_Import_AlreadyRunning verifies that a second POST returns 409.
func TestHandler_Import_AlreadyRunning(t *testing.T) {
	started := make(chan struct{})
	unblock := make(chan struct{})

	// blockingCollect signals started then blocks until unblock is closed.
	// The multi-dir importer calls collect once per configured dir, so the
	// started-signal is guarded by sync.Once to avoid a double close.
	var startedOnce sync.Once
	blockingCollect := func(_ string) ([]string, error) {
		startedOnce.Do(func() { close(started) })
		<-unblock
		return nil, nil
	}

	imp := histsvc.NewImporter(&noopRepo{}).WithCollectFn(blockingCollect)
	router := newTestRouter(imp)

	// First POST — goroutine blocks inside blockingCollect.
	req1 := withAuth(t, httptest.NewRequest(http.MethodPost, "/api/history/import", nil))
	w1 := httptest.NewRecorder()
	router.ServeHTTP(w1, req1)
	require.Equal(t, http.StatusOK, w1.Code)

	// Wait until the goroutine is definitively inside the collect fn (no timing dependency).
	<-started

	// Second POST — must return 409 because the first goroutine is still running.
	req2 := withAuth(t, httptest.NewRequest(http.MethodPost, "/api/history/import", nil))
	w2 := httptest.NewRecorder()
	router.ServeHTTP(w2, req2)

	assert.Equal(t, http.StatusConflict, w2.Code)

	var body map[string]string
	require.NoError(t, json.NewDecoder(w2.Body).Decode(&body))
	assert.Contains(t, body["error"], "already in progress")

	// Unblock the goroutine so it can finish.
	close(unblock)
}

// TestHandler_SSE_ReceivesProgress verifies that an SSE client receives Done progress.
func TestHandler_SSE_ReceivesProgress(t *testing.T) {
	// Inject an empty collector so the import completes instantly and the test is
	// independent of the developer machine's real ~/.claude session count (a real
	// full-file scan of hundreds of sessions would blow the 2s deadline below).
	imp := histsvc.NewImporter(&noopRepo{}).WithCollectFn(func(string) ([]string, error) { return nil, nil })
	router := newTestRouter(imp)

	// Trigger an import (no files to scan — completes immediately).
	postReq := withAuth(t, httptest.NewRequest(http.MethodPost, "/api/history/import", nil))
	postW := httptest.NewRecorder()
	router.ServeHTTP(postW, postReq)
	require.Equal(t, http.StatusOK, postW.Code)

	// Give the goroutine time to finish.
	time.Sleep(50 * time.Millisecond)

	// Connect an SSE client — should receive the final Done progress immediately
	// because the job is already finished.
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	sseReq := withAuth(t, httptest.NewRequest(http.MethodGet, "/api/history/import/status", nil).WithContext(ctx))
	sseW := httptest.NewRecorder()

	done := make(chan struct{})
	go func() {
		defer close(done)
		router.ServeHTTP(sseW, sseReq)
	}()

	select {
	case <-done:
	case <-ctx.Done():
		t.Fatal("SSE stream did not close in time")
	}

	body := sseW.Body.String()
	require.True(t, strings.Contains(body, "data:"), "expected at least one SSE data line, got: %q", body)

	// Parse the last event and verify Done=true.
	scanner := bufio.NewScanner(strings.NewReader(body))
	var lastProgress histsvc.ImportProgress
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		require.NoError(t, json.Unmarshal([]byte(payload), &lastProgress))
	}
	assert.True(t, lastProgress.Done, "expected last SSE event to have Done=true")
}

// TestHandler_Import_NotWedgedByBackgroundScan is a regression test: a scheduled
// background scan holds the importer's global single-instance guard WITHOUT going
// through the handler (so it never touches the per-user currentJobs map). A manual
// POST during that window must 409, and — critically — must NOT leave a stale
// {Done:false} per-user record that permanently blocks later manual imports. After
// the background scan finishes, a fresh manual POST must succeed.
func TestHandler_Import_NotWedgedByBackgroundScan(t *testing.T) {
	started := make(chan struct{})
	unblock := make(chan struct{})
	var startedOnce sync.Once
	blockingCollect := func(_ string) ([]string, error) {
		startedOnce.Do(func() { close(started) })
		<-unblock
		return nil, nil
	}

	imp := histsvc.NewImporter(&noopRepo{}).WithCollectFn(blockingCollect)
	router := newTestRouter(imp)

	// Simulate the scheduled background scan: call Run directly (bypasses the
	// handler / currentJobs map), holding the global guard via blockingCollect.
	require.NoError(t, imp.Run(context.Background(), func(histsvc.ImportProgress) {}))
	<-started

	// Manual POST while the background scan holds the guard → 409.
	req1 := withAuth(t, httptest.NewRequest(http.MethodPost, "/api/history/import", nil))
	w1 := httptest.NewRecorder()
	router.ServeHTTP(w1, req1)
	require.Equal(t, http.StatusConflict, w1.Code)

	// Let the background scan finish and release the guard.
	close(unblock)
	require.Eventually(t, func() bool {
		req := withAuth(t, httptest.NewRequest(http.MethodPost, "/api/history/import", nil))
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		// Drain whatever goroutine this kicked off against the empty projects dir.
		return w.Code == http.StatusOK
	}, 2*time.Second, 20*time.Millisecond, "manual import must recover after the background scan, not stay wedged at 409")
}
