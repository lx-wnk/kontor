package pipeline

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/lx-wnk/kontor/server/internal/db/ent"
	"github.com/lx-wnk/kontor/server/internal/db/repo"
	"github.com/lx-wnk/kontor/server/internal/proc"
)

// sweepAwaitingUserRuns reaps awaiting_user stage_runs whose agent process has
// died or whose wallclock time has exceeded the configured limit.
// allRunning is the prefetched slice of non-terminal runs from tick().
func (o *PipelineOrchestrator) sweepAwaitingUserRuns(ctx context.Context, allRunning []*ent.StageRun) error {
	timeoutSec := o.configCache.Number(ctx, awaitingUserTimeoutKey, defaultAwaitingUserTimeout)
	for _, run := range allRunning {
		if run.Status != "awaiting_user" {
			continue
		}
		task, err := o.opts.TaskRepo.GetByID(ctx, run.TaskID)
		if err != nil {
			continue
		}
		// Only reap runs that had a live agent process which has since died.
		// Nil-PID runs are legitimate: backlog/ready WaitUser transitions never
		// spawn an agent, so IsPidAlive(0) == false must not trigger the reaper.
		if run.Pid != nil && !proc.IsPidAlive(*run.Pid) {
			slog.Warn("orchestrator: awaiting_user run has dead PID — reaping as failed",
				"runID", run.ID, "stage", run.Stage, "pid", *run.Pid)
			if _, locked, err := o.sweepApplyTransition(ctx, task, run, FailTransition{
				Reason: "awaiting_user reaper: stage agent exited while permissions pending",
			}); err != nil {
				slog.Error("sweepAwaitingUserRuns.applyTransition", "err", err)
			} else if !locked {
				slog.Debug("sweepAwaitingUserRuns: task locked by progressTask — deferring to next tick", "taskID", task.ID)
			}
			continue
		}
		// Only a live agent can busy-wait. A run whose agent already exited is
		// waiting for a human, and a human wait has no limit.
		if timeoutSec > 0 && run.Pid != nil {
			anchor := run.StartedAt
			if run.LastGrantAt != nil {
				anchor = run.LastGrantAt
			}
			if anchor != nil && time.Since(*anchor) > time.Duration(timeoutSec)*time.Second {
				timeoutPid := 0
				if run.Pid != nil {
					timeoutPid = *run.Pid
				}
				slog.Warn("orchestrator: awaiting_user run exceeded wallclock timeout — killing agent",
					"runID", run.ID, "pid", timeoutPid, "timeoutSec", timeoutSec)
				_ = syscallKill(timeoutPid)
				fresh, _ := o.opts.StageRunRepo.GetByID(ctx, run.ID)
				if fresh != nil && fresh.Status == "awaiting_user" {
					elapsed := time.Since(*anchor).Seconds()
					if _, locked, err := o.sweepApplyTransition(ctx, task, fresh, FailTransition{
						Reason: fmt.Sprintf("awaiting_user timeout: ran %.0fs (limit %ds) — agent likely busy-waiting", elapsed, timeoutSec),
					}); err != nil {
						slog.Error("sweepAwaitingUserRuns.timeout.applyTransition", "err", err)
					} else if !locked {
						slog.Debug("sweepAwaitingUserRuns timeout: task locked by progressTask — deferring to next tick", "taskID", task.ID)
					}
				}
			}
		}
	}
	return nil
}

// sweepRequeueableRuns promotes requeued runs back to pending once their cooldown has elapsed.
func (o *PipelineOrchestrator) sweepRequeueableRuns(ctx context.Context) error {
	requeued, _ := o.opts.StageRunRepo.ListByStatus(ctx, "requeued", "rate_limited")
	now := time.Now()
	for _, run := range requeued {
		if run.NextRetryAt == nil || run.NextRetryAt.After(now) {
			continue
		}
		slog.Info("orchestrator: requeue cooldown elapsed — promoting to pending",
			"runID", run.ID, "stage", run.Stage, "retryCount", run.RetryCount)
		if _, err := o.opts.StageRunRepo.Update(ctx, run.ID, repo.UpdateStageRunInput{
			Status:           strPtr("pending"),
			PIDClear:         true,
			StartedAtClear:   true,
			NextRetryAtClear: true,
			OutputClear:      true,
		}); err != nil {
			slog.Error("sweepRequeueableRuns.update", "runID", run.ID, "err", err)
		}
	}
	return nil
}

// sweepOrphanRuns reaps four zombie modes:
//  1. Non-terminal stage_run whose parent task is parked (done/cancelled/on_hold).
//  2. on_hold stage_run with a dead PID.
//  3. pending stage_run stuck > 5 min without a PID.
//  4. running stage_run that started before this orchestrator process did and
//     has no live PID (see case 4 below for why a nil/zero PID alone isn't enough).
func (o *PipelineOrchestrator) sweepOrphanRuns(ctx context.Context, allRunning []*ent.StageRun) error {
	pendings, _ := o.opts.StageRunRepo.ListPending(ctx)
	requeued, _ := o.opts.StageRunRepo.ListByStatus(ctx, "requeued", "rate_limited")
	all := make([]*ent.StageRun, 0, len(allRunning)+len(pendings)+len(requeued))
	all = append(all, allRunning...)
	all = append(all, pendings...)
	all = append(all, requeued...)
	for _, run := range all {
		task, err := o.opts.TaskRepo.GetByID(ctx, run.TaskID)
		if err != nil {
			continue
		}
		// Case 1: task is parked but stage_run is non-terminal
		if task.CurrentStage == "done" || task.CurrentStage == "cancelled" || task.CurrentStage == "on_hold" {
			fresh, _ := o.opts.StageRunRepo.GetByID(ctx, run.ID)
			if fresh == nil || fresh.Status == "done" || fresh.Status == "failed" {
				continue
			}
			pid := stageRunPid(fresh)
			if proc.IsPidAlive(pid) {
				_ = syscallKill(pid)
			}
			slog.Warn("orchestrator: orphan stage_run — task is parked, reaping run as failed",
				"runID", fresh.ID, "taskStage", task.CurrentStage)
			if _, locked, err := o.sweepApplyTransition(ctx, task, fresh, FailTransition{
				Reason: fmt.Sprintf("orphan reaper: task reached %s with stage_run still %s", task.CurrentStage, fresh.Status),
			}); err != nil {
				slog.Error("sweepOrphanRuns.case1.applyTransition", "err", err)
			} else if !locked {
				slog.Debug("sweepOrphanRuns case1: task locked by progressTask — deferring to next tick", "taskID", task.ID)
			}
			continue
		}
		// Case 2: on_hold with dead PID
		if run.Status == "on_hold" && run.Pid != nil && !proc.IsPidAlive(*run.Pid) {
			fresh, _ := o.opts.StageRunRepo.GetByID(ctx, run.ID)
			if fresh == nil || fresh.Status == "done" || fresh.Status == "failed" {
				continue
			}
			slog.Warn("orchestrator: on_hold run has dead PID — reaping as failed", "runID", fresh.ID)
			if _, locked, err := o.sweepApplyTransition(ctx, task, fresh, FailTransition{Reason: "orphan reaper: on_hold agent exited"}); err != nil {
				slog.Error("sweepOrphanRuns.case2.applyTransition", "err", err)
			} else if !locked {
				slog.Debug("sweepOrphanRuns case2: task locked by progressTask — deferring to next tick", "taskID", task.ID)
			}
			continue
		}
		// Case 3: pending stuck > 5 min without a PID
		if run.Status == "pending" && run.Pid == nil && run.StartedAt != nil {
			if time.Since(*run.StartedAt) > pendingStaleDuration {
				fresh, _ := o.opts.StageRunRepo.GetByID(ctx, run.ID)
				if fresh == nil || fresh.Status != "pending" {
					continue
				}
				elapsed := time.Since(*run.StartedAt).Seconds()
				slog.Warn("orchestrator: pending run stuck without spawn — reaping as failed",
					"runID", fresh.ID, "elapsedSec", elapsed)
				if _, locked, err := o.sweepApplyTransition(ctx, task, fresh, FailTransition{
					Reason: fmt.Sprintf("orphan reaper: pending stage_run never promoted to running (%.0fs elapsed)", elapsed),
				}); err != nil {
					slog.Error("sweepOrphanRuns.case3.applyTransition", "err", err)
				} else if !locked {
					slog.Debug("sweepOrphanRuns case3: task locked by progressTask — deferring to next tick", "taskID", task.ID)
				}
			}
			continue
		}
		// Case 4: running run that predates this orchestrator process.
		// A nil/zero PID alone is not evidence of a zombie — HTTP-adapter stages run
		// legitimately with no local process (stage_handlers.go AsyncRunningTransition{PID: 0}).
		// Only "started before we did" rules that out: no in-process goroutine of ours is
		// still feeding it, and a live subprocess PID would mean it survived our restart.
		//
		// The verdict comes from DecideRecovery, the same authority recoverRunningStageRuns
		// uses for this exact row state at startup: a run with a session is re-queued so it
		// can be respawned with --resume, and only a run with nothing to resume from is
		// failed. This sweep covers what the startup pass structurally cannot — a run whose
		// process was still alive when we recovered and died afterwards.
		if run.Status == "running" && run.StartedAt != nil && run.StartedAt.Before(o.startedAt) {
			fresh, _ := o.opts.StageRunRepo.GetByID(ctx, run.ID)
			if fresh == nil || fresh.Status != "running" {
				continue
			}
			// Decide on the re-fetched row: session_id is attached asynchronously
			// after spawn, so a run that became resumable since the tick snapshot
			// would otherwise be judged unresumable and failed.
			decision := DecideRecovery(fresh)
			if decision.Kind == "alive" {
				continue
			}
			if decision.Kind == "resume" {
				slog.Warn("orchestrator: running run predates this orchestrator — re-queueing for resume",
					"runID", fresh.ID, "stage", fresh.Stage, "reason", decision.Reason)
				if locked, err := o.sweepMarkPending(ctx, task.ID, fresh.ID); err != nil {
					slog.Error("sweepOrphanRuns.case4.markPending", "err", err)
				} else if !locked {
					slog.Debug("sweepOrphanRuns case4: task locked by progressTask — deferring to next tick", "taskID", task.ID)
				}
				continue
			}
			slog.Warn("orchestrator: running run predates this orchestrator with nothing to resume — reaping as failed",
				"runID", fresh.ID, "stage", fresh.Stage, "reason", decision.Reason)
			if _, locked, err := o.sweepApplyTransition(ctx, task, fresh, FailTransition{
				Reason: "orphan reaper: run was started by a previous orchestrator process that no longer exists, and no live agent process took it over",
			}); err != nil {
				slog.Error("sweepOrphanRuns.case4.applyTransition", "err", err)
			} else if !locked {
				slog.Debug("sweepOrphanRuns case4: task locked by progressTask — deferring to next tick", "taskID", task.ID)
			}
		}
	}
	return nil
}
