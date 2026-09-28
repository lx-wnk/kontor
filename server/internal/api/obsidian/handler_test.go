package obsidian_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	apiobsidian "github.com/lx-wnk/kontor/server/internal/api/obsidian"
	obsidianapp "github.com/lx-wnk/kontor/server/internal/apps/obsidian"
	"github.com/lx-wnk/kontor/server/internal/db"
	"github.com/lx-wnk/kontor/server/internal/db/ent"
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
	mux.HandleFunc("/{$}", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
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

	h := apiobsidian.NewHandler(obsidianapp.NewClientHolder(client), mem, gate, spaceID)
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

	h := apiobsidian.NewHandler(obsidianapp.NewClientHolder(nil), mem, gate, spaceID)
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

	h := apiobsidian.NewHandler(obsidianapp.NewClientHolder(client), mem, gate, spaceID)
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

	h := apiobsidian.NewHandler(obsidianapp.NewClientHolder(client), mem, gate, spaceID)

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

type graphVault struct {
	*httptest.Server
	mu     sync.Mutex
	calls  []string
	mtimes string
}

func newGraphVault(t *testing.T, searchStatus int) *graphVault {
	t.Helper()
	v := &graphVault{mtimes: `[{"filename":"root/b.md","result":1700000000000},{"filename":"root/a.md","result":1700000001000},{"filename":"other/x.md","result":1},{"filename":"root/pic.png","result":5}]`}
	v.Server = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		v.mu.Lock()
		v.calls = append(v.calls, r.Method+" "+r.URL.Path)
		mtimes := v.mtimes
		v.mu.Unlock()
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/search/":
			if searchStatus != http.StatusOK {
				w.WriteHeader(searchStatus)
				return
			}
			body, _ := io.ReadAll(r.Body)
			switch string(body) {
			case `{"var":"stat.mtime"}`:
				_, _ = w.Write([]byte(mtimes))
			case `{"var":"links"}`:
				_, _ = w.Write([]byte(`[{"filename":"root/a.md","result":["root/b.md","other/x.md","root/missing.md","root/a.md"]},{"filename":"other/x.md","result":["root/a.md"]}]`))
			default:
				w.WriteHeader(http.StatusBadRequest)
			}
		case r.Method == http.MethodPost && strings.HasPrefix(r.URL.Path, "/open/"):
			w.WriteHeader(http.StatusOK)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(v.Close)
	return v
}

func (v *graphVault) requests() []string {
	v.mu.Lock()
	defer v.mu.Unlock()
	return append([]string(nil), v.calls...)
}

func (v *graphVault) setMtimes(answer string) {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.mtimes = answer
}

func opened(requests []string) []string {
	var out []string
	for _, r := range requests {
		if strings.HasPrefix(r, "POST /open/") {
			out = append(out, r)
		}
	}
	return out
}

type failingGrants struct{ repo.GrantRepo }

const grantsFailure = "database is locked: grants table detail"

func (failingGrants) ListForCapability(context.Context, string) ([]*ent.Grant, error) {
	return nil, errors.New(grantsFailure)
}

func serve(h *apiobsidian.Handler, method, target, body string) *httptest.ResponseRecorder {
	r := chi.NewRouter()
	h.Mount(r)
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	return rec
}

func TestGraphAndOpen_UnconfiguredVault(t *testing.T) {
	mem, gate, spaceID := testDeps(t)
	grantCapability(t, gate.Grants, repo.CapabilityMemoryRead)
	h := apiobsidian.NewHandler(obsidianapp.NewClientHolder(nil), mem, gate, spaceID)

	graph := serve(h, http.MethodGet, "/api/obsidian/graph", "")
	assert.Equal(t, http.StatusOK, graph.Code)
	assert.JSONEq(t, `{"configured":false}`, graph.Body.String())

	open := serve(h, http.MethodPost, "/api/obsidian/open", `{"path":"a.md"}`)
	assert.Equal(t, http.StatusServiceUnavailable, open.Code)
}

func TestGraphAndOpen_MissingMemoryReadIsForbiddenAndNeverReachesTheVault(t *testing.T) {
	mem, gate, spaceID := testDeps(t)
	vault := newGraphVault(t, http.StatusOK)
	h := apiobsidian.NewHandler(obsidianapp.NewClientHolder(newTestClient(t, vault.Server)), mem, gate, spaceID)

	assert.Equal(t, http.StatusForbidden, serve(h, http.MethodGet, "/api/obsidian/graph", "").Code)
	assert.Equal(t, http.StatusForbidden, serve(h, http.MethodPost, "/api/obsidian/open", `{"path":"a.md"}`).Code)
	assert.Empty(t, vault.requests(), "the vault must never be contacted before memory.read is granted")
}

func TestGraph_ServesTheConfinedGraphAndCachesIt(t *testing.T) {
	mem, gate, spaceID := testDeps(t)
	grantCapability(t, gate.Grants, repo.CapabilityMemoryRead)
	vault := newGraphVault(t, http.StatusOK)
	h := apiobsidian.NewHandler(obsidianapp.NewClientHolder(newTestClient(t, vault.Server)), mem, gate, spaceID)

	first := serve(h, http.MethodGet, "/api/obsidian/graph", "")
	require.Equal(t, http.StatusOK, first.Code)
	assert.JSONEq(t,
		`{"configured":true,"notes":[["a.md",1700000001000],["b.md",1700000000000]],"links":[[0,1]]}`,
		first.Body.String())

	second := serve(h, http.MethodGet, "/api/obsidian/graph", "")
	require.Equal(t, http.StatusOK, second.Code)
	assert.Len(t, vault.requests(), 2, "a second request within 60 s is served from the cache")
}

func TestOpen_OpensOnlyNotesTheGraphLists(t *testing.T) {
	mem, gate, spaceID := testDeps(t)
	grantCapability(t, gate.Grants, repo.CapabilityMemoryRead)
	vault := newGraphVault(t, http.StatusOK)
	h := apiobsidian.NewHandler(obsidianapp.NewClientHolder(newTestClient(t, vault.Server)), mem, gate, spaceID)

	assert.Equal(t, http.StatusNoContent, serve(h, http.MethodPost, "/api/obsidian/open", `{"path":"a.md"}`).Code)
	assert.Contains(t, vault.requests(), "POST /open/root/a.md")

	assert.Equal(t, http.StatusNotFound, serve(h, http.MethodPost, "/api/obsidian/open", `{"path":"nope.md"}`).Code)
	assert.NotContains(t, vault.requests(), "POST /open/root/nope.md", "a path the graph does not list must never reach /open, which would create it")

	assert.Equal(t, http.StatusBadRequest, serve(h, http.MethodPost, "/api/obsidian/open", `not json`).Code)
}

func TestGraph_UpstreamFailureIsBadGatewayWithoutTheVaultURL(t *testing.T) {
	mem, gate, spaceID := testDeps(t)
	grantCapability(t, gate.Grants, repo.CapabilityMemoryRead)
	vault := newGraphVault(t, http.StatusInternalServerError)
	h := apiobsidian.NewHandler(obsidianapp.NewClientHolder(newTestClient(t, vault.Server)), mem, gate, spaceID)

	rec := serve(h, http.MethodGet, "/api/obsidian/graph", "")
	assert.Equal(t, http.StatusBadGateway, rec.Code)
	assert.NotContains(t, rec.Body.String(), vault.Listener.Addr().String())
}

func TestGraphAndOpen_GateFailureIsServerErrorNotForbidden(t *testing.T) {
	mem, gate, spaceID := testDeps(t)
	gate.Grants = failingGrants{}
	vault := newGraphVault(t, http.StatusOK)
	h := apiobsidian.NewHandler(obsidianapp.NewClientHolder(newTestClient(t, vault.Server)), mem, gate, spaceID)

	for _, rec := range []*httptest.ResponseRecorder{
		serve(h, http.MethodGet, "/api/obsidian/graph", ""),
		serve(h, http.MethodPost, "/api/obsidian/open", `{"path":"a.md"}`),
	} {
		assert.Equal(t, http.StatusInternalServerError, rec.Code, "a failed grant lookup is not a refusal")
		assert.NotContains(t, rec.Body.String(), grantsFailure)
	}
	assert.Empty(t, vault.requests())
}

func TestOpen_RefusesANoteDeletedSinceTheCachedGraph(t *testing.T) {
	mem, gate, spaceID := testDeps(t)
	grantCapability(t, gate.Grants, repo.CapabilityMemoryRead)
	vault := newGraphVault(t, http.StatusOK)
	h := apiobsidian.NewHandler(obsidianapp.NewClientHolder(newTestClient(t, vault.Server)), mem, gate, spaceID)

	require.Equal(t, http.StatusOK, serve(h, http.MethodGet, "/api/obsidian/graph", "").Code)
	vault.setMtimes(`[{"filename":"root/b.md","result":1700000000000}]`)

	assert.Equal(t, http.StatusNotFound, serve(h, http.MethodPost, "/api/obsidian/open", `{"path":"a.md"}`).Code)
	assert.Empty(t, opened(vault.requests()), "a note gone from the vault must never reach /open, which would recreate it")

	graph := serve(h, http.MethodGet, "/api/obsidian/graph", "")
	assert.JSONEq(t, `{"configured":true,"notes":[["b.md",1700000000000]],"links":[]}`, graph.Body.String(),
		"the graph open built is the one the next graph request serves")
	assert.Len(t, vault.requests(), 4)
}

func TestOpen_RefusesABodyOver4KiB(t *testing.T) {
	mem, gate, spaceID := testDeps(t)
	grantCapability(t, gate.Grants, repo.CapabilityMemoryRead)
	vault := newGraphVault(t, http.StatusOK)
	h := apiobsidian.NewHandler(obsidianapp.NewClientHolder(newTestClient(t, vault.Server)), mem, gate, spaceID)

	body := `{"path":"` + strings.Repeat("a", 4<<10) + `"}`
	assert.Equal(t, http.StatusBadRequest, serve(h, http.MethodPost, "/api/obsidian/open", body).Code)
}

func TestOpen_RefusesAnUnknownField(t *testing.T) {
	mem, gate, spaceID := testDeps(t)
	grantCapability(t, gate.Grants, repo.CapabilityMemoryRead)
	vault := newGraphVault(t, http.StatusOK)
	h := apiobsidian.NewHandler(obsidianapp.NewClientHolder(newTestClient(t, vault.Server)), mem, gate, spaceID)

	assert.Equal(t, http.StatusBadRequest,
		serve(h, http.MethodPost, "/api/obsidian/open", `{"path":"a.md","extra":true}`).Code)
}

func TestGraph_ConfiguredVaultWithNoNotesReturnsEmptyArrays(t *testing.T) {
	mem, gate, spaceID := testDeps(t)
	grantCapability(t, gate.Grants, repo.CapabilityMemoryRead)
	vault := newGraphVault(t, http.StatusOK)
	vault.setMtimes(`[]`)
	h := apiobsidian.NewHandler(obsidianapp.NewClientHolder(newTestClient(t, vault.Server)), mem, gate, spaceID)

	rec := serve(h, http.MethodGet, "/api/obsidian/graph", "")
	require.Equal(t, http.StatusOK, rec.Code)
	assert.JSONEq(t, `{"configured":true,"notes":[],"links":[]}`, rec.Body.String())
}

func TestOpen_RefusesAHeadingMarkerInThePath(t *testing.T) {
	mem, gate, spaceID := testDeps(t)
	grantCapability(t, gate.Grants, repo.CapabilityMemoryRead)
	vault := newGraphVault(t, http.StatusOK)
	h := apiobsidian.NewHandler(obsidianapp.NewClientHolder(newTestClient(t, vault.Server)), mem, gate, spaceID)

	assert.Equal(t, http.StatusBadRequest, serve(h, http.MethodPost, "/api/obsidian/open", `{"path":"a.md#Heading"}`).Code)
	assert.Empty(t, vault.requests())
}

func TestStatus_ReportsWhetherTheVaultIsConfigured(t *testing.T) {
	mem, gate, spaceID := testDeps(t)
	clients := obsidianapp.NewClientHolder(nil)
	h := apiobsidian.NewHandler(clients, mem, gate, spaceID)

	rec := serve(h, http.MethodGet, "/api/obsidian/status", "")
	require.Equal(t, http.StatusOK, rec.Code)
	assert.JSONEq(t, `{"configured":false}`, rec.Body.String())

	ts, _ := newFakeVault(t)
	clients.Set(newTestClient(t, ts))
	rec = serve(h, http.MethodGet, "/api/obsidian/status", "")
	require.Equal(t, http.StatusOK, rec.Code)
	assert.JSONEq(t, `{"configured":true,"reachable":true}`, rec.Body.String())
}

// TestStatus_UntrustedCertificateReportsTheSelfSignedHint pins the actual
// failure mode of a freshly configured vault: the Local REST API's
// self-signed certificate under TLSVerify, which index/graph/open all fail
// with a raw x509 error today. status must turn that into a hint the user
// can act on, never the raw error text (it can carry the vault URL).
func TestStatus_UntrustedCertificateReportsTheSelfSignedHint(t *testing.T) {
	mem, gate, spaceID := testDeps(t)
	ts := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(ts.Close)
	client, err := obsidianapp.NewClient(obsidianapp.Config{
		BaseURL:   "https://" + ts.Listener.Addr().String(),
		APIKey:    "secret",
		VaultRoot: "root",
		TLSMode:   obsidianapp.TLSVerify,
	})
	require.NoError(t, err)

	h := apiobsidian.NewHandler(obsidianapp.NewClientHolder(client), mem, gate, spaceID)
	rec := serve(h, http.MethodGet, "/api/obsidian/status", "")

	require.Equal(t, http.StatusOK, rec.Code)
	var body map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	assert.Equal(t, true, body["configured"])
	assert.Equal(t, false, body["reachable"])
	assert.NotContains(t, rec.Body.String(), ts.URL)
	hint, _ := body["hint"].(string)
	assert.Contains(t, hint, "self-signed certificate")
	assert.Contains(t, hint, "insecure-loopback")
}

// TestStatus_UnauthorizedReportsTheApiKeyHint pins the 401 branch: a wrong
// or revoked API key, which is otherwise indistinguishable from a network
// failure to the person reading the panel.
func TestStatus_UnauthorizedReportsTheApiKeyHint(t *testing.T) {
	mem, gate, spaceID := testDeps(t)
	ts := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	t.Cleanup(ts.Close)
	client := newTestClient(t, ts)

	h := apiobsidian.NewHandler(obsidianapp.NewClientHolder(client), mem, gate, spaceID)
	rec := serve(h, http.MethodGet, "/api/obsidian/status", "")

	require.Equal(t, http.StatusOK, rec.Code)
	var body map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	assert.Equal(t, false, body["reachable"])
	assert.Contains(t, body["hint"], "API key")
}

// TestGraph_ClientSwapInvalidatesTheCache pins that ClientHolder.Set — a live
// settings save mid-session — drops the cached graph immediately, instead of
// serving the previous vault's graph for up to graphTTL.
func TestGraph_ClientSwapInvalidatesTheCache(t *testing.T) {
	mem, gate, spaceID := testDeps(t)
	grantCapability(t, gate.Grants, repo.CapabilityMemoryRead)
	vaultA := newGraphVault(t, http.StatusOK)
	vaultB := newGraphVault(t, http.StatusOK)
	vaultB.setMtimes(`[{"filename":"root/c.md","result":1700000002000}]`)

	clients := obsidianapp.NewClientHolder(newTestClient(t, vaultA.Server))
	h := apiobsidian.NewHandler(clients, mem, gate, spaceID)

	first := serve(h, http.MethodGet, "/api/obsidian/graph", "")
	require.Equal(t, http.StatusOK, first.Code)
	assert.Contains(t, first.Body.String(), `"a.md"`)

	clients.Set(newTestClient(t, vaultB.Server))
	second := serve(h, http.MethodGet, "/api/obsidian/graph", "")
	require.Equal(t, http.StatusOK, second.Code)
	assert.JSONEq(t, `{"configured":true,"notes":[["c.md",1700000002000]],"links":[]}`, second.Body.String(),
		"a graph fetched right after a client swap must not still serve the previous vault's cached graph")
}

func TestGraph_ReadsTheClientPerRequest(t *testing.T) {
	mem, gate, spaceID := testDeps(t)
	grantCapability(t, gate.Grants, repo.CapabilityMemoryRead)
	vault := newGraphVault(t, http.StatusOK)
	clients := obsidianapp.NewClientHolder(nil)
	h := apiobsidian.NewHandler(clients, mem, gate, spaceID)

	assert.JSONEq(t, `{"configured":false}`, serve(h, http.MethodGet, "/api/obsidian/graph", "").Body.String())

	clients.Set(newTestClient(t, vault.Server))
	rec := serve(h, http.MethodGet, "/api/obsidian/graph", "")
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), `"configured":true`)
}
