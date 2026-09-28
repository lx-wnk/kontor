package providers

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/lx-wnk/kontor/server/internal/apierr"
	"github.com/lx-wnk/kontor/server/internal/provider"
	"github.com/lx-wnk/kontor/server/internal/providersettings"
)

// Handler serves provider enable-state for the Settings UI.
type Handler struct {
	registry *provider.Registry
	settings *providersettings.Service
}

// NewHandler builds the providers Handler.
func NewHandler(reg *provider.Registry, svc *providersettings.Service) *Handler {
	return &Handler{registry: reg, settings: svc}
}

// providerView is the camelCase JSON shape for a known provider.
type providerView struct {
	ID               string `json:"id"`
	DisplayName      string `json:"displayName"`
	Enabled          bool   `json:"enabled"`
	ConfigDirPresent bool   `json:"configDirPresent"`
}

// List returns every known provider with its enable-state. GET /api/providers
func (h *Handler) List(w http.ResponseWriter, r *http.Request) error {
	infos := h.registry.KnownProviders()
	out := make([]providerView, 0, len(infos))
	for _, in := range infos {
		out = append(out, providerView{
			ID:               in.ID,
			DisplayName:      in.DisplayName,
			Enabled:          h.settings.IsEnabled(in.ID),
			ConfigDirPresent: in.ConfigDirPresent,
		})
	}
	w.Header().Set("Content-Type", "application/json")
	return json.NewEncoder(w).Encode(out)
}

// Patch toggles a provider's enable-state. PATCH /api/providers/{id}
func (h *Handler) Patch(w http.ResponseWriter, r *http.Request) error {
	id := chi.URLParam(r, "id")
	if id == "" {
		return fmt.Errorf("%w: provider id is required", apierr.ErrBadRequest)
	}
	known := false
	var info provider.ProviderInfo
	for _, in := range h.registry.KnownProviders() {
		if in.ID == id {
			known, info = true, in
			break
		}
	}
	if !known {
		return fmt.Errorf("%w: unknown provider %q", apierr.ErrBadRequest, id)
	}
	var body struct {
		Enabled bool `json:"enabled"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		return fmt.Errorf("%w: invalid JSON", apierr.ErrBadRequest)
	}
	if _, err := h.settings.Set(r.Context(), id, body.Enabled); err != nil {
		return fmt.Errorf("providers.Patch: %w", err)
	}
	w.Header().Set("Content-Type", "application/json")
	return json.NewEncoder(w).Encode(providerView{
		ID:               id,
		DisplayName:      info.DisplayName,
		Enabled:          h.settings.IsEnabled(id),
		ConfigDirPresent: info.ConfigDirPresent,
	})
}
