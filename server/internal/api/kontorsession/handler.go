// Package kontorsession serves the Kontor session's HTTP surface.
package kontorsession

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/lx-wnk/kontor/server/internal/apierr"
)

// sessions is the test seam over *kontorsession.Service.
type sessions interface {
	Current(ctx context.Context) (int, bool, error)
	Start(ctx context.Context, prompt string) (int, bool, error)
	Renew(ctx context.Context, prompt string) (int, error)
	End(ctx context.Context, reason string) error
}

type Handler struct{ svc sessions }

func New(s sessions) *Handler { return &Handler{svc: s} }

func (h *Handler) Mount(r chi.Router) {
	r.Get("/api/kontor-session", apierr.ErrorMiddleware(h.get))
	r.Post("/api/kontor-session", apierr.ErrorMiddleware(h.start))
	r.Post("/api/kontor-session/renew", apierr.ErrorMiddleware(h.renew))
	r.Delete("/api/kontor-session", apierr.ErrorMiddleware(h.end))
}

type view struct {
	PID *int `json:"pid"`
	// Started is omitted on GET, where the field is meaningless.
	Started *bool `json:"started,omitempty"`
}

func (h *Handler) get(w http.ResponseWriter, r *http.Request) error {
	pid, ok, err := h.svc.Current(r.Context())
	if err != nil {
		return err
	}
	if !ok {
		apierr.WriteJSON(w, http.StatusOK, view{})
		return nil
	}
	apierr.WriteJSON(w, http.StatusOK, view{PID: &pid})
	return nil
}

func (h *Handler) start(w http.ResponseWriter, r *http.Request) error {
	prompt, err := decodePrompt(w, r, true)
	if err != nil {
		return err
	}
	pid, started, err := h.svc.Start(r.Context(), prompt)
	if err != nil {
		return apierr.NewAppError(http.StatusInternalServerError, "kontor session: "+err.Error())
	}
	apierr.WriteJSON(w, http.StatusOK, view{PID: &pid, Started: &started})
	return nil
}

func (h *Handler) renew(w http.ResponseWriter, r *http.Request) error {
	prompt, err := decodePrompt(w, r, false)
	if err != nil {
		return err
	}
	pid, err := h.svc.Renew(r.Context(), prompt)
	if err != nil {
		return apierr.NewAppError(http.StatusInternalServerError, "kontor session: "+err.Error())
	}
	started := true
	apierr.WriteJSON(w, http.StatusOK, view{PID: &pid, Started: &started})
	return nil
}

func decodePrompt(w http.ResponseWriter, r *http.Request, required bool) (string, error) {
	var body struct {
		Prompt string `json:"prompt"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&body); err != nil {
		return "", apierr.NewAppError(http.StatusBadRequest, "invalid JSON body")
	}
	if required && strings.TrimSpace(body.Prompt) == "" {
		return "", apierr.NewAppError(http.StatusBadRequest, "prompt is required")
	}
	return body.Prompt, nil
}

func (h *Handler) end(w http.ResponseWriter, r *http.Request) error {
	if err := h.svc.End(r.Context(), "ended by operator"); err != nil {
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}
