package repo_test

import (
	"context"
	"testing"
	"time"

	"github.com/lx-wnk/kontor/server/internal/db/repo"
	"github.com/stretchr/testify/require"
)

func TestStageRunRepo_CreateAndGet(t *testing.T) {
	client := openDB(t)
	ctx := context.Background()
	tr := repo.NewTaskRepo(client)
	sr := repo.NewStageRunRepo(client)

	taskID := createTask(t, tr, "sr-task-1")

	run, err := sr.Create(ctx, repo.CreateStageRunInput{
		TaskID:    taskID,
		Stage:     "backlog",
		Iteration: 1,
	})
	require.NoError(t, err)
	require.Equal(t, taskID, run.TaskID)
	require.Equal(t, "backlog", run.Stage)

	got, err := sr.GetByID(ctx, run.ID)
	require.NoError(t, err)
	require.Equal(t, run.ID, got.ID)
}

func TestStageRunRepo_GetLatestForTask(t *testing.T) {
	client := openDB(t)
	ctx := context.Background()
	tr := repo.NewTaskRepo(client)
	sr := repo.NewStageRunRepo(client)

	taskID := createTask(t, tr, "sr-task-2")

	run1, err := sr.Create(ctx, repo.CreateStageRunInput{TaskID: taskID, Stage: "backlog", Iteration: 1})
	require.NoError(t, err)
	run2, err := sr.Create(ctx, repo.CreateStageRunInput{TaskID: taskID, Stage: "backlog", Iteration: 2})
	require.NoError(t, err)

	latest, err := sr.GetLatestForTask(ctx, taskID)
	require.NoError(t, err)
	// Latest by created_at — run2 was created after run1.
	require.NotEqual(t, run1.ID, latest.ID)
	require.Equal(t, run2.ID, latest.ID)
}

func TestStageRunRepo_ListByStatus(t *testing.T) {
	client := openDB(t)
	ctx := context.Background()
	tr := repo.NewTaskRepo(client)
	sr := repo.NewStageRunRepo(client)

	taskID := createTask(t, tr, "sr-task-3")

	run1, err := sr.Create(ctx, repo.CreateStageRunInput{TaskID: taskID, Stage: "backlog", Iteration: 1})
	require.NoError(t, err)
	_, err = sr.Create(ctx, repo.CreateStageRunInput{TaskID: taskID, Stage: "backlog", Iteration: 2})
	require.NoError(t, err)

	status := "running"
	_, err = sr.Update(ctx, run1.ID, repo.UpdateStageRunInput{Status: &status})
	require.NoError(t, err)

	running, err := sr.ListByStatus(ctx, "running")
	require.NoError(t, err)
	require.Len(t, running, 1)
	require.Equal(t, run1.ID, running[0].ID)
}

func TestStageRunRepo_Update_Status(t *testing.T) {
	client := openDB(t)
	ctx := context.Background()
	tr := repo.NewTaskRepo(client)
	sr := repo.NewStageRunRepo(client)

	taskID := createTask(t, tr, "sr-task-4")
	run, err := sr.Create(ctx, repo.CreateStageRunInput{TaskID: taskID, Stage: "backlog", Iteration: 1})
	require.NoError(t, err)

	status := "running"
	updated, err := sr.Update(ctx, run.ID, repo.UpdateStageRunInput{Status: &status})
	require.NoError(t, err)
	require.Equal(t, "running", updated.Status)
}

func TestStageRunRepo_SumCompletedCostCents(t *testing.T) {
	client := openDB(t)
	ctx := context.Background()
	tr := repo.NewTaskRepo(client)
	sr := repo.NewStageRunRepo(client)

	taskID := createTask(t, tr, "sr-task-5")

	run1, err := sr.Create(ctx, repo.CreateStageRunInput{TaskID: taskID, Stage: "backlog", Iteration: 1})
	require.NoError(t, err)
	run2, err := sr.Create(ctx, repo.CreateStageRunInput{TaskID: taskID, Stage: "backlog", Iteration: 2})
	require.NoError(t, err)
	run3, err := sr.Create(ctx, repo.CreateStageRunInput{TaskID: taskID, Stage: "backlog", Iteration: 3})
	require.NoError(t, err)

	doneCost := 100
	status := "done"
	_, err = sr.Update(ctx, run1.ID, repo.UpdateStageRunInput{Status: &status, CostCents: &doneCost})
	require.NoError(t, err)
	failCost := 100
	failStatus := "failed"
	_, err = sr.Update(ctx, run2.ID, repo.UpdateStageRunInput{Status: &failStatus, CostCents: &failCost})
	require.NoError(t, err)
	pendingCost := 50
	_, err = sr.Update(ctx, run3.ID, repo.UpdateStageRunInput{CostCents: &pendingCost})
	require.NoError(t, err)

	sum, err := sr.SumCompletedCostCents(ctx, taskID)
	require.NoError(t, err)
	require.Equal(t, int64(200), sum)
}

func TestStageRunRepo_GetLatestForTasks(t *testing.T) {
	client := openDB(t)
	ctx := context.Background()
	tr := repo.NewTaskRepo(client)
	sr := repo.NewStageRunRepo(client)

	taskID1 := createTask(t, tr, "sr-multi-1")
	taskID2 := createTask(t, tr, "sr-multi-2")

	_, err := sr.Create(ctx, repo.CreateStageRunInput{TaskID: taskID1, Stage: "backlog", Iteration: 1})
	require.NoError(t, err)
	run1b, err := sr.Create(ctx, repo.CreateStageRunInput{TaskID: taskID1, Stage: "backlog", Iteration: 2})
	require.NoError(t, err)
	run2, err := sr.Create(ctx, repo.CreateStageRunInput{TaskID: taskID2, Stage: "backlog", Iteration: 1})
	require.NoError(t, err)

	latest, err := sr.GetLatestForTasks(ctx, []string{taskID1, taskID2})
	require.NoError(t, err)
	require.Equal(t, run1b.ID, latest[taskID1].ID)
	require.Equal(t, run2.ID, latest[taskID2].ID)
}

func TestStageRunRepo_Update_StartedAtClear(t *testing.T) {
	client := openDB(t)
	ctx := context.Background()
	tr := repo.NewTaskRepo(client)
	sr := repo.NewStageRunRepo(client)

	taskID := createTask(t, tr, "sr-started-at-clear")
	run, err := sr.Create(ctx, repo.CreateStageRunInput{TaskID: taskID, Stage: "backlog", Iteration: 0})
	require.NoError(t, err)

	now := time.Now().Truncate(time.Second)
	updated, err := sr.Update(ctx, run.ID, repo.UpdateStageRunInput{StartedAt: &now})
	require.NoError(t, err)
	require.NotNil(t, updated.StartedAt)
	require.Equal(t, now.UTC(), updated.StartedAt.UTC())

	cleared, err := sr.Update(ctx, run.ID, repo.UpdateStageRunInput{StartedAtClear: true})
	require.NoError(t, err)
	require.Nil(t, cleared.StartedAt)

	reloaded, err := sr.GetByID(ctx, run.ID)
	require.NoError(t, err)
	require.Nil(t, reloaded.StartedAt)
}

func TestStageRunRepo_ListInWindow(t *testing.T) {
	client := openDB(t)
	ctx := context.Background()
	tr := repo.NewTaskRepo(client)
	sr := repo.NewStageRunRepo(client)

	taskID := createTask(t, tr, "sr-window-1")

	// Create three stage_runs and note their creation times.
	run1, err := sr.Create(ctx, repo.CreateStageRunInput{TaskID: taskID, Stage: "backlog", Iteration: 1})
	require.NoError(t, err)
	run2, err := sr.Create(ctx, repo.CreateStageRunInput{TaskID: taskID, Stage: "implement", Iteration: 1})
	require.NoError(t, err)
	run3, err := sr.Create(ctx, repo.CreateStageRunInput{TaskID: taskID, Stage: "review", Iteration: 1})
	require.NoError(t, err)

	// All three share the same created_at second — use a window that captures all.
	from := run1.CreatedAt.Add(-time.Second)
	to := run3.CreatedAt.Add(time.Second)

	all, err := sr.ListInWindow(ctx, from, to)
	require.NoError(t, err)
	require.Len(t, all, 3)

	// Window that excludes future runs (before run1 was created) should return nothing.
	none, err := sr.ListInWindow(ctx, run1.CreatedAt.Add(-2*time.Hour), run1.CreatedAt.Add(-time.Hour))
	require.NoError(t, err)
	require.Len(t, none, 0)

	// Verify IDs round-trip.
	ids := make(map[string]struct{}, 3)
	for _, r := range all {
		ids[r.ID] = struct{}{}
	}
	require.Contains(t, ids, run1.ID)
	require.Contains(t, ids, run2.ID)
	require.Contains(t, ids, run3.ID)
}

func TestStageRunRepo_Update_RetryFields(t *testing.T) {
	client := openDB(t)
	ctx := context.Background()
	tr := repo.NewTaskRepo(client)
	sr := repo.NewStageRunRepo(client)

	taskID := createTask(t, tr, "sr-retry-1")
	run, err := sr.Create(ctx, repo.CreateStageRunInput{TaskID: taskID, Stage: "backlog", Iteration: 1})
	require.NoError(t, err)

	retryCount := 3
	nextRetryAt := time.Now().Add(time.Minute).Truncate(time.Second)
	updated, err := sr.Update(ctx, run.ID, repo.UpdateStageRunInput{
		RetryCount:  &retryCount,
		NextRetryAt: &nextRetryAt,
	})
	require.NoError(t, err)
	require.Equal(t, 3, updated.RetryCount)
	require.NotNil(t, updated.NextRetryAt)
	require.Equal(t, nextRetryAt.UTC(), updated.NextRetryAt.UTC())

	reloaded, err := sr.GetByID(ctx, run.ID)
	require.NoError(t, err)
	require.Equal(t, 3, reloaded.RetryCount)
	require.NotNil(t, reloaded.NextRetryAt)
	require.Equal(t, nextRetryAt.UTC(), reloaded.NextRetryAt.UTC())

	cleared, err := sr.Update(ctx, run.ID, repo.UpdateStageRunInput{NextRetryAtClear: true})
	require.NoError(t, err)
	require.Nil(t, cleared.NextRetryAt)

	reloaded2, err := sr.GetByID(ctx, run.ID)
	require.NoError(t, err)
	require.Nil(t, reloaded2.NextRetryAt)
}

func TestStageRunRepo_ListBySessionIDs(t *testing.T) {
	client := openDB(t)
	ctx := context.Background()
	tr := repo.NewTaskRepo(client)
	sr := repo.NewStageRunRepo(client)

	taskID := createTask(t, tr, "sr-batch-session")

	run1, err := sr.Create(ctx, repo.CreateStageRunInput{TaskID: taskID, Stage: "backlog", Iteration: 1})
	require.NoError(t, err)
	run2, err := sr.Create(ctx, repo.CreateStageRunInput{TaskID: taskID, Stage: "implement", Iteration: 1})
	require.NoError(t, err)
	// run3 is left without a session_id — it must never surface in a session lookup.
	_, err = sr.Create(ctx, repo.CreateStageRunInput{TaskID: taskID, Stage: "review", Iteration: 1})
	require.NoError(t, err)

	sess1 := "sess-batch-1"
	sess2 := "sess-batch-2"
	_, err = sr.Update(ctx, run1.ID, repo.UpdateStageRunInput{SessionID: &sess1})
	require.NoError(t, err)
	_, err = sr.Update(ctx, run2.ID, repo.UpdateStageRunInput{SessionID: &sess2})
	require.NoError(t, err)

	tests := []struct {
		name       string
		sessionIDs []string
		wantIDs    []string
	}{
		{name: "empty input returns nil", sessionIDs: nil, wantIDs: nil},
		{name: "no match returns empty", sessionIDs: []string{"sess-unknown"}, wantIDs: nil},
		{name: "single match", sessionIDs: []string{sess1}, wantIDs: []string{run1.ID}},
		{name: "multi match ignores unknown", sessionIDs: []string{sess1, sess2, "sess-unknown"}, wantIDs: []string{run1.ID, run2.ID}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := sr.ListBySessionIDs(ctx, tt.sessionIDs)
			require.NoError(t, err)
			gotIDs := make([]string, 0, len(got))
			for _, r := range got {
				gotIDs = append(gotIDs, r.ID)
			}
			require.ElementsMatch(t, tt.wantIDs, gotIDs)
		})
	}
}

func TestStageRunRepo_SumCompletedTokens(t *testing.T) {
	client := openDB(t)
	ctx := context.Background()
	tr := repo.NewTaskRepo(client)
	sr := repo.NewStageRunRepo(client)

	taskID := createTask(t, tr, "sr-task-tokens")

	run1, err := sr.Create(ctx, repo.CreateStageRunInput{TaskID: taskID, Stage: "backlog", Iteration: 1})
	require.NoError(t, err)
	run2, err := sr.Create(ctx, repo.CreateStageRunInput{TaskID: taskID, Stage: "backlog", Iteration: 2})
	require.NoError(t, err)
	run3, err := sr.Create(ctx, repo.CreateStageRunInput{TaskID: taskID, Stage: "backlog", Iteration: 3})
	require.NoError(t, err)

	doneTok := 1000
	status := "done"
	_, err = sr.Update(ctx, run1.ID, repo.UpdateStageRunInput{Status: &status, TokensUsed: &doneTok})
	require.NoError(t, err)
	failTok := 1000
	failStatus := "failed"
	_, err = sr.Update(ctx, run2.ID, repo.UpdateStageRunInput{Status: &failStatus, TokensUsed: &failTok})
	require.NoError(t, err)
	pendingTok := 500
	_, err = sr.Update(ctx, run3.ID, repo.UpdateStageRunInput{TokensUsed: &pendingTok})
	require.NoError(t, err)

	sum, err := sr.SumCompletedTokens(ctx, taskID)
	require.NoError(t, err)
	require.Equal(t, int64(2000), sum)
}
