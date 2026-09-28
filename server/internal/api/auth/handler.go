// Package auth provides HTTP handlers for OAuth authentication.
package auth

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/lx-wnk/kontor/server/internal/apierr"
	serverauth "github.com/lx-wnk/kontor/server/internal/auth"
	"github.com/lx-wnk/kontor/server/internal/db/ent"
	"github.com/lx-wnk/kontor/server/internal/db/repo"
)

// Deps holds all dependencies for the auth handler.
type Deps struct {
	JWTSecret        string
	CallbackURL      string
	OAuthProvider    serverauth.OAuthProvider
	UserRepo         repo.UserRepo
	IsLoopback       bool   // true when Host is 127.0.0.1 / ::1 / localhost
	BypassAuth       bool   // true when DASHBOARD_AUTH=none; all requests treated as local admin
	AuthPluginSecret string // shared secret for POST /api/auth/session; empty disables the endpoint
	// PluginLoginURL is the URL of the auth plugin's login endpoint.
	// When non-empty, GET /api/auth/login redirects here instead of handling OAuth in core.
	PluginLoginURL string
}

// Handler handles OAuth authentication routes.
type Handler struct {
	deps Deps
}

// NewHandler creates an auth Handler.
func NewHandler(deps Deps) *Handler {
	return &Handler{deps: deps}
}

// issueSession upserts the user, signs a JWT, and sets the auth_token cookie.
// It is the single place that creates a new authenticated session.
func (h *Handler) issueSession(ctx context.Context, w http.ResponseWriter, info repo.ProviderUserInfo) error {
	user, err := h.deps.UserRepo.Upsert(ctx, info)
	if err != nil {
		return fmt.Errorf("auth: upsert user: %w", err)
	}

	tokenPayload := serverauth.JWTPayload{
		Sub:   user.ID,
		Login: user.ProviderLogin,
	}
	jwtToken, err := serverauth.SignJWT(tokenPayload, h.deps.JWTSecret, 86400)
	if err != nil {
		return fmt.Errorf("auth: sign jwt: %w", err)
	}

	http.SetCookie(w, &http.Cookie{ //nolint:gosec
		Name:     "auth_token",
		Value:    jwtToken,
		MaxAge:   86400,
		HttpOnly: true,
		Secure:   !h.deps.IsLoopback,
		SameSite: http.SameSiteStrictMode,
		Path:     "/",
	})
	return nil
}

// LoginRedirect redirects the browser to the OAuth provider's authorization URL.
// When a PluginLoginURL is configured the request is forwarded to the auth plugin,
// which owns the entire OAuth dance and calls POST /api/auth/session when done.
// GET /api/auth/login
func (h *Handler) LoginRedirect(w http.ResponseWriter, r *http.Request) error {
	// Plugin-driven flow: redirect to the plugin's login endpoint with a nonce.
	if h.deps.PluginLoginURL != "" {
		nonce, err := serverauth.GenerateNonce(h.deps.JWTSecret)
		if err != nil {
			return apierr.NewAppError(http.StatusInternalServerError, "failed to generate nonce")
		}
		redirectURL := h.deps.PluginLoginURL + "?nonce=" + url.QueryEscape(nonce)
		http.Redirect(w, r, redirectURL, http.StatusFound)
		return nil
	}
	// Direct in-core flow: used when no PluginLoginURL is set but an OAuthProvider is available.
	if h.deps.OAuthProvider == nil {
		return apierr.NewAppError(http.StatusServiceUnavailable, "OAuth provider not configured")
	}
	state, err := serverauth.SignOAuthState(h.deps.JWTSecret)
	if err != nil {
		return fmt.Errorf("auth: build state: %w", err)
	}
	http.SetCookie(w, &http.Cookie{ //nolint:gosec
		Name:     "oauth_state",
		Value:    state,
		MaxAge:   300,
		HttpOnly: true,
		Secure:   !h.deps.IsLoopback,
		SameSite: http.SameSiteLaxMode,
		Path:     "/",
	})
	authURL, err := h.deps.OAuthProvider.BuildAuthURL(r.Context(), state, h.deps.CallbackURL)
	if err != nil {
		return fmt.Errorf("auth: build auth URL: %w", err)
	}
	http.Redirect(w, r, authURL, http.StatusFound)
	return nil
}

// Callback handles the OAuth callback when core manages the OAuth dance.
// GET /api/auth/callback?code=XXX&state=YYY
func (h *Handler) Callback(w http.ResponseWriter, r *http.Request) error {
	// When PluginLoginURL is set the plugin owns the full OAuth dance; callback is unused.
	if h.deps.PluginLoginURL != "" {
		return apierr.NewAppError(http.StatusNotFound, "auth callback not available in plugin mode")
	}
	if h.deps.OAuthProvider == nil {
		return apierr.NewAppError(http.StatusServiceUnavailable, "OAuth provider not configured")
	}
	if h.deps.UserRepo == nil {
		return apierr.NewAppError(http.StatusServiceUnavailable, "user store unavailable")
	}

	stateCookie, err := r.Cookie("oauth_state")
	if err != nil {
		return apierr.NewAppError(http.StatusBadRequest, "missing state cookie")
	}
	if _, err := serverauth.VerifyOAuthState(stateCookie.Value, h.deps.JWTSecret); err != nil {
		return apierr.NewAppError(http.StatusUnauthorized, "invalid state token")
	}
	if r.URL.Query().Get("state") != stateCookie.Value {
		return apierr.NewAppError(http.StatusUnauthorized, "state mismatch")
	}

	code := r.URL.Query().Get("code")
	if code == "" {
		return apierr.NewAppError(http.StatusBadRequest, "missing code")
	}
	accessToken, err := h.deps.OAuthProvider.ExchangeCode(r.Context(), code, h.deps.CallbackURL)
	if err != nil {
		return fmt.Errorf("auth: exchange code: %w", err)
	}

	profile, err := h.deps.OAuthProvider.GetUser(r.Context(), accessToken)
	if err != nil {
		return fmt.Errorf("auth: get user: %w", err)
	}

	if err := h.issueSession(r.Context(), w, repo.ProviderUserInfo{
		ID:          profile.ID,
		Login:       profile.Login,
		DisplayName: profile.DisplayName,
		AvatarURL:   profile.AvatarURL,
	}); err != nil {
		return err
	}

	http.SetCookie(w, &http.Cookie{ //nolint:gosec
		Name:     "oauth_state",
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   !h.deps.IsLoopback,
		SameSite: http.SameSiteLaxMode,
		Path:     "/",
	})
	http.Redirect(w, r, "/", http.StatusFound)
	return nil
}

// Logout clears the auth cookie.
// POST /api/auth/logout
func (h *Handler) Logout(w http.ResponseWriter, r *http.Request) error {
	http.SetCookie(w, &http.Cookie{
		Name:     "auth_token",
		Value:    "",
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   !h.deps.IsLoopback,
		SameSite: http.SameSiteStrictMode,
		Path:     "/",
	})
	w.WriteHeader(http.StatusNoContent)
	return nil
}

// CreateSession accepts a user profile from a trusted auth plugin and creates a JWT session cookie.
// POST /api/auth/session
//
// The request must include the shared plugin secret in the Authorization header:
//
//	Authorization: Bearer <DASHBOARD_AUTH_PLUGIN_SECRET>
//
// Body: {"provider_id":"...","login":"...","display_name":"...","avatar_url":"..."}
func (h *Handler) CreateSession(w http.ResponseWriter, r *http.Request) error {
	if h.deps.AuthPluginSecret == "" {
		return apierr.NewAppError(http.StatusNotFound, "auth session endpoint not configured")
	}
	if h.deps.UserRepo == nil {
		return apierr.NewAppError(http.StatusServiceUnavailable, "user store unavailable")
	}

	// Validate the shared plugin secret using constant-time comparison to prevent timing attacks.
	authHeader := r.Header.Get("Authorization")
	const prefix = "Bearer "
	token, ok := strings.CutPrefix(authHeader, prefix)
	if !ok || token == "" {
		return apierr.NewAppError(http.StatusUnauthorized, "missing or invalid Authorization header")
	}
	if subtle.ConstantTimeCompare([]byte(token), []byte(h.deps.AuthPluginSecret)) != 1 {
		return apierr.NewAppError(http.StatusUnauthorized, "invalid plugin secret")
	}

	// Limit request body to 64 KiB to prevent memory exhaustion.
	r.Body = http.MaxBytesReader(w, r.Body, 64*1024)

	var body struct {
		ProviderID  string `json:"provider_id"`
		GitHubID    string `json:"github_id"` // backwards-compat alias for provider_id
		Login       string `json:"login"`
		DisplayName string `json:"display_name"`
		AvatarURL   string `json:"avatar_url"`
		Nonce       string `json:"nonce"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		return apierr.NewAppError(http.StatusBadRequest, "invalid request body")
	}
	// Accept github_id as a backwards-compat alias for provider_id (old plugins).
	if body.ProviderID == "" {
		body.ProviderID = body.GitHubID
	}
	if body.ProviderID == "" || body.Login == "" {
		return apierr.NewAppError(http.StatusBadRequest, "provider_id and login are required")
	}

	if err := serverauth.ValidateNonce(h.deps.JWTSecret, body.Nonce); err != nil {
		return apierr.NewAppError(http.StatusUnauthorized, "invalid or expired nonce")
	}

	if err := h.issueSession(r.Context(), w, repo.ProviderUserInfo{
		ID:          body.ProviderID,
		Login:       body.Login,
		DisplayName: body.DisplayName,
		AvatarURL:   body.AvatarURL,
	}); err != nil {
		return err
	}

	w.Header().Set("Content-Type", "application/json")
	return json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

// DeleteMe permanently removes the authenticated user's account (GDPR right-to-erasure).
// DELETE /api/me
func (h *Handler) DeleteMe(w http.ResponseWriter, r *http.Request) error {
	if h.deps.BypassAuth {
		return apierr.NewAppError(http.StatusForbidden, "account deletion not available in bypass-auth mode")
	}
	payload, ok := serverauth.PayloadFromContext(r.Context())
	if !ok {
		return apierr.NewAppError(http.StatusUnauthorized, "unauthorized")
	}
	if h.deps.UserRepo == nil {
		return fmt.Errorf("auth: user repo not configured")
	}
	// Clear cookie first so the deleted session cannot be replayed.
	http.SetCookie(w, &http.Cookie{
		Name:     "auth_token",
		Value:    "",
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   !h.deps.IsLoopback,
		SameSite: http.SameSiteStrictMode,
		Path:     "/",
	})
	if err := h.deps.UserRepo.Delete(r.Context(), payload.Sub); err != nil {
		if ent.IsNotFound(err) {
			// Already gone — treat as success (double-click / stale JWT replay).
			return nil
		}
		return fmt.Errorf("auth: delete user: %w", err)
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

// Me returns the currently authenticated user.
// GET /api/me
func (h *Handler) Me(w http.ResponseWriter, r *http.Request) error {
	if h.deps.BypassAuth {
		w.Header().Set("Content-Type", "application/json")
		return json.NewEncoder(w).Encode(map[string]any{
			"user": map[string]any{
				"id":      serverauth.BypassUserID,
				"login":   serverauth.BypassUserID,
				"isAdmin": true,
			},
			"isAdmin":     true,
			"authEnabled": false,
		})
	}

	payload, ok := serverauth.PayloadFromContext(r.Context())
	if !ok {
		return apierr.NewAppError(http.StatusUnauthorized, "unauthorized")
	}
	if h.deps.UserRepo == nil {
		return fmt.Errorf("auth: user repo not configured")
	}
	user, err := h.deps.UserRepo.GetByID(r.Context(), payload.Sub)
	if err != nil {
		if errors.Is(err, serverauth.ErrTokenInvalid) {
			return apierr.NewAppError(http.StatusUnauthorized, "unauthorized")
		}
		return apierr.NewAppError(http.StatusUnauthorized, "user not found")
	}
	w.Header().Set("Content-Type", "application/json")
	return json.NewEncoder(w).Encode(map[string]any{
		"user": map[string]any{
			"id":    user.ID,
			"login": user.ProviderLogin,
		},
		"authEnabled": true,
	})
}
