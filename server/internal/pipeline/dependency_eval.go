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
//	allSatisfied — every upstream is DepSatisfied; task may be picked
//	blocked      — at least one upstream is DepBlocked
//	unsatisfiable — at least one upstream is DepUnsatisfiable (no path to satisfied)
func EvaluateTaskDeps(
	ctx context.Context,
	taskID string,
	depRepo repo.DependencyRepo,
	resolveStage func(ctx context.Context, taskID string) (string, error),
) (allSatisfied, blocked, unsatisfiable bool, err error) {
	upstreams, err := depRepo.ListUpstream(ctx, taskID)
	if err != nil {
		return false, false, false, err
	}
	if len(upstreams) == 0 {
		return true, false, false, nil
	}
	allSat := true
	for _, dep := range upstreams {
		stage, serr := resolveStage(ctx, dep.DependsOnID)
		if serr != nil {
			// treat resolution failure conservatively as blocked
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
		case DepUnsatisfiable:
			allSat = false
			unsatisfiable = true
		}
	}
	return allSat, blocked, unsatisfiable, nil
}
