// Package coord exposes read-only coordination state (scratchpads and locks) over HTTP.
package coord

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/lx-wnk/kontor/server/internal/apierr"
	"github.com/lx-wnk/kontor/server/internal/db/repo"
)

// Handler serves GET /api/coord/{namespace}/scratchpads and GET /api/coord/{namespace}/locks.
type Handler struct {
	scratch repo.ScratchpadRepo
	locks   repo.CoordLockRepo
}

// New constructs a Handler with the given repos.
func New(scratch repo.ScratchpadRepo, locks repo.CoordLockRepo) *Handler {
	return &Handler{scratch: scratch, locks: locks}
}

// Mount registers the coordination read routes on r.
func (h *Handler) Mount(r chi.Router) {
	r.Get("/api/coord/{namespace}/scratchpads", apierr.ErrorMiddleware(h.listScratchpads))
	r.Get("/api/coord/{namespace}/locks", apierr.ErrorMiddleware(h.listLocks))
}

func (h *Handler) listScratchpads(w http.ResponseWriter, r *http.Request) error {
	ns := chi.URLParam(r, "namespace")
	rows, err := h.scratch.List(r.Context(), ns)
	if err != nil {
		return fmt.Errorf("coord.scratchpads: %w", err)
	}
	return jsonReply(w, http.StatusOK, map[string]any{"entries": rows})
}

func (h *Handler) listLocks(w http.ResponseWriter, r *http.Request) error {
	ns := chi.URLParam(r, "namespace")
	rows, err := h.locks.ListActive(r.Context(), ns)
	if err != nil {
		return fmt.Errorf("coord.locks: %w", err)
	}
	return jsonReply(w, http.StatusOK, map[string]any{"locks": rows})
}

func jsonReply(w http.ResponseWriter, status int, v any) error {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	return json.NewEncoder(w).Encode(v)
}
