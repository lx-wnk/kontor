package pipeline

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/lx-wnk/kontor/server/internal/db/ent"
	"github.com/lx-wnk/kontor/server/internal/db/repo"
	"github.com/lx-wnk/kontor/server/internal/mcpapps"
	"github.com/lx-wnk/kontor/server/internal/proc"
	"github.com/lx-wnk/kontor/server/internal/taskcontrol"
)

// runProgressTaskLocked is the core of ProgressTask — called with the per-task
// mutex already held. It contains both the Re-entry Guard and the
// Lingering-Pending Gate.
func (o *PipelineOrchestrator) runProgressTaskLocked(ctx context.Context, taskID string, opts *ProgressOpts) (*ent.StageRun, error) {
	task, err := o.opts.TaskRepo.GetByID(ctx, taskID)
	if err != nil || IsTerminalStage(task.CurrentStage) {
		return nil, nil
	}

	handler := o.resolveHandler(task.CurrentStage)
	if handler == nil {
		return nil, nil
	}

	// Global runner-slot cap — agent-driven stages only.
	// scheduler.HasFreeSlot issues its own DB query because it is also called
	// from route handlers, not just from the tick loop.
	if handler.RequiresAgent() && !o.scheduler.HasFreeSlot(ctx, task.ID) {
		return nil, nil
	}

	// Lingering-pending gate — prevents respawn while unresolved permission_requests
	// remain on the most recent terminal or zombie-awaiting stage_run.
	// latest is declared here so ensureStageRun can reuse it and skip a redundant query.
	var latest *ent.StageRun
	if handler.RequiresAgent() {
		latest, _ = o.opts.StageRunRepo.GetLatestByTaskAndStage(ctx, task.ID, task.CurrentStage)
		if latest != nil {
			pid := stageRunPid(latest)
			isTerminal := latest.Status == "failed" || latest.Status == "done"
			isZombieAwait := latest.Status == "awaiting_user" && !proc.IsPidAlive(pid)
			if isTerminal || isZombieAwait {
				n, _ := o.opts.PermissionRepo.CountForStageRun(ctx, latest.ID)
				if n > 0 {
					slog.Info("orchestrator: progressTask blocked by lingering permission_requests",
						"taskID", taskID, "count", n, "runID", latest.ID)
					return nil, nil
				}
			}
		}
	}

	// latest is non-nil only for agent-driven stages; ensureStageRun falls back to
	// a DB query when it is nil (non-agent stages). The run is created before the
	// worktree so a worktree failure can be recorded against it via FailTransition.
	stageRun, err := o.ensureStageRun(ctx, task, latest)
	if err != nil {
		return nil, fmt.Errorf("orchestrator.ensureStageRun: %w", err)
	}

	// Auto-create a git worktree before spawning the agent.
	// Triggered when: ForceWorktrees is set (global) OR task has an explicit SourceBranch.
	// A failure here (e.g. branch already checked out in another worktree) is recorded
	// as a failed stage_run rather than a swallowed error that silently re-queues forever.
	needsWorktree := handler.RequiresAgent() && task.Kind != TaskKindJob &&
		(task.WorktreePath == nil || *task.WorktreePath == "") &&
		(o.opts.ForceWorktrees || (task.SourceBranch != nil && *task.SourceBranch != ""))
	if needsWorktree {
		wtPath, wtBranch, wtErr := o.opts.EnsureWorktreeFn(ctx, task, o.opts.WorktreeRoot)
		if wtErr != nil {
			return o.applyTransition(ctx, task, stageRun,
				FailTransition{Reason: fmt.Sprintf("worktree creation failed: %v", wtErr)})
		}
		upd := repo.UpdateTaskInput{WorktreePath: &wtPath}
		if task.SourceBranch == nil || *task.SourceBranch == "" {
			upd.SourceBranch = &wtBranch
		}
		if task, err = o.opts.TaskRepo.Update(ctx, task.ID, upd); err != nil {
			return o.applyTransition(ctx, task, stageRun,
				FailTransition{Reason: fmt.Sprintf("persisting worktree path failed: %v", err)})
		}
		slog.Info("orchestrator: created worktree", "taskID", taskID, "path", wtPath, "branch", wtBranch)

		if o.opts.SetupWorktreeFn != nil {
			if setupErr := o.opts.SetupWorktreeFn(ctx, task.ProjectID, wtPath); setupErr != nil {
				slog.Warn("orchestrator: setup_command failed (task continues)", "taskID", taskID, "err", setupErr)
			}
		}
	}

	// Re-entry guard: if the run is already running with a live PID, return without spawning.
	if handler.RequiresAgent() {
		pid := stageRunPid(stageRun)
		if (stageRun.Status == "running" || stageRun.Status == "awaiting_user") && proc.IsPidAlive(pid) {
			slog.Info("orchestrator: re-entry skipped — live PID already running",
				"stage", stageRun.Stage, "runID", stageRun.ID, "pid", pid)
			return stageRun, nil
		}
	}

	now := time.Now()
	stageRun, err = o.opts.StageRunRepo.Update(ctx, stageRun.ID, repo.UpdateStageRunInput{
		Status:    strPtr("running"),
		StartedAt: &now,
	})
	if err != nil {
		return nil, fmt.Errorf("orchestrator.updateStageRunRunning: %w", err)
	}

	// Start the per-turn checkpoint watcher once the run is confirmed running.
	// Idempotent per stage-run while the worktree lives.
	if handler.RequiresAgent() && o.opts.CheckpointerStartFn != nil &&
		task.WorktreePath != nil && *task.WorktreePath != "" {
		o.opts.CheckpointerStartFn(task.ID, *task.WorktreePath)
	}

	perms, _ := o.opts.PermissionRepo.ListTaskPermissions(ctx, task.ID)
	allRuns, _ := o.opts.StageRunRepo.ListForTask(ctx, task.ID)
	prevOutput := o.getPreviousStageOutput(ctx, task)
	priorIterOutput := o.getPriorIterationOutput(ctx, task, stageRun)

	var resumeSessionID string
	if opts != nil {
		resumeSessionID = opts.ResumeSessionID
	}
	// Derive the resume session from DB when not supplied by the caller (e.g. picker-driven spawn).
	if resumeSessionID == "" && handler.RequiresAgent() {
		resumeSessionID = o.resolveResumeSessionID(ctx, task)
	}

	var userAdditionalPrompt string
	if opts != nil {
		userAdditionalPrompt = opts.UserAdditionalPrompt
	}
	// Fall back to the prompt persisted on the run itself (set by RequeueForUser).
	if userAdditionalPrompt == "" && stageRun.PendingUserPrompt != nil {
		userAdditionalPrompt = *stageRun.PendingUserPrompt
	}

	var additionalDirs []string
	if o.opts.ResolveAdditionalDirs != nil {
		additionalDirs = o.opts.ResolveAdditionalDirs(ctx, task)
	}

	stageCtx := &StageContext{
		Ctx:                   ctx,
		Task:                  task,
		StageRun:              stageRun,
		Permissions:           perms,
		AllStageRuns:          allRuns,
		PreviousOutput:        prevOutput,
		PriorIterationOutput:  priorIterOutput,
		ResumeSessionID:       resumeSessionID,
		UserAdditionalPrompt:  userAdditionalPrompt,
		MCPToken:              o.opts.MCPToken,
		MCPUrl:                o.opts.MCPUrl,
		AllowGitPush:          o.opts.AllowGitPush,
		AdditionalDirs:        additionalDirs,
		SystemPromptRepo:      o.opts.SystemPromptRepo,
		ResolveSpawner:        o.opts.ResolveSpawner,
		StageModelFn:          o.modelResolver.StageDefault,
		InjectMemory:          o.opts.InjectMemory,
		MemoryBudget:          o.opts.MemoryBudget,
		RecordMemoryInjection: o.opts.RecordMemoryInjection,
		AuthorizeMemory:       o.opts.AuthorizeMemory,
		IssueTaskAPIKey:       o.opts.IssueTaskAPIKey,
		ResolveApplications:   o.opts.ResolveApplications,
		RegisterSpawnCleanup:  o.spawnCleanups.register,
		DispatchHTTPSpawn: func(stageRunID, taskID string, spawn func(context.Context) (string, error)) {
			// context.WithoutCancel keeps values (logger, trace ids) but drops the
			// HTTP request's cancellation, which fires the instant the handler
			// returns. context.AfterFunc re-attaches orchestrator-shutdown cancellation.
			detached := context.WithoutCancel(ctx)
			go func() {
				base := o.baseContext()
				// A parked acquire abandons the wait when base ends first — no
				// slot was taken, so spawn must not run and release must not
				// be deferred. The stage_run stays "running"; recoverRunningStageRuns
				// picks it up on the next start.
				if !o.httpPool.acquire(base) {
					return
				}
				defer o.httpPool.release()
				spawnCtx, cancel := context.WithCancel(detached)
				defer cancel()
				defer context.AfterFunc(base, cancel)()
				sessionFile, err := spawn(spawnCtx)
				// Abandon the send once the orchestrator is shutting down: at that
				// point Run's select loop has already returned and nothing drains
				// httpResultCh, so a blocking send here would leak this goroutine
				// and hold its httpPool slot forever.
				select {
				case o.httpResultCh <- httpSpawnResult{
					stageRunID:  stageRunID,
					taskID:      taskID,
					sessionFile: sessionFile,
					err:         err,
				}:
				case <-base.Done():
				}
			}()
		},
		RecordAudit: func(action string, details map[string]any) {
			_ = o.opts.AuditRepo.RecordTaskAudit(ctx, task.ID, nil, action, "task:"+task.ID, details)
		},
		RequestPermission: func(tool, pattern, reason string) *ent.PermissionRequest {
			return o.filePermissionRequest(ctx, task, stageRun.ID, tool, pattern, reason)
		},
		PermissionStatus: func(pollCtx context.Context, id string) (*ent.PermissionRequest, error) {
			// pollCtx, not the enclosing request ctx: an ACP gate polls while the
			// spawn it belongs to outlives the HTTP response.
			return o.opts.PermissionRepo.GetPermissionRequest(pollCtx, id)
		},
	}

	transition, execErr := handler.Execute(stageCtx)
	if execErr != nil {
		transition = FailTransition{Reason: execErr.Error()}
	}

	return o.applyTransition(ctx, task, stageRun, transition)
}

// getTaskMutex returns (or creates) the per-task mutex, ensuring serialized
// ProgressTask calls for the same task ID.
func (o *PipelineOrchestrator) getTaskMutex(taskID string) *sync.Mutex {
	mu, _ := o.taskLocks.LoadOrStore(taskID, &sync.Mutex{})
	return mu.(*sync.Mutex)
}

// sweepApplyTransition applies a transition from within a sweep, guarded by a
// TryLock on the per-task mutex. Returns (nil, false, nil) when the lock is
// already held — progressTask is actively processing the task, so the run is
// not truly an orphan; the sweep defers to the next tick.
func (o *PipelineOrchestrator) sweepApplyTransition(ctx context.Context, task *ent.Task, run *ent.StageRun, t StageTransition) (*ent.StageRun, bool, error) {
	mu := o.getTaskMutex(task.ID)
	if !mu.TryLock() {
		return nil, false, nil
	}
	defer mu.Unlock()
	result, err := o.applyTransition(ctx, task, run, t)
	return result, true, err
}

// sweepMarkPending re-queues a stage_run under the task mutex. It mirrors
// sweepApplyTransition's TryLock contract — never block the tick, defer to
// progressTask instead — and reports locked=false when the task is busy.
// The status is re-read inside the lock: the caller's check ran before the
// TryLock, and a handler holding the mutex in that window can have moved the
// run to a terminal status, which this must not resurrect to pending.
func (o *PipelineOrchestrator) sweepMarkPending(ctx context.Context, taskID, runID string) (bool, error) {
	mu := o.getTaskMutex(taskID)
	if !mu.TryLock() {
		return false, nil
	}
	defer mu.Unlock()
	fresh, err := o.opts.StageRunRepo.GetByID(ctx, runID)
	if err != nil || fresh == nil || fresh.Status != "running" {
		return true, err
	}
	_, err = o.stageRuns.MarkPending(ctx, runID)
	return true, err
}

// filePermissionRequest writes the row an agent's permission request needs and,
// for a task whose autonomy answers by itself, resolves it on the spot. The ACP
// gate polls the row rather than reading a return value, so an allow-all task
// gets a written answer instead of a skipped request. Application tools are
// excluded for the same reason the HTTP path excludes them: their own grant
// governs. Returns nil only when the row could not be written.
func (o *PipelineOrchestrator) filePermissionRequest(
	ctx context.Context, task *ent.Task, stageRunID, tool, pattern, reason string,
) *ent.PermissionRequest {
	pat := (*string)(nil)
	if pattern != "" {
		pat = &pattern
	}
	rsn := (*string)(nil)
	if reason != "" {
		rsn = &reason
	}
	req, err := o.opts.PermissionRepo.CreatePermissionRequest(ctx, repo.CreatePermissionRequestInput{
		StageRunID: stageRunID,
		Tool:       tool,
		Pattern:    pat,
		Reason:     rsn,
	})
	if err != nil {
		slog.Error("orchestrator: CreatePermissionRequest failed", "err", err)
		return nil
	}
	if taskcontrol.IsAllowAll(task.Autonomy) && !mcpapps.IsApplicationTool(tool) {
		if resolveErr := o.opts.PermissionRepo.ResolvePermissionRequest(ctx, req.ID, repo.OutcomeGranted); resolveErr != nil {
			slog.Warn("orchestrator: auto-approve failed", "reqID", req.ID, "err", resolveErr)
		} else {
			_ = o.opts.AuditRepo.RecordTaskAudit(ctx, task.ID, nil, "permission_auto_approved", "task:"+task.ID, map[string]any{
				"requestId": req.ID,
				"tool":      tool,
				"autonomy":  task.Autonomy,
			})
		}
	}
	if o.opts.OnPermissionRequest != nil {
		o.opts.OnPermissionRequest(task.ID, req)
	}
	return req
}
