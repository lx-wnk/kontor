package tasks

import (
	"context"
	"fmt"

	"github.com/lx-wnk/kontor/server/internal/db/ent"
	"github.com/lx-wnk/kontor/server/internal/db/repo"
	"github.com/lx-wnk/kontor/server/internal/taskcontrol"
)

// AdvanceDeps carries everything the shared Advance dispatcher needs.
type AdvanceDeps struct {
	TaskRepo     repo.TaskRepo
	SRRepo       repo.StageRunRepo
	PermRepo     repo.PermissionRepo
	AuditRepo    repo.AuditEventRepo
	Orchestrator OrchestratorIface
	// RefineReader lets Advance compute the same primary the enrich layer shows
	// (refine status discriminates concept-stage actions). May be nil.
	RefineReader RefineStatusReader
}

// AdvanceResult is the response shape returned by Advance.
type AdvanceResult struct {
	Dispatched bool   `json:"dispatched"`
	Primary    string `json:"primary"`
	// Result is the operation-specific payload (ApproveAllPendingResult,
	// stageRunResponse, etc.) or nil for no-op paths.
	Result any `json:"result,omitempty"`
	// Message provides context for non-dispatched cases.
	Message string `json:"message,omitempty"`
}

// stageRunResultOrNil converts a stage run into the wire DTO, matching the
// nil-check every other stage-run route already performs (progress, retry,
// resume, resume-stage): the orchestrator can legitimately return (nil, nil)
// when nothing was there to act on, and toStageRunResponse dereferences its
// argument, so passing it a nil pointer directly would panic.
func stageRunResultOrNil(sr *ent.StageRun) any {
	if sr == nil {
		return nil
	}
	return toStageRunResponse(sr)
}

// Advance executes the primary action from ComputeActions for the task.
// It dispatches exactly one operation based on the locked design:
//   - retry      → RequeueForUser
//   - approve_all_pending → ApproveAllPending (shared service)
//   - advance    → ProgressTask
//   - resume     → ResumeFromUser
//   - approve_spec → no-op (deliberate human gate)
//   - terminal/none → no-op
func Advance(ctx context.Context, d AdvanceDeps, taskID string) (AdvanceResult, error) {
	task, err := d.TaskRepo.GetByID(ctx, taskID)
	if err != nil {
		return AdvanceResult{}, fmt.Errorf("advance: task not found: %w", err)
	}

	// Load latest stage run for this stage to build TaskState.
	latest, _ := d.SRRepo.GetLatestByTaskAndStage(ctx, taskID, task.CurrentStage)
	var runStatus string
	if latest != nil {
		runStatus = latest.Status
	}

	// Count pending permissions only when we have a run on this stage.
	var pendingPerms int
	if latest != nil && latest.Stage == task.CurrentStage {
		pendingPerms, _ = d.PermRepo.CountForStageRun(ctx, latest.ID)
	}

	var refineStatus string
	if d.RefineReader != nil {
		refineStatus, _ = d.RefineReader.State(taskID)
	}

	state := taskcontrol.FromFields(task.CurrentStage, runStatus, refineStatus, pendingPerms, false)
	actions := taskcontrol.ComputeActions(state)

	primary := ""
	for _, a := range actions {
		if a.Primary {
			primary = a.Action
			break
		}
	}

	switch primary {
	case taskcontrol.ActionRetry:
		sr, err := d.Orchestrator.RequeueForUser(ctx, taskID, "")
		if err != nil {
			return AdvanceResult{}, fmt.Errorf("advance(retry): %w", err)
		}
		return AdvanceResult{Dispatched: true, Primary: primary, Result: stageRunResultOrNil(sr)}, nil

	case taskcontrol.ActionApproveAllPending:
		res, err := ApproveAllPending(ctx, ApproveAllPendingDeps{
			TaskRepo:     d.TaskRepo,
			SRRepo:       d.SRRepo,
			PermRepo:     d.PermRepo,
			AuditRepo:    d.AuditRepo,
			Orchestrator: d.Orchestrator,
		}, taskID)
		if err != nil {
			return AdvanceResult{}, fmt.Errorf("advance(approve_all_pending): %w", err)
		}
		return AdvanceResult{Dispatched: true, Primary: primary, Result: res}, nil

	case taskcontrol.ActionAdvance:
		sr, err := d.Orchestrator.ProgressTask(ctx, taskID, nil)
		if err != nil {
			return AdvanceResult{}, fmt.Errorf("advance(progress): %w", err)
		}
		return AdvanceResult{Dispatched: true, Primary: primary, Result: stageRunResultOrNil(sr)}, nil

	case taskcontrol.ActionResume:
		sr, err := d.Orchestrator.ResumeFromUser(ctx, taskID, "")
		if err != nil {
			return AdvanceResult{}, fmt.Errorf("advance(resume): %w", err)
		}
		return AdvanceResult{Dispatched: true, Primary: primary, Result: stageRunResultOrNil(sr)}, nil

	case taskcontrol.ActionApproveSpec:
		// Spec approval is a deliberate human gate; advance must not auto-approve.
		return AdvanceResult{
			Dispatched: false,
			Primary:    primary,
			Message:    "spec approval requires explicit approve_spec",
		}, nil

	default:
		// Terminal stages, on_hold with no primary, or empty primary.
		return AdvanceResult{Dispatched: false, Primary: primary}, nil
	}
}
