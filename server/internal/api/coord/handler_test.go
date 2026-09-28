package coord_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/require"

	"github.com/lx-wnk/kontor/server/internal/api/coord"
	"github.com/lx-wnk/kontor/server/internal/db"
	"github.com/lx-wnk/kontor/server/internal/db/repo"
)

// newMux spins up a chi router with a fresh in-memory DB and mounts the coord handler.
func newMux(t *testing.T) (*chi.Mux, repo.ScratchpadRepo, repo.CoordLockRepo) {
	t.Helper()
	bundle, err := db.Open(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { _ = bundle.Client.Close() })

	scratch := repo.NewScratchpadRepo(bundle.Client)
	locks := repo.NewCoordLockRepo(bundle.Client)
	h := coord.New(scratch, locks)

	mux := chi.NewRouter()
	h.Mount(mux)
	return mux, scratch, locks
}

func TestListScratchpads_ReturnsEntry(t *testing.T) {
	mux, scratch, _ := newMux(t)

	ctx := context.Background()
	require.NoError(t, scratch.Write(ctx, "ns-a", "key1", "val1", "task-x"))

	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/coord/ns-a/scratchpads", nil))
	require.Equal(t, http.StatusOK, w.Code)

	var resp map[string]any
	require.NoError(t, json.NewDecoder(w.Body).Decode(&resp))
	entries, ok := resp["entries"].([]any)
	require.True(t, ok, "entries should be an array")
	require.Len(t, entries, 1)
	entry := entries[0].(map[string]any)
	require.Equal(t, "key1", entry["key"])
	require.Equal(t, "val1", entry["value"])
}

func TestListScratchpads_EmptyNamespace(t *testing.T) {
	mux, _, _ := newMux(t)

	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/coord/empty-ns/scratchpads", nil))
	require.Equal(t, http.StatusOK, w.Code)

	var resp map[string]any
	require.NoError(t, json.NewDecoder(w.Body).Decode(&resp))
	// ent returns nil slice → JSON null for an empty namespace
	entries, _ := resp["entries"].([]any)
	require.Empty(t, entries)
}

func TestListLocks_ReturnsActiveLock(t *testing.T) {
	mux, _, locks := newMux(t)

	ctx := context.Background()
	acquired, _, _, err := locks.Acquire(ctx, "ns-b", "lock1", "task-y", 10*time.Minute)
	require.NoError(t, err)
	require.True(t, acquired)

	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/coord/ns-b/locks", nil))
	require.Equal(t, http.StatusOK, w.Code)

	var resp map[string]any
	require.NoError(t, json.NewDecoder(w.Body).Decode(&resp))
	lockList, ok := resp["locks"].([]any)
	require.True(t, ok, "locks should be an array")
	require.Len(t, lockList, 1)
	lock := lockList[0].(map[string]any)
	require.Equal(t, "lock1", lock["key"])
	require.Equal(t, "task-y", lock["owner_task_id"])
}

func TestListLocks_EmptyNamespace(t *testing.T) {
	mux, _, _ := newMux(t)

	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/coord/no-locks/locks", nil))
	require.Equal(t, http.StatusOK, w.Code)

	var resp map[string]any
	require.NoError(t, json.NewDecoder(w.Body).Decode(&resp))
	lockList, _ := resp["locks"].([]any)
	require.Empty(t, lockList)
}
