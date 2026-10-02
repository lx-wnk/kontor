// Package plan implements approve/reject/status for the plan_review gate.
package plan

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"time"

	"github.com/lx-wnk/kontor/server/internal/db/ent"
	"github.com/lx-wnk/kontor/server/internal/db/repo"
	"github.com/lx-wnk/kontor/server/internal/pipeline"
)

// ApproveDeps are the dependencies required by ApprovePlan.
type ApproveDeps struct {
	Turns     repo.RefinementTurnRepo
	Tasks     repo.TaskRepo
	StageRuns repo.StageRunRepo
	Advance   func(ctx context.Context, taskID string) error
	// Revoke invalidates the plan_review stage run's MCP credentials once it is
	// marked done — its agent is gone by then. Nil disables revocation; a
	// failure logs and never blocks the approval.
	Revoke func(ctx context.Context, stageRunID string) error
}

// RejectDeps are the dependencies required by RejectPlan.
type RejectDeps struct {
	Turns     repo.RefinementTurnRepo
	Tasks     repo.TaskRepo
	StageRuns repo.StageRunRepo
	// Requeue triggers a new plan_review run carrying the feedback prompt.
	Requeue func(ctx context.Context, taskID, prompt string) error
}

// StatusDeps are the dependencies required by PlanStatus.
type StatusDeps struct {
	Turns     repo.RefinementTurnRepo
	Tasks     repo.TaskRepo
	StageRuns repo.StageRunRepo
}

// PlanStatusResult carries the gate state and latest plan output.
type PlanStatusResult struct {
	GateState    string         `json:"gate_state"`
	ApprovedPlan map[string]any `json:"approved_plan,omitempty"`
}

// iterationCapKey and feedbackKey are the task metadata keys used by the plan gate.
const (
	iterationCapKey = "planRejectCount"
	feedbackKey     = "planReviewFeedback"
	approvedPlanKey = "approvedPlan"
	// DefaultPlanIterationCap is defined in db/defaults.go; mirror here for the
	// cap check so this package has no cross-package import just for a constant.
	localDefaultPlanIterationCap = 3
)

// ErrPlanNotReady is returned when approve/reject is attempted while the latest
// plan_review run is missing or not awaiting_user, or when approve finds no
// submitted plan in its output.
var ErrPlanNotReady = errors.New("plan_review is not ready")

// requireAwaitingPlanReview returns the latest plan_review run only when it is
// awaiting_user, whatever its output holds.
func requireAwaitingPlanReview(ctx context.Context, runs repo.StageRunRepo, taskID string) (*ent.StageRun, error) {
	if runs == nil {
		return nil, fmt.Errorf("%w: no plan_review run", ErrPlanNotReady)
	}
	sr, err := runs.GetLatestByTaskAndStage(ctx, taskID, "plan_review")
	if err != nil {
		if ent.IsNotFound(err) {
			return nil, fmt.Errorf("%w: no plan_review run", ErrPlanNotReady)
		}
		return nil, fmt.Errorf("requireAwaitingPlanReview: %w", err)
	}
	if sr == nil {
		return nil, fmt.Errorf("%w: no plan_review run", ErrPlanNotReady)
	}
	if sr.Status != "awaiting_user" {
		return nil, fmt.Errorf("%w: plan_review is %s, not awaiting_user", ErrPlanNotReady, sr.Status)
	}
	return sr, nil
}

// requireReadyPlan additionally demands the agent-submitted marker plus content
// beyond it: orchestrator notes in the same field (validation_error,
// synthetic_session_file, wait_reason) carry no marker and are never a plan.
func requireReadyPlan(ctx context.Context, runs repo.StageRunRepo, taskID string) (*ent.StageRun, error) {
	sr, err := requireAwaitingPlanReview(ctx, runs, taskID)
	if err != nil {
		return nil, err
	}
	if submitted, _ := sr.Output[pipeline.StageOutputSubmittedKey].(bool); !submitted {
		return nil, fmt.Errorf("%w: plan output was not submitted by the agent", ErrPlanNotReady)
	}
	if len(sr.Output) == 1 {
		return nil, fmt.Errorf("%w: plan output is empty", ErrPlanNotReady)
	}
	return sr, nil
}

// ApprovePlan freezes the plan onto the task, marks the plan_review stage_run done,
// and advances the task to implementation. Mirrors refine.Confirm. Returns
// ErrPlanNotReady unless the plan_review run is awaiting_user with a submitted plan.
func ApprovePlan(ctx context.Context, d ApproveDeps, taskID string) (*ent.Task, error) {
	sr, err := requireReadyPlan(ctx, d.StageRuns, taskID)
	if err != nil {
		return nil, err
	}

	planOutput := maps.Clone(sr.Output)
	delete(planOutput, pipeline.StageOutputSubmittedKey)

	// Build metadata update: freeze plan + clear reject counter.
	meta := map[string]any{approvedPlanKey: planOutput}

	implementation := "implementation"
	update := repo.UpdateTaskInput{
		CurrentStage: &implementation,
		Metadata:     meta,
	}

	// Persist an approved sentinel turn.
	if d.Turns != nil {
		phase := "plan_approved"
		if _, err := d.Turns.Create(ctx, repo.CreateTurnInput{
			TaskID:  taskID,
			Role:    "assistant",
			Content: "plan approved",
			Phase:   &phase,
		}); err != nil {
			return nil, fmt.Errorf("approve_plan: store sentinel turn: %w", err)
		}
	}

	// Mark the plan_review stage run done.
	now := time.Now()
	done := "done"
	_, _ = d.StageRuns.Update(ctx, sr.ID, repo.UpdateStageRunInput{
		Status:  &done,
		EndedAt: &now,
	})
	if d.Revoke != nil {
		if rerr := d.Revoke(ctx, sr.ID); rerr != nil {
			slog.Warn("plan: revoking stage-run credentials failed", "stageRun", sr.ID, "err", rerr)
		}
	}

	if d.Tasks != nil {
		if _, err := d.Tasks.Update(ctx, taskID, update); err != nil {
			return nil, fmt.Errorf("approve_plan: update task: %w", err)
		}
	}

	if d.Advance != nil {
		_ = d.Advance(ctx, taskID)
	}

	return d.Tasks.GetByID(ctx, taskID)
}

// RejectPlan records feedback and triggers a new plan_review run up to DefaultPlanIterationCap.
// Beyond the cap, it stores feedback but does not requeue. Returns ErrPlanNotReady
// unless the plan_review run is awaiting_user; plan content is not required, so a
// run that produced no usable plan can still be sent back.
func RejectPlan(ctx context.Context, d RejectDeps, taskID, feedback string) error {
	if _, err := requireAwaitingPlanReview(ctx, d.StageRuns, taskID); err != nil {
		return err
	}

	task, err := d.Tasks.GetByID(ctx, taskID)
	if err != nil {
		return fmt.Errorf("reject_plan: get task: %w", err)
	}

	// Read current reject count from metadata.
	count := 0
	if v, ok := task.Metadata[iterationCapKey]; ok {
		switch n := v.(type) {
		case int:
			count = n
		case float64:
			count = int(n)
		}
	}

	// Store feedback and increment counter.
	newMeta := make(map[string]any, len(task.Metadata)+2)
	for k, v := range task.Metadata {
		newMeta[k] = v
	}
	newMeta[feedbackKey] = feedback
	newMeta[iterationCapKey] = count + 1

	if _, err := d.Turns.Create(ctx, repo.CreateTurnInput{
		TaskID:  taskID,
		Role:    "user",
		Content: feedback,
		Phase:   strPtr("plan_rejected"),
	}); err != nil {
		return fmt.Errorf("reject_plan: store feedback turn: %w", err)
	}

	if _, err := d.Tasks.Update(ctx, taskID, repo.UpdateTaskInput{Metadata: newMeta}); err != nil {
		return fmt.Errorf("reject_plan: update task metadata: %w", err)
	}

	// Only requeue when under the cap.
	if count < localDefaultPlanIterationCap && d.Requeue != nil {
		if err := d.Requeue(ctx, taskID, feedback); err != nil {
			return fmt.Errorf("reject_plan: requeue: %w", err)
		}
	}

	return nil
}

// PlanStatus returns the current gate state and latest plan output for a task.
func PlanStatus(ctx context.Context, d StatusDeps, taskID string) (PlanStatusResult, error) {
	result := PlanStatusResult{GateState: "unknown"}

	var sr *ent.StageRun
	if d.StageRuns != nil {
		if fetched, err := d.StageRuns.GetLatestByTaskAndStage(ctx, taskID, "plan_review"); err == nil && fetched != nil {
			sr = fetched
			result.GateState = sr.Status
		}
	}

	if d.Tasks != nil {
		task, err := d.Tasks.GetByID(ctx, taskID)
		if err == nil && task != nil {
			if v, ok := task.Metadata[approvedPlanKey]; ok {
				if m, ok := v.(map[string]any); ok {
					result.ApprovedPlan = m
				}
			}
		}
	}

	// Fall back to the live stage_run output before approval freezes the plan.
	if result.ApprovedPlan == nil && sr != nil && len(sr.Output) > 0 {
		result.ApprovedPlan = maps.Clone(sr.Output)
		delete(result.ApprovedPlan, pipeline.StageOutputSubmittedKey)
	}

	return result, nil
}

func strPtr(s string) *string { return &s }
