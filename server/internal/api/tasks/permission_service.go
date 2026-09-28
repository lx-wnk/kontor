package tasks

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/lx-wnk/kontor/server/internal/auth"
	"github.com/lx-wnk/kontor/server/internal/db/ent"
	"github.com/lx-wnk/kontor/server/internal/db/repo"
	"github.com/lx-wnk/kontor/server/internal/permissions"
)

// ApproveAllRequeuer re-queues a task that is awaiting user input. Implemented
// by the pipeline orchestrator; the caller may leave it nil.
type ApproveAllRequeuer interface {
	RequeueForUser(ctx context.Context, taskID, userPrompt string) (*ent.StageRun, error)
}

// ApproveAllPendingDeps is the repo subset needed to approve every pending
// permission request for a task and re-queue it. Shared by the REST handler and
// the MCP approve_all_pending tool so both transports grant persistent overrides
// and re-queue identically.
type ApproveAllPendingDeps struct {
	TaskRepo     repo.TaskRepo
	SRRepo       repo.StageRunRepo
	PermRepo     repo.PermissionRepo
	AuditRepo    repo.AuditEventRepo
	Orchestrator ApproveAllRequeuer
}

// ApproveAllPendingResult reports what the operation did.
type ApproveAllPendingResult struct {
	Approved int  `json:"approved"`
	Requeued bool `json:"requeued"`
}

// ApproveAllPending resolves every pending permission_request for a task as
// granted, persists each as a manual-override grant so a re-queued agent's
// repeated request is auto-satisfied, then re-queues the task when its latest
// stage run is awaiting_user. Re-queue is skipped when nothing was pending.
func ApproveAllPending(ctx context.Context, d ApproveAllPendingDeps, taskID string) (ApproveAllPendingResult, error) {
	var res ApproveAllPendingResult

	task, err := d.TaskRepo.GetByID(ctx, taskID)
	if err != nil {
		return res, fmt.Errorf("approve_all_pending: task not found: %w", err)
	}

	runs, err := d.SRRepo.ListForTask(ctx, taskID)
	if err != nil {
		return res, fmt.Errorf("approve_all_pending: list runs: %w", err)
	}
	runIDs := make([]string, len(runs))
	for i, sr := range runs {
		runIDs[i] = sr.ID
	}

	pending, err := d.PermRepo.ListPendingForTask(ctx, taskID, runIDs)
	if err != nil {
		return res, fmt.Errorf("approve_all_pending: list pending: %w", err)
	}

	for _, req := range pending {
		if err := d.PermRepo.ResolvePermissionRequest(ctx, req.ID, repo.OutcomeGranted); err != nil {
			return res, fmt.Errorf("approve_all_pending: resolve %s: %w", req.ID, err)
		}
	}

	// ctx carries a real JWT payload only on the REST leg (r.Context()); the
	// MCP leg's ctx never has one, so this correctly resolves empty there
	// rather than misattributing an MCP-authenticated grant to a REST identity.
	var decidedBy string
	if payload, ok := auth.PayloadFromContext(ctx); ok {
		decidedBy = payload.Sub
	}

	entries := make([]repo.GrantEntry, 0, len(pending))
	for _, req := range pending {
		entries = append(entries, repo.GrantEntry{Tool: req.Tool, Pattern: req.Pattern, DecidedBy: decidedBy})
	}
	if len(entries) > 0 {
		if _, errs := grantOverrideEntries(ctx, d.PermRepo, taskID, entries); len(errs) > 0 {
			slog.Warn("approve_all_pending: grant failed", "taskID", taskID, "errs", errs)
		}
	}

	_ = d.AuditRepo.RecordTaskAudit(ctx, taskID, nil, "permissions_bulk_approved", "task:"+taskID, map[string]any{
		"actor": "user",
		"count": len(pending),
	})

	// Re-queue only when we actually cleared a pending request and the latest
	// stage run is awaiting_user — nothing to unblock otherwise.
	if d.Orchestrator != nil && len(pending) > 0 {
		latest, srErr := d.SRRepo.GetLatestByTaskAndStage(ctx, taskID, task.CurrentStage)
		if srErr == nil && latest != nil && latest.Status == "awaiting_user" {
			if sr, reqErr := d.Orchestrator.RequeueForUser(ctx, taskID, ""); reqErr == nil && sr != nil {
				res.Requeued = true
			}
		}
	}

	res.Approved = len(pending)
	return res, nil
}

// grantOverrideEntries validates each entry as a human override and persists the
// safe ones via BulkGrantPermissions. override=true bypasses the Bash allow-list
// and injection guard but still requires a non-empty pattern (and a WebFetch
// domain). Invalid entries are collected and returned rather than aborting.
func grantOverrideEntries(ctx context.Context, permRepo repo.PermissionRepo, taskID string, entries []repo.GrantEntry) ([]*ent.TaskPermission, []string) {
	var safe []repo.GrantEntry
	var errs []string
	for _, e := range entries {
		pattern := ""
		if e.Pattern != nil {
			pattern = *e.Pattern
		}
		if err := permissions.ValidateGrantEntryWithOverride(e.Tool, pattern, true); err != nil {
			errs = append(errs, fmt.Sprintf("grant skipped (%s %s): %v", e.Tool, pattern, err))
			continue
		}
		e.ManualOverride = true
		safe = append(safe, e)
	}
	if len(safe) == 0 {
		return nil, errs
	}
	granted, err := permRepo.BulkGrantPermissions(ctx, taskID, safe)
	if err != nil {
		errs = append(errs, fmt.Sprintf("BulkGrantPermissions: %v", err))
		return nil, errs
	}
	tools := make([]string, 0, len(granted))
	for _, p := range granted {
		tools = append(tools, p.Tool)
	}
	slog.Info("permission grant from resolved request", "taskID", taskID, "tools", tools)
	return granted, errs
}
