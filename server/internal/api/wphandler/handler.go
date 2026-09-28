// Package wphandler implements Web Push VAPID and subscription endpoints.
package wphandler

import (
	"encoding/json"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/lx-wnk/kontor/server/internal/apierr"
	"github.com/lx-wnk/kontor/server/internal/db/rawrepo"
	"github.com/lx-wnk/kontor/server/internal/webpush"
)

// Handler handles VAPID key management and subscription registration endpoints.
type Handler struct {
	svc *webpush.Service
}

// NewHandler returns a Handler backed by svc.
func NewHandler(svc *webpush.Service) *Handler {
	return &Handler{svc: svc}
}

// Mount registers all Web Push routes on r.
// All routes require JWT auth — mount inside a protected group.
func (h *Handler) Mount(r chi.Router) {
	r.Post("/api/settings/webpush/vapid", apierr.ErrorMiddleware(h.generateVAPID))
	r.Get("/api/settings/webpush/vapid", apierr.ErrorMiddleware(h.getVAPID))
	r.Post("/api/settings/webpush/subscribe", apierr.ErrorMiddleware(h.subscribe))
}

// POST /api/settings/webpush/vapid — generate VAPID keys (idempotent).
func (h *Handler) generateVAPID(w http.ResponseWriter, r *http.Request) error {

	var body struct {
		Subject string `json:"subject"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		// Body is optional — a missing or empty body is not an error.
		body.Subject = ""
	}

	pubKey, alreadyExisted, err := h.svc.GenerateVAPIDKeys(r.Context(), body.Subject)
	if err != nil {
		return err
	}

	w.Header().Set("Content-Type", "application/json")
	return json.NewEncoder(w).Encode(map[string]any{
		"publicKey":        pubKey,
		"alreadyGenerated": alreadyExisted,
	})
}

// GET /api/settings/webpush/vapid — return public VAPID key.
func (h *Handler) getVAPID(w http.ResponseWriter, r *http.Request) error {

	pubKey, found, err := h.svc.GetPublicKey(r.Context())
	if err != nil {
		return err
	}
	if !found {
		return apierr.NewAppError(http.StatusNotFound, "VAPID keys not yet generated")
	}

	w.Header().Set("Content-Type", "application/json")
	return json.NewEncoder(w).Encode(map[string]string{"publicKey": pubKey})
}

// POST /api/settings/webpush/subscribe — register a browser push subscription.
func (h *Handler) subscribe(w http.ResponseWriter, r *http.Request) error {

	var body struct {
		Endpoint string `json:"endpoint"`
		Keys     struct {
			P256dh string `json:"p256dh"`
			Auth   string `json:"auth"`
		} `json:"keys"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		return apierr.NewAppError(http.StatusBadRequest, "invalid JSON body")
	}
	if body.Endpoint == "" {
		return apierr.NewAppError(http.StatusBadRequest, "endpoint is required")
	}
	if body.Keys.P256dh == "" {
		return apierr.NewAppError(http.StatusBadRequest, "keys.p256dh is required")
	}
	if body.Keys.Auth == "" {
		return apierr.NewAppError(http.StatusBadRequest, "keys.auth is required")
	}

	sub := rawrepo.PushSubscription{
		Endpoint: body.Endpoint,
		P256dh:   body.Keys.P256dh,
		Auth:     body.Keys.Auth,
	}
	if err := h.svc.RegisterSubscription(r.Context(), sub); err != nil {
		return err
	}

	w.Header().Set("Content-Type", "application/json")
	return json.NewEncoder(w).Encode(map[string]bool{"ok": true})
}
