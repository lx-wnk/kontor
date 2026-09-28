package tasks

import (
	"context"
	"time"

	"github.com/lx-wnk/agent-dashboard/server/internal/db/ent"
	"github.com/lx-wnk/agent-dashboard/server/internal/db/rawrepo"
	"github.com/lx-wnk/agent-dashboard/server/internal/db/repo"
	"github.com/lx-wnk/agent-dashboard/server/internal/pipeline"
	"github.com/lx-wnk/agent-dashboard/server/internal/proc"
	"github.com/lx-wnk/agent-dashboard/server/internal/taskcontrol"
)

// TaskResponse is the API response shape for a task's own stored columns. It is
// the base of EnrichedTask — embedded, so the enriched payload's wire format is
// unchanged — and the whole answer for routes outside this package that return a
// task and have no repos to compute anything from (plan approve, refine confirm).
//
// The ent entity's own tags are the storage column names and carry omitempty,
// which drops silverBullet: false and planMode: false from the payload instead
// of sending them, and leaks the empty edges container.
type TaskResponse struct {
	ID                  string                 `json:"id"`
	Slug                string                 `json:"slug"`
	Title               string                 `json:"title"`
	Description         *string                `json:"description"`
	Cwd                 string                 `json:"cwd"`
	WorktreePath        *string                `json:"worktreePath"`
	SourceBranch        *string                `json:"sourceBranch"`
	TargetBranch        *string                `json:"targetBranch"`
	CurrentStage        string                 `json:"currentStage"`
	Kind                string                 `json:"kind"`
	RoutineID           *string                `json:"routineId"`
	Priority            string                 `json:"priority"`
	Autonomy            string                 `json:"autonomy"`
	UserID              *string                `json:"userId"`
	ParentTaskID        *string                `json:"parentTaskId"`
	ProjectID           *string                `json:"projectId"`
	SpawnerID           *string                `json:"spawnerId"`
	MaxIterations       int                    `json:"maxIterations"`
	TokenBudget         *int                   `json:"tokenBudget"`
	CostBudgetCents     *int                   `json:"costBudgetCents"`
	StageTimeoutSeconds int                    `json:"stageTimeoutSeconds"`
	SilverBullet        bool                   `json:"silverBullet"`
	PlanMode            bool                   `json:"planMode"`
	Rank                *float64               `json:"rank"`
	Metadata            map[string]interface{} `json:"metadata"`
	CreatedAt           time.Time              `json:"createdAt"`
	UpdatedAt           time.Time              `json:"updatedAt"`
}

// ToTaskResponse maps a stored task onto the wire shape src/types.ts declares as
// PipelineTask's non-computed half.
func ToTaskResponse(t *ent.Task) TaskResponse {
	return TaskResponse{
		ID:                  t.ID,
		Slug:                t.Slug,
		Title:               t.Title,
		Description:         t.Description,
		Cwd:                 t.Cwd,
		WorktreePath:        t.WorktreePath,
		SourceBranch:        t.SourceBranch,
		TargetBranch:        t.TargetBranch,
		CurrentStage:        t.CurrentStage,
		Kind:                t.Kind,
		RoutineID:           t.RoutineID,
		Priority:            t.Priority,
		Autonomy:            t.Autonomy,
		UserID:              t.UserID,
		ParentTaskID:        t.ParentTaskID,
		ProjectID:           t.ProjectID,
		SpawnerID:           t.SpawnerID,
		MaxIterations:       t.MaxIterations,
		TokenBudget:         t.TokenBudget,
		CostBudgetCents:     t.CostBudgetCents,
		StageTimeoutSeconds: t.StageTimeoutSeconds,
		SilverBullet:        t.SilverBullet,
		PlanMode:            t.PlanMode,
		Rank:                t.Rank,
		Metadata:            t.Metadata,
		CreatedAt:           t.CreatedAt,
		UpdatedAt:           t.UpdatedAt,
	}
}

// EnrichedTask is a task plus the fields computed at read time. The embedded
// TaskResponse is anonymous, so encoding/json flattens its fields into the same
// object — the payload is identical to the flat struct this replaced.
type EnrichedTask struct {
	TaskResponse
	// Computed fields — not stored in DB.
	NeedsUser                   bool                 `json:"needsUser"`
	LatestStageRunStatus        *string              `json:"latestStageRunStatus"`
	AutoRetryCount              *int                 `json:"autoRetryCount,omitempty"`
	NextRetryAt                 *time.Time           `json:"nextRetryAt,omitempty"`
	RefineStatus                *string              `json:"refineStatus,omitempty"`
	RefineError                 *string              `json:"refineError,omitempty"`
	CurrentIteration            int                  `json:"currentIteration"`
	ActiveSessionID             *string              `json:"activeSessionId"`
	ActivePID                   *int                 `json:"activePid"`
	HasPendingPermissions       bool                 `json:"hasPendingPermissions"`
	BlockedByPendingPermissions bool                 `json:"blockedByPendingPermissions"`
	AvailableActions            []taskcontrol.Action `json:"availableActions"`

	// Dependency state — computed from task_dependency rows; false when no upstreams.
	IsBlocked       bool `json:"isBlocked"`
	IsUnsatisfiable bool `json:"isUnsatisfiable"`

	// Child-task summary — populated by ChildSummariesByParent, zero when no children.
	ChildCount       int          `json:"childCount"`
	ActiveChildCount int          `json:"activeChildCount"`
	ActiveChild      *ActiveChild `json:"activeChild"`

	// pendingPermsCount preserves the raw count so a later RecomputeAvailableActions
	// (e.g. from applyRefineStatus) stays faithful instead of assuming zero.
	pendingPermsCount int
}

// ActiveChild is the representative active child attached to a parent EnrichedTask.
type ActiveChild struct {
	TokensUsed      int    `json:"tokensUsed"`
	CostCents       int    `json:"costCents"`
	DurationSeconds int    `json:"durationSeconds"`
	CurrentStage    string `json:"currentStage"`
	LatestOutput    string `json:"latestOutput"`
}

// pidAliveMemo returns a func(int) bool that wraps proc.IsPidAlive and caches
// each distinct pid's liveness for the lifetime of the returned closure. A single
// memo is shared across one enrich pass so a pid reused by several sibling tasks
// costs exactly one syscall. The closure is not safe for concurrent use — build
// one per enrich call.
func pidAliveMemo() func(int) bool {
	return memoizeProbe(proc.IsPidAlive)
}

// memoizeProbe caches each distinct pid's probe result for the lifetime of the
// returned closure. Split out from pidAliveMemo so the dedup logic is unit-testable
// with a counting probe.
func memoizeProbe(probe func(int) bool) func(int) bool {
	cache := make(map[int]bool)
	return func(pid int) bool {
		if alive, ok := cache[pid]; ok {
			return alive
		}
		alive := probe(pid)
		cache[pid] = alive
		return alive
	}
}

// stageResolver returns a closure that resolves a task's current_stage by ID,
// or nil when no taskRepo is available (dependency state is then skipped).
func stageResolver(taskRepo repo.TaskRepo) func(context.Context, string) (string, error) {
	if taskRepo == nil {
		return nil
	}
	return func(ctx context.Context, id string) (string, error) {
		up, err := taskRepo.GetByID(ctx, id)
		if err != nil {
			return "", err
		}
		return up.CurrentStage, nil
	}
}

// bulkStageResolver resolves upstream stages during a bulk enrich. It seeds a
// cache with the stages of every task already in the slice (no query) and
// memoizes a GetByID fallback for upstreams outside it, so each distinct
// out-of-slice upstream is fetched at most once per pass.
func bulkStageResolver(taskRepo repo.TaskRepo, slice []*ent.Task) func(context.Context, string) (string, error) {
	if taskRepo == nil {
		return nil
	}
	cache := make(map[string]string, len(slice))
	for _, t := range slice {
		cache[t.ID] = t.CurrentStage
	}
	return func(ctx context.Context, id string) (string, error) {
		if s, ok := cache[id]; ok {
			return s, nil
		}
		up, err := taskRepo.GetByID(ctx, id)
		if err != nil {
			return "", err
		}
		cache[id] = up.CurrentStage
		return up.CurrentStage, nil
	}
}

func EnrichTask(ctx context.Context, t *ent.Task, srRepo repo.StageRunRepo, permRepo repo.PermissionRepo, bulkRepo rawrepo.StageRunBulkRepo) (*EnrichedTask, error) {
	return EnrichTaskWithDeps(ctx, t, srRepo, permRepo, bulkRepo, nil, nil)
}

func EnrichTaskWithDeps(ctx context.Context, t *ent.Task, srRepo repo.StageRunRepo, permRepo repo.PermissionRepo, bulkRepo rawrepo.StageRunBulkRepo, depRepo repo.DependencyRepo, taskRepo repo.TaskRepo) (*EnrichedTask, error) {
	latest, _ := srRepo.GetLatestForTask(ctx, t.ID)
	var pendingCount int
	if latest != nil && latest.Stage == t.CurrentStage {
		pendingCount, _ = permRepo.CountForStageRun(ctx, latest.ID)
	}
	var childSummary *rawrepo.ChildSummary
	if bulkRepo != nil {
		childMap, _ := bulkRepo.ChildSummariesByParent(ctx, []string{t.ID})
		childSummary = childMap[t.ID]
	}
	return enrichOne(ctx, t, latest, pendingCount, pidAliveMemo(), childSummary, depRepo, stageResolver(taskRepo))
}

// EnrichTasksBulk enriches a slice of tasks with their latest stage-run status
// and pending-permission counts. It uses the window-function bulk repo
// (rawrepo.StageRunBulkRepo.LatestPerTask) for an exact per-task latest query,
// which is correct regardless of iteration count. srRepo is retained for the
// single-task EnrichTask path; it is unused here.
func EnrichTasksBulk(ctx context.Context, tasks []*ent.Task, _ repo.StageRunRepo, permRepo repo.PermissionRepo, bulkRepo rawrepo.StageRunBulkRepo) ([]*EnrichedTask, error) {
	return EnrichTasksBulkWithDeps(ctx, tasks, nil, permRepo, bulkRepo, nil, nil)
}

func EnrichTasksBulkWithDeps(ctx context.Context, tasks []*ent.Task, _ repo.StageRunRepo, permRepo repo.PermissionRepo, bulkRepo rawrepo.StageRunBulkRepo, depRepo repo.DependencyRepo, taskRepo repo.TaskRepo) ([]*EnrichedTask, error) {
	if len(tasks) == 0 {
		return []*EnrichedTask{}, nil
	}
	ids := make([]string, len(tasks))
	for i, t := range tasks {
		ids[i] = t.ID
	}
	latestMap, err := bulkRepo.LatestPerTask(ctx, ids)
	if err != nil {
		return nil, err
	}

	childMap, err := bulkRepo.ChildSummariesByParent(ctx, ids)
	if err != nil {
		return nil, err
	}

	// Collect stage run IDs that belong to the current stage — only those need a count.
	srIDs := make([]string, 0, len(tasks))
	for _, t := range tasks {
		if sr := latestMap[t.ID]; sr != nil && sr.Stage == t.CurrentStage {
			srIDs = append(srIDs, sr.ID)
		}
	}
	pendingCounts, err := permRepo.CountForStageRunsBulk(ctx, srIDs)
	if err != nil {
		return nil, err
	}

	// One memo shared across the whole slice so a pid reused by sibling tasks
	// hits the liveness syscall once per tick.
	isAlive := pidAliveMemo()
	resolveStage := bulkStageResolver(taskRepo, tasks)

	result := make([]*EnrichedTask, len(tasks))
	for i, t := range tasks {
		latest := latestMap[t.ID]
		var pendingCount int
		if latest != nil && latest.Stage == t.CurrentStage {
			pendingCount = pendingCounts[latest.ID]
		}
		enriched, err := enrichOne(ctx, t, latest, pendingCount, isAlive, childMap[t.ID], depRepo, resolveStage)
		if err != nil {
			return nil, err
		}
		result[i] = enriched
	}
	return result, nil
}

func enrichOne(ctx context.Context, t *ent.Task, latest *ent.StageRun, pendingPermsCount int, isAlive func(int) bool, childSummary *rawrepo.ChildSummary, depRepo repo.DependencyRepo, resolveStage func(context.Context, string) (string, error)) (*EnrichedTask, error) {
	latestBelongsToCurrent := latest != nil && latest.Stage == t.CurrentStage
	var latestStatus *string
	currentIteration := 0
	if latestBelongsToCurrent {
		latestStatus = &latest.Status
		currentIteration = latest.Iteration
	}

	// hasPending answers "does someone owe this task a decision", independent of
	// run status: the agent files the request as it parks on awaiting_user, and
	// its process may be alive or already gone. blockedBy answers the narrower
	// "are these requests stranded", which is what gates the next spawn.
	// Cancelled tasks are excluded — their leftover requests need no answer.
	hasPendingPermissions := pendingPermsCount > 0 && t.CurrentStage != "cancelled"
	isTerminal := latestStatus != nil && (*latestStatus == "failed" || *latestStatus == "done")
	isZombieAwait := latestStatus != nil && *latestStatus == "awaiting_user" && latest != nil &&
		(latest.Pid == nil || !isAlive(*latest.Pid))
	blockedByPendingPermissions := (isTerminal || isZombieAwait) && pendingPermsCount > 0

	needsUser := t.CurrentStage == "on_hold" ||
		(latestStatus != nil && (*latestStatus == "awaiting_user" || *latestStatus == "on_hold" || *latestStatus == "failed")) ||
		hasPendingPermissions || blockedByPendingPermissions

	var autoRetryCount *int
	var nextRetryAt *time.Time
	if latestBelongsToCurrent {
		if latest.RetryCount > 0 {
			autoRetryCount = &latest.RetryCount
		}
		nextRetryAt = latest.NextRetryAt
	}

	var activeSessionID *string
	var activePID *int
	if latest != nil {
		activeSessionID = latest.SessionID
		if latest.Status == "running" {
			activePID = latest.Pid
		}
	}

	// Display-only flags: on eval error both stay false (task shows runnable). The
	// picker gate is authoritative and fails conservative (errs → skip), so an
	// optimistic display here cannot cause an unmet-dependency task to actually run.
	var isBlocked, isUnsatisfiable bool
	if depRepo != nil && resolveStage != nil {
		if _, blocked, unsatisfiable, err := pipeline.EvaluateTaskDeps(ctx, t.ID, depRepo, resolveStage); err == nil {
			isBlocked = blocked
			isUnsatisfiable = unsatisfiable
		}
	}

	e := &EnrichedTask{
		TaskResponse:                ToTaskResponse(t),
		NeedsUser:                   needsUser,
		LatestStageRunStatus:        latestStatus,
		AutoRetryCount:              autoRetryCount,
		NextRetryAt:                 nextRetryAt,
		CurrentIteration:            currentIteration,
		ActiveSessionID:             activeSessionID,
		ActivePID:                   activePID,
		HasPendingPermissions:       hasPendingPermissions,
		BlockedByPendingPermissions: blockedByPendingPermissions,
		IsBlocked:                   isBlocked,
		IsUnsatisfiable:             isUnsatisfiable,
		pendingPermsCount:           pendingPermsCount,
	}

	if childSummary != nil {
		e.ChildCount = childSummary.ChildCount
		e.ActiveChildCount = childSummary.ActiveChildCount
		if childSummary.HasActive {
			e.ActiveChild = &ActiveChild{
				TokensUsed:      childSummary.TokensUsed,
				CostCents:       childSummary.CostCents,
				DurationSeconds: childSummary.DurationSeconds,
				CurrentStage:    childSummary.CurrentStage,
				LatestOutput:    childSummary.LatestOutput,
			}
		}
	}

	recomputeAvailableActionsWithPerms(e, pendingPermsCount)
	return e, nil
}

// RecomputeAvailableActions rebuilds AvailableActions from the current enriched
// fields. Called once by enrichOne and again by applyRefineStatus so that the
// refine runner status is always reflected in the action set.
func (e *EnrichedTask) RecomputeAvailableActions() {
	recomputeAvailableActionsWithPerms(e, e.pendingPermsCount)
}

// recomputeAvailableActionsWithPerms rebuilds AvailableActions with the exact
// pending permission count. Used by enrichOne which has the raw count available.
func recomputeAvailableActionsWithPerms(e *EnrichedTask, pendingPermsCount int) {
	runStatus := ""
	if e.LatestStageRunStatus != nil {
		runStatus = *e.LatestStageRunStatus
	}
	refineStatus := ""
	if e.RefineStatus != nil {
		refineStatus = *e.RefineStatus
	}
	s := taskcontrol.FromFields(e.CurrentStage, runStatus, refineStatus, pendingPermsCount, e.NeedsUser)
	e.AvailableActions = taskcontrol.ComputeActions(s)
}
