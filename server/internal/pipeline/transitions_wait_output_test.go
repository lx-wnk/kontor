package pipeline_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/lx-wnk/kontor/server/internal/db/repo"
	"github.com/lx-wnk/kontor/server/internal/pipeline"
)

func TestWaitUserTransition_PersistsWaitReasonInColumn(t *testing.T) {
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
	require.NotNil(t, got.WaitReason, "WaitUserTransition with a Reason must persist it in the wait_reason column")
	require.Equal(t, "Plan review: awaiting user approval", *got.WaitReason)
	require.Empty(t, got.Output, "the reason is orchestrator metadata and must not be written into the stage result")
}

func TestWaitUserTransition_NoReasonLeavesColumnEmpty(t *testing.T) {
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
	require.Nil(t, got.WaitReason, "WaitUserTransition with empty Reason must leave the wait_reason column empty")
	require.Equal(t, map[string]any{"some": "data"}, got.Output, "tr.Output must be stored as given")
}

func TestWaitUserTransition_ReasonLeavesExistingOutputUntouched(t *testing.T) {
	ctx := context.Background()
	orch, taskRepo, srRepo := makeOrchestratorFull(t)

	task, err := taskRepo.Create(ctx, repo.CreateTaskInput{
		Slug: "wait-preserve-output", Title: "t", Cwd: "/tmp", CurrentStage: "plan_review",
		Priority: "medium", MaxIterations: 3,
	})
	require.NoError(t, err)
	sr, err := srRepo.Create(ctx, repo.CreateStageRunInput{TaskID: task.ID, Stage: "plan_review", SessionName: "wpo-0"})
	require.NoError(t, err)
	sr, err = srRepo.Update(ctx, sr.ID, repo.UpdateStageRunInput{
		Status: strPtr("running"),
		Output: map[string]any{"plan": "the plan under review"},
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
	require.NotNil(t, got.WaitReason)
	require.Equal(t, "Plan review: awaiting user approval", *got.WaitReason)
	require.Equal(t, map[string]any{"plan": "the plan under review"}, got.Output,
		"existing output must be exactly what it was: no wait_reason key added")
}

func TestWaitUserTransition_LeavingAwaitingUserClearsWaitReason(t *testing.T) {
	ctx := context.Background()
	orch, taskRepo, srRepo := makeOrchestratorFull(t)

	task, err := taskRepo.Create(ctx, repo.CreateTaskInput{
		Slug: "wait-reason-cleared", Title: "t", Cwd: "/tmp", CurrentStage: "plan_review",
		Priority: "medium", MaxIterations: 3,
	})
	require.NoError(t, err)
	sr, err := srRepo.Create(ctx, repo.CreateStageRunInput{TaskID: task.ID, Stage: "plan_review", SessionName: "wrc-0"})
	require.NoError(t, err)
	sr, err = srRepo.Update(ctx, sr.ID, repo.UpdateStageRunInput{Status: strPtr("running")})
	require.NoError(t, err)

	parked, err := orch.ApplyTransitionForTest(ctx, task, sr, pipeline.WaitUserTransition{
		Reason: "Plan review: awaiting user approval", AgentDone: true,
	})
	require.NoError(t, err)
	require.NotNil(t, parked.WaitReason)

	_, err = orch.ApplyTransitionForTest(ctx, task, parked, pipeline.FailTransition{Reason: "user gave up"})
	require.NoError(t, err)

	got, err := srRepo.GetByID(ctx, sr.ID)
	require.NoError(t, err)
	require.Equal(t, "failed", got.Status)
	require.Nil(t, got.WaitReason, "a run that left awaiting_user must not keep its wait reason")
}
