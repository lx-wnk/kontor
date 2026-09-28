package settings

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/lx-wnk/kontor/server/internal/apierr"
	"github.com/lx-wnk/kontor/server/internal/secretbox"
	settingssvc "github.com/lx-wnk/kontor/server/internal/settings"
)

// Handler serves the DB-backed settings registry.
type Handler struct{ svc *settingssvc.Service }

// NewHandler builds the settings Handler.
func NewHandler(svc *settingssvc.Service) *Handler { return &Handler{svc: svc} }

// MountRead registers the read-only settings route on r.
func (h *Handler) MountRead(r chi.Router) {
	r.Get("/api/settings", apierr.ErrorMiddleware(h.list))
}

// MountWrite registers PATCH /api/settings/{key}: mounted in the protected
// group (loopback host, same-origin for mutations, session auth unless
// bypassed), no admin gate.
func (h *Handler) MountWrite(r chi.Router) {
	r.Patch("/api/settings/{key}", apierr.ErrorMiddleware(h.patch))
}

type settingView struct {
	Key      string   `json:"key"`
	Type     string   `json:"type"`
	Value    string   `json:"value"`
	Default  string   `json:"default"`
	Apply    string   `json:"apply"`
	Category string   `json:"category"`
	Enum     []string `json:"enum,omitempty"`
}

func (h *Handler) list(w http.ResponseWriter, _ *http.Request) error {
	eff := h.svc.Effective()
	defs := settingssvc.All()
	out := make([]settingView, 0, len(defs))
	for _, d := range defs {
		out = append(out, settingView{
			Key:      d.Key,
			Type:     string(d.Type),
			Value:    eff[d.Key],
			Default:  d.Default,
			Apply:    string(d.Apply),
			Category: d.Category,
			Enum:     d.Enum,
		})
	}
	w.Header().Set("Content-Type", "application/json")
	return json.NewEncoder(w).Encode(out)
}

func (h *Handler) patch(w http.ResponseWriter, r *http.Request) error {
	key := chi.URLParam(r, "key")
	def, ok := settingssvc.Lookup(key)
	if !ok {
		return fmt.Errorf("%w: unknown setting %q", apierr.ErrBadRequest, key)
	}
	var body struct {
		Value string `json:"value"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		return fmt.Errorf("%w: invalid JSON", apierr.ErrBadRequest)
	}
	if err := h.svc.Set(r.Context(), key, body.Value); err != nil {
		var verr *settingssvc.ValidationError
		if errors.As(err, &verr) {
			return fmt.Errorf("%w: %s", apierr.ErrBadRequest, err.Error())
		}
		return fmt.Errorf("settings.patch: %w", err)
	}
	// A secret value is never echoed back, even the one just submitted: the
	// caller already has it, and a response body is more places for it to
	// leak (logs, proxies, devtools history) than it needs to be. An empty
	// value is the exception: it cleared the secret (settings.Service.Set),
	// so masking it would report a configured secret where there is none.
	respValue := body.Value
	if def.Secret && respValue != "" {
		respValue = secretbox.MaskedSentinel
	}
	w.Header().Set("Content-Type", "application/json")
	return json.NewEncoder(w).Encode(map[string]string{
		"key":     key,
		"value":   respValue,
		"applied": string(h.svc.ApplyOf(key)),
	})
}
