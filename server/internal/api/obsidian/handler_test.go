package obsidian_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	apiobsidian "github.com/lx-wnk/kontor/server/internal/api/obsidian"
	obsidianapp "github.com/lx-wnk/kontor/server/internal/apps/obsidian"
	"github.com/lx-wnk/kontor/server/internal/db"
	"github.com/lx-wnk/kontor/server/internal/db/repo"
	"github.com/lx-wnk/kontor/server/internal/memory"
)

// testDeps wires the repos the handler needs against a fresh in-memory
// SQLite database, with the capability catalogue seeded the same way
// obsidian's own index_test.go does — a bare capability class ("resource"
// for memory.write, "reach" for the obsidian.* caps), no grants. That is
// the real fresh-install shape: obsidian.Register and repo.SeedCapabilities
// both run unconditionally at boot, long before any grant exists.
func testDeps(t *testing.T) (mem repo.MemoryRepo, gate memory.Gate, spaceID string) {
	t.Helper()
	bundle, err := db.Open(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { _ = bundle.Client.Close() })

	capRepo := repo.NewCapabilityRepo(bundle.Client)
	repo.SeedCapabilities(context.Background(), capRepo)
	resources := repo.NewResourceRepo(bundle.Client)
	require.NoError(t, obsidianapp.Register(context.Background(), resources, capRepo))

	mem = repo.NewMemoryRepo(bundle.Client, bundle.WriteClient)
	space, err := mem.CreateSpace(context.Background(), repo.CreateSpaceInput{
		Slug: "obsidian", Name: "Obsidian", Scope: repo.GlobalScope(),
	})
	require.NoError(t, err)

	grants := repo.NewGrantRepo(bundle.Client)
	grantUsage := repo.NewGrantUsageRepo(bundle.Client, bundle.WriteClient)
	// gate deliberately carries no Asker, matching how di.go builds it for
	// this handler in production.
	gate = memory.Gate{Capabilities: capRepo, Grants: grants, GrantUsage: grantUsage}
	return mem, gate, space.ID
}

func grantCapability(t *testing.T, grants repo.GrantRepo, capName string) {
	t.Helper()
	_, err := grants.Create(context.Background(), repo.CreateGrantInput{
		CapabilityName: capName,
		Context:        repo.GrantContextFor(repo.GrantContextGlobal, ""),
		Pattern:        "",
		Mode:           repo.GrantModeAllow,
		GrantedBy:      "test",
	})
	require.NoError(t, err)
}

// newFakeVault serves a minimal Obsidian Local REST API: one note under
// "root/". It records whether it was ever contacted, so a test can prove
// the denial path never reaches the vault.
func newFakeVault(t *testing.T) (*httptest.Server, *bool) {
	t.Helper()
	called := false
	mux := http.NewServeMux()
	mux.HandleFunc("/search/simple/", func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode([]map[string]any{{"filename": "root/a.md", "score": 1}})
	})
	mux.HandleFunc("/vault/", func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("hello from the vault"))
	})
	ts := httptest.NewTLSServer(mux)
	t.Cleanup(ts.Close)
	return ts, &called
}

func newTestClient(t *testing.T, ts *httptest.Server) *obsidianapp.Client {
	t.Helper()
	client, err := obsidianapp.NewClient(obsidianapp.Config{
		BaseURL:   "https://" + ts.Listener.Addr().String(),
		APIKey:    "secret",
		VaultRoot: "root",
		TLSMode:   obsidianapp.TLSPinned,
	})
	require.NoError(t, err)
	return client
}

func doPost(h *apiobsidian.Handler) *httptest.ResponseRecorder {
	r := chi.NewRouter()
	h.Mount(r)
	req := httptest.NewRequest(http.MethodPost, "/api/obsidian/index", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	return rec
}

// TestIndex_MissingGrantIsForbiddenNotServerError pins the trap this task's
// brief names explicitly: a fresh install has the capability catalogue
// seeded but no grant, which resolves to "ask" for a class-"resource" or
// class-"reach" capability (capability.Decide's defaultEffect) — and this
// handler's Gate is built with no Asker, so ServerEnforcer.Enforce returns
// capability.ErrAskRequired, not capability.ErrDenied. Both must map to 403;
// missing either one is exactly how this would regress to a 500.
func TestIndex_MissingGrantIsForbiddenNotServerError(t *testing.T) {
	mem, gate, spaceID := testDeps(t)
	ts, called := newFakeVault(t)
	client := newTestClient(t, ts)

	h := apiobsidian.NewHandler(client, mem, gate, spaceID)
	rec := doPost(h)

	assert.Equal(t, http.StatusForbidden, rec.Code)
	assert.NotEqual(t, http.StatusInternalServerError, rec.Code)
	assert.False(t, *called, "the vault must never be contacted before the grant is checked")

	var body struct {
		Error string `json:"error"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	assert.Contains(t, body.Error, repo.CapabilityMemoryWrite,
		"the 403 must name which capability is missing (memory.write, checked first), not just say \"forbidden\"")
}

// TestIndex_VaultUnconfiguredIsServiceUnavailableNotServerError grants every
// capability IndexNotes checks, so a handler that forgot the nil-client
// guard would reach obsidianapp.IndexNotes and dereference a nil client
// instead of never getting that far — proving the guard is load-bearing,
// not simply unreached because the auth check denies first.
func TestIndex_VaultUnconfiguredIsServiceUnavailableNotServerError(t *testing.T) {
	mem, gate, spaceID := testDeps(t)
	grantCapability(t, gate.Grants, repo.CapabilityMemoryWrite)
	grantCapability(t, gate.Grants, obsidianapp.CapabilitySearch)
	grantCapability(t, gate.Grants, obsidianapp.CapabilityRead)

	h := apiobsidian.NewHandler(nil, mem, gate, spaceID)
	rec := doPost(h)

	assert.Equal(t, http.StatusServiceUnavailable, rec.Code)
}

func TestIndex_GrantedRunReturnsIndexedCount(t *testing.T) {
	mem, gate, spaceID := testDeps(t)
	ts, _ := newFakeVault(t)
	client := newTestClient(t, ts)

	grantsRepo := gate.Grants
	grantCapability(t, grantsRepo, repo.CapabilityMemoryWrite)
	grantCapability(t, grantsRepo, obsidianapp.CapabilitySearch)
	grantCapability(t, grantsRepo, obsidianapp.CapabilityRead)

	h := apiobsidian.NewHandler(client, mem, gate, spaceID)
	rec := doPost(h)

	require.Equal(t, http.StatusOK, rec.Code)
	var body struct {
		Indexed int `json:"indexed"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	assert.Equal(t, 1, body.Indexed)
}

// TestIndex_ConcurrentRunsAreSerialized pins fix-round finding BLOCKING 2:
// two overlapping POSTs against a one-note vault must not both run
// IndexNotes to completion — that duplicates every pointer permanently
// (no unique index on (space_id, source_ref), and the reconciliation loop
// only ever tracks one id per path, so the extra entry can never be
// reconciled away). The fake vault's /search/simple/ blocks until released,
// so the first request can be held mid-flight while the second is fired.
func TestIndex_ConcurrentRunsAreSerialized(t *testing.T) {
	mem, gate, spaceID := testDeps(t)
	grantCapability(t, gate.Grants, repo.CapabilityMemoryWrite)
	grantCapability(t, gate.Grants, obsidianapp.CapabilitySearch)
	grantCapability(t, gate.Grants, obsidianapp.CapabilityRead)

	unblock := make(chan struct{})
	started := make(chan struct{})
	var startedOnce sync.Once
	mux := http.NewServeMux()
	mux.HandleFunc("/search/simple/", func(w http.ResponseWriter, r *http.Request) {
		startedOnce.Do(func() { close(started) })
		<-unblock
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode([]map[string]any{{"filename": "root/a.md", "score": 1}})
	})
	mux.HandleFunc("/vault/", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("hello from the vault"))
	})
	ts := httptest.NewTLSServer(mux)
	t.Cleanup(ts.Close)
	client := newTestClient(t, ts)

	h := apiobsidian.NewHandler(client, mem, gate, spaceID)

	firstDone := make(chan *httptest.ResponseRecorder, 1)
	go func() { firstDone <- doPost(h) }()

	<-started // the first request is now blocked inside IndexNotes' Search call
	rec2 := doPost(h)

	close(unblock)
	rec1 := <-firstDone

	assert.Equal(t, http.StatusOK, rec1.Code)
	assert.Equal(t, http.StatusConflict, rec2.Code, "a run already in flight must reject a second trigger, not race it")

	entries, err := mem.ListValid(context.Background(), spaceID, time.Now())
	require.NoError(t, err)
	assert.Len(t, entries, 1, "two overlapping runs must leave exactly one pointer, not a duplicate")
}
