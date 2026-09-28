package plan_test

import (
	"context"
	"testing"

	"github.com/lx-wnk/kontor/server/internal/api/plan"
	"github.com/lx-wnk/kontor/server/internal/db"
	"github.com/lx-wnk/kontor/server/internal/db/repo"
	"github.com/stretchr/testify/require"
)

// seedPlanReviewTask creates a task in plan_review stage with an awaiting_user stage run.
func seedPlanReviewTask(t *testing.T, ctx context.Context, taskRepo repo.TaskRepo, srRepo repo.StageRunRepo) (string, string) {
	t.Helper()
	task, err := taskRepo.Create(ctx, repo.CreateTaskInput{
		Slug:          "plan-svc-test-" + t.Name(),
		Title:         "Plan Service Test",
		Cwd:           t.TempDir(),
		MaxIterations: 3,
		Priority:      "normal",
		CurrentStage:  "plan_review",
	})
	require.NoError(t, err)

	run, err := srRepo.Create(ctx, repo.CreateStageRunInput{
		TaskID:    task.ID,
		Stage:     "plan_review",
		Iteration: 0,
	})
	require.NoError(t, err)
	status := "awaiting_user"
	_, err = srRepo.Update(ctx, run.ID, repo.UpdateStageRunInput{Status: &status})
	require.NoError(t, err)

	return task.ID, run.ID
}

func TestApprovePlan_AdvancesTaskToImplementation(t *testing.T) {
	bundle, err := db.Open(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { _ = bundle.Client.Close() })

	ctx := context.Background()
	taskRepo := repo.NewTaskRepo(bundle.Client)
	srRepo := repo.NewStageRunRepo(bundle.Client)
	turnsRepo := repo.NewRefinementTurnRepo(bundle.Client)

	taskID, _ := seedPlanReviewTask(t, ctx, taskRepo, srRepo)

	advanced := false
	task, err := plan.ApprovePlan(ctx, plan.ApproveDeps{
		Turns:     turnsRepo,
		Tasks:     taskRepo,
		StageRuns: srRepo,
		Advance: func(_ context.Context, id string) error {
			if id == taskID {
				advanced = true
			}
			return nil
		},
	}, taskID)

	require.NoError(t, err)
	require.NotNil(t, task)
	require.Equal(t, "implementation", task.CurrentStage)
	require.True(t, advanced, "Advance must be called")

	// plan_review stage run should be marked done.
	sr, err := srRepo.GetLatestByTaskAndStage(ctx, taskID, "plan_review")
	require.NoError(t, err)
	require.NotNil(t, sr)
	require.Equal(t, "done", sr.Status)

	// A confirmed sentinel turn should be persisted.
	turns, err := turnsRepo.ListForTask(ctx, taskID, 0)
	require.NoError(t, err)
	var found bool
	for _, tr := range turns {
		if tr.Phase != nil && *tr.Phase == "plan_approved" {
			found = true
		}
	}
	require.True(t, found, "expected a plan_approved sentinel turn")

	// approvedPlan key should be set in metadata.
	updated, err := taskRepo.GetByID(ctx, taskID)
	require.NoError(t, err)
	_, hasMeta := updated.Metadata["approvedPlan"]
	require.True(t, hasMeta, "expected approvedPlan key in task metadata")
}

func TestApprovePlan_RevokesTheStageRunCredential(t *testing.T) {
	bundle, err := db.Open(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { _ = bundle.Client.Close() })

	ctx := context.Background()
	taskRepo := repo.NewTaskRepo(bundle.Client)
	srRepo := repo.NewStageRunRepo(bundle.Client)
	turnsRepo := repo.NewRefinementTurnRepo(bundle.Client)

	taskID, runID := seedPlanReviewTask(t, ctx, taskRepo, srRepo)

	var revoked []string
	_, err = plan.ApprovePlan(ctx, plan.ApproveDeps{
		Turns:     turnsRepo,
		Tasks:     taskRepo,
		StageRuns: srRepo,
		Revoke: func(_ context.Context, stageRunID string) error {
			revoked = append(revoked, stageRunID)
			return nil
		},
	}, taskID)

	require.NoError(t, err)
	require.Equal(t, []string{runID}, revoked,
		"approving the plan must revoke the plan_review run's MCP credentials")
}

func TestRejectPlan_RerunsStageAndStoresFeedback(t *testing.T) {
	bundle, err := db.Open(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { _ = bundle.Client.Close() })

	ctx := context.Background()
	taskRepo := repo.NewTaskRepo(bundle.Client)
	srRepo := repo.NewStageRunRepo(bundle.Client)
	turnsRepo := repo.NewRefinementTurnRepo(bundle.Client)

	taskID, _ := seedPlanReviewTask(t, ctx, taskRepo, srRepo)

	requeued := 0
	rejectDeps := plan.RejectDeps{
		Turns:     turnsRepo,
		Tasks:     taskRepo,
		StageRuns: srRepo,
		Requeue: func(_ context.Context, id, prompt string) error {
			if id == taskID {
				requeued++
			}
			return nil
		},
	}

	// First rejection — within cap.
	err = plan.RejectPlan(ctx, rejectDeps, taskID, "feedback round 1")
	require.NoError(t, err)
	require.Equal(t, 1, requeued, "first rejection must trigger requeue")

	// Second rejection.
	err = plan.RejectPlan(ctx, rejectDeps, taskID, "feedback round 2")
	require.NoError(t, err)
	require.Equal(t, 2, requeued)

	// Third rejection (at cap = 3, this is the third reject so still within cap).
	err = plan.RejectPlan(ctx, rejectDeps, taskID, "feedback round 3")
	require.NoError(t, err)
	require.Equal(t, 3, requeued)

	// Fourth rejection — beyond cap, must NOT requeue.
	err = plan.RejectPlan(ctx, rejectDeps, taskID, "feedback round 4")
	require.NoError(t, err, "beyond-cap reject must not error, just skip requeue")
	require.Equal(t, 3, requeued, "fourth rejection must not requeue (cap exceeded)")

	// Feedback should be stored in task metadata.
	updated, err := taskRepo.GetByID(ctx, taskID)
	require.NoError(t, err)
	_, hasMeta := updated.Metadata["planReviewFeedback"]
	require.True(t, hasMeta, "expected planReviewFeedback key in task metadata")
	require.Equal(t, "feedback round 4", updated.Metadata["planReviewFeedback"],
		"planReviewFeedback must hold the exact feedback string — key or value mismatch breaks the reject→rerun feedback loop")
}

func TestPlanStatus_ReturnsCurrentState(t *testing.T) {
	bundle, err := db.Open(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { _ = bundle.Client.Close() })

	ctx := context.Background()
	taskRepo := repo.NewTaskRepo(bundle.Client)
	srRepo := repo.NewStageRunRepo(bundle.Client)
	turnsRepo := repo.NewRefinementTurnRepo(bundle.Client)

	taskID, _ := seedPlanReviewTask(t, ctx, taskRepo, srRepo)

	status, err := plan.PlanStatus(ctx, plan.StatusDeps{
		Turns:     turnsRepo,
		Tasks:     taskRepo,
		StageRuns: srRepo,
	}, taskID)
	require.NoError(t, err)
	require.Equal(t, "awaiting_user", status.GateState)
}

func TestPlanStatus_ReturnsLivePlanBeforeApproval(t *testing.T) {
	bundle, err := db.Open(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { _ = bundle.Client.Close() })

	ctx := context.Background()
	taskRepo := repo.NewTaskRepo(bundle.Client)
	srRepo := repo.NewStageRunRepo(bundle.Client)
	turnsRepo := repo.NewRefinementTurnRepo(bundle.Client)

	taskID, runID := seedPlanReviewTask(t, ctx, taskRepo, srRepo)

	// Seed a live plan output into the stage_run — no approvedPlan metadata yet.
	livePlan := map[string]any{"summary": "LIVE_PLAN_SENTINEL", "steps": []any{"step1"}}
	_, err = srRepo.Update(ctx, runID, repo.UpdateStageRunInput{Output: livePlan})
	require.NoError(t, err)

	status, err := plan.PlanStatus(ctx, plan.StatusDeps{
		Turns:     turnsRepo,
		Tasks:     taskRepo,
		StageRuns: srRepo,
	}, taskID)
	require.NoError(t, err)
	require.Equal(t, "awaiting_user", status.GateState)
	require.NotNil(t, status.ApprovedPlan, "live plan must be surfaced before approval")
	require.Equal(t, "LIVE_PLAN_SENTINEL", status.ApprovedPlan["summary"],
		"ApprovedPlan must contain the live stage_run output before approval")
}

func TestPlanStatus_FrozenPlanAfterApproval(t *testing.T) {
	bundle, err := db.Open(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { _ = bundle.Client.Close() })

	ctx := context.Background()
	taskRepo := repo.NewTaskRepo(bundle.Client)
	srRepo := repo.NewStageRunRepo(bundle.Client)
	turnsRepo := repo.NewRefinementTurnRepo(bundle.Client)

	taskID, runID := seedPlanReviewTask(t, ctx, taskRepo, srRepo)

	livePlan := map[string]any{"summary": "LIVE_PLAN_SENTINEL"}
	_, err = srRepo.Update(ctx, runID, repo.UpdateStageRunInput{Output: livePlan})
	require.NoError(t, err)

	// Approve — freezes plan into metadata, marks stage_run done.
	_, err = plan.ApprovePlan(ctx, plan.ApproveDeps{
		Turns:     turnsRepo,
		Tasks:     taskRepo,
		StageRuns: srRepo,
		Advance:   func(_ context.Context, _ string) error { return nil },
	}, taskID)
	require.NoError(t, err)

	status, err := plan.PlanStatus(ctx, plan.StatusDeps{
		Turns:     turnsRepo,
		Tasks:     taskRepo,
		StageRuns: srRepo,
	}, taskID)
	require.NoError(t, err)
	require.NotNil(t, status.ApprovedPlan, "frozen plan must be returned after approval")
	require.Equal(t, "LIVE_PLAN_SENTINEL", status.ApprovedPlan["summary"],
		"frozen plan must match the plan that was approved")
}
