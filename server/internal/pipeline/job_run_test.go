package pipeline_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lx-wnk/kontor/server/internal/db"
	"github.com/lx-wnk/kontor/server/internal/db/ent"
	"github.com/lx-wnk/kontor/server/internal/db/repo"
	"github.com/lx-wnk/kontor/server/internal/pipeline"
)

// countingEnsureWorktreeFn counts how many times a worktree was requested.
type countingEnsureWorktreeFn struct {
	calls int
}

func (c *countingEnsureWorktreeFn) ensure(_ context.Context, task *ent.Task, worktreeRoot string) (string, string, error) {
	c.calls++
	return worktreeRoot + "/" + task.Slug, "wt/" + task.Slug, nil
}

// makeOrchWithCaptureSpawnAndWorktree builds an orchestrator like
// makeOrchWithCaptureSpawn but forces worktree creation and counts
// EnsureWorktreeFn calls, so tests can assert a job never triggers one.
func makeOrchWithCaptureSpawnAndWorktree(t *testing.T, spawnCapture *captureSpawnFn, wt *countingEnsureWorktreeFn) (*pipeline.PipelineOrchestrator, repo.TaskRepo) {
	t.Helper()
	bundle, err := db.Open(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { _ = bundle.Client.Close() })

	taskRepo := repo.NewTaskRepo(bundle.Client)

	orch, err := pipeline.NewOrchestrator(pipeline.OrchestratorOptions{
		TaskRepo:         taskRepo,
		StageRunRepo:     repo.NewStageRunRepo(bundle.Client),
		PermissionRepo:   repo.NewPermissionRepo(bundle.Client),
		AuditRepo:        repo.NewAuditEventRepo(bundle.Client),
		ConfigRepo:       repo.NewPipelineConfigRepo(bundle.Client),
		SpawnFn:          spawnCapture.spawn,
		ForceWorktrees:   true,
		EnsureWorktreeFn: wt.ensure,
	})
	require.NoError(t, err)
	return orch, taskRepo
}

// TestJob_NeverCreatesWorktreeEvenWhenForced verifies job tasks run in their
// own directory (task.Cwd), never a git worktree, even with ForceWorktrees.
func TestJob_NeverCreatesWorktreeEvenWhenForced(t *testing.T) {
	spawnCapture := &captureSpawnFn{}
	wt := &countingEnsureWorktreeFn{}
	orch, taskRepo := makeOrchWithCaptureSpawnAndWorktree(t, spawnCapture, wt)
	ctx := context.Background()

	cwd := t.TempDir()
	task, err := taskRepo.Create(ctx, repo.CreateTaskInput{
		Slug:          "job-task",
		Title:         "Job task",
		Cwd:           cwd,
		CurrentStage:  pipeline.StageJob,
		Priority:      "medium",
		MaxIterations: 3,

		Kind: pipeline.TaskKindJob,
	})
	require.NoError(t, err)

	_, err = orch.ProgressTask(ctx, task.ID, nil)
	require.NoError(t, err)

	assert.Equal(t, 0, wt.calls, "job task must never trigger EnsureWorktreeFn")
	opts := spawnCapture.capturedOpts()
	require.NotNil(t, opts, "job task must still spawn an agent")
	assert.Equal(t, cwd, opts.Task.Cwd)
	assert.True(t, opts.Task.WorktreePath == nil || *opts.Task.WorktreePath == "",
		"job task must not carry a worktree path")
}

// TestPipeline_StillCreatesWorktreeWhenForced verifies the job guard does not
// regress ForceWorktrees for regular pipeline tasks.
func TestPipeline_StillCreatesWorktreeWhenForced(t *testing.T) {
	spawnCapture := &captureSpawnFn{}
	wt := &countingEnsureWorktreeFn{}
	orch, taskRepo := makeOrchWithCaptureSpawnAndWorktree(t, spawnCapture, wt)
	ctx := context.Background()

	task, err := taskRepo.Create(ctx, repo.CreateTaskInput{
		Slug:          "pipeline-task",
		Title:         "Pipeline task",
		Cwd:           t.TempDir(),
		CurrentStage:  "implementation",
		Priority:      "medium",
		MaxIterations: 3,
	})
	require.NoError(t, err)

	_, err = orch.ProgressTask(ctx, task.ID, nil)
	require.NoError(t, err)

	assert.Equal(t, 1, wt.calls, "pipeline task must still get a worktree when forced")
}

// TestJob_CompletedRunFinishesTheTask verifies a completed job stage_run
// decides DoneTransition directly, without falling through to NextStage.
func TestJob_CompletedRunFinishesTheTask(t *testing.T) {
	spawnCapture := &captureSpawnFn{}
	orch, taskRepo, _, _ := makeOrchWithCaptureSpawn(t, spawnCapture)
	ctx := context.Background()

	task, err := taskRepo.Create(ctx, repo.CreateTaskInput{
		Slug:          "job-completed",
		Title:         "Job completed",
		Cwd:           t.TempDir(),
		CurrentStage:  pipeline.StageJob,
		Priority:      "medium",
		MaxIterations: 3,

		Kind: pipeline.TaskKindJob,
	})
	require.NoError(t, err)

	output := map[string]any{"summary": "s", "result": "r"}
	transition := orch.DecideCompletedTransitionForTest(ctx, task, &ent.StageRun{Stage: pipeline.StageJob}, output)

	done, ok := transition.(pipeline.DoneTransition)
	require.True(t, ok, "expected DoneTransition, got %T", transition)
	assert.Equal(t, output, done.Output)
}
