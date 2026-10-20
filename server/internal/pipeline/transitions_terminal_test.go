package pipeline_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/lx-wnk/kontor/server/internal/db/repo"
	"github.com/lx-wnk/kontor/server/internal/pipeline"
)

// A caller reads the task and its running stage run, then the task is marked
// done (Mark Done / Cancel) before the caller's transition commits. The
// transition must not move the task out of its terminal stage or start new work.
func TestApplyTransition_TaskTerminatedMeanwhile_LeavesTaskTerminal(t *testing.T) {
	cases := map[string]pipeline.StageTransition{
		"next":    pipeline.NextTransition{Stage: "finalization"},
		"onHold":  pipeline.OnHoldTransition{PermissionRequestID: "perm-1"},
		"iterate": pipeline.IterateTransition{},
		"requeue": pipeline.RequeueTransition{Reason: "busy", Attempt: 1, NextRetryAt: time.Now().Add(time.Minute)},
	}
	for name, transition := range cases {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			c := openSharedBundle(t).Client
			taskRepo := repo.NewTaskRepo(c)
			srRepo := repo.NewStageRunRepo(c)

			orch, err := pipeline.NewOrchestrator(pipeline.OrchestratorOptions{
				TaskRepo:       taskRepo,
				StageRunRepo:   srRepo,
				PermissionRepo: repo.NewPermissionRepo(c),
				AuditRepo:      repo.NewAuditEventRepo(c),
				ConfigRepo:     repo.NewPipelineConfigRepo(c),
				Client:         c,
			})
			require.NoError(t, err)

			staleTask, err := taskRepo.Create(ctx, repo.CreateTaskInput{
				Slug:          "terminated-meanwhile-" + name,
				Title:         "Terminated meanwhile",
				Cwd:           "/tmp",
				CurrentStage:  "implementation",
				Priority:      "medium",
				MaxIterations: 3,
			})
			require.NoError(t, err)
			staleRun, err := srRepo.Create(ctx, repo.CreateStageRunInput{
				TaskID:      staleTask.ID,
				Stage:       "implementation",
				SessionName: "terminated-meanwhile-session",
			})
			require.NoError(t, err)

			done := "done"
			_, err = taskRepo.Update(ctx, staleTask.ID, repo.UpdateTaskInput{CurrentStage: &done})
			require.NoError(t, err)
			_, err = srRepo.Update(ctx, staleRun.ID, repo.UpdateStageRunInput{Status: strPtr("cancelled")})
			require.NoError(t, err)

			_, applyErr := orch.ApplyTransitionForTest(ctx, staleTask, staleRun, transition)

			require.Error(t, applyErr, "a transition on a task that turned terminal must be refused")
			reloaded, err := taskRepo.GetByID(ctx, staleTask.ID)
			require.NoError(t, err)
			require.Equal(t, "done", reloaded.CurrentStage)
			runs, err := srRepo.ListForTask(ctx, staleTask.ID)
			require.NoError(t, err)
			require.Len(t, runs, 1, "no new stage run may be started for a terminal task")
			require.Equal(t, "cancelled", runs[0].Status, "the run's terminal status must not be overwritten")
		})
	}
}
