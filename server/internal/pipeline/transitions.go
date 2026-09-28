package pipeline

import (
	"context"
	"fmt"
	"time"

	"github.com/lx-wnk/kontor/server/internal/db/ent"
	"github.com/lx-wnk/kontor/server/internal/db/repo"
)

// applyTransition executes a stage transition. When a Client is available it
// wraps all DB writes in a single SQLite transaction to prevent torn state on
// mid-write crashes or context cancellations.
func (o *PipelineOrchestrator) applyTransition(ctx context.Context, task *ent.Task, sr *ent.StageRun, t StageTransition) (result *ent.StageRun, retErr error) {
	if nt, ok := t.(NextTransition); ok {
		if reason := StageKindViolation(task.Kind, nt.Stage); reason != "" {
			t = FailTransition{Reason: reason}
		}
	}
	if o.opts.Client != nil {
		tx, err := o.opts.Client.Tx(ctx)
		if err != nil {
			return nil, fmt.Errorf("applyTransition.beginTx: %w", err)
		}
		defer func() {
			if retErr != nil {
				_ = tx.Rollback()
			}
		}()
		txSR := repo.NewStageRunRepo(tx.Client())
		txTask := repo.NewTaskRepo(tx.Client())
		txAudit := repo.NewAuditEventRepo(tx.Client())
		txPerm := repo.NewPermissionRepo(tx.Client())
		// applyTransitionWrites reads the enriched payload in-tx (sees uncommitted
		// writes) and returns it alongside the stage run. The broadcast fires after
		// tx.Commit() so clients receive a consistent post-commit snapshot.
		var enrichedPayload any
		var postCommit []func()
		result, enrichedPayload, postCommit, retErr = o.applyTransitionWrites(ctx, task, sr, t, txSR, txTask, txAudit, txPerm)
		if retErr != nil {
			return nil, retErr
		}
		if retErr = tx.Commit(); retErr != nil {
			return nil, fmt.Errorf("applyTransition.commit: %w", retErr)
		}
		// postCommit closures (dependent-task cascade, taskLocks.Delete, OnStageFailed)
		// run only once the write is durable — never for a rolled-back transaction.
		for _, fn := range postCommit {
			fn()
		}
		// Worktree removal runs a separate DB write; it must happen after the tx
		// commits to avoid blocking on SQLite's single-writer lock.
		o.afterCommitTerminalCleanup(ctx, task, t)
		// Broadcast after successful commit — payload was already read in-tx.
		if o.opts.OnTaskChanged != nil {
			o.opts.OnTaskChanged(task.ID, transitionKindName(t), enrichedPayload)
		}
		return result, nil
	}
	// No client available (e.g. in tests with mocked repos) — write without tx.
	// permRepo is nil; buildEnrichedPayload is skipped; OnTaskChanged receives nil
	// payload and falls back to a live read in the closure.
	var enrichedPayload any
	var postCommit []func()
	result, enrichedPayload, postCommit, retErr = o.applyTransitionWrites(ctx, task, sr, t, o.opts.StageRunRepo, o.opts.TaskRepo, o.opts.AuditRepo, nil)
	if retErr != nil {
		return nil, retErr
	}
	// No tx to gate on — run the closures immediately (D2).
	for _, fn := range postCommit {
		fn()
	}
	o.afterCommitTerminalCleanup(ctx, task, t)
	if o.opts.OnTaskChanged != nil {
		o.opts.OnTaskChanged(task.ID, transitionKindName(t), enrichedPayload)
	}
	return result, nil
}

func (o *PipelineOrchestrator) applyTransitionWrites(
	ctx context.Context,
	task *ent.Task,
	sr *ent.StageRun,
	t StageTransition,
	srRepo repo.StageRunRepo,
	taskRepo repo.TaskRepo,
	auditRepo repo.AuditEventRepo,
	permRepo repo.PermissionRepo,
) (*ent.StageRun, any, []func(), error) {
	now := time.Now()
	var updatedRunID string
	var newRunID string
	var postCommit []func()

	switch tr := t.(type) {
	case NextTransition:
		if _, err := srRepo.Update(ctx, sr.ID, repo.UpdateStageRunInput{
			Status:  strPtr("done"),
			EndedAt: &now,
			Output:  tr.Output,
		}); err != nil {
			return nil, nil, nil, fmt.Errorf("applyTransition.next.updateRun: %w", err)
		}
		taskUpdate := repo.UpdateTaskInput{CurrentStage: &tr.Stage}
		if tr.MetaClear {
			taskUpdate.MetadataClear = true
		} else if tr.MetadataPatch != nil {
			taskUpdate.Metadata = tr.MetadataPatch
		}
		if _, err := taskRepo.Update(ctx, task.ID, taskUpdate); err != nil {
			return nil, nil, nil, fmt.Errorf("applyTransition.next.updateTask: %w", err)
		}
		_ = auditRepo.RecordTaskAudit(ctx, task.ID, nil, "stage_transition", "task:"+task.ID,
			map[string]any{"from": task.CurrentStage, "to": tr.Stage})
		updatedRunID = sr.ID
		// The completed run's own credential is revoked here too, even though
		// the task moves on rather than finishing: this stage_run's agent has
		// exited and gets no further calls, exactly like Done/Fail.
		postCommit = append(postCommit, func() { o.stageRuns.releaseStageRun(ctx, sr.ID) })

	case DoneTransition:
		if _, err := srRepo.Update(ctx, sr.ID, repo.UpdateStageRunInput{
			Status: strPtr("done"), EndedAt: &now, Output: tr.Output,
		}); err != nil {
			return nil, nil, nil, fmt.Errorf("applyTransition.done.updateRun: %w", err)
		}
		done := "done"
		if _, err := taskRepo.Update(ctx, task.ID, repo.UpdateTaskInput{CurrentStage: &done}); err != nil {
			return nil, nil, nil, fmt.Errorf("applyTransition.done.updateTask: %w", err)
		}
		_ = auditRepo.RecordTaskAudit(ctx, task.ID, nil, "task_done", "task:"+task.ID, nil)
		updatedRunID = sr.ID
		postCommit = append(postCommit, func() {
			o.handleDependentTasks(ctx, task.ID, "done")
			o.taskLocks.Delete(task.ID)
			o.stageRuns.releaseStageRun(ctx, sr.ID)
		})

	case FailTransition:
		output := make(map[string]any, len(sr.Output)+len(tr.Output)+1)
		for k, v := range sr.Output {
			output[k] = v
		}
		for k, v := range tr.Output {
			output[k] = v
		}
		output["error"] = tr.Reason
		if _, err := srRepo.Update(ctx, sr.ID, repo.UpdateStageRunInput{
			Status: strPtr("failed"), EndedAt: &now, Output: output,
		}); err != nil {
			return nil, nil, nil, fmt.Errorf("applyTransition.fail.updateRun: %w", err)
		}
		_ = auditRepo.RecordTaskAudit(ctx, task.ID, nil, "stage_failed", "task:"+task.ID,
			map[string]any{"stage": sr.Stage, "iteration": sr.Iteration, "error": tr.Reason})
		updatedRunID = sr.ID
		if o.opts.OnStageFailed != nil {
			info := StageFailedInfo{StageRunID: sr.ID, Stage: sr.Stage, Iteration: sr.Iteration, Error: tr.Reason}
			postCommit = append(postCommit, func() { o.opts.OnStageFailed(task.ID, info) })
		}
		postCommit = append(postCommit, func() { o.stageRuns.releaseStageRun(ctx, sr.ID) })

	case WaitUserTransition:
		if _, err := srRepo.Update(ctx, sr.ID, repo.UpdateStageRunInput{
			Status:   strPtr("awaiting_user"),
			Output:   tr.Output,
			PIDClear: tr.AgentDone, // clear dead PID so the awaiting_user reaper does not immediately re-fail
		}); err != nil {
			return nil, nil, nil, fmt.Errorf("applyTransition.waitUser.updateRun: %w", err)
		}
		_ = auditRepo.RecordTaskAudit(ctx, task.ID, nil, "awaiting_user", "task:"+task.ID,
			map[string]any{"reason": tr.Reason})
		updatedRunID = sr.ID

	case IterateTransition:
		task2, _ := taskRepo.GetByID(ctx, task.ID)
		maxIter := 20
		if task2 != nil {
			maxIter = task2.MaxIterations
		}
		if sr.Iteration+1 >= maxIter {
			failOutput := tr.Output
			if failOutput == nil {
				failOutput = map[string]any{}
			}
			failOutput["error"] = fmt.Sprintf("iteration limit reached (%d)", maxIter)
			if _, err := srRepo.Update(ctx, sr.ID, repo.UpdateStageRunInput{
				Status: strPtr("failed"), EndedAt: &now, Output: failOutput,
			}); err != nil {
				return nil, nil, nil, fmt.Errorf("applyTransition.iterate.limitFail: %w", err)
			}
			_ = auditRepo.RecordTaskAudit(ctx, task.ID, nil, "iteration_limit_reached", "task:"+task.ID,
				map[string]any{"maxIter": maxIter, "lastIteration": sr.Iteration})
			updatedRunID = sr.ID
			if o.opts.OnStageFailed != nil {
				info := StageFailedInfo{StageRunID: sr.ID, Stage: sr.Stage, Iteration: sr.Iteration, Error: fmt.Sprintf("iteration limit reached (%d)", maxIter)}
				postCommit = append(postCommit, func() { o.opts.OnStageFailed(task.ID, info) })
			}
			postCommit = append(postCommit, func() { o.stageRuns.releaseStageRun(ctx, sr.ID) })
		} else {
			if _, err := srRepo.Update(ctx, sr.ID, repo.UpdateStageRunInput{
				Status: strPtr("done"), EndedAt: &now, Output: tr.Output,
			}); err != nil {
				return nil, nil, nil, fmt.Errorf("applyTransition.iterate.updateRun: %w", err)
			}
			postCommit = append(postCommit, func() { o.stageRuns.releaseStageRun(ctx, sr.ID) })
			newSR, err := srRepo.Create(ctx, repo.CreateStageRunInput{
				TaskID:      task.ID,
				Stage:       sr.Stage,
				Iteration:   sr.Iteration + 1,
				SessionName: BuildSessionName(task.Slug, sr.Stage, sr.Iteration+1),
			})
			if err != nil {
				return nil, nil, nil, fmt.Errorf("applyTransition.iterate.createRun: %w", err)
			}
			updatedRunID = sr.ID
			newRunID = newSR.ID
		}

	case OnHoldTransition:
		if _, err := srRepo.Update(ctx, sr.ID, repo.UpdateStageRunInput{
			Status: strPtr("on_hold"), Output: tr.Output,
		}); err != nil {
			return nil, nil, nil, fmt.Errorf("applyTransition.onHold.updateRun: %w", err)
		}
		onHold := "on_hold"
		if _, err := taskRepo.Update(ctx, task.ID, repo.UpdateTaskInput{CurrentStage: &onHold}); err != nil {
			return nil, nil, nil, fmt.Errorf("applyTransition.onHold.updateTask: %w", err)
		}
		_ = auditRepo.RecordTaskAudit(ctx, task.ID, nil, "moved_on_hold", "task:"+task.ID,
			map[string]any{"permissionRequestId": tr.PermissionRequestID})
		updatedRunID = sr.ID

	case AsyncRunningTransition:
		output := tr.Output
		if tr.SessionFile != "" {
			if output == nil {
				output = map[string]any{}
			} else {
				// shallow-copy to avoid mutating the caller's map
				cp := make(map[string]any, len(output)+1)
				for k, v := range output {
					cp[k] = v
				}
				output = cp
			}
			output["synthetic_session_file"] = tr.SessionFile
		}
		update := repo.UpdateStageRunInput{Status: strPtr("running"), Output: output}
		if tr.PID != 0 {
			update.PID = &tr.PID
		}
		if tr.SessionID != "" {
			update.SessionID = &tr.SessionID
		}
		if _, err := srRepo.Update(ctx, sr.ID, update); err != nil {
			return nil, nil, nil, fmt.Errorf("applyTransition.asyncRunning.updateRun: %w", err)
		}
		_ = auditRepo.RecordTaskAudit(ctx, task.ID, nil, "agent_spawned", "task:"+task.ID,
			map[string]any{"pid": tr.PID, "stage": sr.Stage})
		updatedRunID = sr.ID

	case RequeueTransition:
		output := tr.Output
		if output == nil {
			output = map[string]any{}
		}
		output["requeue_reason"] = tr.Reason
		output["attempt"] = tr.Attempt
		nextRetry := tr.NextRetryAt
		if _, err := srRepo.Update(ctx, sr.ID, repo.UpdateStageRunInput{
			Status:      strPtr("requeued"),
			RetryCount:  &tr.Attempt,
			NextRetryAt: &nextRetry,
			Output:      output,
		}); err != nil {
			return nil, nil, nil, fmt.Errorf("applyTransition.requeue.updateRun: %w", err)
		}
		_ = auditRepo.RecordTaskAudit(ctx, task.ID, nil, "stage_requeued", "task:"+task.ID,
			map[string]any{"stage": sr.Stage, "attempt": tr.Attempt, "reason": tr.Reason})
		updatedRunID = sr.ID
		// A requeue is only ever decided from a completion result, so the agent
		// that held this run's credentials has already exited. Releasing here
		// keeps the respawn from adding a second, concurrently valid key for the
		// same stage_run_id once sweepRequeueableRuns promotes the run back to
		// pending.
		postCommit = append(postCommit, func() { o.stageRuns.releaseStageRun(ctx, sr.ID) })

	default:
		panic(fmt.Sprintf("orchestrator.applyTransition: unhandled transition type %T", t))
	}

	// Build the enriched task payload in-tx so the snapshot reflects the writes
	// above (still uncommitted but visible within the same transaction). The
	// broadcast itself happens after tx.Commit() in applyTransition.
	var enrichedPayload any
	if o.opts.OnTaskChanged != nil && o.opts.BuildTaskPayload != nil && permRepo != nil {
		enrichedPayload = o.opts.BuildTaskPayload(ctx, task.ID, srRepo, permRepo)
	}

	targetID := updatedRunID
	if newRunID != "" {
		targetID = newRunID
	}
	// Use the tx-bound srRepo so the read is consistent with the writes above.
	result, err := srRepo.GetByID(ctx, targetID)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("applyTransition.getResult: %w", err)
	}
	// postCommit is returned, not executed here — the caller (applyTransition)
	// runs these closures only after the surrounding transaction commits.
	return result, enrichedPayload, postCommit, nil
}

func transitionKindName(t StageTransition) string {
	switch t.(type) {
	case NextTransition:
		return "next"
	case DoneTransition:
		return "done"
	case FailTransition:
		return "fail"
	case WaitUserTransition:
		return "wait_user"
	case IterateTransition:
		return "iterate"
	case OnHoldTransition:
		return "on_hold"
	case AsyncRunningTransition:
		return "async_running"
	case RequeueTransition:
		return "requeue"
	default:
		return "unknown"
	}
}

// decideCompletedTransition maps a completed stage_run to its next transition.
// self_review may loop back to implementation; finalization produces DoneTransition.
func (o *PipelineOrchestrator) decideCompletedTransition(ctx context.Context, task *ent.Task, run *ent.StageRun, output map[string]any) StageTransition {
	if run.Stage == StageJob {
		return DoneTransition{Output: output}
	}
	if run.Stage == "finalization" {
		return DoneTransition{Output: output}
	}
	if run.Stage == "self_review" {
		passed, _ := output["passed"].(bool)
		if !passed {
			feedback := SummarizeReviewFindings(output)
			prevCycles := 0
			if task.Metadata != nil {
				if v, ok := task.Metadata["review_cycles"].(float64); ok {
					prevCycles = int(v)
				}
			}
			cycles := prevCycles + 1
			maxCycles := o.configCache.Number(ctx, maxReviewCyclesKey, defaultMaxReviewCycles)
			if task.Metadata != nil {
				if v, ok := task.Metadata["maxReviewCycles"].(float64); ok && int(v) > 0 {
					maxCycles = int(v)
				}
			}
			if cycles >= maxCycles {
				return WaitUserTransition{
					Reason:    fmt.Sprintf("review cycle limit (%d) reached", maxCycles),
					Output:    output,
					AgentDone: true,
				}
			}
			meta := map[string]any{}
			if task.Metadata != nil {
				for k, v := range task.Metadata {
					meta[k] = v
				}
			}
			meta["review_feedback"] = feedback
			meta["review_cycles"] = cycles
			return NextTransition{Stage: "implementation", Output: output, MetadataPatch: meta}
		}
		// Passed — clear stale review feedback
		if task.Metadata != nil {
			if _, hasFeedback := task.Metadata["review_feedback"]; hasFeedback {
				rest := map[string]any{}
				for k, v := range task.Metadata {
					if k != "review_feedback" && k != "review_cycles" {
						rest[k] = v
					}
				}
				if len(rest) == 0 {
					return NextTransition{Stage: "finalization", Output: output, MetaClear: true}
				}
				return NextTransition{Stage: "finalization", Output: output, MetadataPatch: rest}
			}
		}
		return NextTransition{Stage: "finalization", Output: output}
	}
	// plan_review gates on human approval; it never auto-advances.
	if run.Stage == "plan_review" {
		return WaitUserTransition{Reason: "Plan review: awaiting user approval", AgentDone: true}
	}
	// After ready, enter plan_review only when the task opted into plan mode.
	if run.Stage == "ready" {
		if task.PlanMode {
			return NextTransition{Stage: "plan_review", Output: output}
		}
		return NextTransition{Stage: "implementation", Output: output}
	}
	return NextTransition{Stage: NextStage(run.Stage), Output: output}
}
