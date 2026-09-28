package refine

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/lx-wnk/kontor/server/internal/db/ent"
	"github.com/lx-wnk/kontor/server/internal/db/repo"
	"github.com/lx-wnk/kontor/server/internal/refine"
	"github.com/lx-wnk/kontor/server/internal/worktree"
)

// ConfirmDeps are the subset of Deps required by Confirm.
type ConfirmDeps struct {
	Turns     repo.RefinementTurnRepo
	Tasks     repo.TaskRepo
	StageRuns repo.StageRunRepo
	Advance   func(ctx context.Context, taskID string) error
	// Revoke invalidates the concept stage run's MCP credentials once it is
	// marked done — its agent is gone by then. Nil disables revocation; a
	// failure logs and never blocks confirmation.
	Revoke func(ctx context.Context, stageRunID string) error
}

// Confirm freezes the concept from refinement turns onto the task and advances
// it past the backlog stage. It is the shared implementation called by both
// the REST handler and the MCP approve_spec tool.
// Returns the updated task on success.
func Confirm(ctx context.Context, d ConfirmDeps, taskID string) (*ent.Task, error) {
	ready := "ready"
	update := repo.UpdateTaskInput{CurrentStage: &ready}
	if d.Turns != nil {
		if turns, err := d.Turns.ListForTask(ctx, taskID, 0); err == nil {
			if concept, ok := refine.ExtractConcept(turns); ok {
				if meta := concept.Metadata(); len(meta) > 0 {
					update.Metadata = meta
				}
				if concept.RefinedTitle != "" {
					update.Title = &concept.RefinedTitle
				}
				if concept.SourceBranch != "" {
					n, err := d.Tasks.CountActiveBySourceBranch(ctx, concept.SourceBranch, taskID)
					if err != nil {
						return nil, err
					}
					if n > 0 {
						return nil, fmt.Errorf("source_branch %q already in use by another active task", concept.SourceBranch)
					}
					if task, terr := d.Tasks.GetByID(ctx, taskID); terr == nil && task != nil {
						if held, gErr := worktree.BranchCheckedOutAt(ctx, task.Cwd, concept.SourceBranch); gErr == nil && held != "" {
							return nil, fmt.Errorf("source_branch %q already checked out at %s", concept.SourceBranch, held)
						}
					}
					update.SourceBranch = &concept.SourceBranch
				}
				if concept.TargetBranch != "" {
					update.TargetBranch = &concept.TargetBranch
				}
			}
		}
	}

	phase := "confirmed"
	if _, err := d.Turns.Create(ctx, repo.CreateTurnInput{
		TaskID:  taskID,
		Role:    "assistant",
		Content: "confirmed",
		Phase:   &phase,
	}); err != nil {
		return nil, err
	}

	if d.StageRuns != nil {
		now := time.Now()
		done := "done"
		if sr, err := d.StageRuns.GetLatestByTaskAndStage(ctx, taskID, "backlog"); err == nil && sr != nil {
			_, _ = d.StageRuns.Update(ctx, sr.ID, repo.UpdateStageRunInput{
				Status:  &done,
				EndedAt: &now,
			})
			if d.Revoke != nil {
				if rerr := d.Revoke(ctx, sr.ID); rerr != nil {
					slog.Warn("refine: revoking stage-run credentials failed", "stageRun", sr.ID, "err", rerr)
				}
			}
		}
	}
	// The task update is the load-bearing write (stage move + concept freeze);
	// fail loudly rather than advancing a task whose spec never landed.
	if d.Tasks != nil {
		if _, err := d.Tasks.Update(ctx, taskID, update); err != nil {
			return nil, fmt.Errorf("confirm: apply concept to task: %w", err)
		}
	}
	if d.Advance != nil {
		_ = d.Advance(ctx, taskID)
	}

	return d.Tasks.GetByID(ctx, taskID)
}
