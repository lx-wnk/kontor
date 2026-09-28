package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	obsidianapp "github.com/lx-wnk/kontor/server/internal/apps/obsidian"
	"github.com/lx-wnk/kontor/server/internal/db"
	"github.com/lx-wnk/kontor/server/internal/db/repo"
	mcp "github.com/lx-wnk/kontor/server/internal/mcp"
	"github.com/lx-wnk/kontor/server/internal/memory"
)

// TestObsidianToolsHaveScopeEntries proves all four Obsidian tools have a
// ToolScopeMap entry — Register panics at construction without one.
func TestObsidianToolsHaveScopeEntries(t *testing.T) {
	for _, name := range []string{"obsidian_read", "obsidian_search", "obsidian_write", "obsidian_delete"} {
		if _, ok := mcp.ToolScopeMap[name]; !ok {
			t.Errorf("%s has no ToolScopeMap entry — Register panics at construction without one", name)
		}
	}
}

// TestObsidianScopesAreGrantableToKeys asserts both new scopes appear in the
// set an API key may be granted directly, mirroring
// TestMemoryScopesAreGrantableToKeys.
func TestObsidianScopesAreGrantableToKeys(t *testing.T) {
	require.True(t, validKeyScopes["obsidian:read"], "obsidian:read must be directly grantable to a key")
	require.True(t, validKeyScopes["obsidian:write"], "obsidian:write must be directly grantable to a key")
}

// TestKeysManageImpliesObsidianScopes asserts the expansion of keys:manage
// contains both obsidian scopes explicitly — scopeImplies is one level
// deep, not transitive, so keys:manage's own entry must list them by hand.
func TestKeysManageImpliesObsidianScopes(t *testing.T) {
	resolved := mcp.ResolveScopes([]string{"keys:manage"})
	require.True(t, resolved["obsidian:read"], "keys:manage must imply obsidian:read")
	require.True(t, resolved["obsidian:write"], "keys:manage must imply obsidian:write")
}

// TestObsidianWriteImpliesObsidianRead asserts scopeImplies directly for the
// obsidian:write -> obsidian:read edge, not just through keys:manage's
// aggregate expansion above. A dropped row here would leave every
// write-only key unable to call obsidian_read, uncaught by the keys:manage
// test alone.
func TestObsidianWriteImpliesObsidianRead(t *testing.T) {
	resolved := mcp.ResolveScopes([]string{"obsidian:write"})
	require.True(t, resolved["obsidian:read"], "obsidian:write must imply obsidian:read")
}

// newFakeObsidianVault serves a minimal Obsidian Local REST API and records
// whether it was ever contacted, mirroring internal/api/obsidian/handler_test.go's
// newFakeVault. A test uses the returned bool to prove the gate denied a
// call before the vault client ever made a request.
//
// Search reports two notes: "root/a.md" inside the configured VaultRoot and
// "elsewhere/secret.md" outside it. The upstream endpoint is vault-wide by
// design, so a fake that only ever returned in-root hits could not tell a
// confining caller from a non-confining one.
func newFakeObsidianVault(t *testing.T) (*httptest.Server, *bool) {
	t.Helper()
	called := false
	mux := http.NewServeMux()
	mux.HandleFunc("/search/simple/", func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode([]map[string]any{
			{"filename": "root/a.md", "score": 1},
			{"filename": "elsewhere/secret.md", "score": 0.9},
		})
	})
	mux.HandleFunc("/vault/", func(w http.ResponseWriter, r *http.Request) {
		called = true
		if r.Method == http.MethodDelete {
			w.WriteHeader(http.StatusOK)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("hello from the vault"))
	})
	ts := httptest.NewTLSServer(mux)
	t.Cleanup(ts.Close)
	return ts, &called
}

func newTestObsidianClient(t *testing.T, ts *httptest.Server) *obsidianapp.Client {
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

// newObsidianDepsForTest wires ObsidianDeps against an in-memory database
// and a fake vault server that records whether it was reached. Capabilities
// are deliberately left unseeded — no repo.SeedCapabilities and no
// obsidianapp.Register call — mirroring newMemoryDepsForTest, so a test can
// exercise the "capability never catalogued, therefore denied" path
// explicitly. The Gate carries no Asker, same as newMemoryDepsForTest's.
func newObsidianDepsForTest(t *testing.T) (ObsidianDeps, repo.GrantRepo, repo.CapabilityRepo, context.Context, *bool) {
	t.Helper()
	bundle, err := db.Open(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { _ = bundle.Client.Close() })

	capabilities := repo.NewCapabilityRepo(bundle.Client)
	grants := repo.NewGrantRepo(bundle.Client)

	ts, called := newFakeObsidianVault(t)
	deps := ObsidianDeps{
		Clients: obsidianapp.NewClientHolder(newTestObsidianClient(t, ts)),
		Gate: memory.Gate{
			Capabilities: capabilities,
			Grants:       grants,
			GrantUsage:   repo.NewGrantUsageRepo(bundle.Client, bundle.WriteClient),
		},
	}
	return deps, grants, capabilities, context.Background(), called
}

// obsidianTestDepsWithCatalogue mirrors internal/api/obsidian/handler_test.go's
// testDeps: it runs obsidianapp.Register so the four obsidian.* capabilities
// are catalogued as class "reach" (rather than left unseeded), letting a
// test exercise the "catalogued, no grant" ask path distinctly from
// newObsidianDepsForTest's "never catalogued" deny path.
func obsidianTestDepsWithCatalogue(t *testing.T) (ObsidianDeps, repo.GrantRepo, context.Context, *bool) {
	t.Helper()
	bundle, err := db.Open(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { _ = bundle.Client.Close() })

	capabilities := repo.NewCapabilityRepo(bundle.Client)
	resources := repo.NewResourceRepo(bundle.Client)
	ctx := context.Background()
	require.NoError(t, obsidianapp.Register(ctx, resources, capabilities))

	grants := repo.NewGrantRepo(bundle.Client)
	ts, called := newFakeObsidianVault(t)
	deps := ObsidianDeps{
		Clients: obsidianapp.NewClientHolder(newTestObsidianClient(t, ts)),
		// No Asker: proves the ask-effect path fails closed
		// (capability.ErrAskRequired) at the unit-test level, the same way
		// memory's equivalent test does. Production wiring (di_mcp.go)
		// carries a real Asker instead — see ObsidianDeps' own doc comment.
		Gate: memory.Gate{Capabilities: capabilities, Grants: grants, GrantUsage: repo.NewGrantUsageRepo(bundle.Client, bundle.WriteClient)},
	}
	return deps, grants, ctx, called
}

// newObsidianDepsWithCaller mirrors obsidianTestDepsWithCatalogue but also
// wires Caller against the same database, and returns the bundle so a test
// can seed a stage run and prove the credential's task/routine context
// reaches the gate.
func newObsidianDepsWithCaller(t *testing.T) (ObsidianDeps, repo.GrantRepo, *db.DBBundle, context.Context, *bool) {
	t.Helper()
	bundle, err := db.Open(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { _ = bundle.Client.Close() })

	capabilities := repo.NewCapabilityRepo(bundle.Client)
	resources := repo.NewResourceRepo(bundle.Client)
	ctx := context.Background()
	require.NoError(t, obsidianapp.Register(ctx, resources, capabilities))

	grants := repo.NewGrantRepo(bundle.Client)
	ts, called := newFakeObsidianVault(t)
	deps := ObsidianDeps{
		Clients: obsidianapp.NewClientHolder(newTestObsidianClient(t, ts)),
		Caller: mcp.CallerResolver{
			StageRuns: repo.NewStageRunRepo(bundle.Client),
			Tasks:     repo.NewTaskRepo(bundle.Client),
		},
		Gate: memory.Gate{Capabilities: capabilities, Grants: grants, GrantUsage: repo.NewGrantUsageRepo(bundle.Client, bundle.WriteClient)},
	}
	return deps, grants, bundle, ctx, called
}

// seedObsidianRun creates a task (optionally bound to a routine) plus one
// stage run on it, mirroring internal/mcp/caller_test.go's seedRun, and
// returns the stage run's id.
func seedObsidianRun(t *testing.T, bundle *db.DBBundle, slug, routineID string) string {
	t.Helper()
	ctx := context.Background()
	tasks := repo.NewTaskRepo(bundle.Client)
	runs := repo.NewStageRunRepo(bundle.Client)

	in := repo.CreateTaskInput{
		Slug: slug, Title: slug, Cwd: "/tmp",
		CurrentStage: "implementation", Priority: "medium",
		MaxIterations: 5, StageTimeoutSeconds: 60,
	}
	if routineID != "" {
		in.RoutineID = &routineID
	}
	task, err := tasks.Create(ctx, in)
	require.NoError(t, err)

	run, err := runs.Create(ctx, repo.CreateStageRunInput{TaskID: task.ID, Stage: "implementation"})
	require.NoError(t, err)
	return run.ID
}

// TestObsidianRoutineGrantAppliesOnlyToThatRoutine is the whole point of
// this change, in one test: one grant, two callers, one difference. A grant
// scoped to routine "sched-1" allows a call carrying a stage-run credential
// whose task that routine materialized, denies a call whose task has no
// routine, and denies a call carrying an ordinary machine-wide key (no stage
// run at all).
//
// One case per tool, because each tool passes d.Caller.Contexts(ctx) to its
// own Authorize call: dropping it from any single one of them compiles, vets
// and — with only obsidian_write covered — passed.
func TestObsidianRoutineGrantAppliesOnlyToThatRoutine(t *testing.T) {
	cases := []struct {
		tool       string
		capability string
		args       map[string]any
	}{
		{"obsidian_read", obsidianapp.CapabilityRead, map[string]any{"path": "notes/a.md"}},
		{"obsidian_search", obsidianapp.CapabilitySearch, map[string]any{"query": "anything"}},
		{"obsidian_write", obsidianapp.CapabilityWrite, map[string]any{"path": "notes/a.md", "content": "x"}},
		{"obsidian_delete", obsidianapp.CapabilityDelete, map[string]any{"path": "notes/a.md"}},
	}
	for _, tc := range cases {
		t.Run(tc.tool, func(t *testing.T) {
			deps, grants, bundle, ctx, _ := newObsidianDepsWithCaller(t)
			_, err := grants.Create(ctx, repo.CreateGrantInput{
				CapabilityName: tc.capability,
				Context:        repo.GrantContextFor(repo.GrantContextRoutine, "sched-1"),
				Pattern:        "*",
				Mode:           repo.GrantModeAllow,
				GrantedBy:      "test",
			})
			require.NoError(t, err)

			runFromRoutine := seedObsidianRun(t, bundle, "from-routine", "sched-1")
			runFromHuman := seedObsidianRun(t, bundle, "hand-made", "")

			registry := mcp.ToolRegistry{}
			RegisterObsidianTools(registry, deps)

			// The routine's own task may call it.
			_, err = registry[tc.tool].Handler(
				mcp.ContextWithAuth(ctx, &mcp.MCPAuthInfo{StageRunID: runFromRoutine}), tc.args)
			require.NoError(t, err)

			// A task the same routine did not start may not.
			_, err = registry[tc.tool].Handler(
				mcp.ContextWithAuth(ctx, &mcp.MCPAuthInfo{StageRunID: runFromHuman}), tc.args)
			require.Error(t, err)

			// Neither may the machine-wide key.
			_, err = registry[tc.tool].Handler(
				mcp.ContextWithAuth(ctx, &mcp.MCPAuthInfo{KeyID: "user-key"}), tc.args)
			require.Error(t, err)
		})
	}
}

// TestObsidianDeleteDeniedBeforeVaultCall is the regression test decision D8
// exists for: obsidian.delete is never catalogued here (repo.SeedCapabilities
// does not seed it — only Claude Code tool names and the memory
// capabilities), so the zero-value CapabilityView's empty Class resolves to
// deny before the client is ever touched.
func TestObsidianDeleteDeniedBeforeVaultCall(t *testing.T) {
	deps, _, capRepo, ctx, vaultCalled := newObsidianDepsForTest(t)
	repo.SeedCapabilities(ctx, capRepo)
	registry := mcp.ToolRegistry{}
	RegisterObsidianTools(registry, deps)

	_, err := registry["obsidian_delete"].Handler(ctx, map[string]any{"path": "notes/a.md"})
	require.Error(t, err)
	// Pins the deny path specifically: if obsidian.delete were ever added to
	// SeedCapabilities, this would silently flip to the ask path
	// (capability.ErrAskRequired) while require.Error alone stayed green.
	require.ErrorContains(t, err, "capability denied")
	assert.False(t, *vaultCalled, "the vault must not be touched before the gate allows it")
}

// TestObsidianToolsDenyBeforeVaultCallWhenCapabilityUncatalogued extends the
// same proof to obsidian_read, obsidian_search and obsidian_write — write
// and delete are the two operations decision D8 calls out as irreversible,
// so both must be shown, not just delete.
func TestObsidianToolsDenyBeforeVaultCallWhenCapabilityUncatalogued(t *testing.T) {
	cases := []struct {
		tool string
		args map[string]any
	}{
		{"obsidian_read", map[string]any{"path": "notes/a.md"}},
		{"obsidian_search", map[string]any{"query": "anything"}},
		{"obsidian_write", map[string]any{"path": "notes/a.md", "content": "hello"}},
	}
	for _, tc := range cases {
		t.Run(tc.tool, func(t *testing.T) {
			deps, _, capRepo, ctx, vaultCalled := newObsidianDepsForTest(t)
			repo.SeedCapabilities(ctx, capRepo)
			registry := mcp.ToolRegistry{}
			RegisterObsidianTools(registry, deps)

			_, err := registry[tc.tool].Handler(ctx, tc.args)
			require.Error(t, err)
			require.ErrorContains(t, err, tc.tool)
			// Same pin as TestObsidianDeleteDeniedBeforeVaultCall: without
			// this, a future SeedCapabilities change could silently swap
			// deny for ask here and every assertion above would stay green.
			require.ErrorContains(t, err, "capability denied")
			assert.False(t, *vaultCalled, "the vault must not be touched before the gate allows it")
		})
	}
}

// TestObsidianWriteDeniedWithoutGrantEvenWhenCapabilityCatalogued proves the
// second sentinel this task's brief calls out: with obsidian.write
// catalogued (class "reach", via obsidianapp.Register) but no grant at all,
// Decide has no candidate and defaultEffect("reach") resolves to "ask" — and
// with no Asker wired, ServerEnforcer fails closed with
// capability.ErrAskRequired, not capability.ErrDenied. This is the ask path;
// TestObsidianDeleteDeniedBeforeVaultCall above is the deny path — both must
// be reachable and both must be covered.
func TestObsidianWriteDeniedWithoutGrantEvenWhenCapabilityCatalogued(t *testing.T) {
	deps, _, ctx, called := obsidianTestDepsWithCatalogue(t)
	registry := mcp.ToolRegistry{}
	RegisterObsidianTools(registry, deps)

	_, err := registry["obsidian_write"].Handler(ctx, map[string]any{"path": "notes/a.md", "content": "hi"})
	require.Error(t, err)
	require.ErrorContains(t, err, "obsidian_write")
	require.ErrorContains(t, err, "no asker is configured",
		"a catalogued reach-class capability with no grant resolves to ask, not deny — this must be capability.ErrAskRequired, not ErrDenied")
	assert.False(t, *called, "the vault must not be touched while the ask is unanswerable")
}

// TestObsidianToolsFollowClientHolderLiveness pins the still-standing
// decision (see obsidianAvailable's doc comment) with the live-swap
// mechanism that replaced the old "don't register while nil" one: an agent
// must never discover a tool it cannot use, so the four tools are neither
// listed in tools/list nor callable via tools/call while the vault client
// holder is nil, and become both the moment the holder is Set to a client —
// with no server restart in between.
func TestObsidianToolsFollowClientHolderLiveness(t *testing.T) {
	bundle, err := db.Open(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { _ = bundle.Client.Close() })

	capabilities := repo.NewCapabilityRepo(bundle.Client)
	resources := repo.NewResourceRepo(bundle.Client)
	ctx := context.Background()
	require.NoError(t, obsidianapp.Register(ctx, resources, capabilities))
	grants := repo.NewGrantRepo(bundle.Client)

	holder := obsidianapp.NewClientHolder(nil)
	deps := ObsidianDeps{
		Clients: holder,
		Gate:    memory.Gate{Capabilities: capabilities, Grants: grants, GrantUsage: repo.NewGrantUsageRepo(bundle.Client, bundle.WriteClient)},
	}
	registry := mcp.ToolRegistry{}
	RegisterObsidianTools(registry, deps)
	mustAllowMemoryGrant(t, grants, ctx, obsidianapp.CapabilityRead)

	handler := mcp.MCPHandler(registry, nil, nil)
	auth := &mcp.MCPAuthInfo{KeyID: "test-key", Scopes: mcp.ResolveScopes([]string{"obsidian:read"})}

	assert.False(t, listedTools(t, handler)["obsidian_read"], "obsidian_read must not be listed while the vault client is nil")
	callErr := callToolRPC(t, handler, auth, "obsidian_read", map[string]any{"path": "a.md"})
	require.NotNil(t, callErr, "obsidian_read must refuse a call while the vault client is nil")
	assert.Equal(t, float64(-32601), callErr["code"], "an unavailable tool must answer exactly like an unknown one")

	ts, _ := newFakeObsidianVault(t)
	holder.Set(newTestObsidianClient(t, ts))

	assert.True(t, listedTools(t, handler)["obsidian_read"], "obsidian_read must be listed once the vault client is set")
	callErr = callToolRPC(t, handler, auth, "obsidian_read", map[string]any{"path": "a.md"})
	assert.Nil(t, callErr, "obsidian_read must be callable once the vault client is set, got: %v", callErr)
}

// listedTools posts tools/list and returns the set of tool names present.
func listedTools(t *testing.T, handler http.HandlerFunc) map[string]bool {
	t.Helper()
	resp := postMCP(t, handler, nil, map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "tools/list", "params": map[string]any{},
	})
	result := resp["result"].(map[string]any)
	names := make(map[string]bool)
	for _, tool := range result["tools"].([]any) {
		names[tool.(map[string]any)["name"].(string)] = true
	}
	return names
}

// callToolRPC posts tools/call and returns the RPC error object, or nil on success.
func callToolRPC(t *testing.T, handler http.HandlerFunc, auth *mcp.MCPAuthInfo, name string, args map[string]any) map[string]any {
	t.Helper()
	resp := postMCP(t, handler, auth, map[string]any{
		"jsonrpc": "2.0", "id": 2, "method": "tools/call",
		"params": map[string]any{"name": name, "arguments": args},
	})
	rpcErr, _ := resp["error"].(map[string]any)
	return rpcErr
}

func postMCP(t *testing.T, handler http.HandlerFunc, auth *mcp.MCPAuthInfo, body map[string]any) map[string]any {
	t.Helper()
	b, err := json.Marshal(body)
	require.NoError(t, err)
	req := httptest.NewRequest(http.MethodPost, "/api/mcp", bytes.NewReader(b))
	if auth != nil {
		req = req.WithContext(mcp.ContextWithAuth(req.Context(), auth))
	}
	rec := httptest.NewRecorder()
	handler(rec, req)
	var resp map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	return resp
}

func TestObsidianReadSucceedsWithAnAllowGrant(t *testing.T) {
	deps, grants, ctx, called := obsidianTestDepsWithCatalogue(t)
	mustAllowMemoryGrant(t, grants, ctx, obsidianapp.CapabilityRead)

	registry := mcp.ToolRegistry{}
	RegisterObsidianTools(registry, deps)

	out := invokeCoordTool(t, registry, ctx, "obsidian_read", map[string]any{"path": "a.md"})
	assert.Equal(t, "hello from the vault", out["content"])
	assert.True(t, *called, "an allowed read must reach the vault")
}

func TestObsidianSearchSucceedsWithAnAllowGrant(t *testing.T) {
	deps, grants, ctx, called := obsidianTestDepsWithCatalogue(t)
	mustAllowMemoryGrant(t, grants, ctx, obsidianapp.CapabilitySearch)

	registry := mcp.ToolRegistry{}
	RegisterObsidianTools(registry, deps)

	out := invokeCoordTool(t, registry, ctx, "obsidian_search", map[string]any{"query": "anything"})
	results, ok := out["results"].([]any)
	require.True(t, ok, "results must be a list, got %T", out["results"])
	require.Len(t, results, 1, "only the in-root note may be reported; the fake vault also returns one outside VaultRoot")
	assert.True(t, *called, "an allowed search must reach the vault")
}

// TestObsidianSearchDropsResultsOutsideVaultRoot is the regression test for
// the whole-branch review's search-confinement finding: Client.Search is
// vault-wide, so returning its results verbatim let one obsidian.search
// grant enumerate note paths anywhere in the vault — past the boundary
// resolveVaultPath calls "a boundary, not a suggestion". A follow-up
// obsidian_read of such a path is refused, so the content never leaks, but
// the filename and the note's existence do.
//
// The reported path must also be the root-relative one: that is the form
// obsidian_read accepts, so a vault-full path would be a result the agent
// cannot act on.
func TestObsidianSearchDropsResultsOutsideVaultRoot(t *testing.T) {
	deps, grants, ctx, called := obsidianTestDepsWithCatalogue(t)
	mustAllowMemoryGrant(t, grants, ctx, obsidianapp.CapabilitySearch)

	registry := mcp.ToolRegistry{}
	RegisterObsidianTools(registry, deps)

	out := invokeCoordTool(t, registry, ctx, "obsidian_search", map[string]any{"query": "anything"})
	require.True(t, *called, "the search must actually have reached the vault")

	raw, err := json.Marshal(out["results"])
	require.NoError(t, err)
	var results []obsidianapp.SearchResult
	require.NoError(t, json.Unmarshal(raw, &results))

	require.Len(t, results, 1)
	assert.Equal(t, "a.md", results[0].Path, "the in-root hit must be reported root-relative, the form obsidian_read accepts")
	assert.NotContains(t, string(raw), "secret.md", "a note outside VaultRoot must not be named at all")
}

// TestObsidianWriteSucceedsWithAnAllowGrant is the D8 write half of this
// task: obsidian.write is class "reach" and Reversible:false — a grant here
// is a human deliberately authorizing a destructive action, not a default.
func TestObsidianWriteSucceedsWithAnAllowGrant(t *testing.T) {
	deps, grants, ctx, called := obsidianTestDepsWithCatalogue(t)
	mustAllowMemoryGrant(t, grants, ctx, obsidianapp.CapabilityWrite)

	registry := mcp.ToolRegistry{}
	RegisterObsidianTools(registry, deps)

	out := invokeCoordTool(t, registry, ctx, "obsidian_write", map[string]any{"path": "a.md", "content": "hello"})
	assert.Equal(t, "a.md", out["path"])
	assert.True(t, *called, "an allowed write must reach the vault")
}

// TestObsidianDeleteSucceedsWithAnAllowGrant is the D8 delete half: the
// gate this whole task exists to prove is exercised against the one
// operation that cannot be undone.
func TestObsidianDeleteSucceedsWithAnAllowGrant(t *testing.T) {
	deps, grants, ctx, called := obsidianTestDepsWithCatalogue(t)
	mustAllowMemoryGrant(t, grants, ctx, obsidianapp.CapabilityDelete)

	registry := mcp.ToolRegistry{}
	RegisterObsidianTools(registry, deps)

	out := invokeCoordTool(t, registry, ctx, "obsidian_delete", map[string]any{"path": "a.md"})
	assert.Equal(t, "a.md", out["path"])
	assert.Equal(t, true, out["deleted"])
	assert.True(t, *called, "an allowed delete must reach the vault")
}

// TestObsidianToolsRefuseTraversalEscapingAPatternNarrowedGrant is the
// regression test for fix round 1's CRITICAL finding: the gate and the
// vault client must agree on the same target. Client.Read/Write/Delete
// normalize notePath through resolveVaultPath before building a request,
// but capability.Match (pattern.go) is a plain strings.HasPrefix over
// whatever string it is handed — so a raw, un-normalized
// "notes/../secrets/keys.md" passes a "notes/*" grant on the strength of
// its literal prefix, then resolves to "secrets/keys.md" once it reaches
// the client, a target the grant never covered. Client.NormalizeNotePath is
// the fix: every handler below normalizes once and hands the SAME string to
// both Authorize and the client, so what the gate approves is exactly what
// the vault receives.
func TestObsidianToolsRefuseTraversalEscapingAPatternNarrowedGrant(t *testing.T) {
	cases := []struct {
		tool string
		cap  string
		args map[string]any
	}{
		{"obsidian_read", obsidianapp.CapabilityRead, map[string]any{"path": "notes/../secrets/keys.md"}},
		{"obsidian_write", obsidianapp.CapabilityWrite, map[string]any{"path": "notes/../secrets/keys.md", "content": "pwned"}},
		{"obsidian_delete", obsidianapp.CapabilityDelete, map[string]any{"path": "notes/../secrets/keys.md"}},
	}
	for _, tc := range cases {
		t.Run(tc.tool, func(t *testing.T) {
			deps, grants, ctx, called := obsidianTestDepsWithCatalogue(t)
			_, err := grants.Create(ctx, repo.CreateGrantInput{
				CapabilityName: tc.cap,
				Context:        repo.GrantContextFor(repo.GrantContextGlobal, ""),
				Pattern:        "notes/*",
				Mode:           repo.GrantModeAllow,
				GrantedBy:      "test",
			})
			require.NoError(t, err)

			registry := mcp.ToolRegistry{}
			RegisterObsidianTools(registry, deps)

			_, err = registry[tc.tool].Handler(ctx, tc.args)
			require.Error(t, err)
			// The grant exists for this capability but its "notes/*"
			// pattern never matches the normalized "secrets/keys.md", so
			// Decide has no matching candidate and falls to
			// defaultEffect("reach") = ask; with no Asker wired here that
			// surfaces as capability.ErrAskRequired, not ErrDenied — the
			// same two-sentinel distinction TestObsidianWriteDeniedWithoutGrantEvenWhenCapabilityCatalogued
			// covers. Which sentinel fires is not the property under test;
			// that the vault is never touched is.
			require.ErrorContains(t, err, tc.tool)
			assert.False(t, *called, "a path that only starts with the grant's prefix before traversal is collapsed must never reach the vault")
		})
	}
}

// TestObsidianDeleteAllowsATraversalThatStillLandsInTheGrantedSubtree
// proves the fix does not simply reject every ".." — a request whose
// normalized form still falls inside the granted pattern must succeed, the
// same way TestObsidianDeleteSucceedsWithAnAllowGrant already does for the
// no-traversal case. This is what distinguishes "normalize and compare" from
// "reject any path containing .."; the brief was explicit that the former,
// not the latter, is required.
func TestObsidianDeleteAllowsATraversalThatStillLandsInTheGrantedSubtree(t *testing.T) {
	deps, grants, ctx, called := obsidianTestDepsWithCatalogue(t)
	_, err := grants.Create(ctx, repo.CreateGrantInput{
		CapabilityName: obsidianapp.CapabilityDelete,
		Context:        repo.GrantContextFor(repo.GrantContextGlobal, ""),
		Pattern:        "notes/*",
		Mode:           repo.GrantModeAllow,
		GrantedBy:      "test",
	})
	require.NoError(t, err)

	registry := mcp.ToolRegistry{}
	RegisterObsidianTools(registry, deps)

	out := invokeCoordTool(t, registry, ctx, "obsidian_delete", map[string]any{"path": "notes/sub/../a.md"})
	assert.Equal(t, "notes/a.md", out["path"], "the normalized path, not the raw one, is what the tool reports and acts on")
	assert.True(t, *called, "a traversal that still resolves inside the granted subtree must reach the vault")
}
