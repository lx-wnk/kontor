// server/internal/agentbroadcast/enricher.go
package agentbroadcast

import (
	"context"
	"log/slog"
	"time"

	sdk "github.com/lx-wnk/kontor/sdk"
	"github.com/lx-wnk/kontor/server/internal/capability"
	"github.com/lx-wnk/kontor/server/internal/db/ent"
	"github.com/lx-wnk/kontor/server/internal/db/repo"
	"github.com/lx-wnk/kontor/server/internal/mcpapps"
	"github.com/lx-wnk/kontor/server/internal/merger"
)

// NewPipelineTaskEnricher returns a merger.Enricher that annotates each agent
// with its linked pipeline task (ID + title), resolved read-only from SQLite:
// agent.SessionID → StageRunRepo.ListBySessionIDs → stageRun.TaskID →
// TaskRepo.ListByIDs → task.Title.
//
// When perms is non-nil, it also attaches any pending permission requests for
// the resolved stage run to PendingPermissions.
//
// When both grants and caps are non-nil, each application-tool pending
// request is also labelled DeniedByDefault using the same resolution
// mcpapps.Resolver uses for a run's allow list, so the UI can tell "asking"
// apart from "already denied — change the grant instead". Built-in tools
// (mcpapps.IsApplicationTool is false) and requests with no resolved task are
// left false without a grants lookup. Results are cached per capability name
// for the pass, so a burst of pending requests for the same tool costs one
// lookup, not one per request.
//
// When projects is non-nil, an agent whose task belongs to a Kontor project
// gets that project's ID and name, overriding any folder match from
// NewProjectFolderEnricher (a pipeline worktree lives outside the project's
// folders and is named after the task).
//
// All lookups are batched to one query each per tick (session IDs → stage
// runs, resolved task IDs → tasks, the tasks' project IDs → projects, resolved
// stage run IDs → pending permissions) instead of per-agent round-trips, then
// joined in-memory.
//
// The crossing is one-way (pipeline → agent annotation) and best-effort: nil
// repos, a session with no stage_run (the common case for ad-hoc sessions), or
// any query error leaves PipelineTaskID/Title/PendingPermissions empty and
// ProjectID/ProjectName as they were without failing the scan.
//
// agentbroadcast is a peer of merger and may import db/repo, which keeps merger
// itself free of any db dependency (Go layer direction).
func NewPipelineTaskEnricher(stageRuns repo.StageRunRepo, tasks repo.TaskRepo, perms repo.PermissionRepo, grants repo.GrantRepo, caps repo.CapabilityRepo, projects repo.ProjectRepo) merger.Enricher {
	return func(ctx context.Context, agents []sdk.Agent) {
		if stageRuns == nil || tasks == nil {
			return
		}

		sessionIDs := make([]string, 0, len(agents))
		seen := make(map[string]struct{}, len(agents))
		for i := range agents {
			sid := agents[i].SessionID
			if sid == "" {
				continue
			}
			if _, ok := seen[sid]; ok {
				continue
			}
			seen[sid] = struct{}{}
			sessionIDs = append(sessionIDs, sid)
		}
		if len(sessionIDs) == 0 {
			return
		}

		runs, err := stageRuns.ListBySessionIDs(ctx, sessionIDs)
		if err != nil {
			slog.Debug("pipeline enricher: stage_run batch lookup failed", "err", err)
			return
		}

		stageRunBySession := make(map[string]*ent.StageRun, len(runs))
		taskIDSeen := make(map[string]struct{}, len(runs))
		taskIDs := make([]string, 0, len(runs))
		stageRunIDs := make([]string, 0, len(runs))
		for _, sr := range runs {
			if sr.SessionID == nil {
				continue
			}
			stageRunBySession[*sr.SessionID] = sr
			stageRunIDs = append(stageRunIDs, sr.ID)
			if _, ok := taskIDSeen[sr.TaskID]; !ok {
				taskIDSeen[sr.TaskID] = struct{}{}
				taskIDs = append(taskIDs, sr.TaskID)
			}
		}

		taskByID := make(map[string]*ent.Task, len(taskIDs))
		taskList, err := tasks.ListByIDs(ctx, taskIDs)
		if err != nil {
			slog.Debug("pipeline enricher: task batch lookup failed", "err", err)
		} else {
			for _, t := range taskList {
				taskByID[t.ID] = t
			}
		}

		projectNameByID := projectNamesByID(ctx, projects, taskByID)

		pendingByStageRun := make(map[string][]*ent.PermissionRequest)
		if perms != nil {
			pendingReqs, perr := perms.ListPendingForStageRuns(ctx, stageRunIDs)
			if perr != nil {
				slog.Debug("pipeline enricher: pending permissions batch lookup failed", "err", perr)
			} else {
				for _, req := range pendingReqs {
					pendingByStageRun[req.StageRunID] = append(pendingByStageRun[req.StageRunID], req)
				}
			}
		}

		deniedCache := make(map[string]bool)

		// Iterate by index so writes land on the slice elements, not on copies.
		for i := range agents {
			sr, ok := stageRunBySession[agents[i].SessionID]
			if !ok {
				continue
			}
			agents[i].PipelineTaskID = sr.TaskID

			task, hasTask := taskByID[sr.TaskID]
			if hasTask {
				agents[i].PipelineTaskTitle = task.Title
				if task.ProjectID != nil {
					if name, found := projectNameByID[*task.ProjectID]; found {
						agents[i].ProjectID = *task.ProjectID
						agents[i].ProjectName = name
					}
				}
			}

			if pendingReqs := pendingByStageRun[sr.ID]; len(pendingReqs) > 0 {
				pp := make([]sdk.PendingPermission, len(pendingReqs))
				for j, req := range pendingReqs {
					pp[j] = sdk.PendingPermission{
						ID:          req.ID,
						Tool:        req.Tool,
						Pattern:     req.Pattern,
						Reason:      req.Reason,
						RequestedAt: req.RequestedAt.UTC().Format(time.RFC3339),
					}
					if grants != nil && caps != nil && hasTask {
						pp[j].DeniedByDefault = isDeniedByDefault(ctx, grants, caps, req.Tool, task, deniedCache)
					}
				}
				agents[i].PendingPermissions = pp
			}
		}
	}
}

// projectNamesByID batch-resolves the names of the projects taskByID's tasks
// belong to. A nil repo or a lookup error yields an empty map.
func projectNamesByID(ctx context.Context, projects repo.ProjectRepo, taskByID map[string]*ent.Task) map[string]string {
	names := make(map[string]string)
	if projects == nil {
		return names
	}
	seen := make(map[string]struct{})
	var ids []string
	for _, t := range taskByID {
		if t.ProjectID == nil {
			continue
		}
		if _, ok := seen[*t.ProjectID]; !ok {
			seen[*t.ProjectID] = struct{}{}
			ids = append(ids, *t.ProjectID)
		}
	}
	if len(ids) == 0 {
		return names
	}
	list, err := projects.ListByIDs(ctx, ids)
	if err != nil {
		slog.Debug("pipeline enricher: project batch lookup failed", "err", err)
		return names
	}
	for _, p := range list {
		names[p.ID] = p.Name
	}
	return names
}

// isDeniedByDefault reports whether tool already resolves to a deny in task's
// contexts, per the same mcpapps.Decide a run's allow list uses. Only
// application tools (mcpapps.IsApplicationTool) are ever checked — built-in
// tools like Bash and Read have no capability row and no grant to look up.
// Any error degrades to false: the request stays listed and askable rather
// than silently dropped.
func isDeniedByDefault(ctx context.Context, grants repo.GrantRepo, caps repo.CapabilityRepo, tool string, task *ent.Task, cache map[string]bool) bool {
	if !mcpapps.IsApplicationTool(tool) {
		return false
	}
	// Keyed by task as well as tool: a routine- or task-scoped deny answers
	// differently for another task in the same tick, and one pass can carry
	// tasks of several routines.
	key := task.ID + "\x00" + tool
	if denied, ok := cache[key]; ok {
		return denied
	}
	decision, err := mcpapps.Decide(ctx, grants, caps, tool, mcpapps.RunContexts(task))
	denied := err == nil && decision.Effect == capability.EffectDeny
	cache[key] = denied
	return denied
}
