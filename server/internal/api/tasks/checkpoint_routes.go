package tasks

import (
	"context"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/lx-wnk/kontor/server/internal/apierr"
	"github.com/lx-wnk/kontor/server/internal/checkpoint"
)

// CheckpointServiceIface is the narrow surface the checkpoint routes consume.
// *checkpoint.Service satisfies it.
type CheckpointServiceIface interface {
	List(ctx context.Context, taskID string) ([]checkpoint.CheckpointView, error)
	Revert(ctx context.Context, taskID, checkpointID, worktreePath string) error
}

func (h *Handler) listCheckpoints(w http.ResponseWriter, r *http.Request) error {
	id := chi.URLParam(r, "id")
	views, err := h.checkpointSvc.List(r.Context(), id)
	if err != nil {
		return err
	}
	if views == nil {
		views = []checkpoint.CheckpointView{}
	}
	return jsonReply(w, http.StatusOK, views)
}

func (h *Handler) revertCheckpoint(w http.ResponseWriter, r *http.Request) error {
	id := chi.URLParam(r, "id")
	cpID := chi.URLParam(r, "cpId")

	task, err := h.taskRepo.GetByID(r.Context(), id)
	if err != nil || task == nil {
		return apierr.ErrNotFound
	}
	if task.WorktreePath == nil || *task.WorktreePath == "" {
		return apierr.NewAppError(http.StatusConflict, "task has no active worktree")
	}
	if err := h.checkpointSvc.Revert(r.Context(), id, cpID, *task.WorktreePath); err != nil {
		return err
	}
	h.broadcastEnrichedUpdate(r.Context(), id)
	return jsonReply(w, http.StatusOK, map[string]string{"status": "reverted"})
}
