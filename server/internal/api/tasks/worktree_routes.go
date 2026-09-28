package tasks

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/lx-wnk/kontor/sdk"
	"github.com/lx-wnk/kontor/server/internal/apierr"
	"github.com/lx-wnk/kontor/server/internal/db/ent"
	"github.com/lx-wnk/kontor/server/internal/services"
)

// WorktreeStatusProvider is the subset of services.WorktreeManager the
// handler needs. Keeping it as an interface lets the test layer swap in a
// fake without depending on a real git binary.
type WorktreeStatusProvider interface {
	WorktreeStatus(ctx context.Context, taskID string) (*sdk.WorktreeStatusDTO, error)
	CreateWorktree(ctx context.Context, taskID string) (string, error)
	RemoveWorktree(ctx context.Context, taskID string, force bool) error
}

// getWorktreeStatusHandler answers GET /api/tasks/{id}/worktree with a
// WorktreeStatusDTO, or 204 No Content if the task has no worktree.
func (h *Handler) getWorktreeStatusHandler(w http.ResponseWriter, r *http.Request) error {
	id := chi.URLParam(r, "id")
	if h.worktreeMgr == nil {
		return apierr.NewAppError(http.StatusServiceUnavailable, "worktree status unavailable")
	}
	dto, err := h.worktreeMgr.WorktreeStatus(r.Context(), id)
	if err != nil {
		if ent.IsNotFound(err) {
			return apierr.ErrNotFound
		}
		return fmt.Errorf("worktree_status: %w", err)
	}
	if dto == nil {
		w.WriteHeader(http.StatusNoContent)
		return nil
	}
	return jsonReply(w, http.StatusOK, dto)
}

func (h *Handler) createWorktreeHandler(w http.ResponseWriter, r *http.Request) error {
	id := chi.URLParam(r, "id")
	if h.worktreeMgr == nil {
		return apierr.NewAppError(http.StatusServiceUnavailable, "worktree manager unavailable")
	}
	path, err := h.worktreeMgr.CreateWorktree(r.Context(), id)
	if err != nil {
		if ent.IsNotFound(err) {
			return apierr.ErrNotFound
		}
		return fmt.Errorf("create_worktree: %w", err)
	}
	h.broadcastEnrichedUpdate(r.Context(), id)
	return jsonReply(w, http.StatusOK, map[string]string{"worktreePath": path})
}

func (h *Handler) removeWorktreeHandler(w http.ResponseWriter, r *http.Request) error {
	id := chi.URLParam(r, "id")
	if h.worktreeMgr == nil {
		return apierr.NewAppError(http.StatusServiceUnavailable, "worktree manager unavailable")
	}
	force := r.URL.Query().Get("force") == "true"
	err := h.worktreeMgr.RemoveWorktree(r.Context(), id, force)
	if err != nil {
		switch {
		case errors.Is(err, services.ErrNoWorktree):
			return apierr.NewAppError(http.StatusNotFound, "task has no worktree")
		case errors.Is(err, services.ErrWorktreeDirty):
			return apierr.NewAppError(http.StatusConflict, "worktree has uncommitted changes; pass force=true to remove")
		case ent.IsNotFound(err):
			return apierr.ErrNotFound
		default:
			return fmt.Errorf("remove_worktree: %w", err)
		}
	}
	h.broadcastEnrichedUpdate(r.Context(), id)
	w.WriteHeader(http.StatusNoContent)
	return nil
}
