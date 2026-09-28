// Package skills exposes the HTTP trigger for skill materialization: the one
// route in this server that writes into the user's own config directories.
package skills

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sync/atomic"

	"github.com/go-chi/chi/v5"

	"github.com/lx-wnk/kontor/server/internal/apierr"
	"github.com/lx-wnk/kontor/server/internal/materializer"
)

// maxRequestBytes caps the request body. The body carries one boolean.
const maxRequestBytes = 4096

// Handler serves POST /api/skills/materialize.
type Handler struct {
	m *materializer.Materializer
	// running single-flights the route. This is not redundant with the node
	// lease: coord_lock.Acquire is re-entrant for the same owner
	// (repo/coord_lock_repo.go:73) and the lease owner is per process, so two
	// concurrent requests in one server would both hold it and race each other
	// into the same files. The lease keeps two *instances* apart; this keeps
	// two requests apart. Same guard api/obsidian uses, for the same reason.
	running atomic.Bool
}

// NewHandler creates a Handler.
func NewHandler(m *materializer.Materializer) *Handler { return &Handler{m: m} }

// Mount registers the /api/skills/* routes on r.
func (h *Handler) Mount(r chi.Router) {
	r.Post("/api/skills/materialize", apierr.ErrorMiddleware(h.materialize))
}

type materializeRequest struct {
	// DryRun is a pointer so an absent field is distinguishable from an
	// explicit false. Absent means true: the default of a route that can
	// overwrite a hand-edited file is the one that writes nothing.
	DryRun *bool `json:"dryRun"`
}

func (h *Handler) materialize(w http.ResponseWriter, r *http.Request) error {
	dryRun := true
	var req materializeRequest
	err := json.NewDecoder(io.LimitReader(r.Body, maxRequestBytes)).Decode(&req)
	switch {
	case errors.Is(err, io.EOF):
		// An empty body is a dry run, the same as an absent field.
	case err != nil:
		return apierr.NewAppError(http.StatusBadRequest, "invalid request body")
	case req.DryRun != nil:
		dryRun = *req.DryRun
	}

	if !h.running.CompareAndSwap(false, true) {
		return apierr.NewAppError(http.StatusConflict, "a materialization run is already running")
	}
	defer h.running.Store(false)

	rep, err := h.m.Run(r.Context(), dryRun)
	if err != nil {
		return fmt.Errorf("skills.materialize: %w", err)
	}

	// The report types carry their own camelCase json tags. Unlike the
	// registry and memory routes there is no ent entity here to re-encode:
	// Report exists only to be reported, so it has no schema columns to
	// republish by accident.
	apierr.WriteJSON(w, http.StatusOK, rep)
	return nil
}
