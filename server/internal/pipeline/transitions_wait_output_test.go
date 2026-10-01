package pipeline_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/lx-wnk/kontor/server/internal/db/repo"
	"github.com/lx-wnk/kontor/server/internal/pipeline"
)

func TestWaitUserTransition_PersistsWaitReasonInOutput(t *testing.T) {
	ctx := context.Background()
	orch, taskRepo, srRepo := makeOrchestratorFull(t)

	task, err := taskRepo.Create(ctx, repo.CreateTaskInput{
		Slug: "wait-reason-persist", Title: "t", Cwd: "/tmp", CurrentStage: "plan_review",
		Priority: "medium", MaxIterations: 3,
	})
	require.NoError(t, err)
	sr, err := srRepo.Create(ctx, repo.CreateStageRunInput{TaskID: task.ID, Stage: "plan_review", SessionName: "wrp-0"})
	require.NoError(t, err)
	sr, err = srRepo.Update(ctx, sr.ID, repo.UpdateStageRunInput{
		Status: strPtr("running"),
	})
	require.NoError(t, err)

	_, err = orch.ApplyTransitionForTest(ctx, task, sr, pipeline.WaitUserTransition{
		Reason:    "Plan review: awaiting user approval",
		AgentDone: true,
	})
	require.NoError(t, err)

	got, err := srRepo.GetByID(ctx, sr.ID)
	require.NoError(t, err)
	require.Equal(t, "awaiting_user", got.Status)
	require.Equal(t, "Plan review: awaiting user approval", got.Output["wait_reason"],
		"WaitUserTransition with a Reason must persist it as wait_reason in the stage run output")
}

func TestWaitUserTransition_NoReasonWritesNoWaitReasonKey(t *testing.T) {
	ctx := context.Background()
	orch, taskRepo, srRepo := makeOrchestratorFull(t)

	task, err := taskRepo.Create(ctx, repo.CreateTaskInput{
		Slug: "wait-no-reason", Title: "t", Cwd: "/tmp", CurrentStage: "implementation",
		Priority: "medium", MaxIterations: 3,
	})
	require.NoError(t, err)
	sr, err := srRepo.Create(ctx, repo.CreateStageRunInput{TaskID: task.ID, Stage: "implementation", SessionName: "wnr-0"})
	require.NoError(t, err)
	sr, err = srRepo.Update(ctx, sr.ID, repo.UpdateStageRunInput{
		Status: strPtr("running"),
	})
	require.NoError(t, err)

	_, err = orch.ApplyTransitionForTest(ctx, task, sr, pipeline.WaitUserTransition{
		Reason:    "",
		Output:    map[string]any{"some": "data"},
		AgentDone: true,
	})
	require.NoError(t, err)

	got, err := srRepo.GetByID(ctx, sr.ID)
	require.NoError(t, err)
	require.Equal(t, "awaiting_user", got.Status)
	_, hasWaitReason := got.Output["wait_reason"]
	require.False(t, hasWaitReason, "WaitUserTransition with empty Reason must not write a wait_reason key")
	require.Equal(t, "data", got.Output["some"], "original output must be preserved")
}
