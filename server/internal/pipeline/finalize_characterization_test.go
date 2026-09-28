package pipeline_test

// Characterization tests for finalizeCompletedAsyncRuns (orchestrator.go:449).
//
// These lock the CURRENT observable behavior of every branch before the
// CQ-05 (enforceBudget) and CQ-04 (split finalizeCompletedAsyncRuns) refactors.
// Branches already covered by TestFinalizeCompletedAsyncRuns_* in
// orchestrator_test.go (infra requeue/exhausted, rate-limit requeue/exhausted,
// schema-reject iter0/iter1, plain hard-fail) are not duplicated here.

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/lx-wnk/kontor/server/internal/db/ent"
	"github.com/lx-wnk/kontor/server/internal/db/repo"
	"github.com/lx-wnk/kontor/server/internal/pipeline"
)

// makeRunningStageRunAtStage mirrors makeRunningStageRun but allows choosing the
// task's current stage, needed for the "completed" and "external-cancel" branches.
func makeRunningStageRunAtStage(t *testing.T, ctx context.Context, taskRepo repo.TaskRepo, srRepo repo.StageRunRepo, slug, stage string) (*ent.Task, *ent.StageRun) {
	t.Helper()
	task, err := taskRepo.Create(ctx, repo.CreateTaskInput{
		Slug:                slug,
		Title:               slug,
		Cwd:                 "/tmp",
		CurrentStage:        stage,
		Priority:            "medium",
		MaxIterations:       3,
		StageTimeoutSeconds: 1800,
	})
	require.NoError(t, err)

	sr, err := srRepo.Create(ctx, repo.CreateStageRunInput{
		TaskID:      task.ID,
		Stage:       stage,
		Iteration:   0,
		SessionName: slug + "-0",
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

// --- completed branch ---

func TestFinalizeCompletedAsyncRuns_Completed_DispatchesToDecideTransition(t *testing.T) {
	ctx := context.Background()
	orch, taskRepo, srRepo := makeOrchestratorWithSRRepo(t)

	task, run := makeRunningStageRunAtStage(t, ctx, taskRepo, srRepo, "finalize-completed-test", "finalization")

	orch.SetCompletionDetector(func(_ *ent.StageRun, _ string, _ pipeline.CompletionDeps) (pipeline.CompletionResult, error) {
		return pipeline.CompletionResult{Kind: "completed", Output: map[string]any{}}, nil
	})

	err := orch.FinalizeCompletedAsyncRunsForTest(ctx, []*ent.StageRun{run})
	require.NoError(t, err)

	updatedRun, err := srRepo.GetByID(ctx, run.ID)
	require.NoError(t, err)
	require.Equal(t, "done", updatedRun.Status)

	updatedTask, err := taskRepo.GetByID(ctx, task.ID)
	require.NoError(t, err)
	require.Equal(t, "done", updatedTask.CurrentStage, "finalization stage completing must produce DoneTransition")
}

// --- cost-budget kill ---

func TestFinalizeCompletedAsyncRuns_CostBudgetExceeded_KillsAndFails(t *testing.T) {
	ctx := context.Background()
	orch, taskRepo, srRepo := makeOrchestratorWithSRRepo(t)

	task, run := makeRunningStageRunAtStage(t, ctx, taskRepo, srRepo, "cost-budget-test", "implementation")

	// Prior completed run whose CostCents counts toward SumCompletedCostCents.
	prior, err := srRepo.Create(ctx, repo.CreateStageRunInput{
		TaskID:      task.ID,
		Stage:       "implementation",
		Iteration:   -1, // distinct iteration so it doesn't collide with run
		SessionName: "cost-budget-test-prior",
	})
	require.NoError(t, err)
	priorCost := 500
	_, err = srRepo.Update(ctx, prior.ID, repo.UpdateStageRunInput{Status: strPtr("done"), CostCents: &priorCost})
	require.NoError(t, err)

	budget := 100
	_, err = taskRepo.Update(ctx, task.ID, repo.UpdateTaskInput{CostBudgetCents: &budget})
	require.NoError(t, err)

	orch.SetCompletionDetector(func(_ *ent.StageRun, _ string, _ pipeline.CompletionDeps) (pipeline.CompletionResult, error) {
		return pipeline.CompletionResult{Kind: "still_running"}, nil
	})

	err = orch.FinalizeCompletedAsyncRunsForTest(ctx, []*ent.StageRun{run})
	require.NoError(t, err)

	updated, err := srRepo.GetByID(ctx, run.ID)
	require.NoError(t, err)
	require.Equal(t, "failed", updated.Status)
	require.Contains(t, updated.Output["error"], "cost budget exceeded")
}

// --- token-budget kill ---

func TestFinalizeCompletedAsyncRuns_TokenBudgetExceeded_KillsAndFails(t *testing.T) {
	ctx := context.Background()
	orch, taskRepo, srRepo := makeOrchestratorWithSRRepo(t)

	task, run := makeRunningStageRunAtStage(t, ctx, taskRepo, srRepo, "token-budget-test", "implementation")

	prior, err := srRepo.Create(ctx, repo.CreateStageRunInput{
		TaskID:      task.ID,
		Stage:       "implementation",
		Iteration:   -1,
		SessionName: "token-budget-test-prior",
	})
	require.NoError(t, err)
	priorTokens := 5000
	_, err = srRepo.Update(ctx, prior.ID, repo.UpdateStageRunInput{Status: strPtr("done"), TokensUsed: &priorTokens})
	require.NoError(t, err)

	budget := 1000
	_, err = taskRepo.Update(ctx, task.ID, repo.UpdateTaskInput{TokenBudget: &budget})
	require.NoError(t, err)

	orch.SetCompletionDetector(func(_ *ent.StageRun, _ string, _ pipeline.CompletionDeps) (pipeline.CompletionResult, error) {
		return pipeline.CompletionResult{Kind: "still_running"}, nil
	})

	err = orch.FinalizeCompletedAsyncRunsForTest(ctx, []*ent.StageRun{run})
	require.NoError(t, err)

	updated, err := srRepo.GetByID(ctx, run.ID)
	require.NoError(t, err)
	require.Equal(t, "failed", updated.Status)
	require.Contains(t, updated.Output["error"], "token budget exceeded")
}

// --- stage-timeout kill ---

func TestFinalizeCompletedAsyncRuns_StageTimeout_KillsAndFails(t *testing.T) {
	ctx := context.Background()
	orch, taskRepo, srRepo := makeOrchestratorWithSRRepo(t)

	_, run := makeRunningStageRunAtStage(t, ctx, taskRepo, srRepo, "stage-timeout-test", "implementation")

	// defaultStageTimeoutSeconds is 1800s; back-date StartedAt well past that.
	// finalizeCompletedAsyncRuns reads StartedAt off the passed-in struct, not a
	// re-fetch, so the slice element must carry the updated value.
	longAgo := time.Now().Add(-2 * time.Hour)
	run, err := srRepo.Update(ctx, run.ID, repo.UpdateStageRunInput{StartedAt: &longAgo})
	require.NoError(t, err)

	orch.SetCompletionDetector(func(_ *ent.StageRun, _ string, _ pipeline.CompletionDeps) (pipeline.CompletionResult, error) {
		return pipeline.CompletionResult{Kind: "still_running"}, nil
	})

	err = orch.FinalizeCompletedAsyncRunsForTest(ctx, []*ent.StageRun{run})
	require.NoError(t, err)

	updated, err := srRepo.GetByID(ctx, run.ID)
	require.NoError(t, err)
	require.Equal(t, "failed", updated.Status)
	require.Contains(t, updated.Output["error"], "stage timeout")
}

// --- still_running, no threshold crossed: no-op ---

func TestFinalizeCompletedAsyncRuns_StillRunning_NoBudgetOrTimeout_LeavesRunUntouched(t *testing.T) {
	ctx := context.Background()
	orch, taskRepo, srRepo := makeOrchestratorWithSRRepo(t)

	_, run := makeRunningStageRunAtStage(t, ctx, taskRepo, srRepo, "still-running-test", "implementation")

	orch.SetCompletionDetector(func(_ *ent.StageRun, _ string, _ pipeline.CompletionDeps) (pipeline.CompletionResult, error) {
		return pipeline.CompletionResult{Kind: "still_running"}, nil
	})

	err := orch.FinalizeCompletedAsyncRunsForTest(ctx, []*ent.StageRun{run})
	require.NoError(t, err)

	updated, err := srRepo.GetByID(ctx, run.ID)
	require.NoError(t, err)
	require.Equal(t, "running", updated.Status, "no budget/timeout configured — run must stay running")
}

// --- external-cancel on terminal stage ---

func TestFinalizeCompletedAsyncRuns_ExternalCancel_FailsRunOnTerminalStage(t *testing.T) {
	ctx := context.Background()
	orch, taskRepo, srRepo := makeOrchestratorWithSRRepo(t)

	_, run := makeRunningStageRunAtStage(t, ctx, taskRepo, srRepo, "external-cancel-test", "cancelled")

	// detectCompletion must not even be consulted for a terminal-stage task —
	// panic if it is, so this test also locks that ordering.
	orch.SetCompletionDetector(func(_ *ent.StageRun, _ string, _ pipeline.CompletionDeps) (pipeline.CompletionResult, error) {
		t.Fatal("detectCompletion must not be called for a task on a terminal stage")
		return pipeline.CompletionResult{}, nil
	})

	err := orch.FinalizeCompletedAsyncRunsForTest(ctx, []*ent.StageRun{run})
	require.NoError(t, err)

	updated, err := srRepo.GetByID(ctx, run.ID)
	require.NoError(t, err)
	require.Equal(t, "failed", updated.Status)
	require.Equal(t, "task cancelled externally", updated.Output["error"])
}
