package tools

import (
	"context"
	"testing"

	"github.com/lx-wnk/kontor/server/internal/db"
	"github.com/lx-wnk/kontor/server/internal/db/repo"
	mcp "github.com/lx-wnk/kontor/server/internal/mcp"
	"github.com/lx-wnk/kontor/server/internal/memory"
	"github.com/stretchr/testify/require"
)

// TestMemoryToolsHaveScopeEntries proves both memory tools have a
// ToolScopeMap entry — Register panics at construction without one.
func TestMemoryToolsHaveScopeEntries(t *testing.T) {
	for _, name := range []string{"memory_search", "memory_write"} {
		if _, ok := mcp.ToolScopeMap[name]; !ok {
			t.Errorf("%s has no ToolScopeMap entry — Register panics at construction without one", name)
		}
	}
}

// TestMemoryScopesAreGrantableToKeys asserts both new scopes appear in the
// set an API key may be granted directly. agent:coord is the cautionary
// case: it is gated (has a ToolScopeMap entry) but ungrantable, reachable
// only through keys:manage's implication — memory:read and memory:write must
// not end up in that same state.
func TestMemoryScopesAreGrantableToKeys(t *testing.T) {
	require.True(t, validKeyScopes["memory:read"], "memory:read must be directly grantable to a key")
	require.True(t, validKeyScopes["memory:write"], "memory:write must be directly grantable to a key")
}

// TestKeysManageImpliesMemoryScopes asserts the expansion of keys:manage
// contains both memory scopes explicitly. scopeImplies is one level deep,
// not transitive, so keys:manage's own entry must list them by hand.
func TestKeysManageImpliesMemoryScopes(t *testing.T) {
	resolved := mcp.ResolveScopes([]string{"keys:manage"})
	require.True(t, resolved["memory:read"], "keys:manage must imply memory:read")
	require.True(t, resolved["memory:write"], "keys:manage must imply memory:write")
}

// newMemoryDepsForTest wires MemoryDeps against a real in-memory SQLite
// database. Capabilities are NOT seeded here — tests that need a resolvable
// capability row call repo.SeedCapabilities themselves, so the "capability
// never catalogued" fail-closed path stays exercisable.
func newMemoryDepsForTest(t *testing.T) (MemoryDeps, repo.GrantRepo, repo.CapabilityRepo, context.Context) {
	t.Helper()
	bundle, err := db.Open(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { _ = bundle.Client.Close() })

	memRepo := repo.NewMemoryRepo(bundle.Client, bundle.WriteClient)
	grants := repo.NewGrantRepo(bundle.Client)
	capabilities := repo.NewCapabilityRepo(bundle.Client)
	deps := MemoryDeps{
		Repo:      memRepo,
		Retriever: memory.NewRetriever(bundle.DB, memRepo),
		Gate: memory.Gate{
			Capabilities: capabilities,
			Grants:       grants,
			GrantUsage:   repo.NewGrantUsageRepo(bundle.Client, bundle.WriteClient),
		},
	}
	return deps, grants, capabilities, context.Background()
}

// mustAllowMemoryGrant creates a global-context allow grant for capName,
// wildcard pattern, so authorizeMemory resolves to EffectAllow regardless of
// the value a caller passes.
func mustAllowMemoryGrant(t *testing.T, grants repo.GrantRepo, ctx context.Context, capName string) {
	t.Helper()
	_, err := grants.Create(ctx, repo.CreateGrantInput{
		CapabilityName: capName,
		Context:        repo.GrantContextFor(repo.GrantContextGlobal, ""),
		Pattern:        "",
		Mode:           repo.GrantModeAllow,
		GrantedBy:      "test",
	})
	require.NoError(t, err)
}

func TestMemoryWriteFailsClosedOnUnknownSpace(t *testing.T) {
	deps, _, _, ctx := newMemoryDepsForTest(t)
	registry := mcp.ToolRegistry{}
	RegisterMemoryTools(registry, deps)

	_, err := registry["memory_write"].Handler(ctx, map[string]any{
		"spaceSlug": "does-not-exist", "summary": "s", "content": "c", "kind": "fact", "sourceKind": "user",
	})
	require.Error(t, err)
}

// TestMemoryWriteDeniedBeforeSpaceLookup is the regression test for the
// space-existence oracle: with the capability seeded but no grant at all,
// the denial must happen before GetSpace runs, so an ungranted caller's
// error never reveals whether "does-not-exist" is actually a real space.
func TestMemoryWriteDeniedBeforeSpaceLookup(t *testing.T) {
	deps, _, capRepo, ctx := newMemoryDepsForTest(t)
	repo.SeedCapabilities(ctx, capRepo)
	registry := mcp.ToolRegistry{}
	RegisterMemoryTools(registry, deps)

	_, err := registry["memory_write"].Handler(ctx, map[string]any{
		"spaceSlug": "does-not-exist", "summary": "s", "content": "c", "kind": "fact", "sourceKind": "user",
	})
	require.Error(t, err)
	require.NotContains(t, err.Error(), "unknown space",
		"authorization must run before the space lookup so an ungranted caller cannot use the error to probe space existence")
}

func TestMemoryWriteFailsClosedOnUnknownScope(t *testing.T) {
	deps, _, _, ctx := newMemoryDepsForTest(t)
	registry := mcp.ToolRegistry{}
	RegisterMemoryTools(registry, deps)

	_, err := registry["memory_write"].Handler(ctx, map[string]any{
		"spaceSlug": "notes", "summary": "s", "content": "c", "kind": "fact", "sourceKind": "user",
		"scope": "bogus",
	})
	require.Error(t, err)
}

func TestMemoryWriteFailsClosedOnMissingScopeRef(t *testing.T) {
	deps, _, _, ctx := newMemoryDepsForTest(t)
	registry := mcp.ToolRegistry{}
	RegisterMemoryTools(registry, deps)

	_, err := registry["memory_write"].Handler(ctx, map[string]any{
		"spaceSlug": "notes", "summary": "s", "content": "c", "kind": "fact", "sourceKind": "user",
		"scope": "project",
	})
	require.Error(t, err)
}

// TestMemoryWriteDeniedWhenCapabilityNotSeeded proves the exact failure the
// controller ruling calls out: a capability catalogue miss must deny, not
// silently allow. Without SeedCapabilities ever having run, memory.write has
// no row, capView is the zero value, and defaultEffect("") denies.
func TestMemoryWriteDeniedWhenCapabilityNotSeeded(t *testing.T) {
	deps, _, _, ctx := newMemoryDepsForTest(t)
	_, err := deps.Repo.CreateSpace(ctx, repo.CreateSpaceInput{Slug: "notes", Name: "Notes", Scope: repo.GlobalScope()})
	require.NoError(t, err)

	registry := mcp.ToolRegistry{}
	RegisterMemoryTools(registry, deps)

	_, err = registry["memory_write"].Handler(ctx, map[string]any{
		"spaceSlug": "notes", "summary": "s", "content": "c", "kind": "fact", "sourceKind": "user",
	})
	require.ErrorContains(t, err, "memory_write")
}

// TestMemoryWriteDeniedWithoutGrantEvenWhenCapabilitySeeded proves the
// second authorization layer: with memory.write seeded (class "resource")
// but no grant at all, Decide has no candidate and defaultEffect("resource")
// resolves to "ask" — and with no Asker wired, ServerEnforcer fails closed
// rather than treating the unanswerable ask as an allow. A key holding the
// memory:write MCP scope still cannot write where its context has no grant.
func TestMemoryWriteDeniedWithoutGrantEvenWhenCapabilitySeeded(t *testing.T) {
	deps, _, capRepo, ctx := newMemoryDepsForTest(t)
	repo.SeedCapabilities(ctx, capRepo)
	_, err := deps.Repo.CreateSpace(ctx, repo.CreateSpaceInput{Slug: "notes", Name: "Notes", Scope: repo.GlobalScope()})
	require.NoError(t, err)

	registry := mcp.ToolRegistry{}
	RegisterMemoryTools(registry, deps)

	_, err = registry["memory_write"].Handler(ctx, map[string]any{
		"spaceSlug": "notes", "summary": "s", "content": "c", "kind": "fact", "sourceKind": "user",
	})
	require.ErrorContains(t, err, "memory_write")
}

func TestMemoryWriteFailsClosedWhenContentEmptiedBySanitize(t *testing.T) {
	deps, grants, capRepo, ctx := newMemoryDepsForTest(t)
	repo.SeedCapabilities(ctx, capRepo)
	_, err := deps.Repo.CreateSpace(ctx, repo.CreateSpaceInput{Slug: "notes", Name: "Notes", Scope: repo.GlobalScope()})
	require.NoError(t, err)
	mustAllowMemoryGrant(t, grants, ctx, repo.CapabilityMemoryWrite)

	registry := mcp.ToolRegistry{}
	RegisterMemoryTools(registry, deps)

	// Escaped, not written literally — see ingest_test.go's identical fixture.
	_, err = registry["memory_write"].Handler(ctx, map[string]any{
		"spaceSlug": "notes", "summary": "", "content": "\u202e\u202c", "kind": "fact", "sourceKind": "user",
	})
	require.ErrorContains(t, err, "memory_write")
}

func TestMemoryWriteFailsClosedOnUnknownKind(t *testing.T) {
	deps, grants, capRepo, ctx := newMemoryDepsForTest(t)
	repo.SeedCapabilities(ctx, capRepo)
	_, err := deps.Repo.CreateSpace(ctx, repo.CreateSpaceInput{Slug: "notes", Name: "Notes", Scope: repo.GlobalScope()})
	require.NoError(t, err)
	mustAllowMemoryGrant(t, grants, ctx, repo.CapabilityMemoryWrite)

	registry := mcp.ToolRegistry{}
	RegisterMemoryTools(registry, deps)

	_, err = registry["memory_write"].Handler(ctx, map[string]any{
		"spaceSlug": "notes", "summary": "s", "content": "c", "kind": "wharrgarbl", "sourceKind": "user",
	})
	require.ErrorContains(t, err, "memory_write")
}

func TestMemoryWriteSucceedsWithAnAllowGrant(t *testing.T) {
	deps, grants, capRepo, ctx := newMemoryDepsForTest(t)
	repo.SeedCapabilities(ctx, capRepo)
	space, err := deps.Repo.CreateSpace(ctx, repo.CreateSpaceInput{Slug: "notes", Name: "Notes", Scope: repo.GlobalScope()})
	require.NoError(t, err)
	mustAllowMemoryGrant(t, grants, ctx, repo.CapabilityMemoryWrite)

	registry := mcp.ToolRegistry{}
	RegisterMemoryTools(registry, deps)

	out := invokeCoordTool(t, registry, ctx, "memory_write", map[string]any{
		"spaceSlug": "notes", "summary": "hello", "content": "world", "kind": "fact", "sourceKind": "user",
	})
	require.Equal(t, space.ID, out["space_id"])
	require.Equal(t, float64(1), out["confidence"], "confidence must default to 1 when omitted")
}

func TestMemorySearchDeniedWithoutGrant(t *testing.T) {
	deps, _, capRepo, ctx := newMemoryDepsForTest(t)
	repo.SeedCapabilities(ctx, capRepo)

	registry := mcp.ToolRegistry{}
	RegisterMemoryTools(registry, deps)

	_, err := registry["memory_search"].Handler(ctx, map[string]any{"query": "anything"})
	require.ErrorContains(t, err, "memory_search")
}

func TestMemorySearchReturnsRankedEntriesWithGrant(t *testing.T) {
	deps, grants, capRepo, ctx := newMemoryDepsForTest(t)
	repo.SeedCapabilities(ctx, capRepo)
	mustAllowMemoryGrant(t, grants, ctx, repo.CapabilityMemoryRead)

	space, err := deps.Repo.CreateSpace(ctx, repo.CreateSpaceInput{Slug: "notes", Name: "Notes", Scope: repo.GlobalScope()})
	require.NoError(t, err)
	_, err = deps.Repo.CreateEntry(ctx, repo.CreateEntryInput{
		SpaceID: space.ID, Summary: "database migration runbook", Content: "how to run a migration",
		Kind: "fact", SourceKind: "user", Confidence: 0.9,
	})
	require.NoError(t, err)

	registry := mcp.ToolRegistry{}
	RegisterMemoryTools(registry, deps)

	out := invokeCoordTool(t, registry, ctx, "memory_search", map[string]any{"query": "migration"})
	entries, ok := out["entries"].([]any)
	require.True(t, ok, "entries must be a list, got %T", out["entries"])
	require.Len(t, entries, 1)
}

// newMemoryDepsWithCaller mirrors newMemoryDepsForTest but also wires Caller
// against the same database and returns the bundle, so a test can seed a
// stage run and prove the credential's task/routine context reaches the gate.
func newMemoryDepsWithCaller(t *testing.T) (MemoryDeps, repo.GrantRepo, *db.DBBundle, context.Context) {
	t.Helper()
	bundle, err := db.Open(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { _ = bundle.Client.Close() })

	memRepo := repo.NewMemoryRepo(bundle.Client, bundle.WriteClient)
	grants := repo.NewGrantRepo(bundle.Client)
	capabilities := repo.NewCapabilityRepo(bundle.Client)
	repo.SeedCapabilities(context.Background(), capabilities)

	deps := MemoryDeps{
		Repo:      memRepo,
		Retriever: memory.NewRetriever(bundle.DB, memRepo),
		Caller: mcp.CallerResolver{
			StageRuns: repo.NewStageRunRepo(bundle.Client),
			Tasks:     repo.NewTaskRepo(bundle.Client),
		},
		Gate: memory.Gate{
			Capabilities: capabilities,
			Grants:       grants,
			GrantUsage:   repo.NewGrantUsageRepo(bundle.Client, bundle.WriteClient),
		},
	}
	return deps, grants, bundle, context.Background()
}

// seedMemoryRun creates a task (optionally bound to a routine) plus one stage
// run on it, mirroring internal/mcp/caller_test.go's seedRun, and returns the
// stage run's id.
func seedMemoryRun(t *testing.T, bundle *db.DBBundle, slug, routineID string) string {
	t.Helper()
	ctx := context.Background()
	in := repo.CreateTaskInput{
		Slug: slug, Title: slug, Cwd: "/tmp",
		CurrentStage: "implementation", Priority: "medium",
		MaxIterations: 5,
	}
	if routineID != "" {
		in.RoutineID = &routineID
	}
	task, err := repo.NewTaskRepo(bundle.Client).Create(ctx, in)
	require.NoError(t, err)

	run, err := repo.NewStageRunRepo(bundle.Client).Create(ctx, repo.CreateStageRunInput{
		TaskID: task.ID, Stage: "implementation",
	})
	require.NoError(t, err)
	return run.ID
}

// TestMemoryRoutineGrantAppliesOnlyToThatRoutine is the memory-side twin of
// TestObsidianRoutineGrantAppliesOnlyToThatRoutine: one grant scoped to
// routine "sched-1", three callers, one difference. One case per tool,
// because each passes d.Caller.Contexts(ctx) to its own Authorize call —
// dropping it from either compiles, vets and leaves the rest of the suite
// green, while a routine-scoped grant silently widens to every caller.
func TestMemoryRoutineGrantAppliesOnlyToThatRoutine(t *testing.T) {
	cases := []struct {
		tool       string
		capability string
		args       map[string]any
	}{
		{"memory_search", repo.CapabilityMemoryRead, map[string]any{"query": "anything"}},
		{"memory_write", repo.CapabilityMemoryWrite, map[string]any{
			"spaceSlug": "notes", "summary": "s", "content": "c", "kind": "fact", "sourceKind": "user",
		}},
	}
	for _, tc := range cases {
		t.Run(tc.tool, func(t *testing.T) {
			deps, grants, bundle, ctx := newMemoryDepsWithCaller(t)
			_, err := deps.Repo.CreateSpace(ctx, repo.CreateSpaceInput{
				Slug: "notes", Name: "Notes", Scope: repo.GlobalScope(),
			})
			require.NoError(t, err)

			_, err = grants.Create(ctx, repo.CreateGrantInput{
				CapabilityName: tc.capability,
				Context:        repo.GrantContextFor(repo.GrantContextRoutine, "sched-1"),
				Pattern:        "*",
				Mode:           repo.GrantModeAllow,
				GrantedBy:      "test",
			})
			require.NoError(t, err)

			runFromRoutine := seedMemoryRun(t, bundle, "from-routine", "sched-1")
			runFromHuman := seedMemoryRun(t, bundle, "hand-made", "")

			registry := mcp.ToolRegistry{}
			RegisterMemoryTools(registry, deps)

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
