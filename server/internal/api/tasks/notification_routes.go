package tasks

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/lx-wnk/kontor/server/internal/apierr"
)

// cgnatBlock is the 100.64.0.0/10 CGNAT range (RFC 6598).
// Constructed once at package init to avoid repeated allocations inside the
// address-validation loop.
var cgnatBlock = func() *net.IPNet {
	_, n, _ := net.ParseCIDR("100.64.0.0/10")
	return n
}()

// validateWebhookURL rejects loopback/private/link-local/CGNAT/multicast/unspecified
// targets to prevent SSRF. Only http and https schemes are allowed.
func validateWebhookURL(raw string) error {
	if raw == "" {
		return nil // empty = no webhook configured, always valid
	}
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("invalid URL: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("webhook_url must use http or https scheme")
	}
	host := u.Hostname()
	addrs, err := net.LookupHost(host)
	if err != nil {
		// Unresolvable host — block it; legitimate webhooks must be DNS-resolvable.
		return fmt.Errorf("webhook_url host could not be resolved: %w", err)
	}
	for _, addr := range addrs {
		ip := net.ParseIP(addr)
		if ip == nil {
			continue
		}
		if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() ||
			cgnatBlock.Contains(ip) || ip.IsUnspecified() || ip.IsMulticast() {
			return errors.New("webhook_url must not point to a private, CGNAT, or reserved address")
		}
	}
	return nil
}

var validChannels = map[string]bool{
	"email":   true,
	"webhook": true,
	"browser": true,
	"system":  true,
}

var validEventTypes = map[string]bool{
	"on_hold":           true,
	"approval_needed":   true,
	"completed":         true,
	"failed":            true,
	"budget_exceeded":   true,
	"iteration_warning": true,
}

// validConfigKeys is the allowlist of accepted notification config key names.
// Any key not in this set is rejected with 400.
var validConfigKeys = map[string]bool{
	"webhook_url":          true,
	"webhook_hmac_enabled": true,
	"webhook_hmac_secret":  true,
}

const notifPrefPrefix = "notif:pref:"
const notifCfgPrefix = "notif:config:"

type notifPref struct {
	EventType string   `json:"eventType"`
	Channels  []string `json:"channels"`
	Enabled   bool     `json:"enabled"`
}

// approvalPushWanted reports whether the operator enabled browser notifications
// for approval_needed in Settings → Notifications.
func (h *Handler) approvalPushWanted(ctx context.Context) bool {
	raw := h.cfgRepo.GetString(ctx, notifPrefPrefix+"approval_needed", "")
	if raw == "" {
		return false
	}
	var p notifPref
	if err := json.Unmarshal([]byte(raw), &p); err != nil {
		return false
	}
	return p.Enabled && slices.Contains(p.Channels, "browser")
}

func (h *Handler) listNotificationPreferences(w http.ResponseWriter, r *http.Request) error {
	all, err := h.cfgRepo.GetAll(r.Context())
	if err != nil {
		return fmt.Errorf("notif_prefs.list: %w", err)
	}
	prefs := make([]notifPref, 0)
	for k, v := range all {
		if !strings.HasPrefix(k, notifPrefPrefix) {
			continue
		}
		et := strings.TrimPrefix(k, notifPrefPrefix)
		var p notifPref
		if err2 := json.Unmarshal([]byte(v), &p); err2 != nil {
			continue
		}
		p.EventType = et
		prefs = append(prefs, p)
	}
	return jsonReply(w, http.StatusOK, prefs)
}

func (h *Handler) putNotificationPreference(w http.ResponseWriter, r *http.Request) error {
	eventType := chi.URLParam(r, "eventType")
	if !validEventTypes[eventType] {
		return apierr.NewAppError(http.StatusBadRequest, "unknown eventType")
	}
	var body struct {
		Channels []string `json:"channels"`
		Enabled  *bool    `json:"enabled"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, 4096)
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		return apierr.NewAppError(http.StatusBadRequest, "invalid JSON body")
	}
	if body.Channels == nil {
		return apierr.NewAppError(http.StatusBadRequest, "channels must be an array")
	}
	for _, ch := range body.Channels {
		if !validChannels[ch] {
			return apierr.NewAppError(http.StatusBadRequest, "unknown channel: "+ch)
		}
	}
	enabled := true
	if body.Enabled != nil {
		enabled = *body.Enabled
	}
	pref := notifPref{EventType: eventType, Channels: body.Channels, Enabled: enabled}
	encoded, _ := json.Marshal(pref)
	if err := h.cfgRepo.Set(r.Context(), notifPrefPrefix+eventType, string(encoded)); err != nil {
		return fmt.Errorf("notif_prefs.put: %w", err)
	}
	return jsonReply(w, http.StatusOK, pref)
}

func (h *Handler) getNotificationConfig(w http.ResponseWriter, r *http.Request) error {
	all, err := h.cfgRepo.GetAll(r.Context())
	if err != nil {
		return fmt.Errorf("notif_config.get: %w", err)
	}
	result := make(map[string]string)
	for k, v := range all {
		if strings.HasPrefix(k, notifCfgPrefix) {
			result[strings.TrimPrefix(k, notifCfgPrefix)] = v
		}
	}
	return jsonReply(w, http.StatusOK, result)
}

func (h *Handler) putNotificationConfig(w http.ResponseWriter, r *http.Request) error {
	var updates map[string]any
	r.Body = http.MaxBytesReader(w, r.Body, 4096)
	if err := json.NewDecoder(r.Body).Decode(&updates); err != nil {
		return apierr.NewAppError(http.StatusBadRequest, "invalid JSON body")
	}
	for k, v := range updates {
		if !validConfigKeys[k] {
			return apierr.NewAppError(http.StatusBadRequest, "unknown config key: "+k)
		}
		var val string
		switch tv := v.(type) {
		case string:
			val = tv
		case nil:
			val = ""
		default:
			val = fmt.Sprintf("%v", tv)
		}
		if k == "webhook_url" {
			if err := validateWebhookURL(val); err != nil {
				return apierr.NewAppError(http.StatusBadRequest, "webhook_url: "+err.Error())
			}
		}
		if err := h.cfgRepo.Set(r.Context(), notifCfgPrefix+k, val); err != nil {
			return fmt.Errorf("notif_config.put: %w", err)
		}
	}
	return h.getNotificationConfig(w, r)
}
