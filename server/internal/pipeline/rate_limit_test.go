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

// --- helpers ---

func makeRateLimitOrch(t *testing.T) (*pipeline.PipelineOrchestrator, repo.TaskRepo, repo.StageRunRepo) {
	t.Helper()
	bundle, err := db.Open(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { _ = bundle.Client.Close() })

	taskRepo := repo.NewTaskRepo(bundle.Client)
	srRepo := repo.NewStageRunRepo(bundle.Client)
	permRepo := repo.NewPermissionRepo(bundle.Client)
	auditRepo := repo.NewAuditEventRepo(bundle.Client)
	cfgRepo := repo.NewPipelineConfigRepo(bundle.Client)

	orch, err := pipeline.NewOrchestrator(pipeline.OrchestratorOptions{
		TaskRepo:       taskRepo,
		StageRunRepo:   srRepo,
		PermissionRepo: permRepo,
		AuditRepo:      auditRepo,
		ConfigRepo:     cfgRepo,
	})
	require.NoError(t, err)
	return orch, taskRepo, srRepo
}

func makeRunningRunForStage(t *testing.T, ctx context.Context, taskRepo repo.TaskRepo, srRepo repo.StageRunRepo, stage string, retryCount int) (*ent.Task, *ent.StageRun) {
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
		Status:     strPtr("running"),
		PID:        &deadPID,
		StartedAt:  &now,
		RetryCount: &retryCount,
	})
	require.NoError(t, err)
	return task, sr
}

// --- (a) Table test: 429 on each stage → status rate_limited, stage unchanged, review_cycles intact ---

func TestRateLimited_AllStages_StatusRateLimited_NoAdvance(t *testing.T) {
	stages := []string{"implementation", "self_review", "finalization", "job"}

	for _, stage := range stages {
		t.Run(stage, func(t *testing.T) {
			ctx := context.Background()
			orch, taskRepo, srRepo := makeRateLimitOrch(t)

			task, run := makeRunningRunForStage(t, ctx, taskRepo, srRepo, stage, 0)

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

			// Stage run must be rate_limited, not done/requeued.
			updated, err := srRepo.GetByID(ctx, run.ID)
			require.NoError(t, err)
			require.Equal(t, "rate_limited", updated.Status, "stage run status must be rate_limited")

			// Task stage must NOT have advanced.
			freshTask, err := taskRepo.GetByID(ctx, task.ID)
			require.NoError(t, err)
			require.Equal(t, stage, freshTask.CurrentStage, "task must stay on %s", stage)

			// review_cycles must not be incremented.
			if freshTask.Metadata != nil {
				_, hasCycles := freshTask.Metadata["review_cycles"]
				require.False(t, hasCycles, "review_cycles must not be set on a rate-limited run")
			}
		})
	}
}

// --- (b) self_review output missing "passed" → WaitUserTransition, not NextTransition ---

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

	// Output with findings and summary but NO "passed" field.
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

// --- (c) sweepRequeueableRuns: rate_limited → pending with nil Output ---

func TestSweepRequeueableRuns_RateLimited_PromotesToPending_ClearsOutput(t *testing.T) {
	ctx := context.Background()
	orch, taskRepo, srRepo := makeRateLimitOrch(t)

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

	// Set to rate_limited with a NextRetryAt in the past.
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

// --- (d) exhausted rate-limit budget → status failed with rate_limit_retries_exhausted ---

func TestRateLimited_ExhaustedBudget_FailsWithMarker(t *testing.T) {
	ctx := context.Background()
	orch, taskRepo, srRepo := makeRateLimitOrch(t)

	// defaultMaxRateLimitRetries is 36; start at retryCount == 36 → exhausted.
	_, run := makeRunningRunForStage(t, ctx, taskRepo, srRepo, "implementation", 36)

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
	require.Equal(t, "failed", updated.Status, "exhausted budget must hard-fail")
	require.NotNil(t, updated.Output["rate_limit_retries_exhausted"])
	require.EqualValues(t, 36, updated.Output["rate_limit_retries_exhausted"])
}

// --- (e) RateLimitedTransition via applyTransition: verify status, RetryCount, NextRetryAt ---

func TestApplyTransition_RateLimited_SetsCorrectFields(t *testing.T) {
	ctx := context.Background()
	orch, taskRepo, srRepo := makeRateLimitOrch(t)

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
	require.Equal(t, 2, result.RetryCount)
	require.NotNil(t, result.NextRetryAt)
	require.Equal(t, "usage limit hit", result.Output["requeue_reason"])

	// Task must NOT have advanced.
	freshTask, err := taskRepo.GetByID(ctx, task.ID)
	require.NoError(t, err)
	require.Equal(t, "implementation", freshTask.CurrentStage)
}

// --- (f) OutputClear in repo works correctly ---

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

	// Set output.
	_, err = srRepo.Update(ctx, sr.ID, repo.UpdateStageRunInput{
		Output: map[string]any{"requeue_reason": "429", "attempt": 1},
	})
	require.NoError(t, err)

	// Clear it.
	updated, err := srRepo.Update(ctx, sr.ID, repo.UpdateStageRunInput{
		OutputClear: true,
	})
	require.NoError(t, err)
	require.Nil(t, updated.Output, "Output must be nil after OutputClear")

	// Re-read to confirm persistence.
	reread, err := srRepo.GetByID(ctx, sr.ID)
	require.NoError(t, err)
	require.Nil(t, reread.Output, "Output must be nil after re-read")
}
