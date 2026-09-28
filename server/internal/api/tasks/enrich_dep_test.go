package tasks_test

import (
	"context"
	"testing"

	"github.com/lx-wnk/kontor/server/internal/api/tasks"
	"github.com/lx-wnk/kontor/server/internal/db"
	"github.com/lx-wnk/kontor/server/internal/db/repo"
	"github.com/stretchr/testify/require"
)

// openEnrichDB opens an in-memory SQLite DB for enrich dep tests.
func openEnrichDB(t *testing.T) *db.DBBundle {
	t.Helper()
	bundle, err := db.Open(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { _ = bundle.Client.Close() })
	return bundle
}

// createEnrichTask creates a task at the given stage.
func createEnrichTask(t *testing.T, taskRepo repo.TaskRepo, slug, stage string) string {
	t.Helper()
	task, err := taskRepo.Create(context.Background(), repo.CreateTaskInput{
		Slug:          slug,
		Title:         slug,
		Cwd:           "/tmp",
		CurrentStage:  stage,
		Priority:      "medium",
		MaxIterations: 3,
	})
	require.NoError(t, err)
	return task.ID
}

// enrichWithDeps calls EnrichTaskWithDeps with real repos and a nil bulkRepo
// (bulkRepo is only needed for child-summary, which is out of scope here).
func enrichWithDeps(t *testing.T, taskRepo repo.TaskRepo, srRepo repo.StageRunRepo, permRepo repo.PermissionRepo, depRepo repo.DependencyRepo, taskID string) *tasks.EnrichedTask {
	t.Helper()
	task, err := taskRepo.GetByID(context.Background(), taskID)
	require.NoError(t, err)
	enriched, err := tasks.EnrichTaskWithDeps(context.Background(), task, srRepo, permRepo, nil, depRepo, taskRepo)
	require.NoError(t, err)
	return enriched
}

// TestEnrichDep_UpstreamInProgress_IsBlocked asserts that IsBlocked==true and
// IsUnsatisfiable==false when the upstream is still in progress.
func TestEnrichDep_UpstreamInProgress_IsBlocked(t *testing.T) {
	bundle := openEnrichDB(t)
	ctx := context.Background()
	taskRepo := repo.NewTaskRepo(bundle.Client)
	srRepo := repo.NewStageRunRepo(bundle.Client)
	permRepo := repo.NewPermissionRepo(bundle.Client)
	depRepo := repo.NewDependencyRepo(bundle.Client)

	upstreamID := createEnrichTask(t, taskRepo, "enrich-dep-up-progress", "implementation")
	downstreamID := createEnrichTask(t, taskRepo, "enrich-dep-down-progress", "ready")

	_, err := depRepo.Add(ctx, downstreamID, upstreamID, "done", "on_hold")
	require.NoError(t, err)

	enriched := enrichWithDeps(t, taskRepo, srRepo, permRepo, depRepo, downstreamID)

	require.True(t, enriched.IsBlocked, "IsBlocked must be true when upstream is in progress")
	require.False(t, enriched.IsUnsatisfiable, "IsUnsatisfiable must be false when upstream is merely in progress")
}

// TestEnrichDep_UpstreamCancelled_IsUnsatisfiable asserts that IsUnsatisfiable==true
// when the upstream is cancelled and on_cancel_action is not "start".
func TestEnrichDep_UpstreamCancelled_IsUnsatisfiable(t *testing.T) {
	bundle := openEnrichDB(t)
	ctx := context.Background()
	taskRepo := repo.NewTaskRepo(bundle.Client)
	srRepo := repo.NewStageRunRepo(bundle.Client)
	permRepo := repo.NewPermissionRepo(bundle.Client)
	depRepo := repo.NewDependencyRepo(bundle.Client)

	upstreamID := createEnrichTask(t, taskRepo, "enrich-dep-up-cancel", "cancelled")
	downstreamID := createEnrichTask(t, taskRepo, "enrich-dep-down-cancel", "ready")

	_, err := depRepo.Add(ctx, downstreamID, upstreamID, "done", "on_hold")
	require.NoError(t, err)

	enriched := enrichWithDeps(t, taskRepo, srRepo, permRepo, depRepo, downstreamID)

	require.True(t, enriched.IsUnsatisfiable, "IsUnsatisfiable must be true when upstream cancelled and action is not start")
	require.False(t, enriched.IsBlocked, "IsBlocked must be false when dep is unsatisfiable (not merely blocked)")
}

// TestEnrichDep_UpstreamDone_NeitherBlockedNorUnsatisfiable asserts that both
// flags are false when the upstream has reached the required "done" stage.
func TestEnrichDep_UpstreamDone_NeitherBlockedNorUnsatisfiable(t *testing.T) {
	bundle := openEnrichDB(t)
	ctx := context.Background()
	taskRepo := repo.NewTaskRepo(bundle.Client)
	srRepo := repo.NewStageRunRepo(bundle.Client)
	permRepo := repo.NewPermissionRepo(bundle.Client)
	depRepo := repo.NewDependencyRepo(bundle.Client)

	upstreamID := createEnrichTask(t, taskRepo, "enrich-dep-up-done", "done")
	downstreamID := createEnrichTask(t, taskRepo, "enrich-dep-down-done", "ready")

	_, err := depRepo.Add(ctx, downstreamID, upstreamID, "done", "on_hold")
	require.NoError(t, err)

	enriched := enrichWithDeps(t, taskRepo, srRepo, permRepo, depRepo, downstreamID)

	require.False(t, enriched.IsBlocked, "IsBlocked must be false when upstream is done")
	require.False(t, enriched.IsUnsatisfiable, "IsUnsatisfiable must be false when upstream is done")
}

// TestEnrichDep_NoDeps_NeitherBlockedNorUnsatisfiable asserts that a task with
// no upstream deps has both flags false.
func TestEnrichDep_NoDeps_NeitherBlockedNorUnsatisfiable(t *testing.T) {
	bundle := openEnrichDB(t)
	taskRepo := repo.NewTaskRepo(bundle.Client)
	srRepo := repo.NewStageRunRepo(bundle.Client)
	permRepo := repo.NewPermissionRepo(bundle.Client)
	depRepo := repo.NewDependencyRepo(bundle.Client)

	taskID := createEnrichTask(t, taskRepo, "enrich-dep-no-deps", "ready")

	enriched := enrichWithDeps(t, taskRepo, srRepo, permRepo, depRepo, taskID)

	require.False(t, enriched.IsBlocked, "IsBlocked must be false when task has no deps")
	require.False(t, enriched.IsUnsatisfiable, "IsUnsatisfiable must be false when task has no deps")
}
