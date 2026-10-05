package plan_test

import (
	"context"
	"errors"
	"github.com/lx-wnk/kontor/server/internal/pipeline"
	"testing"

	"github.com/lx-wnk/kontor/server/internal/api/plan"
	"github.com/lx-wnk/kontor/server/internal/db"
	"github.com/lx-wnk/kontor/server/internal/db/repo"
	"github.com/stretchr/testify/require"
)

// seedPlanReviewTaskNoRun creates a task in plan_review stage without any stage run.
func seedPlanReviewTaskNoRun(t *testing.T, ctx context.Context, taskRepo repo.TaskRepo) string {
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
	return task.ID
}

// seedPlanReviewRun creates a plan_review task whose stage run has the given
// status and output (nil output leaves it unset).
func seedPlanReviewRun(t *testing.T, ctx context.Context, taskRepo repo.TaskRepo, srRepo repo.StageRunRepo, status string, output map[string]any) (string, string) {
	t.Helper()
	taskID := seedPlanReviewTaskNoRun(t, ctx, taskRepo)

	run, err := srRepo.Create(ctx, repo.CreateStageRunInput{
		TaskID:    taskID,
		Stage:     "plan_review",
		Iteration: 0,
	})
	require.NoError(t, err)
	_, err = srRepo.Update(ctx, run.ID, repo.UpdateStageRunInput{Status: &status})
	require.NoError(t, err)
	if output != nil {
		_, err = srRepo.Update(ctx, run.ID, repo.UpdateStageRunInput{Output: output})
		require.NoError(t, err)
	}

	return taskID, run.ID
}

// seedPlanReviewTask creates a task in plan_review stage with an awaiting_user
// stage run carrying plan content.
func seedPlanReviewTask(t *testing.T, ctx context.Context, taskRepo repo.TaskRepo, srRepo repo.StageRunRepo) (string, string) {
	t.Helper()
	return seedPlanReviewRun(t, ctx, taskRepo, srRepo, "awaiting_user", submittedPlan("plan", "test plan content"))
}

// submittedPlan builds the output set_stage_output stores: plan content plus the
// agent-submitted marker.
func submittedPlan(key, value string) map[string]any {
	return map[string]any{key: value, pipeline.StageOutputSubmittedKey: true}
}

// assertPlanGateUntouched asserts a refused approve/reject changed nothing: the
// task is still in plan_review, the run keeps wantRunStatus (when a run exists),
// and no turn was written.
func assertPlanGateUntouched(t *testing.T, ctx context.Context, bundle *db.DBBundle, taskID, wantRunStatus string) {
	t.Helper()
	task, err := repo.NewTaskRepo(bundle.Client).GetByID(ctx, taskID)
	require.NoError(t, err)
	require.Equal(t, "plan_review", task.CurrentStage)

	if wantRunStatus != "" {
		sr, err := repo.NewStageRunRepo(bundle.Client).GetLatestByTaskAndStage(ctx, taskID, "plan_review")
		require.NoError(t, err)
		require.Equal(t, wantRunStatus, sr.Status)
	}

	turns, err := repo.NewRefinementTurnRepo(bundle.Client).ListForTask(ctx, taskID, 0)
	require.NoError(t, err)
	require.Empty(t, turns, "a refused plan gate action must not write turns")
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
	livePlan := map[string]any{"summary": "LIVE_PLAN_SENTINEL", "steps": []any{"step1"}, pipeline.StageOutputSubmittedKey: true}
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
	require.NotContains(t, status.ApprovedPlan, pipeline.StageOutputSubmittedKey)
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

	livePlan := submittedPlan("summary", "LIVE_PLAN_SENTINEL")
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

func TestPlanStatus_PlanReady(t *testing.T) {
	cases := map[string]struct {
		status string
		output map[string]any
		want   bool
	}{
		"marker_and_plan":             {"awaiting_user", submittedPlan("plan", "test plan content"), true},
		"validation_error":            {"awaiting_user", map[string]any{"validation_error": "missing field: summary", "rejected_output": map[string]any{"steps": []any{}}}, false},
		"wait_reason":                 {"awaiting_user", map[string]any{"wait_reason": "rate_limit"}, false},
		"empty":                       {"awaiting_user", map[string]any{}, false},
		"nil_output":                  {"awaiting_user", nil, false},
		"marker_only":                 {"awaiting_user", map[string]any{pipeline.StageOutputSubmittedKey: true}, false},
		"marker_and_wait_reason_only": {"awaiting_user", map[string]any{pipeline.StageOutputSubmittedKey: true, pipeline.WaitReasonKey: "Plan review: awaiting user approval"}, false},
		"marker_false":                {"awaiting_user", map[string]any{"plan": "draft", pipeline.StageOutputSubmittedKey: false}, false},
		"gate_running":                {"running", submittedPlan("plan", "test plan content"), false},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			bundle, err := db.Open(":memory:")
			require.NoError(t, err)
			t.Cleanup(func() { _ = bundle.Client.Close() })

			ctx := context.Background()
			taskRepo := repo.NewTaskRepo(bundle.Client)
			srRepo := repo.NewStageRunRepo(bundle.Client)
			taskID, _ := seedPlanReviewRun(t, ctx, taskRepo, srRepo, tc.status, tc.output)

			status, err := plan.PlanStatus(ctx, plan.StatusDeps{Tasks: taskRepo, StageRuns: srRepo}, taskID)
			require.NoError(t, err)
			require.Equal(t, tc.want, status.PlanReady)

			_, approveErr := plan.ApprovePlan(ctx, plan.ApproveDeps{
				Turns:     repo.NewRefinementTurnRepo(bundle.Client),
				Tasks:     taskRepo,
				StageRuns: srRepo,
			}, taskID)
			require.Equal(t, tc.want, approveErr == nil, "plan_ready must agree with ApprovePlan: %v", approveErr)
		})
	}
}

func TestPlanStatus_NoRun_PlanNotReady(t *testing.T) {
	bundle, err := db.Open(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { _ = bundle.Client.Close() })

	ctx := context.Background()
	taskRepo := repo.NewTaskRepo(bundle.Client)
	taskID := seedPlanReviewTaskNoRun(t, ctx, taskRepo)

	status, err := plan.PlanStatus(ctx, plan.StatusDeps{Tasks: taskRepo, StageRuns: repo.NewStageRunRepo(bundle.Client)}, taskID)
	require.NoError(t, err)
	require.False(t, status.PlanReady)
}

func TestApprovePlan_RunningRun_ReturnsConflict(t *testing.T) {
	bundle, err := db.Open(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { _ = bundle.Client.Close() })

	ctx := context.Background()
	taskRepo := repo.NewTaskRepo(bundle.Client)
	srRepo := repo.NewStageRunRepo(bundle.Client)
	turnsRepo := repo.NewRefinementTurnRepo(bundle.Client)
	taskID, _ := seedPlanReviewRun(t, ctx, taskRepo, srRepo, "running", nil)

	revoked := false
	_, err = plan.ApprovePlan(ctx, plan.ApproveDeps{
		Turns:     turnsRepo,
		Tasks:     taskRepo,
		StageRuns: srRepo,
		Revoke:    func(context.Context, string) error { revoked = true; return nil },
	}, taskID)

	require.True(t, errors.Is(err, plan.ErrPlanNotReady), "got %v", err)
	assertPlanGateUntouched(t, ctx, bundle, taskID, "running")
	require.False(t, revoked, "Revoke must not be called when approval is refused")
}

func TestApprovePlan_NoRun_ReturnsConflict(t *testing.T) {
	bundle, err := db.Open(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { _ = bundle.Client.Close() })

	ctx := context.Background()
	taskRepo := repo.NewTaskRepo(bundle.Client)
	srRepo := repo.NewStageRunRepo(bundle.Client)
	turnsRepo := repo.NewRefinementTurnRepo(bundle.Client)
	taskID := seedPlanReviewTaskNoRun(t, ctx, taskRepo)

	_, err = plan.ApprovePlan(ctx, plan.ApproveDeps{
		Turns:     turnsRepo,
		Tasks:     taskRepo,
		StageRuns: srRepo,
	}, taskID)

	require.True(t, errors.Is(err, plan.ErrPlanNotReady), "got %v", err)
	assertPlanGateUntouched(t, ctx, bundle, taskID, "")
}

func TestApprovePlan_AwaitingUserNilOutput_ReturnsConflict(t *testing.T) {
	bundle, err := db.Open(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { _ = bundle.Client.Close() })

	ctx := context.Background()
	taskRepo := repo.NewTaskRepo(bundle.Client)
	srRepo := repo.NewStageRunRepo(bundle.Client)
	turnsRepo := repo.NewRefinementTurnRepo(bundle.Client)
	taskID, _ := seedPlanReviewRun(t, ctx, taskRepo, srRepo, "awaiting_user", nil)

	_, err = plan.ApprovePlan(ctx, plan.ApproveDeps{
		Turns:     turnsRepo,
		Tasks:     taskRepo,
		StageRuns: srRepo,
	}, taskID)

	require.True(t, errors.Is(err, plan.ErrPlanNotReady), "got %v", err)
	assertPlanGateUntouched(t, ctx, bundle, taskID, "awaiting_user")
}

func TestApprovePlan_AwaitingUserOnlyMarkerOutput_ReturnsConflict(t *testing.T) {
	bundle, err := db.Open(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { _ = bundle.Client.Close() })

	ctx := context.Background()
	taskRepo := repo.NewTaskRepo(bundle.Client)
	srRepo := repo.NewStageRunRepo(bundle.Client)
	turnsRepo := repo.NewRefinementTurnRepo(bundle.Client)
	taskID, _ := seedPlanReviewRun(t, ctx, taskRepo, srRepo, "awaiting_user",
		map[string]any{pipeline.StageOutputSubmittedKey: true})

	_, err = plan.ApprovePlan(ctx, plan.ApproveDeps{
		Turns:     turnsRepo,
		Tasks:     taskRepo,
		StageRuns: srRepo,
	}, taskID)

	require.True(t, errors.Is(err, plan.ErrPlanNotReady), "got %v", err)
	assertPlanGateUntouched(t, ctx, bundle, taskID, "awaiting_user")
}

func TestRejectPlan_RunningRun_ReturnsConflict(t *testing.T) {
	bundle, err := db.Open(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { _ = bundle.Client.Close() })

	ctx := context.Background()
	taskRepo := repo.NewTaskRepo(bundle.Client)
	srRepo := repo.NewStageRunRepo(bundle.Client)
	turnsRepo := repo.NewRefinementTurnRepo(bundle.Client)
	taskID, _ := seedPlanReviewRun(t, ctx, taskRepo, srRepo, "running", nil)

	requeued := false
	err = plan.RejectPlan(ctx, plan.RejectDeps{
		Turns:     turnsRepo,
		Tasks:     taskRepo,
		StageRuns: srRepo,
		Requeue:   func(context.Context, string, string) error { requeued = true; return nil },
	}, taskID, "needs more detail")

	require.True(t, errors.Is(err, plan.ErrPlanNotReady), "got %v", err)
	assertPlanGateUntouched(t, ctx, bundle, taskID, "running")
	require.False(t, requeued, "Requeue must not be called when rejection is refused")
}

func TestRejectPlan_NoRun_ReturnsConflict(t *testing.T) {
	bundle, err := db.Open(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { _ = bundle.Client.Close() })

	ctx := context.Background()
	taskRepo := repo.NewTaskRepo(bundle.Client)
	srRepo := repo.NewStageRunRepo(bundle.Client)
	turnsRepo := repo.NewRefinementTurnRepo(bundle.Client)
	taskID := seedPlanReviewTaskNoRun(t, ctx, taskRepo)

	err = plan.RejectPlan(ctx, plan.RejectDeps{
		Turns:     turnsRepo,
		Tasks:     taskRepo,
		StageRuns: srRepo,
	}, taskID, "needs more detail")

	require.True(t, errors.Is(err, plan.ErrPlanNotReady), "got %v", err)
	assertPlanGateUntouched(t, ctx, bundle, taskID, "")
}

func TestApprovePlan_OutputWithoutSubmittedMarker_ReturnsConflict(t *testing.T) {
	outputs := map[string]map[string]any{
		"validation_error":       {"validation_error": "missing field: summary", "rejected_output": map[string]any{"steps": []any{}}},
		"synthetic_session_file": {"synthetic_session_file": "/tmp/session.jsonl"},
		"wait_reason":            {"wait_reason": "rate_limit"},
		"empty":                  {},
		"marker_false":           {"plan": "draft", pipeline.StageOutputSubmittedKey: false},
	}
	for name, output := range outputs {
		t.Run(name, func(t *testing.T) {
			bundle, err := db.Open(":memory:")
			require.NoError(t, err)
			t.Cleanup(func() { _ = bundle.Client.Close() })

			ctx := context.Background()
			taskRepo := repo.NewTaskRepo(bundle.Client)
			srRepo := repo.NewStageRunRepo(bundle.Client)
			taskID, _ := seedPlanReviewRun(t, ctx, taskRepo, srRepo, "awaiting_user", output)

			revoked := false
			_, err = plan.ApprovePlan(ctx, plan.ApproveDeps{
				Turns:     repo.NewRefinementTurnRepo(bundle.Client),
				Tasks:     taskRepo,
				StageRuns: srRepo,
				Revoke:    func(context.Context, string) error { revoked = true; return nil },
			}, taskID)

			require.True(t, errors.Is(err, plan.ErrPlanNotReady), "got %v", err)
			assertPlanGateUntouched(t, ctx, bundle, taskID, "awaiting_user")
			require.False(t, revoked, "Revoke must not be called when approval is refused")
		})
	}
}

func TestApprovePlan_SubmittedPlan_FreezesPlanWithoutMarker(t *testing.T) {
	bundle, err := db.Open(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { _ = bundle.Client.Close() })

	ctx := context.Background()
	taskRepo := repo.NewTaskRepo(bundle.Client)
	srRepo := repo.NewStageRunRepo(bundle.Client)
	taskID, _ := seedPlanReviewRun(t, ctx, taskRepo, srRepo, "awaiting_user", submittedPlan("summary", "SUBMITTED_PLAN"))

	task, err := plan.ApprovePlan(ctx, plan.ApproveDeps{
		Turns:     repo.NewRefinementTurnRepo(bundle.Client),
		Tasks:     taskRepo,
		StageRuns: srRepo,
	}, taskID)

	require.NoError(t, err)
	require.Equal(t, "implementation", task.CurrentStage)
	require.Equal(t, map[string]any{"summary": "SUBMITTED_PLAN"}, task.Metadata["approvedPlan"])
}

func TestRejectPlan_AwaitingUserWithoutPlanContent_RequeuesWithFeedback(t *testing.T) {
	outputs := map[string]map[string]any{
		"nil":              nil,
		"empty":            {},
		"validation_error": {"validation_error": "missing field: summary", "rejected_output": map[string]any{}},
		"wait_reason":      {"wait_reason": "rate_limit"},
	}
	for name, output := range outputs {
		t.Run(name, func(t *testing.T) {
			bundle, err := db.Open(":memory:")
			require.NoError(t, err)
			t.Cleanup(func() { _ = bundle.Client.Close() })

			ctx := context.Background()
			taskRepo := repo.NewTaskRepo(bundle.Client)
			srRepo := repo.NewStageRunRepo(bundle.Client)
			turnsRepo := repo.NewRefinementTurnRepo(bundle.Client)
			taskID, _ := seedPlanReviewRun(t, ctx, taskRepo, srRepo, "awaiting_user", output)

			var requeuedPrompt string
			err = plan.RejectPlan(ctx, plan.RejectDeps{
				Turns:     turnsRepo,
				Tasks:     taskRepo,
				StageRuns: srRepo,
				Requeue:   func(_ context.Context, _, prompt string) error { requeuedPrompt = prompt; return nil },
			}, taskID, "write the plan again")

			require.NoError(t, err)
			require.Equal(t, "write the plan again", requeuedPrompt)
			turns, err := turnsRepo.ListForTask(ctx, taskID, 0)
			require.NoError(t, err)
			require.Len(t, turns, 1)
			require.Equal(t, "plan_rejected", *turns[0].Phase)
		})
	}
}

func TestRejectPlan_OutsideAwaitingUser_ReturnsConflict(t *testing.T) {
	for _, status := range []string{"done", "failed"} {
		t.Run(status, func(t *testing.T) {
			bundle, err := db.Open(":memory:")
			require.NoError(t, err)
			t.Cleanup(func() { _ = bundle.Client.Close() })

			ctx := context.Background()
			taskRepo := repo.NewTaskRepo(bundle.Client)
			srRepo := repo.NewStageRunRepo(bundle.Client)
			taskID, _ := seedPlanReviewRun(t, ctx, taskRepo, srRepo, status, submittedPlan("plan", "test plan content"))

			requeued := false
			err = plan.RejectPlan(ctx, plan.RejectDeps{
				Turns:     repo.NewRefinementTurnRepo(bundle.Client),
				Tasks:     taskRepo,
				StageRuns: srRepo,
				Requeue:   func(context.Context, string, string) error { requeued = true; return nil },
			}, taskID, "needs more detail")

			require.True(t, errors.Is(err, plan.ErrPlanNotReady), "got %v", err)
			assertPlanGateUntouched(t, ctx, bundle, taskID, status)
			require.False(t, requeued, "Requeue must not be called when rejection is refused")
		})
	}
}

func TestApprovePlan_SubmittedPlanWithWaitReason_FreezesOnlyThePlan(t *testing.T) {
	bundle, err := db.Open(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { _ = bundle.Client.Close() })

	ctx := context.Background()
	taskRepo := repo.NewTaskRepo(bundle.Client)
	srRepo := repo.NewStageRunRepo(bundle.Client)
	output := submittedPlan("summary", "SUBMITTED_PLAN")
	output[pipeline.WaitReasonKey] = "Plan review: awaiting user approval"
	taskID, _ := seedPlanReviewRun(t, ctx, taskRepo, srRepo, "awaiting_user", output)

	status, err := plan.PlanStatus(ctx, plan.StatusDeps{Tasks: taskRepo, StageRuns: srRepo}, taskID)
	require.NoError(t, err)
	require.Equal(t, map[string]any{"summary": "SUBMITTED_PLAN"}, status.ApprovedPlan, "the live plan view must not show orchestrator keys")

	task, err := plan.ApprovePlan(ctx, plan.ApproveDeps{
		Turns:     repo.NewRefinementTurnRepo(bundle.Client),
		Tasks:     taskRepo,
		StageRuns: srRepo,
	}, taskID)
	require.NoError(t, err)
	require.Equal(t, map[string]any{"summary": "SUBMITTED_PLAN"}, task.Metadata["approvedPlan"], "wait_reason must not be frozen into the approved plan")
}
