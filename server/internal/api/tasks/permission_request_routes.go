package tasks

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/lx-wnk/kontor/server/internal/apierr"
	"github.com/lx-wnk/kontor/server/internal/auth"
	"github.com/lx-wnk/kontor/server/internal/capability"
	"github.com/lx-wnk/kontor/server/internal/db/ent"
	"github.com/lx-wnk/kontor/server/internal/db/repo"
	"github.com/lx-wnk/kontor/server/internal/mcpapps"
	"github.com/lx-wnk/kontor/server/internal/permissions"
	"github.com/lx-wnk/kontor/server/internal/taskcontrol"
)

// permissionRequestResponse is the API response shape for a single pending permission request.
// It extends the DB row with computed fields that are not stored in the database.
type permissionRequestResponse struct {
	ID          string  `json:"id"`
	StageRunID  string  `json:"stageRunId"`
	Tool        string  `json:"tool"`
	Pattern     *string `json:"pattern"`
	Reason      *string `json:"reason"`
	Outcome     *string `json:"outcome"`
	RequestedAt string  `json:"requestedAt"`
	ResolvedAt  *string `json:"resolvedAt"`
	// OutsideSafeList is true when the request is for a Bash command not in the
	// safe allow-list, meaning a Grant is a conscious human override.
	OutsideSafeList bool `json:"outsideSafeList"`
	// DeniedByDefault is true when a grant already denies this capability in
	// this task's contexts — the same resolution the run's allow list uses. An
	// answer here would not reach the agent; the grant has to change instead.
	DeniedByDefault bool `json:"deniedByDefault"`
}

func toPermissionRequestResponse(req *ent.PermissionRequest) permissionRequestResponse {
	r := permissionRequestResponse{
		ID:          req.ID,
		StageRunID:  req.StageRunID,
		Tool:        req.Tool,
		Pattern:     req.Pattern,
		Reason:      req.Reason,
		Outcome:     req.Outcome,
		RequestedAt: req.RequestedAt.Format("2006-01-02T15:04:05Z07:00"),
	}
	if req.ResolvedAt != nil {
		s := req.ResolvedAt.Format("2006-01-02T15:04:05Z07:00")
		r.ResolvedAt = &s
	}
	if req.Tool == "Bash" && req.Pattern != nil {
		normalized := strings.Join(strings.Fields(*req.Pattern), " ")
		if normalized != "" {
			ok, _ := permissions.IsSafeBashPattern(normalized)
			r.OutsideSafeList = !ok
		}
	}
	return r
}

// listPermissionRequests returns pending permission requests across all stage_runs for a task.
func (h *Handler) listPermissionRequests(w http.ResponseWriter, r *http.Request) error {
	taskID := chi.URLParam(r, "id")
	runs, err := h.srRepo.ListForTask(r.Context(), taskID)
	if err != nil {
		return fmt.Errorf("permission_requests.list: %w", err)
	}
	if len(runs) == 0 {
		return jsonReply(w, http.StatusOK, []permissionRequestResponse{})
	}
	ids := make([]string, len(runs))
	for i, sr := range runs {
		ids[i] = sr.ID
	}
	reqs, err := h.permRepo.ListPendingForTask(r.Context(), taskID, ids)
	if err != nil {
		return fmt.Errorf("permission_requests.list: %w", err)
	}
	resp := make([]permissionRequestResponse, len(reqs))
	denied := h.deniedByDefault(r.Context(), taskID, reqs)
	for i, req := range reqs {
		resp[i] = toPermissionRequestResponse(req)
		resp[i].DeniedByDefault = denied[req.ID]
	}
	return jsonReply(w, http.StatusOK, resp)
}

// createPermissionRequest creates a single permission request and flips the stage_run to awaiting_user if running.
// For allow-all tasks (autonomy spec_gated or full), the request is created already approved.
func (h *Handler) createPermissionRequest(w http.ResponseWriter, r *http.Request) error {
	var body struct {
		StageRunID string  `json:"stageRunId"`
		Tool       string  `json:"tool"`
		Pattern    *string `json:"pattern"`
		Reason     *string `json:"reason"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		return apierr.NewAppError(http.StatusBadRequest, "invalid JSON body")
	}
	if body.StageRunID == "" || body.Tool == "" {
		return apierr.NewAppError(http.StatusBadRequest, "stageRunId and tool are required")
	}

	req, err := h.permRepo.CreatePermissionRequest(r.Context(), repo.CreatePermissionRequestInput{
		StageRunID: body.StageRunID,
		Tool:       body.Tool,
		Pattern:    body.Pattern,
		Reason:     body.Reason,
	})
	if err != nil {
		return fmt.Errorf("permission_request.create: %w", err)
	}

	// Resolve the owning task to check autonomy level.
	sr, srErr := h.srRepo.GetByID(r.Context(), body.StageRunID)
	if srErr == nil {
		task, taskErr := h.taskRepo.GetByID(r.Context(), sr.TaskID)
		if taskErr == nil && taskcontrol.IsAllowAll(task.Autonomy) && !mcpapps.IsApplicationTool(body.Tool) {
			// Auto-approve: task operates in allow-all mode — no human gating needed.
			if resolveErr := h.permRepo.ResolvePermissionRequest(r.Context(), req.ID, repo.OutcomeGranted); resolveErr != nil {
				slog.Warn("createPermissionRequest: auto-approve failed", "reqID", req.ID, "err", resolveErr)
			} else {
				_ = h.auditRepo.RecordTaskAudit(r.Context(), sr.TaskID, nil, "permission_auto_approved", "task:"+sr.TaskID, map[string]any{
					"requestId": req.ID,
					"tool":      body.Tool,
					"autonomy":  task.Autonomy,
				})
				// Re-fetch to return the resolved row.
				if resolved, fetchErr := h.permRepo.GetPermissionRequest(r.Context(), req.ID); fetchErr == nil {
					req = resolved
				}
			}
			h.BroadcastEnrichedEvent(r.Context(), "permission_request", sr.TaskID)
			return jsonReply(w, http.StatusCreated, toPermissionRequestResponse(req))
		}

		// Gated path: flip stage_run to awaiting_user if it is currently running.
		if sr.Status == "running" {
			awaitingUser := "awaiting_user"
			if _, err2 := h.srRepo.Update(r.Context(), body.StageRunID, repo.UpdateStageRunInput{Status: &awaitingUser}); err2 != nil {
				slog.Warn("createPermissionRequest: flip to awaiting_user failed", "stageRunID", body.StageRunID, "err", err2)
			}
			h.BroadcastEnrichedEvent(r.Context(), "permission_request", sr.TaskID)
		}
		if req.Outcome == nil && h.notifier != nil && h.approvalPushWanted(r.Context()) {
			title := ""
			if taskErr == nil {
				title = task.Title
			}
			h.notifier.PermissionRequested(r.Context(), sr.TaskID, title, body.Tool)
		}
	} else {
		// Stage-run lookup failed: the request was created but can be neither
		// auto-approved nor surfaced via the awaiting_user gate. Log so a dropped
		// gating is observable rather than silent.
		slog.Warn("createPermissionRequest: stage-run lookup failed; request not gated", "stageRunID", body.StageRunID, "reqID", req.ID, "err", srErr)
	}

	return jsonReply(w, http.StatusCreated, toPermissionRequestResponse(req))
}

// bulkGrantPermissions handles POST /api/tasks/{id}/permissions/bulk.
func (h *Handler) bulkGrantPermissions(w http.ResponseWriter, r *http.Request) error {
	taskID := chi.URLParam(r, "id")
	var body struct {
		Template    *string `json:"template"`
		Permissions []struct {
			Tool      string  `json:"tool"`
			Pattern   *string `json:"pattern"`
			ExpiresAt *string `json:"expiresAt"`
		} `json:"permissions"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		return apierr.NewAppError(http.StatusBadRequest, "invalid JSON body")
	}

	var entries []repo.GrantEntry

	// Template grants.
	if body.Template != nil {
		templateEntries, err := resolveTemplate(*body.Template)
		if err != nil {
			return apierr.NewAppError(http.StatusBadRequest, err.Error())
		}
		entries = append(entries, templateEntries...)
	}

	// Explicit grants.
	for _, p := range body.Permissions {
		if p.Tool == "" {
			return apierr.NewAppError(http.StatusBadRequest, "each permission entry requires a tool")
		}
		pattern := ""
		if p.Pattern != nil {
			pattern = *p.Pattern
		}
		if err := permissions.ValidateGrantEntry(p.Tool, pattern); err != nil {
			return apierr.NewAppError(http.StatusBadRequest, err.Error())
		}
		e := repo.GrantEntry{Tool: p.Tool, Pattern: p.Pattern}
		if p.ExpiresAt != nil {
			t, err := time.Parse(time.RFC3339, *p.ExpiresAt)
			if err != nil {
				return apierr.NewAppError(http.StatusBadRequest, "expiresAt must be RFC3339")
			}
			e.ExpiresAt = &t
		}
		entries = append(entries, e)
	}

	// When a client is available, wrap the grant + audit in a single transaction
	// so a partial failure cannot leave a grant with no forensic trace (or vice-versa).
	var granted []*ent.TaskPermission
	if h.client != nil {
		tx, err := h.client.Tx(r.Context())
		if err != nil {
			return fmt.Errorf("bulk_grant: begin tx: %w", err)
		}
		txCommitted := false
		defer func() {
			if !txCommitted {
				_ = tx.Rollback()
			}
		}()

		txPermRepo := repo.NewPermissionRepo(tx.Client())
		granted, err = txPermRepo.BulkGrantPermissions(r.Context(), taskID, entries)
		if err != nil {
			return fmt.Errorf("bulk_grant: %w", err)
		}
		if granted == nil {
			granted = []*ent.TaskPermission{}
		}

		var userID *string
		if payload, ok := auth.PayloadFromContext(r.Context()); ok && payload.Sub != "" {
			s := payload.Sub
			userID = &s
		}
		tools := make([]string, 0, len(granted))
		for _, p := range granted {
			tools = append(tools, p.Tool)
		}
		txAuditRepo := repo.NewAuditEventRepo(tx.Client())
		if err = txAuditRepo.RecordAudit(r.Context(), userID,
			repo.AuditActionPermissionGrant,
			taskID,
			map[string]any{"tools": tools},
		); err != nil {
			return fmt.Errorf("bulk_grant: record audit: %w", err)
		}

		if err = tx.Commit(); err != nil {
			return fmt.Errorf("bulk_grant: commit: %w", err)
		}
		txCommitted = true
	} else {
		// No client (e.g. tests with mocked repos) — write without tx.
		var err error
		granted, err = h.permRepo.BulkGrantPermissions(r.Context(), taskID, entries)
		if err != nil {
			return fmt.Errorf("bulk_grant: %w", err)
		}
		if granted == nil {
			granted = []*ent.TaskPermission{}
		}
		if h.auditRepo != nil {
			var userID *string
			if payload, ok := auth.PayloadFromContext(r.Context()); ok && payload.Sub != "" {
				s := payload.Sub
				userID = &s
			}
			tools := make([]string, 0, len(granted))
			for _, p := range granted {
				tools = append(tools, p.Tool)
			}
			if err = h.auditRepo.RecordAudit(r.Context(), userID,
				repo.AuditActionPermissionGrant,
				taskID,
				map[string]any{"tools": tools},
			); err != nil {
				return fmt.Errorf("bulk_grant: record audit: %w", err)
			}
		}
	}

	h.broadcastEnrichedUpdate(r.Context(), taskID)
	resp := make([]taskPermissionResponse, len(granted))
	for i, p := range granted {
		resp[i] = toTaskPermissionResponse(p)
	}
	return jsonReply(w, http.StatusOK, resp)
}

// bulkCreatePermissionRequests handles POST /api/permission-requests/bulk.
// Auto-resolves entries already covered by task_permissions; creates rows for the rest.
func (h *Handler) bulkCreatePermissionRequests(w http.ResponseWriter, r *http.Request) error {
	var body struct {
		StageRunID string `json:"stageRunId"`
		Entries    []struct {
			Tool    string  `json:"tool"`
			Pattern *string `json:"pattern"`
			Reason  *string `json:"reason"`
		} `json:"entries"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		return apierr.NewAppError(http.StatusBadRequest, "invalid JSON body")
	}
	if body.StageRunID == "" {
		return apierr.NewAppError(http.StatusBadRequest, "stageRunId is required")
	}

	sr, err := h.srRepo.GetByID(r.Context(), body.StageRunID)
	if err != nil {
		if ent.IsNotFound(err) {
			return apierr.ErrNotFound
		}
		return fmt.Errorf("bulk_perm_req: get stage run: %w", err)
	}

	// Fetch current effective task permissions to auto-resolve covered entries.
	effectivePerms, err := h.permRepo.ListEffectiveTaskPermissions(r.Context(), sr.TaskID)
	if err != nil {
		return fmt.Errorf("bulk_perm_req: list perms: %w", err)
	}

	// Load presets for this project cwd; a nil presetRepo or lookup failure is non-fatal.
	var presets []*ent.PermissionPreset
	if h.presetRepo != nil {
		task, taskErr := h.taskRepo.GetByID(r.Context(), sr.TaskID)
		if taskErr != nil {
			slog.Warn("bulk_perm_req: preset lookup: failed to get task", "taskID", sr.TaskID, "err", taskErr)
		} else {
			var userID *string
			if payload, ok := auth.PayloadFromContext(r.Context()); ok && payload.Sub != "" {
				s := payload.Sub
				userID = &s
			}
			ps, psErr := h.presetRepo.ListForCwd(r.Context(), userID, task.Cwd)
			if psErr != nil {
				slog.Warn("bulk_perm_req: preset lookup: ListForCwd failed", "cwd", task.Cwd, "err", psErr)
			} else {
				presets = ps
			}
		}
	}

	type result struct {
		Tool        string  `json:"tool"`
		Pattern     *string `json:"pattern"`
		AutoGranted bool    `json:"autoGranted"`
		RequestID   *string `json:"requestId,omitempty"`
	}

	results := make([]result, 0, len(body.Entries))
	hasNewRequests := false

	task, taskErr := h.taskRepo.GetByID(r.Context(), sr.TaskID)
	taskIsAllowAll := taskErr == nil && taskcontrol.IsAllowAll(task.Autonomy)

	pushWanted := h.approvalPushWanted(r.Context())
	for _, e := range body.Entries {
		if e.Tool == "" {
			continue
		}
		if isCovered(e.Tool, e.Pattern, effectivePerms) || presetCovers(e.Tool, e.Pattern, presets) {
			results = append(results, result{Tool: e.Tool, Pattern: e.Pattern, AutoGranted: true})
			continue
		}
		req, err2 := h.permRepo.CreatePermissionRequest(r.Context(), repo.CreatePermissionRequestInput{
			StageRunID: body.StageRunID,
			Tool:       e.Tool,
			Pattern:    e.Pattern,
			Reason:     e.Reason,
		})
		if err2 != nil {
			return fmt.Errorf("bulk_perm_req: create: %w", err2)
		}
		if taskIsAllowAll && !mcpapps.IsApplicationTool(e.Tool) {
			// Auto-approve: task operates in allow-all mode.
			if resolveErr := h.permRepo.ResolvePermissionRequest(r.Context(), req.ID, repo.OutcomeGranted); resolveErr != nil {
				slog.Warn("bulkCreatePermissionRequests: auto-approve failed", "reqID", req.ID, "err", resolveErr)
			} else {
				_ = h.auditRepo.RecordTaskAudit(r.Context(), sr.TaskID, nil, "permission_auto_approved", "task:"+sr.TaskID, map[string]any{
					"requestId": req.ID,
					"tool":      e.Tool,
					"autonomy":  task.Autonomy,
				})
			}
			results = append(results, result{Tool: e.Tool, Pattern: e.Pattern, AutoGranted: true})
			continue
		}
		hasNewRequests = true
		id := req.ID
		if h.notifier != nil && pushWanted {
			title := ""
			if taskErr == nil {
				title = task.Title
			}
			h.notifier.PermissionRequested(r.Context(), sr.TaskID, title, e.Tool)
		}
		results = append(results, result{Tool: e.Tool, Pattern: e.Pattern, AutoGranted: false, RequestID: &id})
	}

	// Flip stage_run to awaiting_user if there are new unresolved requests and it is running.
	if hasNewRequests && sr.Status == "running" {
		awaitingUser := "awaiting_user"
		if _, err2 := h.srRepo.Update(r.Context(), body.StageRunID, repo.UpdateStageRunInput{Status: &awaitingUser}); err2 != nil {
			slog.Warn("bulkCreatePermissionRequests: flip to awaiting_user failed", "stageRunID", body.StageRunID, "err", err2)
		}
	}
	// Broadcast for all paths: gated (new requests → awaiting_user flip) and
	// allow-all (auto-approved → client refreshes the task state).
	if hasNewRequests || taskIsAllowAll {
		h.BroadcastEnrichedEvent(r.Context(), "permission_request", sr.TaskID)
	}

	return jsonReply(w, http.StatusOK, results)
}

// deniedResumePrompt is what a resumed agent is told after a human refused tools.
func deniedResumePrompt(tools []string) string {
	return "A human refused permission to use: " + strings.Join(tools, ", ") +
		". Continue without these tools and state in your output what you could not do because of that."
}

// bulkResolvePermissionRequests handles POST /api/permission-requests/bulk-resolve.
func (h *Handler) bulkResolvePermissionRequests(w http.ResponseWriter, r *http.Request) error {
	var body struct {
		TaskID        string   `json:"taskId"`
		Decision      string   `json:"decision"`
		Outcome       string   `json:"outcome"`
		PermissionIDs []string `json:"permissionIds"`
		All           bool     `json:"all"`
		Remember      bool     `json:"remember"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		return apierr.NewAppError(http.StatusBadRequest, "invalid JSON body")
	}
	if body.TaskID == "" {
		return apierr.NewAppError(http.StatusBadRequest, "taskId is required")
	}
	decision, err := parseDecision(body.Decision, body.Outcome)
	if err != nil {
		return err
	}
	task, err := h.taskRepo.GetByID(r.Context(), body.TaskID)
	if err != nil {
		return apierr.ErrNotFound
	}

	// Object-level authz: only the task's own pending requests are resolvable,
	// so a caller cannot flip permission requests belonging to a different task.
	runs, err := h.srRepo.ListForTask(r.Context(), body.TaskID)
	if err != nil {
		return fmt.Errorf("bulk_resolve: list runs: %w", err)
	}
	runIDs := make([]string, len(runs))
	for i, sr := range runs {
		runIDs[i] = sr.ID
	}
	pending, err := h.permRepo.ListPendingForTask(r.Context(), body.TaskID, runIDs)
	if err != nil {
		return fmt.Errorf("bulk_resolve: list pending: %w", err)
	}

	var idsToResolve []string
	resolveErrors := []string{}
	if body.All {
		for _, req := range pending {
			idsToResolve = append(idsToResolve, req.ID)
		}
	} else {
		allowed := make(map[string]bool, len(pending))
		for _, req := range pending {
			allowed[req.ID] = true
		}
		for _, id := range body.PermissionIDs {
			if allowed[id] {
				idsToResolve = append(idsToResolve, id)
			} else {
				resolveErrors = append(resolveErrors, fmt.Sprintf("permission %s not pending for task %s", id, body.TaskID))
			}
		}
	}

	resolveSet := make(map[string]bool, len(idsToResolve))
	for _, id := range idsToResolve {
		resolveSet[id] = true
	}
	var tools []string
	for _, req := range pending {
		if resolveSet[req.ID] {
			tools = append(tools, req.Tool)
		}
	}

	// Validate the decision against every selected tool before resolving anything:
	// a refused routine decision must leave every request pending and not resume.
	if err := validateDecision(decision, task, tools); err != nil {
		return err
	}
	if (decision == DecisionAllowRoutine || decision == DecisionDenyRoutine) && h.grantRepo == nil {
		return apierr.NewAppError(http.StatusServiceUnavailable, "grants unavailable")
	}

	outcome := repo.OutcomeDenied
	if decision == DecisionAllowOnce || decision == DecisionAllowRoutine {
		outcome = repo.OutcomeGranted
	}

	resolvedCount := 0
	for _, id := range idsToResolve {
		if err := h.permRepo.ResolvePermissionRequest(r.Context(), id, outcome); err != nil {
			resolveErrors = append(resolveErrors, err.Error())
			continue
		}
		resolvedCount++
	}

	if len(idsToResolve) > 0 {
		decidedBy := decidedByFromRequest(r)

		// allow_once additionally writes task_permissions so the respawned
		// agent's allow-list includes the newly approved tools.
		if decision == DecisionAllowOnce {
			var entries []repo.GrantEntry
			for _, req := range pending {
				if resolveSet[req.ID] {
					entries = append(entries, repo.GrantEntry{Tool: req.Tool, Pattern: req.Pattern, DecidedBy: decidedBy})
				}
			}
			if _, errs := h.grantValidatedEntries(r.Context(), body.TaskID, entries); len(errs) > 0 {
				resolveErrors = append(resolveErrors, errs...)
			}

			// Persist presets so future requests for this project cwd are auto-approved.
			if body.Remember && h.presetRepo != nil && resolvedCount > 0 {
				var userID *string
				if payload, ok := auth.PayloadFromContext(r.Context()); ok && payload.Sub != "" {
					s := payload.Sub
					userID = &s
				}
				presetInputs := make([]repo.UpsertPresetInput, 0, len(entries))
				for _, e := range entries {
					presetInputs = append(presetInputs, repo.UpsertPresetInput{
						UserID:     userID,
						ProjectCwd: task.Cwd,
						Tool:       e.Tool,
						Pattern:    e.Pattern,
					})
				}
				if err := h.presetRepo.UpsertBatch(r.Context(), presetInputs); err != nil {
					slog.Warn("bulk_resolve: remember: UpsertBatch failed", "taskID", body.TaskID, "err", err)
				}
			}
		}

		if h.grantRepo != nil {
			for _, req := range pending {
				if !resolveSet[req.ID] {
					continue
				}
				for _, row := range decisionGrants(decision, task, req.Tool, decidedBy) {
					if _, err := mcpapps.EnsureGrant(r.Context(), h.grantRepo, row); err != nil {
						resolveErrors = append(resolveErrors, fmt.Sprintf("grant %s: %v", req.Tool, err))
					}
				}
			}
		}

		if _, err := h.orchestrator.ResumeFromUser(r.Context(), body.TaskID, decisionResumePrompt(decision, tools)); err != nil {
			slog.Warn("bulk_resolve: ResumeFromUser failed", "taskID", body.TaskID, "err", err)
		}
	}

	h.broadcastEnrichedUpdate(r.Context(), body.TaskID)
	return jsonReply(w, http.StatusOK, map[string]any{
		"resolved": resolvedCount,
		"errors":   resolveErrors,
	})
}

// approveAllPending resolves every pending permission_request for a task as
// granted, then re-queues the task when its latest stage run is awaiting_user.
func (h *Handler) approveAllPending(w http.ResponseWriter, r *http.Request) error {
	taskID := chi.URLParam(r, "id")

	if _, err := h.taskRepo.GetByID(r.Context(), taskID); err != nil {
		return apierr.ErrNotFound
	}

	res, err := ApproveAllPending(r.Context(), ApproveAllPendingDeps{
		TaskRepo:     h.taskRepo,
		SRRepo:       h.srRepo,
		PermRepo:     h.permRepo,
		AuditRepo:    h.auditRepo,
		Orchestrator: h.orchestrator,
	}, taskID)
	if err != nil {
		return fmt.Errorf("approve_all_pending: %w", err)
	}

	h.broadcastEnrichedUpdate(r.Context(), taskID)
	return jsonReply(w, http.StatusOK, res)
}

// isCovered checks whether a tool+pattern request is already satisfied by effective permissions.
func isCovered(tool string, pattern *string, perms []*ent.TaskPermission) bool {
	for _, p := range perms {
		if p.Tool != tool {
			continue
		}
		// No pattern required — any grant for this tool covers it.
		if pattern == nil {
			return true
		}
		// Pattern required — must match exactly or perm has no pattern restriction.
		if p.Pattern == nil {
			return true
		}
		if *p.Pattern == *pattern {
			return true
		}
	}
	return false
}

// presetCovers checks whether a tool+pattern request is covered by a stored preset.
// A preset with nil Pattern covers any request pattern for that tool (wildcard);
// otherwise the pattern must match exactly. Mirrors isCovered's semantics.
func presetCovers(tool string, pattern *string, presets []*ent.PermissionPreset) bool {
	for _, p := range presets {
		if p.Tool != tool {
			continue
		}
		if pattern == nil {
			return true
		}
		if p.Pattern == nil {
			return true
		}
		if *p.Pattern == *pattern {
			return true
		}
	}
	return false
}

// grantValidatedEntries persists human-override grants for the handler's repo.
// Logic lives in grantOverrideEntries, shared with the approve-all-pending service.
func (h *Handler) grantValidatedEntries(ctx context.Context, taskID string, entries []repo.GrantEntry) ([]*ent.TaskPermission, []string) {
	return grantOverrideEntries(ctx, h.permRepo, taskID, entries)
}

// resolveTemplate expands a named permission template to GrantEntry slice.
// Delegates to permissions.ResolveTemplate — the single source of truth for template definitions.
func resolveTemplate(name string) ([]repo.GrantEntry, error) {
	tools, err := permissions.ResolveTemplate(name)
	if err != nil {
		return nil, err
	}
	entries := make([]repo.GrantEntry, len(tools))
	for i, t := range tools {
		entries[i] = repo.GrantEntry{Tool: t}
	}
	return entries, nil
}

// deniedByDefault answers, per request id, whether that request's capability
// already resolves to a deny in the task's contexts. It uses mcpapps.Decide —
// the resolution the run's allow list is built from — so the card and the
// spawner can never disagree about what an answer would achieve. Built-in
// tools are skipped, and any failure leaves every entry false: a request that
// cannot be classified is still worth asking about.
func (h *Handler) deniedByDefault(ctx context.Context, taskID string, reqs []*ent.PermissionRequest) map[string]bool {
	out := make(map[string]bool, len(reqs))
	if h.grantRepo == nil || h.capabilityRepo == nil {
		return out
	}
	task, err := h.taskRepo.GetByID(ctx, taskID)
	if err != nil || task == nil {
		return out
	}
	contexts := mcpapps.RunContexts(task)
	cache := make(map[string]bool, len(reqs))
	for _, req := range reqs {
		if !mcpapps.IsApplicationTool(req.Tool) {
			continue
		}
		if denied, ok := cache[req.Tool]; ok {
			out[req.ID] = denied
			continue
		}
		decision, derr := mcpapps.Decide(ctx, h.grantRepo, h.capabilityRepo, req.Tool, contexts)
		denied := derr == nil && decision.Effect == capability.EffectDeny
		cache[req.Tool] = denied
		out[req.ID] = denied
	}
	return out
}
