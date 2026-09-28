// Package presets implements GET/DELETE /api/settings/permission-presets.
package presets

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/lx-wnk/kontor/server/internal/apierr"
	"github.com/lx-wnk/kontor/server/internal/auth"
	"github.com/lx-wnk/kontor/server/internal/db/repo"
)

// Handler handles the /api/settings/permission-presets endpoints.
type Handler struct {
	repo repo.PermissionPresetRepo
}

// NewHandler creates a Handler.
func NewHandler(r repo.PermissionPresetRepo) *Handler {
	return &Handler{repo: r}
}

// Mount registers all permission-preset routes on r.
// When JWT auth is active, handlers use the authenticated user ID to scope presets.
// When bypass auth is enabled (DASHBOARD_AUTH=none), userID is nil and only global presets are accessible.
func (h *Handler) Mount(r chi.Router) {
	r.Get("/api/settings/permission-presets", apierr.ErrorMiddleware(h.list))
	r.Delete("/api/settings/permission-presets", apierr.ErrorMiddleware(h.delete))
}

func (h *Handler) list(w http.ResponseWriter, r *http.Request) error {
	var userID *string
	payload, ok := auth.PayloadFromContext(r.Context())
	if ok {
		userID = &payload.Sub
	}
	// If !ok, userID is nil (bypass mode — will return global presets)
	summaries, err := h.repo.ListSummaries(r.Context(), userID)
	if err != nil {
		return fmt.Errorf("presets.list: %w", err)
	}
	if summaries == nil {
		summaries = []repo.PresetProjectSummary{}
	}
	w.Header().Set("Content-Type", "application/json")
	return json.NewEncoder(w).Encode(summaries)
}

func (h *Handler) delete(w http.ResponseWriter, r *http.Request) error {
	var body struct {
		Cwd string `json:"cwd"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		return apierr.NewAppError(http.StatusBadRequest, "invalid JSON body")
	}
	if body.Cwd == "" {
		return apierr.NewAppError(http.StatusBadRequest, "cwd is required")
	}

	var userID *string
	payload, ok := auth.PayloadFromContext(r.Context())
	if ok {
		userID = &payload.Sub
	}
	// If !ok, userID is nil (bypass mode — will delete global presets for the cwd)
	if err := h.repo.DeleteForProject(r.Context(), userID, body.Cwd); err != nil {
		return fmt.Errorf("presets.delete: %w", err)
	}
	w.Header().Set("Content-Type", "application/json")
	return json.NewEncoder(w).Encode(map[string]bool{"ok": true})
}
