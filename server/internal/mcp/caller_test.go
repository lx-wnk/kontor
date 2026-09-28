package mcp_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/lx-wnk/kontor/server/internal/capability"
	"github.com/lx-wnk/kontor/server/internal/db"
	"github.com/lx-wnk/kontor/server/internal/db/repo"
	"github.com/lx-wnk/kontor/server/internal/mcp"
)

// seedRun creates a task (optionally bound to a routine) plus one stage run on
// it, and returns the stage run's id.
func seedRun(t *testing.T, client *db.DBBundle, slug, routineID string) (string, string) {
	t.Helper()
	ctx := context.Background()
	tasks := repo.NewTaskRepo(client.Client)
	runs := repo.NewStageRunRepo(client.Client)

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
	return run.ID, task.ID
}

func newResolver(t *testing.T) (mcp.CallerResolver, *db.DBBundle) {
	t.Helper()
	bundle, err := db.Open(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { _ = bundle.Client.Close() })
	return mcp.CallerResolver{
		StageRuns: repo.NewStageRunRepo(bundle.Client),
		Tasks:     repo.NewTaskRepo(bundle.Client),
	}, bundle
}

func TestCallerResolver_NoAuthYieldsNoContexts(t *testing.T) {
	resolver, _ := newResolver(t)
	require.Empty(t, resolver.Contexts(context.Background()))
}

// A machine-wide key must behave exactly as it did before this existed.
func TestCallerResolver_UserKeyYieldsNoContexts(t *testing.T) {
	resolver, _ := newResolver(t)
	ctx := mcp.ContextWithAuth(context.Background(), &mcp.MCPAuthInfo{KeyID: "k1"})
	require.Empty(t, resolver.Contexts(ctx))
}

func TestCallerResolver_StageRunKeyYieldsTaskAndRoutine(t *testing.T) {
	resolver, bundle := newResolver(t)
	runID, taskID := seedRun(t, bundle, "from-routine", "sched-1")

	ctx := mcp.ContextWithAuth(context.Background(), &mcp.MCPAuthInfo{KeyID: "k1", StageRunID: runID})
	got := resolver.Contexts(ctx)

	require.Equal(t, []capability.Context{
		{Kind: "task", Ref: taskID},
		{Kind: "routine", Ref: "sched-1"},
	}, got)
}

// A hand-created task has no routine. A routine context with an empty ref
// would match every grant stored with an empty ContextRef, so it must be
// omitted rather than emitted blank — the same rule memory.RoutineContext applies.
func TestCallerResolver_TaskWithoutRoutineOmitsTheRoutineContext(t *testing.T) {
	resolver, bundle := newResolver(t)
	runID, taskID := seedRun(t, bundle, "hand-made", "")

	ctx := mcp.ContextWithAuth(context.Background(), &mcp.MCPAuthInfo{KeyID: "k1", StageRunID: runID})
	got := resolver.Contexts(ctx)

	require.Equal(t, []capability.Context{{Kind: "task", Ref: taskID}}, got)
}

// A key naming a stage run that no longer exists resolves to nothing rather
// than to a partial chain: failing open to "no context" is the same outcome as
// a user key, which is the safe direction.
func TestCallerResolver_UnknownStageRunYieldsNoContexts(t *testing.T) {
	resolver, _ := newResolver(t)
	ctx := mcp.ContextWithAuth(context.Background(), &mcp.MCPAuthInfo{KeyID: "k1", StageRunID: "gone"})
	require.Empty(t, resolver.Contexts(ctx))
}
