package resources_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/require"

	apiresources "github.com/lx-wnk/kontor/server/internal/api/resources"
	"github.com/lx-wnk/kontor/server/internal/db"
	"github.com/lx-wnk/kontor/server/internal/db/repo"
	"github.com/lx-wnk/kontor/server/internal/memory"
)

// newUngrantedMux wires an apiresources.Handler against a real in-memory
// SQLite database with the capability catalogue seeded and no grant at all —
// the state a fresh install is in.
func newUngrantedMux(t *testing.T) (*chi.Mux, repo.ResourceRepo, repo.GrantRepo, context.Context) {
	t.Helper()
	bundle, err := db.Open(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { _ = bundle.Client.Close() })

	ctx := context.Background()
	repo.SeedCapabilities(ctx, repo.NewCapabilityRepo(bundle.Client))
	resourceRepo := repo.NewResourceRepo(bundle.Client)
	grantRepo := repo.NewGrantRepo(bundle.Client)
	gate := memory.Gate{
		Capabilities: repo.NewCapabilityRepo(bundle.Client),
		Grants:       grantRepo,
		GrantUsage:   repo.NewGrantUsageRepo(bundle.Client, bundle.WriteClient),
	}
	mux := chi.NewRouter()
	apiresources.NewHandler(resourceRepo, repo.NewTaskScheduleRepo(bundle.Client), gate, true).Mount(mux)
	return mux, resourceRepo, grantRepo, ctx
}

// newMux is newUngrantedMux plus an allow grant for memory.read at global
// scope, so the list-behaviour tests below exercise the list rather than the
// gate. TestList_GatesMemorySpaceButNotApplication owns the ungranted case.
func newMux(t *testing.T) (*chi.Mux, repo.ResourceRepo, context.Context) {
	t.Helper()
	mux, resourceRepo, grants, ctx := newUngrantedMux(t)
	_, err := grants.Create(ctx, repo.CreateGrantInput{
		CapabilityName: repo.CapabilityMemoryRead,
		Context:        repo.GrantContextFor(repo.GrantContextGlobal, ""),
		Pattern:        "",
		Mode:           repo.GrantModeAllow,
		GrantedBy:      "test",
	})
	require.NoError(t, err)
	return mux, resourceRepo, ctx
}

func get(t *testing.T, mux *chi.Mux, path string) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
	return w
}

func decodeList(t *testing.T, w *httptest.ResponseRecorder) []map[string]any {
	t.Helper()
	var out []map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &out))
	return out
}

func TestList_AnswersCamelCaseDTO(t *testing.T) {
	mux, resourceRepo, ctx := newMux(t)
	_, err := resourceRepo.Upsert(ctx, repo.UpsertResourceInput{
		Kind:      repo.ResourceKindApplication,
		Slug:      "obsidian",
		Name:      "Obsidian",
		Scope:     repo.GlobalScope(),
		State:     repo.ResourceStateEnabled,
		Version:   "1.0.0",
		Origin:    repo.ResourceOriginBuiltin,
		OriginRef: "builtin:obsidian",
	})
	require.NoError(t, err)

	w := get(t, mux, "/api/resources?kind=application")
	require.Equal(t, http.StatusOK, w.Code)

	rows := decodeList(t, w)
	require.Len(t, rows, 1)
	row := rows[0]
	require.Equal(t, "obsidian", row["slug"])
	require.Equal(t, "Obsidian", row["name"])
	require.Equal(t, "application", row["kind"])
	require.Equal(t, "global", row["scopeKind"])
	require.Equal(t, "", row["scopeRef"])
	require.Equal(t, "local", row["nodeId"])
	require.Equal(t, "enabled", row["state"])
	require.Equal(t, "1.0.0", row["version"])
	require.Equal(t, "builtin", row["origin"])
	require.Equal(t, "builtin:obsidian", row["originRef"])
	require.Contains(t, row, "createdAt")
	require.Contains(t, row, "updatedAt")

	// The wire shape is camelCase, never ent's snake_case struct tags — the
	// whole reason this is a hand-written DTO rather than *ent.Resource.
	for _, snake := range []string{"scope_kind", "scope_ref", "node_id", "origin_ref", "created_at", "updated_at"} {
		require.NotContains(t, row, snake)
	}
}

func TestList_EmptyKindIsAnEmptyArrayNotNull(t *testing.T) {
	mux, _, _ := newMux(t)

	w := get(t, mux, "/api/resources?kind=skill")
	require.Equal(t, http.StatusOK, w.Code)
	// skill has no writer anywhere in the codebase yet, so this list is
	// legitimately empty. It must encode as [] so the client can render
	// "none yet" instead of crashing on a null. routine takes a different
	// code path now (it projects task_schedule rows) and owns the same
	// assertion in routine_projection_test.go.
	require.Equal(t, "[]\n", w.Body.String())
}

func TestList_ScopeRowShadowsGlobalOnSlugCollision(t *testing.T) {
	mux, resourceRepo, ctx := newMux(t)
	_, err := resourceRepo.Upsert(ctx, repo.UpsertResourceInput{
		Kind: repo.ResourceKindMemorySpace, Slug: "notes", Name: "Global notes",
		Scope: repo.GlobalScope(),
	})
	require.NoError(t, err)
	_, err = resourceRepo.Upsert(ctx, repo.UpsertResourceInput{
		Kind: repo.ResourceKindMemorySpace, Slug: "notes", Name: "Project notes",
		Scope: repo.ProjectScope("/tmp/demo"),
	})
	require.NoError(t, err)

	rows := decodeList(t, get(t, mux, "/api/resources?kind=memory_space&scope=project&scopeRef=/tmp/demo"))
	require.Len(t, rows, 1)
	require.Equal(t, "Project notes", rows[0]["name"])
	require.Equal(t, "project", rows[0]["scopeKind"])
}

// TestList_MergedIncludesUnshadowedGlobalRow pins the ListMerged decision
// itself, not just the shadowing behaviour: a global-scope row with no
// scope-specific override must still appear when queried from a non-global
// scope. ListForScope (what GET /api/memory/spaces uses) would answer empty
// here, since it only returns the scope's own rows — the two calls diverge
// on exactly this case, which the slug-collision test above cannot show
// because both queries already agree once a scope-specific row exists.
func TestList_MergedIncludesUnshadowedGlobalRow(t *testing.T) {
	mux, resourceRepo, ctx := newMux(t)
	_, err := resourceRepo.Upsert(ctx, repo.UpsertResourceInput{
		Kind: repo.ResourceKindApplication, Slug: "obsidian", Name: "Obsidian",
		Scope: repo.GlobalScope(),
	})
	require.NoError(t, err)

	rows := decodeList(t, get(t, mux, "/api/resources?kind=application&scope=project&scopeRef=/tmp/demo"))
	require.Len(t, rows, 1)
	require.Equal(t, "obsidian", rows[0]["slug"])
	require.Equal(t, "global", rows[0]["scopeKind"])
}

func TestList_RejectsMissingAndUnknownKind(t *testing.T) {
	mux, _, _ := newMux(t)

	require.Equal(t, http.StatusBadRequest, get(t, mux, "/api/resources").Code)

	w := get(t, mux, "/api/resources?kind=nonsense")
	require.Equal(t, http.StatusBadRequest, w.Code)
	require.Contains(t, w.Body.String(), "unknown kind")
}

func TestList_RejectsScopeMissingItsRef(t *testing.T) {
	mux, _, _ := newMux(t)
	// Fails closed rather than silently answering the global scope's rows —
	// the same rule memory.ParseScope enforces for every other transport.
	w := get(t, mux, "/api/resources?kind=application&scope=project")
	require.Equal(t, http.StatusBadRequest, w.Code)
	require.Contains(t, w.Body.String(), "scopeRef is required")
}

// TestList_GatesMemorySpaceButNotApplication is the asymmetry itself, and both
// halves are load-bearing. The 403 pins that this route cannot hand out the
// same registry rows GET /api/memory/spaces refuses to an ungranted caller —
// including the ?scope=project probe, which would otherwise confirm that a
// named filesystem path holds memory spaces. The 200 pins that the gate was
// not simply hung on the whole route: application rows are public via
// /api/plugins already, so gating them would be a regression, not caution.
func TestList_GatesMemorySpaceButNotApplication(t *testing.T) {
	mux, resourceRepo, _, ctx := newUngrantedMux(t)
	for _, in := range []repo.UpsertResourceInput{
		{Kind: repo.ResourceKindApplication, Slug: "obsidian", Name: "Obsidian", Scope: repo.GlobalScope()},
		{Kind: repo.ResourceKindMemorySpace, Slug: "notes", Name: "Notes", Scope: repo.GlobalScope()},
		{Kind: repo.ResourceKindMemorySpace, Slug: "secrets", Name: "Project secrets", Scope: repo.ProjectScope("/tmp/demo")},
	} {
		_, err := resourceRepo.Upsert(ctx, in)
		require.NoError(t, err)
	}

	require.Equal(t, http.StatusForbidden, get(t, mux, "/api/resources?kind=memory_space").Code)
	require.Equal(t, http.StatusForbidden,
		get(t, mux, "/api/resources?kind=memory_space&scope=project&scopeRef=/tmp/demo").Code)

	w := get(t, mux, "/api/resources?kind=application")
	require.Equal(t, http.StatusOK, w.Code)
	require.Len(t, decodeList(t, w), 1)
}

// TestList_GateChecksTheRequestedScopeNotGlobal pins WHICH scope the
// memory_space gate checks. Substituting repo.GlobalScope() for the requested
// scope leaves the rest of this package green, yet it reopens the disclosure
// the gate closes: the scopeRef echo is an existence oracle for filesystem
// paths, and a caller granted memory.read in any one project could then probe
// every other path with it.
func TestList_GateChecksTheRequestedScopeNotGlobal(t *testing.T) {
	mux, resourceRepo, grants, ctx := newUngrantedMux(t)
	_, err := grants.Create(ctx, repo.CreateGrantInput{
		CapabilityName: repo.CapabilityMemoryRead,
		Context:        repo.GrantContextFor(repo.GrantContextProject, "/a"),
		Pattern:        "",
		Mode:           repo.GrantModeAllow,
		GrantedBy:      "test",
	})
	require.NoError(t, err)
	_, err = resourceRepo.Upsert(ctx, repo.UpsertResourceInput{
		Kind: repo.ResourceKindMemorySpace, Slug: "notes", Name: "Notes",
		Scope: repo.ProjectScope("/a"),
	})
	require.NoError(t, err)

	// The granted path answers. Under a global-scope check this 200 becomes a
	// 403, because the caller holds nothing at global.
	require.Equal(t, http.StatusOK,
		get(t, mux, "/api/resources?kind=memory_space&scope=project&scopeRef=/a").Code)

	// Every other path stays refused — including global, which a project grant
	// does not reach either.
	require.Equal(t, http.StatusForbidden,
		get(t, mux, "/api/resources?kind=memory_space&scope=project&scopeRef=/b").Code)
	require.Equal(t, http.StatusForbidden, get(t, mux, "/api/resources?kind=memory_space").Code)
}
