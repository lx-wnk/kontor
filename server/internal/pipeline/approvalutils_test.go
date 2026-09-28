package pipeline_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/lx-wnk/kontor/server/internal/db"
	"github.com/lx-wnk/kontor/server/internal/db/repo"
	"github.com/lx-wnk/kontor/server/internal/pipeline"
)

func TestBulkGrantConceptStagePermissions_GrantsMissingTools(t *testing.T) {
	bundle, err := db.Open(":memory:")
	require.NoError(t, err)
	defer bundle.Client.Close() //nolint:errcheck

	taskRepo := repo.NewTaskRepo(bundle.Client)
	permRepo := repo.NewPermissionRepo(bundle.Client)
	ctx := context.Background()

	// Create a minimal task so task_id FK constraint is satisfied.
	task, err := taskRepo.Create(ctx, repo.CreateTaskInput{
		Slug:  "test-task",
		Title: "Test Task",
		Cwd:   "/tmp",
	})
	require.NoError(t, err)

	err = pipeline.BulkGrantConceptStagePermissions(ctx, task.ID, permRepo)
	require.NoError(t, err)

	perms, err := permRepo.ListEffectiveTaskPermissions(ctx, task.ID)
	require.NoError(t, err)

	granted := make(map[string]bool)
	for _, p := range perms {
		granted[p.Tool] = true
	}
	require.True(t, granted["Read"], "Read must be granted")
	require.True(t, granted["Glob"], "Glob must be granted")
	require.True(t, granted["Grep"], "Grep must be granted")
}

func TestBulkGrantConceptStagePermissions_IdempotentWhenAlreadyGranted(t *testing.T) {
	bundle, err := db.Open(":memory:")
	require.NoError(t, err)
	defer bundle.Client.Close() //nolint:errcheck

	taskRepo := repo.NewTaskRepo(bundle.Client)
	permRepo := repo.NewPermissionRepo(bundle.Client)
	ctx := context.Background()

	task, err := taskRepo.Create(ctx, repo.CreateTaskInput{
		Slug:  "test-task-2",
		Title: "Test Task 2",
		Cwd:   "/tmp",
	})
	require.NoError(t, err)

	// Pre-grant all backlog tools.
	_, err = permRepo.BulkGrantPermissions(ctx, task.ID, []repo.GrantEntry{
		{Tool: "Read"},
		{Tool: "Glob"},
		{Tool: "Grep"},
	})
	require.NoError(t, err)

	// Should not error and should not double-grant.
	err = pipeline.BulkGrantConceptStagePermissions(ctx, task.ID, permRepo)
	require.NoError(t, err)

	perms, err := permRepo.ListEffectiveTaskPermissions(ctx, task.ID)
	require.NoError(t, err)
	require.Len(t, perms, 3, "must not duplicate existing permissions")
}
