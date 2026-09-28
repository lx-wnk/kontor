package tasks

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/lx-wnk/kontor/server/internal/apierr"
)

// resumeStage reaps any dead run on the current stage and re-queues the task,
// carrying the user's optional instruction so the picker spawns with it applied.
func (h *Handler) resumeStage(w http.ResponseWriter, r *http.Request) error {
	id := chi.URLParam(r, "id")
	if _, err := h.taskRepo.GetByID(r.Context(), id); err != nil {
		return apierr.ErrNotFound
	}
	var body struct {
		AdditionalPrompt string `json:"additionalPrompt"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	sr, err := h.orchestrator.RequeueForUser(r.Context(), id, body.AdditionalPrompt)
	if err != nil {
		return fmt.Errorf("resume_stage: %w", err)
	}
	if sr == nil {
		return apierr.NewAppError(http.StatusConflict, "task cannot be resumed (terminal or missing)")
	}
	h.broadcastEnrichedUpdate(r.Context(), id)
	return jsonReply(w, http.StatusAccepted, toStageRunResponse(sr))
}
