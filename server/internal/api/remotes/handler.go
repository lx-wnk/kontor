// Package remotes implements the /api/remotes REST endpoints.
package remotes

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/lx-wnk/kontor/server/internal/apierr"
	"github.com/lx-wnk/kontor/server/internal/auth"
	"github.com/lx-wnk/kontor/server/internal/db/repo"
	"github.com/lx-wnk/kontor/server/internal/validation"
)

// hashBearerKey returns the SHA-256 hex digest of key, or nil when key is nil.
// The plaintext is never persisted — only the hash is stored.
//
// Note: if a prior build stored plaintext bearer_key values (before SHA-256 hashing was added),
// those rows remain valid but will never match hashed lookups. Re-register the remote to fix.
func hashBearerKey(key *string) *string {
	if key == nil {
		return nil
	}
	sum := sha256.Sum256([]byte(*key))
	h := hex.EncodeToString(sum[:])
	return &h
}

// isSafeRemoteURL returns true when raw is a valid http/https URL that does not
// point to a loopback, link-local, or otherwise blocked host.
func isSafeRemoteURL(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil {
		return false
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return false
	}
	host := u.Hostname()
	if host == "" {
		return false
	}
	// Normalise IPv6 brackets from url.Parse (hostname strips them).
	h := strings.ToLower(host)
	if validation.IsBlockedHost(h) {
		return false
	}
	// Block any IP that net resolves as loopback, link-local,
	// unspecified, multicast, or CGNAT (catches numeric IPv6 like ::ffff:127.0.0.1 etc.).
	if ip := net.ParseIP(h); ip != nil {
		if validation.IsBlockedIP(ip) {
			return false
		}
	}
	return true
}

var connectivityClient = &http.Client{
	Timeout: 15 * time.Second,
	Transport: &http.Transport{
		// validation.SafeDialContext re-validates resolved IPs at connection time
		// to prevent DNS rebinding attacks.
		DialContext: validation.SafeDialContext,
	},
	CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if !isSafeRemoteURL(req.URL.String()) {
			return http.ErrUseLastResponse
		}
		if len(via) >= 3 {
			return errors.New("too many redirects")
		}
		// Strip Authorization on cross-origin redirects to prevent credential leakage.
		if len(via) > 0 && req.URL.Host != via[0].URL.Host {
			req.Header.Del("Authorization")
		}
		return nil
	},
}

// testRemoteConnection attempts GET {baseURL}/api/agents with an optional bearer token.
// Returns true when the server responds (any status code counts as reachable).
func testRemoteConnection(ctx context.Context, baseURL, bearerKey string) bool {
	target := strings.TrimRight(baseURL, "/") + "/api/agents"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return false
	}
	if bearerKey != "" {
		req.Header.Set("Authorization", "Bearer "+bearerKey)
	}
	resp, err := connectivityClient.Do(req)
	if err != nil {
		return false
	}
	_ = resp.Body.Close()
	return true
}

// remoteResponse is the public JSON shape — never includes bearerKey.
type remoteResponse struct {
	ID        string  `json:"id"`
	UserID    string  `json:"userId"`
	URL       string  `json:"url"`
	Name      *string `json:"name"`
	CreatedAt string  `json:"createdAt"`
}

// Handler handles the /api/remotes endpoints.
type Handler struct {
	repo repo.RemoteRegistrationRepo
}

// NewHandler creates a Handler.
func NewHandler(r repo.RemoteRegistrationRepo) *Handler {
	return &Handler{repo: r}
}

// Mount registers all remotes routes on r.
// All routes require JWT auth — they must be mounted inside a protected group.
func (h *Handler) Mount(r chi.Router) {
	r.Get("/api/remotes", apierr.ErrorMiddleware(h.list))
	r.Post("/api/remotes", apierr.ErrorMiddleware(h.create))
	r.Delete("/api/remotes/{id}", apierr.ErrorMiddleware(h.delete))
}

func (h *Handler) list(w http.ResponseWriter, r *http.Request) error {
	payload, ok := auth.PayloadFromContext(r.Context())
	if !ok {
		// Missing payload ⟹ bypass mode (DASHBOARD_AUTH=none); act as local admin.
		payload = auth.BypassPayload()
	}
	regs, err := h.repo.ListForUser(r.Context(), payload.Sub)
	if err != nil {
		return fmt.Errorf("remotes.list: %w", err)
	}
	out := make([]remoteResponse, len(regs))
	for i, reg := range regs {
		out[i] = remoteResponse{
			ID:        reg.ID,
			UserID:    reg.UserID,
			URL:       reg.URL,
			Name:      reg.Name,
			CreatedAt: reg.CreatedAt.UTC().Format(time.RFC3339),
		}
	}
	w.Header().Set("Content-Type", "application/json")
	return json.NewEncoder(w).Encode(out)
}

func (h *Handler) create(w http.ResponseWriter, r *http.Request) error {
	var body struct {
		URL       string  `json:"url"`
		Name      *string `json:"name"`
		BearerKey *string `json:"bearerKey"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		return apierr.NewAppError(http.StatusBadRequest, "invalid JSON body")
	}
	if body.URL == "" {
		return apierr.NewAppError(http.StatusBadRequest, "url is required")
	}
	if !isSafeRemoteURL(body.URL) {
		return apierr.NewAppError(http.StatusBadRequest, "url must be a valid http/https URL pointing to an external host")
	}

	// Connectivity test — non-fatal.
	bearerKey := ""
	if body.BearerKey != nil {
		bearerKey = *body.BearerKey
	}
	connCtx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	connectionOk := testRemoteConnection(connCtx, body.URL, bearerKey)

	payload, ok := auth.PayloadFromContext(r.Context())
	if !ok {
		// Missing payload ⟹ bypass mode (DASHBOARD_AUTH=none); act as local admin.
		payload = auth.BypassPayload()
	}
	existing, err := h.repo.ListForUser(r.Context(), payload.Sub)
	if err != nil {
		return fmt.Errorf("remotes: list for cap check: %w", err)
	}
	if len(existing) >= 50 {
		return apierr.NewAppError(http.StatusBadRequest, "remote registration limit reached (max 50)")
	}
	reg, err := h.repo.Create(r.Context(), repo.CreateRemoteInput{
		UserID:    payload.Sub,
		URL:       body.URL,
		Name:      body.Name,
		BearerKey: hashBearerKey(body.BearerKey), // SEC-05: store SHA-256 hash only, never plaintext
	})
	if err != nil {
		return fmt.Errorf("remotes.create: %w", err)
	}

	resp := struct {
		remoteResponse
		ConnectionOk bool `json:"connectionOk"`
	}{
		remoteResponse: remoteResponse{
			ID:        reg.ID,
			UserID:    reg.UserID,
			URL:       reg.URL,
			Name:      reg.Name,
			CreatedAt: reg.CreatedAt.UTC().Format(time.RFC3339),
		},
		ConnectionOk: connectionOk,
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	return json.NewEncoder(w).Encode(resp)
}

func (h *Handler) delete(w http.ResponseWriter, r *http.Request) error {
	id := chi.URLParam(r, "id")
	payload, ok := auth.PayloadFromContext(r.Context())
	if !ok {
		// Missing payload ⟹ bypass mode (DASHBOARD_AUTH=none); act as local admin.
		payload = auth.BypassPayload()
	}
	found, err := h.repo.Delete(r.Context(), id, payload.Sub)
	if err != nil {
		return fmt.Errorf("remotes.delete: %w", err)
	}
	if !found {
		return apierr.ErrNotFound
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}
