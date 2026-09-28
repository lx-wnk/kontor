package tools

import (
	"context"

	tasksapi "github.com/lx-wnk/kontor/server/internal/api/tasks"
	"github.com/lx-wnk/kontor/server/internal/db/ent"
	"github.com/lx-wnk/kontor/server/internal/db/repo"
	mcp "github.com/lx-wnk/kontor/server/internal/mcp"
	"github.com/lx-wnk/kontor/server/internal/permissions"
	"github.com/lx-wnk/kontor/server/internal/pipeline"
)

// ControlOrchestrator is the orchestrator interface needed by control tools.
// It is a superset of the pipeline operations so that a real orchestrator
// satisfies both this interface and tasks.OrchestratorIface.
type ControlOrchestrator interface {
	ProgressTask(ctx context.Context, taskID string, opts *pipeline.ProgressOpts) (*ent.StageRun, error)
	ResumeFromUser(ctx context.Context, taskID, userPrompt string) (*ent.StageRun, error)
	RequeueForUser(ctx context.Context, taskID, userPrompt string) (*ent.StageRun, error)
	NotifyTaskTerminated(ctx context.Context, taskID, stage string)
	InvalidateConfigCache()
	ClearStalePendingPermissions(ctx context.Context, taskID string)
	EffectiveStageModel(ctx context.Context, stage string) string
}

// ControlDeps holds dependencies required by the pipeline control tools.
type ControlDeps struct {
	TaskRepo     repo.TaskRepo
	SRRepo       repo.StageRunRepo
	PermRepo     repo.PermissionRepo
	AuditRepo    repo.AuditEventRepo
	Orchestrator ControlOrchestrator         // may be nil in tests
	RefineReader tasksapi.RefineStatusReader // may be nil; lets advance_task see refine status
	Broadcast    func(ctx context.Context, eventType, taskID string)
}

// RegisterControlTools registers all control tools into the given registry.
func RegisterControlTools(registry mcp.ToolRegistry, d ControlDeps) {
	registerAdvanceTask(registry, d)
	registerHoldTask(registry, d)
	registerResumeTask(registry, d)
	// NOTE: progress_task is kept for compatibility; advance_task is the preferred entry point.
	registerProgressTask(registry, d)
	registerCancelTask(registry, d)
	registerRetryTask(registry, d)
	registerGrantPermission(registry, d)
	registerResolvePermissionRequest(registry, d)
	registerApproveAllPending(registry, d)
}

func registerAdvanceTask(registry mcp.ToolRegistry, d ControlDeps) {
	registry.Register(&mcp.ToolDef{
		Name:        "advance_task",
		Description: "Execute the primary action for a task (retry, approve permissions, progress, or resume). Spec approval is intentionally excluded — use approve_spec for that.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"id": map[string]any{"type": "string", "description": "Task ID"},
			},
			"required": []string{"id"},
		},
		Handler: func(ctx context.Context, args map[string]any) (*mcp.ToolResult, error) {
			id, err := mcp.StringArg(args, "id")
			if err != nil {
				return nil, err
			}
			res, err := tasksapi.Advance(ctx, tasksapi.AdvanceDeps{
				TaskRepo:     d.TaskRepo,
				SRRepo:       d.SRRepo,
				PermRepo:     d.PermRepo,
				AuditRepo:    d.AuditRepo,
				Orchestrator: d.Orchestrator,
				RefineReader: d.RefineReader,
			}, id)
			if err != nil {
				return nil, mcp.Fail("advance_task: " + err.Error())
			}
			if d.AuditRepo != nil {
				_ = d.AuditRepo.RecordTaskAudit(ctx, id, nil, "task_advanced", "task:"+id, map[string]any{"actor": "mcp", "primary": res.Primary, "dispatched": res.Dispatched})
			}
			safeBroadcast(d.Broadcast, ctx, "task_updated", id)
			return mcp.OK(res)
		},
	})
}

func registerHoldTask(registry mcp.ToolRegistry, d ControlDeps) {
	registry.Register(&mcp.ToolDef{
		Name:        "hold_task",
		Description: "Park a task at on_hold without terminating it. Use resume_task to unpark.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"id": map[string]any{"type": "string", "description": "Task ID"},
			},
			"required": []string{"id"},
		},
		Handler: func(ctx context.Context, args map[string]any) (*mcp.ToolResult, error) {
			id, err := mcp.StringArg(args, "id")
			if err != nil {
				return nil, err
			}
			task, err := d.TaskRepo.GetByID(ctx, id)
			if err != nil {
				return nil, mcp.Fail("Task not found: " + id)
			}
			if task.CurrentStage == "done" || task.CurrentStage == "cancelled" || task.CurrentStage == "on_hold" {
				return nil, mcp.Fail("Task is already " + task.CurrentStage)
			}
			onHold := "on_hold"
			updated, err := d.TaskRepo.Update(ctx, id, repo.UpdateTaskInput{CurrentStage: &onHold})
			if err != nil {
				return nil, mcp.Fail("hold_task: " + err.Error())
			}
			if d.AuditRepo != nil {
				_ = d.AuditRepo.RecordTaskAudit(ctx, id, nil, "task_held", "task:"+id, map[string]any{"actor": "mcp", "fromStage": task.CurrentStage})
			}
			safeBroadcast(d.Broadcast, ctx, "task_updated", id)
			return mcp.OK(map[string]any{"task": updated})
		},
	})
}

func registerResumeTask(registry mcp.ToolRegistry, d ControlDeps) {
	registry.Register(&mcp.ToolDef{
		Name:        "resume_task",
		Description: "Resume an on_hold or awaiting_user task by calling ResumeFromUser on the orchestrator.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"id":                map[string]any{"type": "string", "description": "Task ID"},
				"additional_prompt": map[string]any{"type": "string", "description": "Optional instruction carried into the resumed run"},
			},
			"required": []string{"id"},
		},
		Handler: func(ctx context.Context, args map[string]any) (*mcp.ToolResult, error) {
			id, err := mcp.StringArg(args, "id")
			if err != nil {
				return nil, err
			}
			additionalPrompt, _ := args["additional_prompt"].(string)
			task, err := d.TaskRepo.GetByID(ctx, id)
			if err != nil {
				return nil, mcp.Fail("Task not found: " + id)
			}
			// When on_hold: move back to implementation before re-queuing.
			if task.CurrentStage == "on_hold" {
				impl := "implementation"
				if _, err := d.TaskRepo.Update(ctx, id, repo.UpdateTaskInput{CurrentStage: &impl}); err != nil {
					return nil, mcp.Fail("resume_task: unstage failed: " + err.Error())
				}
			}
			if d.Orchestrator == nil {
				return nil, mcp.Fail("orchestrator not available")
			}
			sr, err := d.Orchestrator.ResumeFromUser(ctx, id, additionalPrompt)
			if err != nil {
				return nil, mcp.Fail("resume_task: " + err.Error())
			}
			if sr == nil {
				return nil, mcp.Fail("Task cannot be resumed (terminal or missing)")
			}
			if d.AuditRepo != nil {
				_ = d.AuditRepo.RecordTaskAudit(ctx, id, nil, "task_resumed", "task:"+id, map[string]any{"actor": "mcp", "hadPrompt": additionalPrompt != ""})
			}
			safeBroadcast(d.Broadcast, ctx, "task_updated", id)
			task, _ = d.TaskRepo.GetByID(ctx, id)
			return mcp.OK(map[string]any{"task": task, "stageRun": sr})
		},
	})
}

func registerProgressTask(registry mcp.ToolRegistry, d ControlDeps) {
	registry.Register(&mcp.ToolDef{
		Name:        "progress_task",
		Description: "Advance a task to the next stage or trigger its current stage handler.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"id": map[string]any{"type": "string", "description": "Task ID"},
			},
			"required": []string{"id"},
		},
		Handler: func(ctx context.Context, args map[string]any) (*mcp.ToolResult, error) {
			id, err := mcp.StringArg(args, "id")
			if err != nil {
				return nil, err
			}
			if _, err := d.TaskRepo.GetByID(ctx, id); err != nil {
				return nil, mcp.Fail("Task not found: " + id)
			}

			if d.Orchestrator == nil {
				return nil, mcp.Fail("orchestrator not available")
			}
			stageRun, err := d.Orchestrator.ProgressTask(ctx, id, nil)
			if err != nil {
				return nil, mcp.Fail("progress_task: " + err.Error())
			}
			if stageRun == nil {
				return nil, mcp.Fail("Task cannot progress (terminal, not found, or no free runner slot)")
			}
			safeBroadcast(d.Broadcast, ctx, "task_updated", id)
			// Refresh task after progression; ignore error — stale data is better than an error on success.
			task, _ := d.TaskRepo.GetByID(ctx, id)
			return mcp.OK(map[string]any{"task": task, "stageRun": stageRun})
		},
	})
}

func registerCancelTask(registry mcp.ToolRegistry, d ControlDeps) {
	registry.Register(&mcp.ToolDef{
		Name:        "cancel_task",
		Description: "Cancel a task, preventing further stage progression.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"id": map[string]any{"type": "string", "description": "Task ID"},
			},
			"required": []string{"id"},
		},
		Handler: func(ctx context.Context, args map[string]any) (*mcp.ToolResult, error) {
			id, err := mcp.StringArg(args, "id")
			if err != nil {
				return nil, err
			}
			task, err := d.TaskRepo.GetByID(ctx, id)
			if err != nil {
				return nil, mcp.Fail("Task not found: " + id)
			}
			if task.CurrentStage == "done" || task.CurrentStage == "cancelled" {
				return nil, mcp.Fail("Task is already " + task.CurrentStage)
			}

			cancelled := "cancelled"
			if _, err := d.TaskRepo.Update(ctx, id, repo.UpdateTaskInput{CurrentStage: &cancelled}); err != nil {
				return nil, mcp.Fail("cancel_task: " + err.Error())
			}
			_ = d.AuditRepo.RecordTaskAudit(ctx, id, nil, "cancelled", "task:"+id, map[string]any{"actor": "user"})
			if d.Orchestrator != nil {
				d.Orchestrator.NotifyTaskTerminated(ctx, id, "cancelled")
			}
			safeBroadcast(d.Broadcast, ctx, "task_updated", id)

			// Refresh task after cancel; ignore error — stale data is better than an error on success.
			task, _ = d.TaskRepo.GetByID(ctx, id)
			return mcp.OK(map[string]any{"task": task})
		},
	})
}

func registerRetryTask(registry mcp.ToolRegistry, d ControlDeps) {
	registry.Register(&mcp.ToolDef{
		Name:        "retry_task",
		Description: "Retry a task that has a failed stage run on its current stage.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"id": map[string]any{"type": "string", "description": "Task ID"},
			},
			"required": []string{"id"},
		},
		Handler: func(ctx context.Context, args map[string]any) (*mcp.ToolResult, error) {
			id, err := mcp.StringArg(args, "id")
			if err != nil {
				return nil, err
			}
			task, err := d.TaskRepo.GetByID(ctx, id)
			if err != nil {
				return nil, mcp.Fail("Task not found: " + id)
			}

			// Get the latest stage run for the task's current stage.
			latest, err := d.SRRepo.GetLatestByTaskAndStage(ctx, id, task.CurrentStage)
			if err != nil {
				return nil, mcp.Fail("retry_task: could not fetch stage run: " + err.Error())
			}
			if latest == nil || latest.Status != "failed" {
				return nil, mcp.Fail("Task has no failed stage run to retry on its current stage")
			}

			_ = d.AuditRepo.RecordTaskAudit(ctx, id, nil, "retry_requested", "task:"+id, map[string]any{
				"actor":     "user",
				"stage":     latest.Stage,
				"iteration": latest.Iteration,
			})

			if d.Orchestrator == nil {
				return nil, mcp.Fail("orchestrator not available")
			}
			stageRun, err := d.Orchestrator.RequeueForUser(ctx, id, "")
			if err != nil {
				return nil, mcp.Fail("retry_task: " + err.Error())
			}
			if stageRun == nil {
				return nil, mcp.Fail("Task could not be re-queued (terminal, missing, or no failed/requeued run)")
			}
			safeBroadcast(d.Broadcast, ctx, "task_updated", id)
			// Refresh task after re-queue; ignore error — stale data is better than an error on success.
			task, _ = d.TaskRepo.GetByID(ctx, id)
			return mcp.OK(map[string]any{"task": task, "stageRun": stageRun})
		},
	})
}

// decidedByFromCtx resolves the calling API key's identity for the
// decided_by column. Empty when no MCP auth is attached to ctx (e.g.
// unauthenticated stdio bridges) — an unknown decider is left unset rather
// than written as a placeholder.
func decidedByFromCtx(ctx context.Context) string {
	if info := mcp.AuthFromContext(ctx); info != nil {
		return info.KeyID
	}
	return ""
}

func registerGrantPermission(registry mcp.ToolRegistry, d ControlDeps) {
	registry.Register(&mcp.ToolDef{
		Name:        "grant_permission",
		Description: "Grant a tool permission to a task.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"task_id": map[string]any{"type": "string", "description": "Task ID"},
				"tool":    map[string]any{"type": "string", "description": "Tool name from the pipeline allow-list"},
				"pattern": map[string]any{"type": "string", "description": "Optional tool argument pattern (max 256 chars)"},
			},
			"required": []string{"task_id", "tool"},
		},
		Handler: func(ctx context.Context, args map[string]any) (*mcp.ToolResult, error) {
			taskID, err := mcp.StringArg(args, "task_id")
			if err != nil {
				return nil, err
			}
			tool, err := mcp.StringArg(args, "tool")
			if err != nil {
				return nil, err
			}

			if _, err := d.TaskRepo.GetByID(ctx, taskID); err != nil {
				return nil, mcp.Fail("Task not found: " + taskID)
			}
			if !permissions.IsAllowedTool(tool) {
				return nil, mcp.Fail("tool not in allow-list: " + tool)
			}

			in := repo.CreateTaskPermissionInput{
				TaskID:    taskID,
				Tool:      tool,
				Granted:   true,
				DecidedBy: decidedByFromCtx(ctx),
			}
			if p := mcp.OptionalString(args, "pattern"); p != "" {
				in.Pattern = &p
			}
			perm, err := d.PermRepo.CreateTaskPermission(ctx, in)
			if err != nil {
				return nil, mcp.Fail("grant_permission: " + err.Error())
			}
			safeBroadcast(d.Broadcast, ctx, "task_updated", taskID)
			return mcp.OK(perm)
		},
	})
}

func registerResolvePermissionRequest(registry mcp.ToolRegistry, d ControlDeps) {
	registry.Register(&mcp.ToolDef{
		Name:        "resolve_permission_request",
		Description: "Resolve a pending permission request as granted or denied.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"request_id": map[string]any{"type": "string", "description": "Permission request ID"},
				"outcome":    map[string]any{"type": "string", "enum": []string{repo.OutcomeGranted, repo.OutcomeDenied}},
			},
			"required": []string{"request_id", "outcome"},
		},
		Handler: func(ctx context.Context, args map[string]any) (*mcp.ToolResult, error) {
			requestID, err := mcp.StringArg(args, "request_id")
			if err != nil {
				return nil, err
			}
			outcome, err := mcp.StringArg(args, "outcome")
			if err != nil {
				return nil, err
			}
			if outcome != repo.OutcomeGranted && outcome != repo.OutcomeDenied {
				return nil, mcp.Fail("outcome must be 'granted' or 'denied'")
			}

			req, err := d.PermRepo.GetPermissionRequest(ctx, requestID)
			if err != nil {
				return nil, mcp.Fail("Permission request not found: " + requestID)
			}

			if err := d.PermRepo.ResolvePermissionRequest(ctx, requestID, outcome); err != nil {
				return nil, mcp.Fail("resolve_permission_request: " + err.Error())
			}

			// If granted, create a task permission for the associated task.
			var resumed bool
			run, runErr := d.SRRepo.GetByID(ctx, req.StageRunID)
			if runErr == nil && run != nil {
				// The request is resolved even when the grant or resume below fails.
				defer safeBroadcast(d.Broadcast, ctx, "task_updated", run.TaskID)
				if outcome == repo.OutcomeGranted {
					in := repo.CreateTaskPermissionInput{
						TaskID:         run.TaskID,
						Tool:           req.Tool,
						Granted:        true,
						ManualOverride: false, // agent/MCP path never gets override
						DecidedBy:      decidedByFromCtx(ctx),
					}
					if req.Pattern != nil {
						in.Pattern = req.Pattern
					}
					if _, permErr := d.PermRepo.CreateTaskPermission(ctx, in); permErr != nil {
						return mcp.OK(map[string]any{
							"resolved": req,
							"resumed":  false,
							"warning":  "permission grant recorded but CreateTaskPermission failed: " + permErr.Error(),
						})
					}
				}
				if d.Orchestrator != nil {
					if _, resumeErr := d.Orchestrator.ResumeFromUser(ctx, run.TaskID, ""); resumeErr != nil {
						// Resume failed — surface as warning so caller knows agent was not re-signalled.
						return mcp.OK(map[string]any{
							"resolved": req,
							"resumed":  false,
							"warning":  "ResumeFromUser failed: " + resumeErr.Error(),
						})
					}
					resumed = true
				}
			} else if outcome == repo.OutcomeGranted {
				// Stage run not found — log via result warning, agent not signalled.
				return mcp.OK(map[string]any{
					"resolved": req,
					"resumed":  false,
					"warning":  "Stage run not found; agent not signalled",
				})
			}

			return mcp.OK(map[string]any{"resolved": req, "resumed": resumed})
		},
	})
}

func registerApproveAllPending(registry mcp.ToolRegistry, d ControlDeps) {
	registry.Register(&mcp.ToolDef{
		Name:        "approve_all_pending",
		Description: "Approve all pending permission requests for a task in one call, then re-queue it when awaiting user input.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"task_id": map[string]any{"type": "string", "description": "Task ID"},
			},
			"required": []string{"task_id"},
		},
		Handler: func(ctx context.Context, args map[string]any) (*mcp.ToolResult, error) {
			taskID, err := mcp.StringArg(args, "task_id")
			if err != nil {
				return nil, err
			}

			var requeuer tasksapi.ApproveAllRequeuer
			if d.Orchestrator != nil {
				requeuer = d.Orchestrator
			}
			res, err := tasksapi.ApproveAllPending(ctx, tasksapi.ApproveAllPendingDeps{
				TaskRepo:     d.TaskRepo,
				SRRepo:       d.SRRepo,
				PermRepo:     d.PermRepo,
				AuditRepo:    d.AuditRepo,
				Orchestrator: requeuer,
			}, taskID)
			if err != nil {
				return nil, mcp.Fail(err.Error())
			}

			safeBroadcast(d.Broadcast, ctx, "task_updated", taskID)
			return mcp.OK(res)
		},
	})
}
