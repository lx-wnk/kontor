package memory_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/require"

	apimemory "github.com/lx-wnk/kontor/server/internal/api/memory"
	"github.com/lx-wnk/kontor/server/internal/db"
	"github.com/lx-wnk/kontor/server/internal/db/ent"
	"github.com/lx-wnk/kontor/server/internal/db/repo"
	"github.com/lx-wnk/kontor/server/internal/memory"
)

// newMux wires an apimemory.Handler against a real in-memory SQLite database
// and returns the mux plus the raw dependencies tests need to seed fixtures.
func newMux(t *testing.T) (*chi.Mux, *ent.Client, repo.MemoryRepo, repo.GrantRepo, repo.CapabilityRepo, context.Context) {
	t.Helper()
	bundle, err := db.Open(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { _ = bundle.Client.Close() })

	memRepo := repo.NewMemoryRepo(bundle.Client, bundle.WriteClient)
	capRepo := repo.NewCapabilityRepo(bundle.Client)
	grantRepo := repo.NewGrantRepo(bundle.Client)
	grantUsageRepo := repo.NewGrantUsageRepo(bundle.Client, bundle.WriteClient)
	gate := memory.Gate{Capabilities: capRepo, Grants: grantRepo, GrantUsage: grantUsageRepo}
	h := apimemory.NewHandler(memRepo, memory.NewRetriever(bundle.DB, memRepo), gate)

	mux := chi.NewRouter()
	h.Mount(mux)
	return mux, bundle.Client, memRepo, grantRepo, capRepo, context.Background()
}

// mustAllowGrant creates an allow grant for capName at the given context,
// wildcard pattern, so it resolves EffectAllow regardless of the value a
// caller passes.
func mustAllowGrant(t *testing.T, grants repo.GrantRepo, ctx context.Context, capName, contextKind, contextRef string) {
	t.Helper()
	_, err := grants.Create(ctx, repo.CreateGrantInput{
		CapabilityName: capName,
		Context:        repo.GrantContextFor(contextKind, contextRef),
		Pattern:        "",
		Mode:           repo.GrantModeAllow,
		GrantedBy:      "test",
	})
	require.NoError(t, err)
}

// mustStageRun creates a task and a stage run against client, for tests
// exercising GET /api/memory/injections?stageRun= — RecordInjection refuses
// a stage_run_id that does not reference a real row.
var stageRunSeq int

func mustStageRun(t *testing.T, client *ent.Client) string {
	t.Helper()
	ctx := context.Background()
	// Unique slug per call: tasks.slug is unique, and the bulk-parameter tests
	// need two stage runs in one database.
	stageRunSeq++
	task, err := repo.NewTaskRepo(client).Create(ctx, repo.CreateTaskInput{
		Slug: fmt.Sprintf("memory-http-task-%d", stageRunSeq), Title: "Test Task", Cwd: "/tmp",
		CurrentStage: "backlog", Priority: "medium", MaxIterations: 20, StageTimeoutSeconds: 1800,
	})
	require.NoError(t, err)
	run, err := repo.NewStageRunRepo(client).Create(ctx, repo.CreateStageRunInput{TaskID: task.ID, Stage: "backlog", Iteration: 1})
	require.NoError(t, err)
	return run.ID
}

func doJSON(t *testing.T, mux *chi.Mux, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var buf bytes.Buffer
	if body != nil {
		require.NoError(t, json.NewEncoder(&buf).Encode(body))
	}
	req := httptest.NewRequest(method, path, &buf)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	return w
}

func decodeMap(t *testing.T, w *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var out map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &out))
	return out
}

func decodeSlice(t *testing.T, w *httptest.ResponseRecorder) []any {
	t.Helper()
	var out []any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &out))
	return out
}

func TestListSpacesDeniedWithoutGrant(t *testing.T) {
	mux, _, _, _, capRepo, ctx := newMux(t)
	repo.SeedCapabilities(ctx, capRepo)

	w := doJSON(t, mux, http.MethodGet, "/api/memory/spaces", nil)
	require.Equal(t, http.StatusForbidden, w.Code)
}

// TestListSpacesDeniedWhenCapabilityNotSeeded proves the catalogue-miss path
// fails closed too: with SeedCapabilities never having run, memory.read has
// no row, and Decide's defaultEffect("") denies.
func TestListSpacesDeniedWhenCapabilityNotSeeded(t *testing.T) {
	mux, _, _, _, _, _ := newMux(t)

	w := doJSON(t, mux, http.MethodGet, "/api/memory/spaces", nil)
	require.Equal(t, http.StatusForbidden, w.Code)
}

func TestListSpacesFailsClosedOnUnparseableScope(t *testing.T) {
	mux, _, _, _, capRepo, ctx := newMux(t)
	repo.SeedCapabilities(ctx, capRepo)

	w := doJSON(t, mux, http.MethodGet, "/api/memory/spaces?scope=bogus", nil)
	require.Equal(t, http.StatusBadRequest, w.Code)
}

func TestCreateSpaceThenListSpaces(t *testing.T) {
	mux, _, _, grants, capRepo, ctx := newMux(t)
	repo.SeedCapabilities(ctx, capRepo)
	mustAllowGrant(t, grants, ctx, repo.CapabilityMemoryWrite, repo.GrantContextGlobal, "")
	mustAllowGrant(t, grants, ctx, repo.CapabilityMemoryRead, repo.GrantContextGlobal, "")

	w := doJSON(t, mux, http.MethodPost, "/api/memory/spaces", map[string]any{"slug": "notes", "name": "Notes"})
	require.Equal(t, http.StatusCreated, w.Code)
	require.Equal(t, "notes", decodeMap(t, w)["slug"])

	w = doJSON(t, mux, http.MethodGet, "/api/memory/spaces", nil)
	require.Equal(t, http.StatusOK, w.Code)
	require.Len(t, decodeSlice(t, w), 1)
}

func TestCreateSpaceFailsClosedOnUnparseableScope(t *testing.T) {
	mux, _, _, grants, capRepo, ctx := newMux(t)
	repo.SeedCapabilities(ctx, capRepo)
	mustAllowGrant(t, grants, ctx, repo.CapabilityMemoryWrite, repo.GrantContextGlobal, "")

	w := doJSON(t, mux, http.MethodPost, "/api/memory/spaces", map[string]any{"slug": "notes", "scope": "bogus"})
	require.Equal(t, http.StatusBadRequest, w.Code)
}

func TestCreateEntryFailsClosedOnUnknownSpace(t *testing.T) {
	mux, _, _, grants, capRepo, ctx := newMux(t)
	repo.SeedCapabilities(ctx, capRepo)
	mustAllowGrant(t, grants, ctx, repo.CapabilityMemoryWrite, repo.GrantContextGlobal, "")

	w := doJSON(t, mux, http.MethodPost, "/api/memory/entries", map[string]any{
		"spaceSlug": "does-not-exist", "summary": "s", "content": "c", "kind": "fact", "sourceKind": "user",
	})
	require.Equal(t, http.StatusNotFound, w.Code)
}

// TestCreateEntryDeniedBeforeSpaceLookup is the regression test for the
// space-existence oracle: without any memory.write grant, an unknown space
// must still come back as a denial (403), not a not-found (404) — otherwise
// an ungranted caller could probe whether a space exists by reading the
// status code alone.
func TestCreateEntryDeniedBeforeSpaceLookup(t *testing.T) {
	mux, _, _, _, capRepo, ctx := newMux(t)
	repo.SeedCapabilities(ctx, capRepo)

	w := doJSON(t, mux, http.MethodPost, "/api/memory/entries", map[string]any{
		"spaceSlug": "does-not-exist", "summary": "s", "content": "c", "kind": "fact", "sourceKind": "user",
	})
	require.Equal(t, http.StatusForbidden, w.Code)
}

func TestCreateEntryFailsClosedOnUnknownKind(t *testing.T) {
	mux, _, memRepo, grants, capRepo, ctx := newMux(t)
	repo.SeedCapabilities(ctx, capRepo)
	mustAllowGrant(t, grants, ctx, repo.CapabilityMemoryWrite, repo.GrantContextGlobal, "")
	_, err := memRepo.CreateSpace(ctx, repo.CreateSpaceInput{Slug: "notes", Name: "Notes", Scope: repo.GlobalScope()})
	require.NoError(t, err)

	w := doJSON(t, mux, http.MethodPost, "/api/memory/entries", map[string]any{
		"spaceSlug": "notes", "summary": "s", "content": "c", "kind": "wharrgarbl", "sourceKind": "user",
	})
	require.Equal(t, http.StatusBadRequest, w.Code)
}

func TestCreateEntryThenSearchFindsIt(t *testing.T) {
	mux, _, memRepo, grants, capRepo, ctx := newMux(t)
	repo.SeedCapabilities(ctx, capRepo)
	mustAllowGrant(t, grants, ctx, repo.CapabilityMemoryWrite, repo.GrantContextGlobal, "")
	mustAllowGrant(t, grants, ctx, repo.CapabilityMemoryRead, repo.GrantContextGlobal, "")
	_, err := memRepo.CreateSpace(ctx, repo.CreateSpaceInput{Slug: "notes", Name: "Notes", Scope: repo.GlobalScope()})
	require.NoError(t, err)

	w := doJSON(t, mux, http.MethodPost, "/api/memory/entries", map[string]any{
		"spaceSlug": "notes", "summary": "migration runbook", "content": "how to run a migration",
		"kind": "fact", "sourceKind": "user",
	})
	require.Equal(t, http.StatusCreated, w.Code)

	w = doJSON(t, mux, http.MethodGet, "/api/memory/entries?q=migration", nil)
	require.Equal(t, http.StatusOK, w.Code)
	require.Len(t, decodeSlice(t, w), 1)
}

// TestSearchEntriesSpaceIDBrowsesWithoutQueryText proves the browse path: a
// spaceId parameter lists that space's valid entries even though the query
// text is blank — retrieve.go returns nothing for an empty query by design,
// so without this branch a space full of entries would answer "no results"
// unless the caller already knew a word inside one of them.
func TestSearchEntriesSpaceIDBrowsesWithoutQueryText(t *testing.T) {
	mux, _, memRepo, grants, capRepo, ctx := newMux(t)
	repo.SeedCapabilities(ctx, capRepo)
	mustAllowGrant(t, grants, ctx, repo.CapabilityMemoryWrite, repo.GrantContextGlobal, "")
	mustAllowGrant(t, grants, ctx, repo.CapabilityMemoryRead, repo.GrantContextGlobal, "")
	space, err := memRepo.CreateSpace(ctx, repo.CreateSpaceInput{Slug: "notes", Name: "Notes", Scope: repo.GlobalScope()})
	require.NoError(t, err)
	_, err = memRepo.CreateEntry(ctx, repo.CreateEntryInput{
		SpaceID: space.ID, Summary: "browse me", Content: "no query text needed",
		Kind: "fact", SourceKind: "user", Confidence: 1,
	})
	require.NoError(t, err)

	w := doJSON(t, mux, http.MethodGet, "/api/memory/entries?spaceId="+space.ID, nil)
	require.Equal(t, http.StatusOK, w.Code)
	entries := decodeSlice(t, w)
	require.Len(t, entries, 1)
	require.Equal(t, "browse me", entries[0].(map[string]any)["summary"])
}

// TestSearchEntriesSpaceIDDeniedBeforeSpaceLookup is the regression test for
// the space-existence oracle on the browse path: without a memory.read grant,
// a real space id and a made-up one must come back with the identical status
// and body — otherwise an ungranted caller could tell them apart.
func TestSearchEntriesSpaceIDDeniedBeforeSpaceLookup(t *testing.T) {
	mux, _, memRepo, grants, capRepo, ctx := newMux(t)
	repo.SeedCapabilities(ctx, capRepo)
	mustAllowGrant(t, grants, ctx, repo.CapabilityMemoryWrite, repo.GrantContextGlobal, "")
	space, err := memRepo.CreateSpace(ctx, repo.CreateSpaceInput{Slug: "notes", Name: "Notes", Scope: repo.GlobalScope()})
	require.NoError(t, err)

	wReal := doJSON(t, mux, http.MethodGet, "/api/memory/entries?spaceId="+space.ID, nil)
	wFake := doJSON(t, mux, http.MethodGet, "/api/memory/entries?spaceId=does-not-exist", nil)

	require.Equal(t, http.StatusForbidden, wReal.Code)
	require.Equal(t, http.StatusForbidden, wFake.Code)
	require.Equal(t, wFake.Code, wReal.Code)
	require.Equal(t, wFake.Body.String(), wReal.Body.String())
}

// TestSearchEntriesSpaceIDFailsClosedOnScopeMismatch proves a grant valid in
// one scope cannot read a space that belongs to a different scope by passing
// its id: the mismatch answers 404, the same status a missing space answers,
// never the entries and never 403.
func TestSearchEntriesSpaceIDFailsClosedOnScopeMismatch(t *testing.T) {
	mux, _, memRepo, grants, capRepo, ctx := newMux(t)
	repo.SeedCapabilities(ctx, capRepo)
	mustAllowGrant(t, grants, ctx, repo.CapabilityMemoryWrite, repo.GrantContextGlobal, "")
	mustAllowGrant(t, grants, ctx, repo.CapabilityMemoryRead, repo.GrantContextGlobal, "")

	space, err := memRepo.CreateSpace(ctx, repo.CreateSpaceInput{Slug: "notes", Name: "Notes", Scope: repo.ProjectScope("/path/A")})
	require.NoError(t, err)
	_, err = memRepo.CreateEntry(ctx, repo.CreateEntryInput{
		SpaceID: space.ID, Summary: "a-only", Content: "a-only detail",
		Kind: "fact", SourceKind: "user", Confidence: 1,
	})
	require.NoError(t, err)

	w := doJSON(t, mux, http.MethodGet, "/api/memory/entries?spaceId="+space.ID+"&scope=project&scopeRef=/path/B", nil)
	require.Equal(t, http.StatusNotFound, w.Code)
}

func TestSearchEntriesFailsClosedOnUnparseableScope(t *testing.T) {
	mux, _, _, _, capRepo, ctx := newMux(t)
	repo.SeedCapabilities(ctx, capRepo)

	w := doJSON(t, mux, http.MethodGet, "/api/memory/entries?q=x&scope=bogus", nil)
	require.Equal(t, http.StatusBadRequest, w.Code)
}

// TestSearchEntriesClampsAbsurdLimit proves a zero, negative or enormous
// limit can never turn into an unbounded response — Retriever.Retrieve
// clamps whatever the query string parses to.
func TestSearchEntriesClampsAbsurdLimit(t *testing.T) {
	mux, _, memRepo, grants, capRepo, ctx := newMux(t)
	repo.SeedCapabilities(ctx, capRepo)
	mustAllowGrant(t, grants, ctx, repo.CapabilityMemoryRead, repo.GrantContextGlobal, "")
	space, err := memRepo.CreateSpace(ctx, repo.CreateSpaceInput{Slug: "notes", Name: "Notes", Scope: repo.GlobalScope()})
	require.NoError(t, err)
	for i := 0; i < 60; i++ {
		_, err := memRepo.CreateEntry(ctx, repo.CreateEntryInput{
			SpaceID: space.ID, Summary: fmt.Sprintf("widget entry %d", i), Content: "widget content",
			Kind: "fact", SourceKind: "user", Confidence: 1,
		})
		require.NoError(t, err)
	}

	for _, limit := range []string{"0", "-5", "999999", "not-a-number"} {
		w := doJSON(t, mux, http.MethodGet, "/api/memory/entries?q=widget&limit="+limit, nil)
		require.Equal(t, http.StatusOK, w.Code)
		entries := decodeSlice(t, w)
		require.LessOrEqualf(t, len(entries), 50, "limit=%s must never return an unbounded result set", limit)
	}
}

// TestSecondScopesEntriesNeverAppear proves the row scoping the search and
// list-spaces queries carry: a project scope's own space and entries must
// never surface a same-slug space or entry that belongs to a different
// project scope, even though both are visible to an identical global grant.
func TestSecondScopesEntriesNeverAppear(t *testing.T) {
	mux, _, memRepo, grants, capRepo, ctx := newMux(t)
	repo.SeedCapabilities(ctx, capRepo)
	mustAllowGrant(t, grants, ctx, repo.CapabilityMemoryRead, repo.GrantContextGlobal, "")
	mustAllowGrant(t, grants, ctx, repo.CapabilityMemoryWrite, repo.GrantContextGlobal, "")

	spaceA, err := memRepo.CreateSpace(ctx, repo.CreateSpaceInput{Slug: "notes", Name: "Notes", Scope: repo.ProjectScope("/path/A")})
	require.NoError(t, err)
	_, err = memRepo.CreateEntry(ctx, repo.CreateEntryInput{
		SpaceID: spaceA.ID, Summary: "alpha secret rollout plan", Content: "alpha only detail",
		Kind: "fact", SourceKind: "user", Confidence: 1,
	})
	require.NoError(t, err)

	spaceB, err := memRepo.CreateSpace(ctx, repo.CreateSpaceInput{Slug: "notes", Name: "Notes", Scope: repo.ProjectScope("/path/B")})
	require.NoError(t, err)
	_, err = memRepo.CreateEntry(ctx, repo.CreateEntryInput{
		SpaceID: spaceB.ID, Summary: "bravo secret rollout plan", Content: "bravo only detail",
		Kind: "fact", SourceKind: "user", Confidence: 1,
	})
	require.NoError(t, err)

	w := doJSON(t, mux, http.MethodGet, "/api/memory/entries?q=secret&scope=project&scopeRef=/path/B", nil)
	require.Equal(t, http.StatusOK, w.Code)
	entries := decodeSlice(t, w)
	require.Len(t, entries, 1, "search scoped to B must not also surface A's entry")
	require.Equal(t, "bravo secret rollout plan", entries[0].(map[string]any)["summary"])

	w = doJSON(t, mux, http.MethodGet, "/api/memory/spaces?scope=project&scopeRef=/path/B", nil)
	require.Equal(t, http.StatusOK, w.Code)
	spaces := decodeSlice(t, w)
	require.Len(t, spaces, 1, "listing B's spaces must not also surface A's same-slug space")
	require.Equal(t, spaceB.ID, spaces[0].(map[string]any)["id"])
}

func TestPatchSupersedeEntry(t *testing.T) {
	mux, _, memRepo, grants, capRepo, ctx := newMux(t)
	repo.SeedCapabilities(ctx, capRepo)
	mustAllowGrant(t, grants, ctx, repo.CapabilityMemoryWrite, repo.GrantContextGlobal, "")
	space, err := memRepo.CreateSpace(ctx, repo.CreateSpaceInput{Slug: "notes", Name: "Notes", Scope: repo.GlobalScope()})
	require.NoError(t, err)
	oldEntry, err := memRepo.CreateEntry(ctx, repo.CreateEntryInput{SpaceID: space.ID, Summary: "old", Content: "old", Kind: "fact", SourceKind: "user", Confidence: 1})
	require.NoError(t, err)
	newEntry, err := memRepo.CreateEntry(ctx, repo.CreateEntryInput{SpaceID: space.ID, Summary: "new", Content: "new", Kind: "fact", SourceKind: "user", Confidence: 1})
	require.NoError(t, err)

	w := doJSON(t, mux, http.MethodPatch, "/api/memory/entries/"+oldEntry.ID, map[string]any{"supersededBy": newEntry.ID})
	require.Equal(t, http.StatusOK, w.Code)
	require.Equal(t, newEntry.ID, decodeMap(t, w)["supersededBy"])
}

func TestPatchSupersedeEntryFailsClosedOnUnknownEntry(t *testing.T) {
	mux, _, _, grants, capRepo, ctx := newMux(t)
	repo.SeedCapabilities(ctx, capRepo)
	mustAllowGrant(t, grants, ctx, repo.CapabilityMemoryWrite, repo.GrantContextGlobal, "")

	w := doJSON(t, mux, http.MethodPatch, "/api/memory/entries/does-not-exist", map[string]any{"supersededBy": "also-missing"})
	require.Equal(t, http.StatusNotFound, w.Code)
}

func TestDeleteExpiresEntry(t *testing.T) {
	mux, _, memRepo, grants, capRepo, ctx := newMux(t)
	repo.SeedCapabilities(ctx, capRepo)
	mustAllowGrant(t, grants, ctx, repo.CapabilityMemoryWrite, repo.GrantContextGlobal, "")
	space, err := memRepo.CreateSpace(ctx, repo.CreateSpaceInput{Slug: "notes", Name: "Notes", Scope: repo.GlobalScope()})
	require.NoError(t, err)
	entry, err := memRepo.CreateEntry(ctx, repo.CreateEntryInput{SpaceID: space.ID, Summary: "gone soon", Content: "gone soon", Kind: "fact", SourceKind: "user", Confidence: 1})
	require.NoError(t, err)

	w := doJSON(t, mux, http.MethodDelete, "/api/memory/entries/"+entry.ID, nil)
	require.Equal(t, http.StatusNoContent, w.Code)

	valid, err := memRepo.ListValid(ctx, space.ID, time.Now())
	require.NoError(t, err)
	require.Empty(t, valid, "DELETE must expire the entry, not merely acknowledge the request")
}

func TestDeleteEntryDeniedWithoutGrant(t *testing.T) {
	mux, _, memRepo, _, capRepo, ctx := newMux(t)
	repo.SeedCapabilities(ctx, capRepo)
	space, err := memRepo.CreateSpace(ctx, repo.CreateSpaceInput{Slug: "notes", Name: "Notes", Scope: repo.GlobalScope()})
	require.NoError(t, err)
	entry, err := memRepo.CreateEntry(ctx, repo.CreateEntryInput{SpaceID: space.ID, Summary: "s", Content: "c", Kind: "fact", SourceKind: "user", Confidence: 1})
	require.NoError(t, err)

	w := doJSON(t, mux, http.MethodDelete, "/api/memory/entries/"+entry.ID, nil)
	require.Equal(t, http.StatusForbidden, w.Code)
}

func TestListInjectionsByStageRun(t *testing.T) {
	mux, client, memRepo, grants, capRepo, ctx := newMux(t)
	repo.SeedCapabilities(ctx, capRepo)
	mustAllowGrant(t, grants, ctx, repo.CapabilityMemoryRead, repo.GrantContextGlobal, "")

	stageRunID := mustStageRun(t, client)
	space, err := memRepo.CreateSpace(ctx, repo.CreateSpaceInput{Slug: "notes", Name: "Notes", Scope: repo.GlobalScope()})
	require.NoError(t, err)
	entry, err := memRepo.CreateEntry(ctx, repo.CreateEntryInput{SpaceID: space.ID, Summary: "s", Content: "c", Kind: "fact", SourceKind: "user", Confidence: 1})
	require.NoError(t, err)
	_, err = memRepo.RecordInjection(ctx, repo.RecordInjectionInput{
		StageRunID: stageRunID, EntryIDs: []string{entry.ID}, CharBudget: 4000, CharsUsed: 100, CandidateCount: 1,
	})
	require.NoError(t, err)

	w := doJSON(t, mux, http.MethodGet, "/api/memory/injections?stageRun="+stageRunID, nil)
	require.Equal(t, http.StatusOK, w.Code)
	list := decodeSlice(t, w)
	require.Len(t, list, 1)
	require.Equal(t, stageRunID, list[0].(map[string]any)["stageRunId"])
}

// TestListInjectionsBulkSpendsOneGrantUse is the whole point of the repeated
// parameter. The grant allows one use per hour; asking for two stage runs in
// one request must answer both runs' records off that single use. Before the
// bulk form the tab issued one gated request per run, so a ten-run modal spent
// ten uses of the same memory.read grant the pipeline's own memory push
// authorizes — reading the memory view exhausted the window and silently
// stopped agent memory retrieval.
func TestListInjectionsBulkSpendsOneGrantUse(t *testing.T) {
	mux, client, memRepo, grants, capRepo, ctx := newMux(t)
	repo.SeedCapabilities(ctx, capRepo)
	_, err := grants.Create(ctx, repo.CreateGrantInput{
		CapabilityName:     repo.CapabilityMemoryRead,
		Context:            repo.GrantContextFor(repo.GrantContextGlobal, ""),
		Mode:               repo.GrantModeAllow,
		LimitCount:         1,
		LimitWindowSeconds: 3600,
		GrantedBy:          "test",
	})
	require.NoError(t, err)

	space, err := memRepo.CreateSpace(ctx, repo.CreateSpaceInput{Slug: "notes", Name: "Notes", Scope: repo.GlobalScope()})
	require.NoError(t, err)
	entry, err := memRepo.CreateEntry(ctx, repo.CreateEntryInput{SpaceID: space.ID, Summary: "s", Content: "c", Kind: "fact", SourceKind: "user", Confidence: 1})
	require.NoError(t, err)
	runA, runB := mustStageRun(t, client), mustStageRun(t, client)
	for _, id := range []string{runA, runB} {
		_, err = memRepo.RecordInjection(ctx, repo.RecordInjectionInput{
			StageRunID: id, EntryIDs: []string{entry.ID}, CharBudget: 4000, CharsUsed: 100, CandidateCount: 1,
		})
		require.NoError(t, err)
	}

	w := doJSON(t, mux, http.MethodGet, "/api/memory/injections?stageRun="+runA+"&stageRun="+runB, nil)
	require.Equal(t, http.StatusOK, w.Code)
	list := decodeSlice(t, w)
	require.Len(t, list, 2)
	require.Equal(t, runA, list[0].(map[string]any)["stageRunId"])
	require.Equal(t, runB, list[1].(map[string]any)["stageRunId"])

	// One use spent, not two: the hourly budget still has nothing left, and a
	// per-run request pair would already have been refused on its second leg.
	require.Equal(t, http.StatusForbidden,
		doJSON(t, mux, http.MethodGet, "/api/memory/injections?stageRun="+runA, nil).Code)
}

// TestListInjectionsSingleIDIsUnchanged pins the documented one-id shape
// against the bulk form: a lone id answers that run's records and nobody
// else's, and an empty value is no id at all.
func TestListInjectionsSingleIDIsUnchanged(t *testing.T) {
	mux, client, memRepo, grants, capRepo, ctx := newMux(t)
	repo.SeedCapabilities(ctx, capRepo)
	mustAllowGrant(t, grants, ctx, repo.CapabilityMemoryRead, repo.GrantContextGlobal, "")

	space, err := memRepo.CreateSpace(ctx, repo.CreateSpaceInput{Slug: "notes", Name: "Notes", Scope: repo.GlobalScope()})
	require.NoError(t, err)
	entry, err := memRepo.CreateEntry(ctx, repo.CreateEntryInput{SpaceID: space.ID, Summary: "s", Content: "c", Kind: "fact", SourceKind: "user", Confidence: 1})
	require.NoError(t, err)
	runA, runB := mustStageRun(t, client), mustStageRun(t, client)
	for _, id := range []string{runA, runB} {
		_, err = memRepo.RecordInjection(ctx, repo.RecordInjectionInput{
			StageRunID: id, EntryIDs: []string{entry.ID}, CharBudget: 4000, CharsUsed: 100, CandidateCount: 1,
		})
		require.NoError(t, err)
	}

	w := doJSON(t, mux, http.MethodGet, "/api/memory/injections?stageRun="+runA, nil)
	require.Equal(t, http.StatusOK, w.Code)
	list := decodeSlice(t, w)
	require.Len(t, list, 1)
	require.Equal(t, runA, list[0].(map[string]any)["stageRunId"])

	require.Equal(t, http.StatusBadRequest,
		doJSON(t, mux, http.MethodGet, "/api/memory/injections?stageRun=", nil).Code)

	// A repeated id is one id: the response must not carry that run twice.
	w = doJSON(t, mux, http.MethodGet, "/api/memory/injections?stageRun="+runA+"&stageRun="+runA, nil)
	require.Equal(t, http.StatusOK, w.Code)
	require.Len(t, decodeSlice(t, w), 1)
}

// TestListInjectionsCapsTheIDList pins the boundary of the repeated parameter.
// One Authorize covers the whole list, so the id count is what bounds the work
// a single capability check pays for.
func TestListInjectionsCapsTheIDList(t *testing.T) {
	mux, _, _, grants, capRepo, ctx := newMux(t)
	repo.SeedCapabilities(ctx, capRepo)
	mustAllowGrant(t, grants, ctx, repo.CapabilityMemoryRead, repo.GrantContextGlobal, "")

	// Distinct unknown ids: the cap is checked before any lookup, so no real
	// stage run is needed to reach it — only the count matters.
	query := func(n int) string {
		parts := make([]string, 0, n)
		for i := range n {
			parts = append(parts, "stageRun=run-"+strconv.Itoa(i))
		}
		return "/api/memory/injections?" + strings.Join(parts, "&")
	}

	w := doJSON(t, mux, http.MethodGet, query(200), nil)
	require.Equal(t, http.StatusOK, w.Code)

	w = doJSON(t, mux, http.MethodGet, query(201), nil)
	require.Equal(t, http.StatusBadRequest, w.Code)
	require.Contains(t, w.Body.String(), "at most 200")
}

func TestListInjectionsRequiresStageRunParam(t *testing.T) {
	mux, _, _, _, capRepo, ctx := newMux(t)
	repo.SeedCapabilities(ctx, capRepo)

	w := doJSON(t, mux, http.MethodGet, "/api/memory/injections", nil)
	require.Equal(t, http.StatusBadRequest, w.Code)
}

func TestListInjectionsDeniedWithoutGrant(t *testing.T) {
	mux, _, _, _, capRepo, ctx := newMux(t)
	repo.SeedCapabilities(ctx, capRepo)

	w := doJSON(t, mux, http.MethodGet, "/api/memory/injections?stageRun=whatever", nil)
	require.Equal(t, http.StatusForbidden, w.Code)
}

// TestMemoryRoutesWireFormat asserts the raw key set on all four payload shapes
// the memory routes answer with. Decoding into typed structs would pass before
// and after the change, so every assertion here is against map keys.
func TestMemoryRoutesWireFormat(t *testing.T) {
	mux, client, memRepo, grants, capRepo, ctx := newMux(t)
	repo.SeedCapabilities(ctx, capRepo)
	mustAllowGrant(t, grants, ctx, repo.CapabilityMemoryWrite, repo.GrantContextGlobal, "")
	mustAllowGrant(t, grants, ctx, repo.CapabilityMemoryRead, repo.GrantContextGlobal, "")

	requireKeys := func(t *testing.T, row map[string]any, want, forbidden []string) {
		t.Helper()
		for _, k := range want {
			require.Contains(t, row, k, "missing key %q in %v", k, row)
		}
		for _, k := range forbidden {
			require.NotContains(t, row, k, "unexpected key %q in %v", k, row)
		}
	}

	spaceKeys := []string{"id", "kind", "slug", "name", "scopeKind", "scopeRef", "nodeId", "state", "version", "origin", "originRef", "createdAt", "updatedAt"}
	spaceForbidden := []string{"scope_kind", "scope_ref", "node_id", "origin_ref", "created_at", "updated_at"}

	w := doJSON(t, mux, http.MethodPost, "/api/memory/spaces", map[string]any{"slug": "wire", "name": "Wire"})
	require.Equal(t, http.StatusCreated, w.Code)
	spaceResp := decodeMap(t, w)
	requireKeys(t, spaceResp, spaceKeys, spaceForbidden)
	spaceID := spaceResp["id"].(string)

	w = doJSON(t, mux, http.MethodGet, "/api/memory/spaces", nil)
	require.Equal(t, http.StatusOK, w.Code)
	spaces := decodeSlice(t, w)
	require.Len(t, spaces, 1)
	requireKeys(t, spaces[0].(map[string]any), spaceKeys, spaceForbidden)

	entryKeys := []string{"id", "spaceId", "summary", "content", "kind", "sourceKind", "sourceRef", "confidence", "validFrom", "validUntil", "supersededBy", "userId", "createdAt", "updatedAt"}
	entryForbidden := []string{"space_id", "source_kind", "source_ref", "valid_from", "valid_until", "superseded_by", "user_id", "created_at", "updated_at", "ID", "SpaceID", "Summary"}

	w = doJSON(t, mux, http.MethodPost, "/api/memory/entries", map[string]any{
		"spaceSlug": "wire", "summary": "wire format note", "content": "the body",
		"kind": "fact", "sourceKind": "user",
	})
	require.Equal(t, http.StatusCreated, w.Code)
	created := decodeMap(t, w)
	requireKeys(t, created, entryKeys, entryForbidden)

	// The search projection is its own shape: memory.Entry carries no json tags
	// at all today, so the wire spells these ID/SpaceID/Summary.
	w = doJSON(t, mux, http.MethodGet, "/api/memory/entries?q=wire", nil)
	require.Equal(t, http.StatusOK, w.Code)
	hits := decodeSlice(t, w)
	require.Len(t, hits, 1)
	requireKeys(t, hits[0].(map[string]any),
		[]string{"id", "spaceId", "summary", "content", "kind", "confidence", "createdAt"},
		[]string{"ID", "SpaceID", "Summary", "Content", "Kind", "Confidence", "CreatedAt", "space_id", "created_at"})

	// SupersedeEntry verifies the replacement id resolves to a real entry
	// before writing the pointer, so the target must be a live entry, not an
	// arbitrary string.
	replacement, err := memRepo.CreateEntry(ctx, repo.CreateEntryInput{
		SpaceID: spaceID, Summary: "replacement", Content: "replacement body", Kind: "fact", SourceKind: "user", Confidence: 1,
	})
	require.NoError(t, err)

	w = doJSON(t, mux, http.MethodPatch, "/api/memory/entries/"+created["id"].(string), map[string]any{"supersededBy": replacement.ID})
	require.Equal(t, http.StatusOK, w.Code)
	requireKeys(t, decodeMap(t, w), entryKeys, entryForbidden)

	stageRunID := mustStageRun(t, client)
	_, err = memRepo.RecordInjection(ctx, repo.RecordInjectionInput{
		StageRunID: stageRunID, EntryIDs: []string{created["id"].(string)}, CharBudget: 4000, CharsUsed: 0, CandidateCount: 0,
	})
	require.NoError(t, err)

	w = doJSON(t, mux, http.MethodGet, "/api/memory/injections?stageRun="+stageRunID, nil)
	require.Equal(t, http.StatusOK, w.Code)
	injections := decodeSlice(t, w)
	require.Len(t, injections, 1)
	injection := injections[0].(map[string]any)
	requireKeys(t, injection,
		[]string{"id", "stageRunId", "entryIds", "charBudget", "charsUsed", "candidateCount", "createdAt", "updatedAt"},
		[]string{"stage_run_id", "entry_ids", "char_budget", "chars_used", "candidate_count", "created_at", "updated_at"})
	require.Equal(t, float64(0), injection["charsUsed"], "a zero char count must be sent, not omitted")
	require.Equal(t, float64(0), injection["candidateCount"], "a zero candidate count must be sent, not omitted")
}
