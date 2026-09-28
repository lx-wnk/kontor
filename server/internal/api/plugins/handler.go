package plugins

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/lx-wnk/kontor/server/internal/apierr"
	"github.com/lx-wnk/kontor/server/internal/plugin"
)

// PluginView is the narrow lifecycle DTO served under /api/plugins. It
// intentionally omits BaseURL/Addr/Command/Env — plugin auth secrets and
// internal addresses must never reach the client (F028).
type PluginView struct {
	ID              string   `json:"id"`
	Name            string   `json:"name"`
	Version         string   `json:"version"`
	State           string   `json:"state"` // discovered|inactive|active
	UpdateAvailable bool     `json:"updateAvailable"`
	Healthy         bool     `json:"healthy"`
	Capabilities    []string `json:"capabilities"`
	HasSettings     bool     `json:"hasSettings"`
}

// controller is the lifecycle behaviour the handler needs; faked in tests and
// implemented by pluginmgmt. Action is one of install|activate|
// deactivate|uninstall.
type controller interface {
	List(ctx context.Context) ([]PluginView, error)
	Transition(ctx context.Context, id, action string) (PluginView, error)
	GetSettings(ctx context.Context, id string) (schema []plugin.SettingField, values map[string]string, err error)
	PutSettings(ctx context.Context, id string, values map[string]string) error
}

// lifecycleActions is the closed set of accepted transition verbs.
var lifecycleActions = map[string]bool{
	"install":    true,
	"activate":   true,
	"deactivate": true,
	"uninstall":  true,
	"update":     true,
}

// LifecycleHandler serves the SP1 lifecycle + settings endpoints under
// /api/plugins. It is independent of the interim #230 enable/disable Handler.
type LifecycleHandler struct {
	ctl controller
}

// NewLifecycle creates a LifecycleHandler backed by the given controller.
func NewLifecycle(ctl controller) *LifecycleHandler { return &LifecycleHandler{ctl: ctl} }

// MountList registers GET /api/plugins for any authenticated user. The list is
// read-only and needed by non-admin users for slot discovery. Mount the write
// endpoints separately under an admin gate.
func (h *LifecycleHandler) MountList(r chi.Router) {
	r.Get("/api/plugins", apierr.ErrorMiddleware(h.list))
}

// Mount registers the lifecycle + settings write routes on r. Callers must apply
// an admin gate before calling Mount.
func (h *LifecycleHandler) Mount(r chi.Router) {
	r.Post("/api/plugins/{id}/{action}", apierr.ErrorMiddleware(h.transition))
	r.Get("/api/plugins/{id}/settings", apierr.ErrorMiddleware(h.getSettings))
	r.Put("/api/plugins/{id}/settings", apierr.ErrorMiddleware(h.putSettings))
}

// classify maps the controller sentinels to 400 and everything else to a plain
// 500 wrap.
func classify(err error, wrap string) error {
	if errors.Is(err, plugin.ErrIllegalTransition) {
		return fmt.Errorf("%w: %s", apierr.ErrConflict, err.Error())
	}
	if errors.Is(err, plugin.ErrUnknownPlugin) || errors.Is(err, plugin.ErrInvalidAction) ||
		errors.Is(err, plugin.ErrUnknownKey) || errors.Is(err, plugin.ErrInvalidValue) {
		return fmt.Errorf("%w: %s", apierr.ErrBadRequest, err.Error())
	}
	return fmt.Errorf("%s: %w", wrap, err)
}

func (h *LifecycleHandler) list(w http.ResponseWriter, r *http.Request) error {
	views, err := h.ctl.List(r.Context())
	if err != nil {
		return fmt.Errorf("plugins.lifecycle.list: %w", err)
	}
	if views == nil {
		views = []PluginView{}
	}
	for i := range views {
		if views[i].Capabilities == nil {
			views[i].Capabilities = []string{}
		}
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-cache")
	return json.NewEncoder(w).Encode(views)
}

func (h *LifecycleHandler) transition(w http.ResponseWriter, r *http.Request) error {
	id := chi.URLParam(r, "id")
	if id == "" {
		return fmt.Errorf("%w: plugin id is required", apierr.ErrBadRequest)
	}
	if !plugin.ValidID(id) {
		return fmt.Errorf("%w: invalid plugin id %q", apierr.ErrBadRequest, id)
	}
	action := chi.URLParam(r, "action")
	if !lifecycleActions[action] {
		return fmt.Errorf("%w: invalid action %q", apierr.ErrBadRequest, action)
	}
	view, err := h.ctl.Transition(r.Context(), id, action)
	if err != nil {
		return classify(err, "plugins.lifecycle.transition")
	}
	w.Header().Set("Content-Type", "application/json")
	return json.NewEncoder(w).Encode(view)
}

type settingsResponse struct {
	Schema []plugin.SettingField `json:"schema"`
	Values map[string]string     `json:"values"`
}

func (h *LifecycleHandler) getSettings(w http.ResponseWriter, r *http.Request) error {
	id := chi.URLParam(r, "id")
	if id == "" {
		return fmt.Errorf("%w: plugin id is required", apierr.ErrBadRequest)
	}
	if !plugin.ValidID(id) {
		return fmt.Errorf("%w: invalid plugin id %q", apierr.ErrBadRequest, id)
	}
	schema, values, err := h.ctl.GetSettings(r.Context(), id)
	if err != nil {
		return classify(err, "plugins.lifecycle.getSettings")
	}
	if schema == nil {
		schema = []plugin.SettingField{}
	}
	if values == nil {
		values = map[string]string{}
	}
	w.Header().Set("Content-Type", "application/json")
	return json.NewEncoder(w).Encode(settingsResponse{Schema: schema, Values: values})
}

func (h *LifecycleHandler) putSettings(w http.ResponseWriter, r *http.Request) error {
	id := chi.URLParam(r, "id")
	if id == "" {
		return fmt.Errorf("%w: plugin id is required", apierr.ErrBadRequest)
	}
	if !plugin.ValidID(id) {
		return fmt.Errorf("%w: invalid plugin id %q", apierr.ErrBadRequest, id)
	}
	var body struct {
		Values map[string]string `json:"values"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		return fmt.Errorf("%w: invalid JSON", apierr.ErrBadRequest)
	}
	if err := h.ctl.PutSettings(r.Context(), id, body.Values); err != nil {
		return classify(err, "plugins.lifecycle.putSettings")
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}
