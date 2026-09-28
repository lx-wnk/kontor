package pipeline_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/lx-wnk/kontor/server/internal/db/repo"
	"github.com/lx-wnk/kontor/server/internal/pipeline"
)

// newReleaseOrch builds an orchestrator wired to revokes, plus the repos a
// release test needs to create its own task and stage run.
func newReleaseOrch(t *testing.T, revokes *recordedRevokes) (*pipeline.PipelineOrchestrator, repo.TaskRepo, repo.StageRunRepo) {
	t.Helper()
	bundle := openSharedBundle(t)
	c := bundle.Client
	taskRepo := repo.NewTaskRepo(c)
	srRepo := repo.NewStageRunRepo(c)
	orch, err := pipeline.NewOrchestrator(pipeline.OrchestratorOptions{
		TaskRepo:          taskRepo,
		StageRunRepo:      srRepo,
		PermissionRepo:    repo.NewPermissionRepo(c),
		AuditRepo:         repo.NewAuditEventRepo(c),
		ConfigRepo:        repo.NewPipelineConfigRepo(c),
		Client:            c,
		RevokeTaskAPIKeys: revokes.revoke,
	})
	require.NoError(t, err)
	return orch, taskRepo, srRepo
}

func seedRunningRun(t *testing.T, ctx context.Context, taskRepo repo.TaskRepo, srRepo repo.StageRunRepo, slug string) (string, string) {
	t.Helper()
	task, err := taskRepo.Create(ctx, repo.CreateTaskInput{
		Slug:          slug,
		Title:         slug,
		Cwd:           "/tmp",
		CurrentStage:  "implementation",
		Priority:      "medium",
		MaxIterations: 3,
	})
	require.NoError(t, err)
	sr, err := srRepo.Create(ctx, repo.CreateStageRunInput{
		TaskID:      task.ID,
		Stage:       "implementation",
		Iteration:   0,
		SessionName: slug + "-session",
	})
	require.NoError(t, err)
	running := "running"
	_, err = srRepo.Update(ctx, sr.ID, repo.UpdateStageRunInput{Status: &running})
	require.NoError(t, err)
	return task.ID, sr.ID
}

// TestApplyTransition_RequeueReleasesTheRequeuedRun pins the requeue arm of
// applyTransitionWrites. A requeue is only ever decided from a completion
// result, so the agent has already exited — yet the arm shipped without a
// revocation hook, and sweepRequeueableRuns later promotes the run back to
// pending, where the respawn mints a second key for the same stage_run_id.
// Deleting the hook compiles, vets and passes every other test.
func TestApplyTransition_RequeueReleasesTheRequeuedRun(t *testing.T) {
	ctx := context.Background()
	revokes := &recordedRevokes{}
	orch, taskRepo, srRepo := newReleaseOrch(t, revokes)

	taskID, srID := seedRunningRun(t, ctx, taskRepo, srRepo, "requeue-release")
	task, err := taskRepo.GetByID(ctx, taskID)
	require.NoError(t, err)
	sr, err := srRepo.GetByID(ctx, srID)
	require.NoError(t, err)

	orch.SeedTaskLockForTest(taskID)
	_, applyErr := orch.ApplyTransitionForTest(ctx, task, sr, pipeline.RequeueTransition{
		Reason:      "infra blip",
		Attempt:     1,
		NextRetryAt: time.Now().Add(time.Minute),
	})
	require.NoError(t, applyErr)

	require.Equal(t, []string{srID}, revokes.all(),
		"requeueing a run must revoke its credentials, else the respawn adds a second valid key")

	updated, err := srRepo.GetByID(ctx, srID)
	require.NoError(t, err)
	require.Equal(t, "requeued", updated.Status, "the run must still be requeued, not ended")
}

// TestNotifyTaskTerminated_CancelsOpenStageRunsAndRevokes covers task
// cancellation, which writes the terminal stage only on the task row. Its
// stage run stayed "running" with a live credential until expires_at; both
// cancel routes (HTTP handler and the MCP control tool) funnel through
// NotifyTaskTerminated, so the guard belongs there.
func TestNotifyTaskTerminated_CancelsOpenStageRunsAndRevokes(t *testing.T) {
	ctx := context.Background()
	revokes := &recordedRevokes{}
	orch, taskRepo, srRepo := newReleaseOrch(t, revokes)

	taskID, srID := seedRunningRun(t, ctx, taskRepo, srRepo, "cancel-release")

	orch.NotifyTaskTerminated(ctx, taskID, "cancelled")

	updated, err := srRepo.GetByID(ctx, srID)
	require.NoError(t, err)
	require.Equal(t, "cancelled", updated.Status,
		"cancelling a task must end its open stage run, else the run keeps a valid credential")
	require.NotNil(t, updated.EndedAt, "a cancelled run must carry an end timestamp")
	require.Equal(t, []string{srID}, revokes.all(),
		"ending the run must revoke the credentials the abandoned agent still holds")
}

// TestNotifyTaskTerminated_LeavesTerminalRunsAlone guards the other direction:
// a run that already ended must not be rewritten, which would revoke twice and
// overwrite its real ended_at with the cancellation time.
func TestNotifyTaskTerminated_LeavesTerminalRunsAlone(t *testing.T) {
	ctx := context.Background()
	revokes := &recordedRevokes{}
	orch, taskRepo, srRepo := newReleaseOrch(t, revokes)

	taskID, srID := seedRunningRun(t, ctx, taskRepo, srRepo, "cancel-terminal")
	done := "done"
	endedAt := time.Now().Add(-time.Hour)
	_, err := srRepo.Update(ctx, srID, repo.UpdateStageRunInput{Status: &done, EndedAt: &endedAt})
	require.NoError(t, err)

	orch.NotifyTaskTerminated(ctx, taskID, "cancelled")

	updated, err := srRepo.GetByID(ctx, srID)
	require.NoError(t, err)
	require.Equal(t, "done", updated.Status, "an already-terminal run must keep its own outcome")
	require.Empty(t, revokes.all(), "a run this call did not end must not be revoked by it")
}

// TestSpawnCleanup_RunsWhenTheRunIsReleased is the end-to-end anchor on the
// wiring itself: StageContext.RegisterSpawnCleanup in progress_guards.go and
// the call in stage_handlers.go. SpawnResult.Cleanup deletes the temp
// --mcp-config file holding the run's bearer token; before this it had no
// production caller at all, so those files accumulated in
// os.TempDir()/dashboard-<uid>/ readable by every agent running as the same
// user. Removing either wiring line compiles, vets and passes every other test.
func TestSpawnCleanup_RunsWhenTheRunIsReleased(t *testing.T) {
	ctx := context.Background()
	revokes := &recordedRevokes{}
	bundle := openSharedBundle(t)
	c := bundle.Client
	taskRepo := repo.NewTaskRepo(c)
	srRepo := repo.NewStageRunRepo(c)
	orch, err := pipeline.NewOrchestrator(pipeline.OrchestratorOptions{
		TaskRepo:          taskRepo,
		StageRunRepo:      srRepo,
		PermissionRepo:    repo.NewPermissionRepo(c),
		AuditRepo:         repo.NewAuditEventRepo(c),
		ConfigRepo:        repo.NewPipelineConfigRepo(c),
		Client:            c,
		RevokeTaskAPIKeys: revokes.revoke,
	})
	require.NoError(t, err)

	cleaned := make(chan struct{}, 1)
	spawnFn := func(pipeline.SpawnAgentOptions) (pipeline.SpawnResult, error) {
		return pipeline.SpawnResult{
			PID:     4242,
			Cleanup: func() { cleaned <- struct{}{} },
		}, nil
	}
	orch.SetHandlerOverride("implementation", pipeline.NewAgentStageHandlerForTest("implementation", spawnFn))

	task, err := taskRepo.Create(ctx, repo.CreateTaskInput{
		Slug:          "spawn-cleanup",
		Title:         "Spawn cleanup",
		Cwd:           "/tmp",
		CurrentStage:  "implementation",
		Priority:      "medium",
		MaxIterations: 3,
	})
	require.NoError(t, err)

	sr, err := orch.ProgressTask(ctx, task.ID, nil)
	require.NoError(t, err)
	require.NotNil(t, sr, "the override handler must have spawned a run")
	require.Empty(t, cleaned, "the cleanup must not run while the agent is alive")

	orch.NotifyTaskTerminated(ctx, task.ID, "cancelled")

	require.Len(t, cleaned, 1,
		"ending the run must remove the spawn artifacts, above all the temp MCP config holding its token")
}

// TestCascadeCancel_ReleasesTheDownstreamRun covers the second cancel path. A
// downstream task cancelled by its upstream reaches cancellation through
// handleDependentTasks -> cascadeCancelDownstream, never through
// NotifyTaskTerminated, so the guard on the direct route does not cover it: the
// cascade wrote the task's stage and left the downstream agent's stage run
// running with a live credential.
func TestCascadeCancel_ReleasesTheDownstreamRun(t *testing.T) {
	ctx := context.Background()
	revokes := &recordedRevokes{}
	bundle := openSharedBundle(t)
	c := bundle.Client
	taskRepo := repo.NewTaskRepo(c)
	srRepo := repo.NewStageRunRepo(c)
	depRepo := repo.NewDependencyRepo(c)
	orch, err := pipeline.NewOrchestrator(pipeline.OrchestratorOptions{
		TaskRepo:          taskRepo,
		StageRunRepo:      srRepo,
		PermissionRepo:    repo.NewPermissionRepo(c),
		AuditRepo:         repo.NewAuditEventRepo(c),
		ConfigRepo:        repo.NewPipelineConfigRepo(c),
		DepRepo:           depRepo,
		Client:            c,
		RevokeTaskAPIKeys: revokes.revoke,
	})
	require.NoError(t, err)

	upstreamID, _ := seedRunningRun(t, ctx, taskRepo, srRepo, "cascade-upstream")
	downstreamID, downstreamRunID := seedRunningRun(t, ctx, taskRepo, srRepo, "cascade-downstream")
	_, err = depRepo.Add(ctx, downstreamID, upstreamID, "implementation", "cancel")
	require.NoError(t, err)

	orch.NotifyTaskTerminated(ctx, upstreamID, "cancelled")

	downstream, err := taskRepo.GetByID(ctx, downstreamID)
	require.NoError(t, err)
	require.Equal(t, "cancelled", downstream.CurrentStage, "the cascade must cancel the downstream task")

	run, err := srRepo.GetByID(ctx, downstreamRunID)
	require.NoError(t, err)
	require.Equal(t, "cancelled", run.Status,
		"a cascade-cancelled task's stage run must end too, else its agent keeps a valid credential")
	require.Contains(t, revokes.all(), downstreamRunID,
		"the cascade must revoke the downstream run's credentials")
}
