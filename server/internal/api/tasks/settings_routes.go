package tasks

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"

	"github.com/lx-wnk/kontor/server/internal/apierr"
)

// listGlobalAudit handles GET /api/audit. Admin only in TypeScript; Go skips the
// admin check since roles are optional in this build.
func (h *Handler) listGlobalAudit(w http.ResponseWriter, r *http.Request) error {
	limit := 100
	offset := 0
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			if n > 500 {
				n = 500
			}
			limit = n
		}
	}
	if v := r.URL.Query().Get("offset"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			offset = n
		}
	}
	logs, err := h.auditRepo.ListAll(r.Context(), limit, offset)
	if err != nil {
		return fmt.Errorf("global_audit.list: %w", err)
	}
	return jsonReply(w, http.StatusOK, toAuditEntryResponses(logs))
}

// getWebhookHMAC handles GET /api/settings/webhook-hmac.
func (h *Handler) getWebhookHMAC(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	all, err := h.cfgRepo.GetAll(ctx)
	if err != nil {
		return fmt.Errorf("webhook_hmac.get: %w", err)
	}
	enabled := all["webhook_hmac_enabled"] == "true"
	hasSecret := all["webhook_hmac_secret"] != ""
	return jsonReply(w, http.StatusOK, map[string]any{
		"enabled":   enabled,
		"hasSecret": hasSecret,
	})
}

// putWebhookHMAC handles POST /api/settings/webhook-hmac.
func (h *Handler) putWebhookHMAC(w http.ResponseWriter, r *http.Request) error {
	var body struct {
		Enabled bool    `json:"enabled"`
		Secret  *string `json:"secret"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		return apierr.NewAppError(http.StatusBadRequest, "invalid JSON body")
	}
	ctx := r.Context()
	enabledStr := "false"
	if body.Enabled {
		enabledStr = "true"
	}
	if err := h.cfgRepo.Set(ctx, "webhook_hmac_enabled", enabledStr); err != nil {
		return fmt.Errorf("webhook_hmac.put: %w", err)
	}
	if body.Enabled {
		secret := ""
		if body.Secret != nil && len(*body.Secret) >= 32 {
			secret = *body.Secret
		} else {
			b := make([]byte, 32)
			if _, err := rand.Read(b); err != nil {
				return fmt.Errorf("webhook_hmac.put: rand: %w", err)
			}
			secret = hex.EncodeToString(b)
		}
		if err := h.cfgRepo.Set(ctx, "webhook_hmac_secret", secret); err != nil {
			return fmt.Errorf("webhook_hmac.put: %w", err)
		}
		return jsonReply(w, http.StatusOK, map[string]any{"enabled": true, "secret": secret})
	}
	return jsonReply(w, http.StatusOK, map[string]any{"enabled": false})
}
