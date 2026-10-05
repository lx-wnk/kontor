package pipeline

import (
	"context"

	"github.com/lx-wnk/kontor/server/internal/db/ent"
	"github.com/lx-wnk/kontor/server/internal/db/repo"
)

// DepStatus is the satisfaction state of a single upstream dependency.
type DepStatus int

const (
	// DepSatisfied means the upstream has reached the required stage (or a
	// cancelled upstream with on_cancel_action==start, which counts as cleared).
	DepSatisfied DepStatus = iota
	// DepBlocked means the upstream exists and is still in progress.
	DepBlocked
	// DepUnsatisfiable means the upstream reached a terminal stage that can
	// never become the required stage (e.g. cancelled when action is not "start").
	DepUnsatisfiable
)

// EvaluateDependency classifies a single upstream dependency given the
// upstream task's current stage. It is the single canonical source of
// dependency-satisfaction logic; both the picker gate and the cancel cascade use it.
func EvaluateDependency(dep *ent.TaskDependency, upstreamStage string) DepStatus {
	if upstreamStage == dep.RequiredStage {
		return DepSatisfied
	}
	if IsTerminalStage(upstreamStage) {
		// upstream cancelled + "start" means: proceed as if satisfied
		if upstreamStage == "cancelled" && dep.OnCancelAction == "start" {
			return DepSatisfied
		}
		return DepUnsatisfiable
	}
	return DepBlocked
}

// EvaluateTaskDeps resolves all upstream deps for taskID and returns the
// aggregate state. resolveStage is called once per upstream task ID to
// obtain that task's current_stage; errors from resolveStage treat the
// upstream as blocked (safe/conservative default).
//
// Returns:
//
//	allSatisfied  — every upstream is DepSatisfied; task may be picked
//	blocked       — at least one upstream is DepBlocked
//	unsatisfiable — at least one upstream is DepUnsatisfiable (no path to satisfied)
//
// EvaluateTaskDeps is a thin adapter over EvaluateTaskDepsDetail that drops the
// per-upstream slug; the scheduler only needs the aggregate booleans.
func EvaluateTaskDeps(
	ctx context.Context,
	taskID string,
	depRepo repo.DependencyRepo,
	resolveStage func(ctx context.Context, taskID string) (string, error),
) (allSatisfied, blocked, unsatisfiable bool, err error) {
	resolveInfo := func(ctx context.Context, id string) (string, string, error) {
		stage, serr := resolveStage(ctx, id)
		return stage, "", serr
	}
	allSatisfied, blocked, unsatisfiable, _, err = EvaluateTaskDepsDetail(ctx, taskID, depRepo, resolveInfo)
	return
}

// BlockingUpstream identifies an upstream dependency that is not yet satisfied,
// carrying enough info for the client to display "Waiting for: slug (stage)".
// Unsatisfiable is true when the upstream has reached a terminal stage that
// can never become the required stage — the client uses it to select the correct
// entry from a mixed list and to render the right label.
type BlockingUpstream struct {
	Slug          string `json:"slug"`
	Stage         string `json:"stage"`
	Unsatisfiable bool   `json:"unsatisfiable,omitempty"`
}

// EvaluateTaskDepsDetail is like EvaluateTaskDeps but additionally returns the
// list of blocking/unsatisfiable upstreams with their slug and current stage.
// resolveInfo returns (currentStage, slug, error) for a given task ID.
func EvaluateTaskDepsDetail(
	ctx context.Context,
	taskID string,
	depRepo repo.DependencyRepo,
	resolveInfo func(ctx context.Context, taskID string) (stage, slug string, err error),
) (allSatisfied, blocked, unsatisfiable bool, upstreams []BlockingUpstream, err error) {
	deps, err := depRepo.ListUpstream(ctx, taskID)
	if err != nil {
		return false, false, false, nil, err
	}
	if len(deps) == 0 {
		return true, false, false, nil, nil
	}
	allSat := true
	for _, dep := range deps {
		stage, slug, serr := resolveInfo(ctx, dep.DependsOnID)
		if serr != nil {
			allSat = false
			blocked = true
			continue
		}
		switch EvaluateDependency(dep, stage) {
		case DepSatisfied:
			// fine
		case DepBlocked:
			allSat = false
			blocked = true
			upstreams = append(upstreams, BlockingUpstream{Slug: slug, Stage: stage})
		case DepUnsatisfiable:
			allSat = false
			unsatisfiable = true
			upstreams = append(upstreams, BlockingUpstream{Slug: slug, Stage: stage, Unsatisfiable: true})
		}
	}
	return allSat, blocked, unsatisfiable, upstreams, nil
}
