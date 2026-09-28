package mcp

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/lx-wnk/kontor/server/internal/capability"
	"github.com/lx-wnk/kontor/server/internal/db/ent"
	"github.com/lx-wnk/kontor/server/internal/db/repo"
)

// StageRunLookup and TaskLookup are the two reads CallerResolver needs. They
// are declared here, narrow, rather than taking the full repo interfaces: the
// resolver depends on the methods it calls and nothing else.
type StageRunLookup interface {
	GetByID(ctx context.Context, id string) (*ent.StageRun, error)
}

type TaskLookup interface {
	GetByID(ctx context.Context, id string) (*ent.Task, error)
}

// CallerResolver turns the stage run on a request's MCP credential into the
// capability contexts that request resolves against.
//
// A user key resolves to nothing, which is why an unattributed call behaves
// exactly as it did before this existed — capability.Decide drops every grant
// whose context the request does not name, so "no contexts" means the scope
// chain alone decides, as it always did.
type CallerResolver struct {
	StageRuns StageRunLookup
	Tasks     TaskLookup
}

// Contexts resolves the caller's chain: the task the stage run belongs to, and
// the routine that materialized that task when there is one.
//
// Every failure resolves to no contexts rather than a partial chain: the safe
// direction is the one a machine-wide key already takes.
func (r CallerResolver) Contexts(ctx context.Context) []capability.Context {
	taskID, err := r.TaskID(ctx)
	if err != nil || taskID == "" || r.Tasks == nil {
		return nil
	}
	task, err := r.Tasks.GetByID(ctx, taskID)
	if err != nil {
		slog.Debug("mcp: task for stage run not found", "task", taskID, "err", err)
		return nil
	}
	out := []capability.Context{{Kind: repo.GrantContextTask, Ref: task.ID}}
	if task.RoutineID != nil && *task.RoutineID != "" {
		out = append(out, capability.Context{Kind: repo.GrantContextRoutine, Ref: *task.RoutineID})
	}
	return out
}

// ErrCallerUnresolved reports a credential that names a stage run but whose
// task cannot be determined. Read tools that narrow to the caller's own task
// must refuse such a request: an unresolvable caller is not an unrestricted
// one.
var ErrCallerUnresolved = errors.New("caller credential names a stage run that cannot be resolved")

// TaskID returns the task the caller's credential is bound to.
//
// Three outcomes, and the difference between the last two is the whole point:
//   - ("", nil)    — no stage run on the credential: a user key, unrestricted.
//   - (id, nil)    — a stage-run key, confined to that task.
//   - ("", error)  — a stage-run key that cannot be resolved: refuse.
func (r CallerResolver) TaskID(ctx context.Context) (string, error) {
	info := AuthFromContext(ctx)
	if info == nil || info.StageRunID == "" {
		return "", nil
	}
	if r.StageRuns == nil {
		return "", fmt.Errorf("%w: no stage run lookup configured", ErrCallerUnresolved)
	}
	run, err := r.StageRuns.GetByID(ctx, info.StageRunID)
	if err != nil {
		return "", fmt.Errorf("%w: stage run %s: %w", ErrCallerUnresolved, info.StageRunID, err)
	}
	if run.TaskID == "" {
		return "", fmt.Errorf("%w: stage run %s has no task", ErrCallerUnresolved, run.ID)
	}
	return run.TaskID, nil
}
