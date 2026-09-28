package pipeline_test

import (
	"context"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/lx-wnk/kontor/server/internal/db"
	"github.com/lx-wnk/kontor/server/internal/db/ent"
	"github.com/lx-wnk/kontor/server/internal/db/repo"
	"github.com/lx-wnk/kontor/server/internal/pipeline"
)

func orchWithCheckpointerSeams(t *testing.T, bundle *db.DBBundle, start func(string, string), stop func(string)) (*pipeline.PipelineOrchestrator, repo.TaskRepo) {
	t.Helper()
	c := bundle.Client
	taskRepo := repo.NewTaskRepo(c)
	orch, err := pipeline.NewOrchestrator(pipeline.OrchestratorOptions{
		TaskRepo:            taskRepo,
		StageRunRepo:        repo.NewStageRunRepo(c),
		PermissionRepo:      repo.NewPermissionRepo(c),
		AuditRepo:           repo.NewAuditEventRepo(c),
		ConfigRepo:          repo.NewPipelineConfigRepo(c),
		CheckpointerStartFn: start,
		CheckpointerStopFn:  stop,
		RemoveWorktreeFn: func(ctx context.Context, task *ent.Task, _ bool) error {
			_, _ = taskRepo.Update(ctx, task.ID, repo.UpdateTaskInput{ClearWorktreePath: true})
			return nil
		},
	})
	require.NoError(t, err)
	return orch, taskRepo
}

func TestCheckpointerStartFn_FiresWhenRunning(t *testing.T) {
	ctx := context.Background()
	bundle := openSharedBundle(t)
	var starts atomic.Int32
	var startedPath atomic.Value
	orch, taskRepo := orchWithCheckpointerSeams(t, bundle,
		func(_, path string) { starts.Add(1); startedPath.Store(path) },
		func(string) {})

	task := seedTaskWithWorktree(t, ctx, taskRepo, "cp-start", "implementation")
	orch.SetHandlerOverride("implementation", &agentStubHandler{
		stage:      "implementation",
		transition: pipeline.WaitUserTransition{Reason: "stub", AgentDone: true},
	})

	_, err := orch.ProgressTask(ctx, task.ID, nil)
	require.NoError(t, err)

	require.Equal(t, int32(1), starts.Load(), "CheckpointerStartFn must fire once the run is running")
	require.Equal(t, *task.WorktreePath, startedPath.Load(), "start must receive the worktree path")
}

func TestCheckpointerStopFn_FiresBeforeWorktreeRemoval(t *testing.T) {
	ctx := context.Background()
	bundle := openSharedBundle(t)
	var stops atomic.Int32
	orch, taskRepo := orchWithCheckpointerSeams(t, bundle,
		func(string, string) {},
		func(string) { stops.Add(1) })

	task := seedTaskWithWorktree(t, ctx, taskRepo, "cp-stop", "implementation")
	orch.NotifyTaskTerminated(ctx, task.ID, "cancelled")

	require.Equal(t, int32(1), stops.Load(), "CheckpointerStopFn must fire on terminal cleanup")
}
