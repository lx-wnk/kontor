package pipeline_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/lx-wnk/kontor/server/internal/db/repo"
	"github.com/lx-wnk/kontor/server/internal/pipeline"
)

func TestFailTransition_KeepsTheRunsExistingOutput(t *testing.T) {
	ctx := context.Background()
	orch, taskRepo, srRepo := makeOrchestratorFull(t)

	task, err := taskRepo.Create(ctx, repo.CreateTaskInput{
		Slug: "fail-keeps-output", Title: "t", Cwd: "/tmp", CurrentStage: "plan_review",
		Priority: "medium", MaxIterations: 3, StageTimeoutSeconds: 1800,
	})
	require.NoError(t, err)
	sr, err := srRepo.Create(ctx, repo.CreateStageRunInput{TaskID: task.ID, Stage: "plan_review", SessionName: "fko-0"})
	require.NoError(t, err)
	sr, err = srRepo.Update(ctx, sr.ID, repo.UpdateStageRunInput{
		Status: strPtr("awaiting_user"),
		Output: map[string]any{"plan": "the plan under review"},
	})
	require.NoError(t, err)

	_, err = orch.ApplyTransitionForTest(ctx, task, sr, pipeline.FailTransition{Reason: "boom"})
	require.NoError(t, err)

	got, err := srRepo.GetByID(ctx, sr.ID)
	require.NoError(t, err)
	require.Equal(t, "failed", got.Status)
	require.Equal(t, "the plan under review", got.Output["plan"], "failing a run must not erase what it produced")
	require.Equal(t, "boom", got.Output["error"])
}
