package pipeline_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/lx-wnk/kontor/server/internal/db"
	"github.com/lx-wnk/kontor/server/internal/db/ent"
	"github.com/lx-wnk/kontor/server/internal/db/repo"
	"github.com/lx-wnk/kontor/server/internal/pipeline"
)

func makeRunningRunForStage(t *testing.T, ctx context.Context, taskRepo repo.TaskRepo, srRepo repo.StageRunRepo, stage string) (*ent.Task, *ent.StageRun) {
	t.Helper()
	task, err := taskRepo.Create(ctx, repo.CreateTaskInput{
		Slug:          "rl-test-" + stage,
		Title:         "Rate Limit Test " + stage,
		Cwd:           "/tmp",
		CurrentStage:  stage,
		Priority:      "medium",
		MaxIterations: 3,
	})
	require.NoError(t, err)

	sr, err := srRepo.Create(ctx, repo.CreateStageRunInput{
		TaskID:      task.ID,
		Stage:       stage,
		Iteration:   0,
		SessionName: "rl-test-" + stage + "-0",
	})
	require.NoError(t, err)

	deadPID := -1
	now := time.Now()
	sr, err = srRepo.Update(ctx, sr.ID, repo.UpdateStageRunInput{
		Status:    strPtr("running"),
		PID:       &deadPID,
		StartedAt: &now,
	})
	require.NoError(t, err)
	return task, sr
}

func TestRateLimited_AllStages_StatusRateLimited_NoAdvance(t *testing.T) {
	stages := []string{"implementation", "self_review", "finalization", "job"}

	for _, stage := range stages {
		t.Run(stage, func(t *testing.T) {
			ctx := context.Background()
			orch, taskRepo, srRepo := makeOrchestratorWithSRRepo(t)

			task, run := makeRunningRunForStage(t, ctx, taskRepo, srRepo, stage)

			orch.SetCompletionDetector(func(_ *ent.StageRun, _ string, _ pipeline.CompletionDeps) (pipeline.CompletionResult, error) {
				return pipeline.CompletionResult{
					Kind:        "failed",
					Error:       "agent hit API rate/usage limit (status 429)",
					Infra:       true,
					RateLimited: true,
				}, nil
			})

			err := orch.FinalizeCompletedAsyncRunsForTest(ctx, []*ent.StageRun{run})
			require.NoError(t, err)

			updated, err := srRepo.GetByID(ctx, run.ID)
			require.NoError(t, err)
			require.Equal(t, "rate_limited", updated.Status, "stage run status must be rate_limited")

			freshTask, err := taskRepo.GetByID(ctx, task.ID)
			require.NoError(t, err)
			require.Equal(t, stage, freshTask.CurrentStage, "task must stay on %s", stage)

			if freshTask.Metadata != nil {
				_, hasCycles := freshTask.Metadata["review_cycles"]
				require.False(t, hasCycles, "review_cycles must not be set on a rate-limited run")
			}
		})
	}
}

func TestDecideCompletedTransition_SelfReview_MissingPassed_ReturnsWaitUser(t *testing.T) {
	ctx := context.Background()
	orch := makeTestOrchestrator(t)

	task := &ent.Task{
		ID:           "task-missing-passed",
		CurrentStage: "self_review",
		Kind:         "pipeline",
	}
	run := &ent.StageRun{
		ID:    "sr-missing-passed",
		Stage: "self_review",
	}

	output := map[string]any{
		"findings": []any{},
		"summary":  "looks good",
	}

	transition := orch.DecideCompletedTransitionForTest(ctx, task, run, output)
	_, isWaitUser := transition.(pipeline.WaitUserTransition)
	require.True(t, isWaitUser, "missing passed must produce WaitUserTransition, got %T", transition)
}

func TestDecideCompletedTransition_SelfReview_PassedFalse_StillWorks(t *testing.T) {
	ctx := context.Background()
	orch := makeTestOrchestrator(t)

	task := &ent.Task{
		ID:           "task-passed-false",
		CurrentStage: "self_review",
		Kind:         "pipeline",
	}
	run := &ent.StageRun{
		ID:    "sr-passed-false",
		Stage: "self_review",
	}

	output := map[string]any{
		"passed":   false,
		"findings": []any{"bug"},
		"summary":  "needs fix",
	}

	transition := orch.DecideCompletedTransitionForTest(ctx, task, run, output)
	_, isNext := transition.(pipeline.NextTransition)
	require.True(t, isNext, "passed=false must produce NextTransition to implementation, got %T", transition)
}

func TestSweepRequeueableRuns_RateLimited_PromotesToPending_ClearsOutput(t *testing.T) {
	ctx := context.Background()
	orch, taskRepo, srRepo := makeOrchestratorWithSRRepo(t)

	task, err := taskRepo.Create(ctx, repo.CreateTaskInput{
		Slug:          "sweep-rl-test",
		Title:         "Sweep RL Test",
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
		SessionName: "sweep-rl-test-impl-0",
	})
	require.NoError(t, err)

	pastRetry := time.Now().Add(-1 * time.Minute)
	retryCount := 1
	_, err = srRepo.Update(ctx, sr.ID, repo.UpdateStageRunInput{
		Status:      strPtr("rate_limited"),
		RetryCount:  &retryCount,
		NextRetryAt: &pastRetry,
		Output:      map[string]any{"requeue_reason": "429", "attempt": 1},
	})
	require.NoError(t, err)

	err = orch.SweepRequeueableRunsForTest(ctx)
	require.NoError(t, err)

	updated, err := srRepo.GetByID(ctx, sr.ID)
	require.NoError(t, err)
	require.Equal(t, "pending", updated.Status, "rate_limited run must be promoted to pending")
	require.Nil(t, updated.Output, "Output must be nil after promotion (OutputClear)")
	require.Nil(t, updated.NextRetryAt, "NextRetryAt must be cleared")
}

func TestApplyTransition_RateLimited_SetsCorrectFields(t *testing.T) {
	ctx := context.Background()
	orch, taskRepo, srRepo := makeOrchestratorWithSRRepo(t)

	task, err := taskRepo.Create(ctx, repo.CreateTaskInput{
		Slug:          "apply-rl-test",
		Title:         "Apply RL Test",
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
		SessionName: "apply-rl-test-impl-0",
	})
	require.NoError(t, err)

	now := time.Now()
	sr, err = srRepo.Update(ctx, sr.ID, repo.UpdateStageRunInput{
		Status:    strPtr("running"),
		StartedAt: &now,
	})
	require.NoError(t, err)

	nextRetry := time.Now().Add(10 * time.Minute)
	result, err := orch.ApplyTransitionForTest(ctx, task, sr, pipeline.RateLimitedTransition{
		Reason:      "usage limit hit",
		Attempt:     2,
		NextRetryAt: nextRetry,
		Output:      map[string]any{"agentMessage": "limit reached"},
	})
	require.NoError(t, err)
	require.Equal(t, "rate_limited", result.Status)
	require.Equal(t, 2, result.RateLimitRetryCount)
	require.NotNil(t, result.NextRetryAt)
	require.Equal(t, "usage limit hit", result.Output["requeue_reason"])

	freshTask, err := taskRepo.GetByID(ctx, task.ID)
	require.NoError(t, err)
	require.Equal(t, "implementation", freshTask.CurrentStage)
}

func TestOutputClear_ClearsExistingOutput(t *testing.T) {
	ctx := context.Background()
	bundle, err := db.Open(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { _ = bundle.Client.Close() })

	taskRepo := repo.NewTaskRepo(bundle.Client)
	srRepo := repo.NewStageRunRepo(bundle.Client)

	task, err := taskRepo.Create(ctx, repo.CreateTaskInput{
		Slug:          "output-clear-test",
		Title:         "Output Clear Test",
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
		SessionName: "output-clear-test-impl-0",
	})
	require.NoError(t, err)

	_, err = srRepo.Update(ctx, sr.ID, repo.UpdateStageRunInput{
		Output: map[string]any{"requeue_reason": "429", "attempt": 1},
	})
	require.NoError(t, err)

	updated, err := srRepo.Update(ctx, sr.ID, repo.UpdateStageRunInput{
		OutputClear: true,
	})
	require.NoError(t, err)
	require.Nil(t, updated.Output, "Output must be nil after OutputClear")

	reread, err := srRepo.GetByID(ctx, sr.ID)
	require.NoError(t, err)
	require.Nil(t, reread.Output, "Output must be nil after re-read")
}

func TestHandleFailedResult_InfraAfterRateLimits_KeepsItsOwnBudget(t *testing.T) {
	ctx := context.Background()
	orch, taskRepo, srRepo := makeOrchestratorWithSRRepo(t)
	_, run := makeRunningStageRun(t, ctx, taskRepo, srRepo, 0)

	result := pipeline.CompletionResult{Kind: "failed", Error: "usage limit (status 429)", Infra: true, RateLimited: true}
	orch.SetCompletionDetector(func(*ent.StageRun, string, pipeline.CompletionDeps) (pipeline.CompletionResult, error) {
		return result, nil
	})
	for range 3 {
		fresh, err := srRepo.GetByID(ctx, run.ID)
		require.NoError(t, err)
		require.NoError(t, orch.FinalizeCompletedAsyncRunsForTest(ctx, []*ent.StageRun{fresh}))
		_, err = srRepo.Update(ctx, run.ID, repo.UpdateStageRunInput{Status: strPtr("running")})
		require.NoError(t, err)
	}

	result = pipeline.CompletionResult{Kind: "failed", Error: "agent process crashed", Infra: true}
	fresh, err := srRepo.GetByID(ctx, run.ID)
	require.NoError(t, err)
	require.NoError(t, orch.FinalizeCompletedAsyncRunsForTest(ctx, []*ent.StageRun{fresh}))

	updated, err := srRepo.GetByID(ctx, run.ID)
	require.NoError(t, err)
	require.Equal(t, "requeued", updated.Status, "rate-limit retries must not spend the infra retry budget")
	require.Equal(t, 1, updated.RetryCount)
	require.Equal(t, 3, updated.RateLimitRetryCount)
}
