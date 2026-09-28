package auth_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	apiauth "github.com/lx-wnk/kontor/server/internal/api/auth"
	"github.com/lx-wnk/kontor/server/internal/apierr"
	authpkg "github.com/lx-wnk/kontor/server/internal/auth"
	"github.com/lx-wnk/kontor/server/internal/db"
	"github.com/lx-wnk/kontor/server/internal/db/repo"
)

func TestHandler_LoginRedirect_NoProvider(t *testing.T) {
	h := apiauth.NewHandler(apiauth.Deps{
		JWTSecret:   "test-secret",
		CallbackURL: "http://localhost/api/auth/callback",
		// OAuthProvider nil: no auth_provider plugin configured
	})
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/api/auth/login", nil)
	err := h.LoginRedirect(w, r)
	require.Error(t, err) // must return error when OAuth provider not configured
}

func TestHandler_LoginRedirect_PluginLoginURL(t *testing.T) {
	// When a PluginLoginURL is set, LoginRedirect must redirect to the plugin with a nonce.
	h := apiauth.NewHandler(apiauth.Deps{
		JWTSecret:      "test-secret-32chars-long-minimum!",
		PluginLoginURL: "http://127.0.0.1:19001/login",
	})
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/api/auth/login", nil)
	err := h.LoginRedirect(w, r)
	require.NoError(t, err)
	require.Equal(t, http.StatusFound, w.Code)

	location := w.Header().Get("Location")
	require.Contains(t, location, "http://127.0.0.1:19001/login?nonce=", "redirect must include a nonce query param")
}

func TestHandler_Logout(t *testing.T) {
	h := apiauth.NewHandler(apiauth.Deps{JWTSecret: "test-secret"})
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/api/auth/logout", nil)
	err := h.Logout(w, r)
	require.NoError(t, err)
	// Cookie should be cleared
	cookies := w.Result().Cookies()
	var found bool
	for _, c := range cookies {
		if c.Name == "auth_token" {
			found = true
			require.LessOrEqual(t, c.MaxAge, 0)
		}
	}
	require.True(t, found, "auth_token cookie should be cleared")
}

func TestHandler_Me_Unauthenticated(t *testing.T) {
	h := apiauth.NewHandler(apiauth.Deps{JWTSecret: "test-secret"})
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/api/auth/me", nil)
	err := h.Me(w, r)
	require.Error(t, err) // no JWT in context → error
}

func TestHandler_Me_NoContextPayload(t *testing.T) {
	// Me returns an error when no JWT payload has been injected into context
	// (i.e. the RequireAuth middleware did not run). Success path deferred — needs
	// the full middleware stack or context injection helper.
	h := apiauth.NewHandler(apiauth.Deps{JWTSecret: "test-secret"})
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/api/auth/me", nil)
	err := h.Me(w, r)
	require.Error(t, err)
}

func TestHandler_CreateSession_NotConfigured(t *testing.T) {
	// When AuthPluginSecret is empty, CreateSession must return 404-style error.
	h := apiauth.NewHandler(apiauth.Deps{JWTSecret: "test-secret"})
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/api/auth/session", nil)
	err := h.CreateSession(w, r)
	require.Error(t, err)
}

func TestHandler_CreateSession_WrongSecret(t *testing.T) {
	h := apiauth.NewHandler(apiauth.Deps{
		JWTSecret:        "test-secret",
		AuthPluginSecret: "correct-plugin-secret",
	})
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/api/auth/session",
		bytes.NewBufferString(`{"github_id":"1","login":"alice"}`))
	r.Header.Set("Authorization", "Bearer wrong-secret")
	err := h.CreateSession(w, r)
	require.Error(t, err)
}

func TestHandler_CreateSession_MissingAuthHeader(t *testing.T) {
	h := apiauth.NewHandler(apiauth.Deps{
		JWTSecret:        "test-secret",
		AuthPluginSecret: "correct-plugin-secret",
	})
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/api/auth/session",
		bytes.NewBufferString(`{"github_id":"1","login":"alice"}`))
	// No Authorization header
	err := h.CreateSession(w, r)
	require.Error(t, err)
}

func TestHandler_CreateSession_MissingNonce(t *testing.T) {
	h := apiauth.NewHandler(apiauth.Deps{
		JWTSecret:        "test-secret-32chars-long-minimum!",
		AuthPluginSecret: "correct-plugin-secret",
	})
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/api/auth/session",
		bytes.NewBufferString(`{"github_id":"1","login":"alice"}`))
	r.Header.Set("Authorization", "Bearer correct-plugin-secret")
	err := h.CreateSession(w, r)
	// nonce is empty → ValidateNonce must reject → 401
	require.Error(t, err)
}

func TestHandler_CreateSession_InvalidNonce(t *testing.T) {
	h := apiauth.NewHandler(apiauth.Deps{
		JWTSecret:        "test-secret-32chars-long-minimum!",
		AuthPluginSecret: "correct-plugin-secret",
	})
	w := httptest.NewRecorder()
	body := `{"github_id":"1","login":"alice","nonce":"not-a-valid-jwt"}`
	r := httptest.NewRequest(http.MethodPost, "/api/auth/session", bytes.NewBufferString(body))
	r.Header.Set("Authorization", "Bearer correct-plugin-secret")
	err := h.CreateSession(w, r)
	require.Error(t, err)
}

// --- TEST-02: GitHub OAuth happy-path callback ---

// httpOAuthProvider is an OAuthProvider backed by real httptest mock servers,
// testing the full code→token→user round-trip without touching github.com.
type httpOAuthProvider struct {
	tokenURL string // mock GitHub token endpoint
	userURL  string // mock GitHub user endpoint
	client   *http.Client
}

func (p *httpOAuthProvider) BuildAuthURL(_ context.Context, state, redirectURI string) (string, error) {
	return "http://mock-github.example.com/login/oauth/authorize?state=" + state + "&redirect_uri=" + redirectURI, nil
}

func (p *httpOAuthProvider) ExchangeCode(ctx context.Context, code, _ string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.tokenURL, nil)
	if err != nil {
		return "", fmt.Errorf("ExchangeCode: build request: %w", err)
	}
	req.Header.Set("Accept", "application/json")

	resp, err := p.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("ExchangeCode: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("ExchangeCode: read body: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("ExchangeCode: HTTP %d", resp.StatusCode)
	}
	var result struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return "", fmt.Errorf("ExchangeCode: decode: %w", err)
	}
	return result.AccessToken, nil
}

func (p *httpOAuthProvider) GetUser(ctx context.Context, accessToken string) (*authpkg.OAuthUserProfile, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.userURL, nil)
	if err != nil {
		return nil, fmt.Errorf("GetUser: build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)

	resp, err := p.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("GetUser: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("GetUser: read body: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GetUser: HTTP %d", resp.StatusCode)
	}
	var raw struct {
		ID        string `json:"id"`
		Login     string `json:"login"`
		Name      string `json:"name"`
		AvatarURL string `json:"avatar_url"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("GetUser: decode: %w", err)
	}
	return &authpkg.OAuthUserProfile{
		ID:          raw.ID,
		Login:       raw.Login,
		DisplayName: raw.Name,
		AvatarURL:   raw.AvatarURL,
	}, nil
}

// TestHandler_Callback_HappyPath exercises the full OAuth callback flow:
//   - state-cookie match validated
//   - code→token exchange via mock GitHub token endpoint
//   - user profile fetched via mock GitHub user endpoint
//   - auth_token JWT cookie set with HttpOnly, correct SameSite and Secure flags
//   - oauth_state cookie cleared (MaxAge < 0) after consumption
func TestHandler_Callback_HappyPath(t *testing.T) {
	cases := []struct {
		name       string
		isLoopback bool
		wantSecure bool
	}{
		{name: "loopback (dev)", isLoopback: true, wantSecure: false},
		{name: "non-loopback (prod)", isLoopback: false, wantSecure: true},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			const jwtSecret = "test-secret-32chars-long-minimum!"

			// Mock GitHub token endpoint — returns a fake access token.
			tokenSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				fmt.Fprintln(w, `{"access_token":"gho_test_token"}`)
			}))
			defer tokenSrv.Close()

			// Mock GitHub user endpoint — validates Bearer token and returns a user profile.
			userSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") {
					http.Error(w, "unauthorized", http.StatusUnauthorized)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				fmt.Fprintln(w, `{"id":"42","login":"alice","name":"Alice Test","avatar_url":"https://example.com/alice.png"}`)
			}))
			defer userSrv.Close()

			userRepo := openTestDB(t)
			provider := &httpOAuthProvider{
				tokenURL: tokenSrv.URL,
				userURL:  userSrv.URL,
				client:   &http.Client{},
			}

			h := apiauth.NewHandler(apiauth.Deps{
				JWTSecret:     jwtSecret,
				CallbackURL:   "http://localhost/api/auth/callback",
				OAuthProvider: provider,
				UserRepo:      userRepo,
				IsLoopback:    tc.isLoopback,
			})

			// Build a valid signed state token and set it as a cookie.
			validState, err := authpkg.SignOAuthState(jwtSecret)
			require.NoError(t, err)

			w := httptest.NewRecorder()
			r := httptest.NewRequest(http.MethodGet,
				"/api/auth/callback?code=authcode&state="+validState, nil)
			r.AddCookie(&http.Cookie{Name: "oauth_state", Value: validState})

			callbackErr := h.Callback(w, r)
			require.NoError(t, callbackErr)

			// Must redirect to /
			require.Equal(t, http.StatusFound, w.Code)
			require.Equal(t, "/", w.Header().Get("Location"))

			// Locate the auth_token and oauth_state cookies in the response.
			var authCookie, stateCookie *http.Cookie
			for _, c := range w.Result().Cookies() {
				switch c.Name {
				case "auth_token":
					authCookie = c
				case "oauth_state":
					stateCookie = c
				}
			}
			require.NotNil(t, authCookie, "auth_token cookie must be set")
			require.True(t, authCookie.HttpOnly, "auth_token must be HttpOnly")
			require.Equal(t, tc.wantSecure, authCookie.Secure,
				"Secure flag must be %v when IsLoopback=%v", tc.wantSecure, tc.isLoopback)
			require.Equal(t, http.SameSiteStrictMode, authCookie.SameSite,
				"SameSite must be Strict for session cookies")
			require.Greater(t, authCookie.MaxAge, 0, "MaxAge must be positive")

			// oauth_state must be cleared after consumption to prevent replay.
			require.NotNil(t, stateCookie, "oauth_state cookie must be cleared on response")
			require.Less(t, stateCookie.MaxAge, 0,
				"oauth_state MaxAge must be negative to instruct browser to delete it")

			// JWT must be verifiable with the same secret.
			payload, err := authpkg.VerifyJWT(authCookie.Value, jwtSecret)
			require.NoError(t, err)
			require.Equal(t, "alice", payload.Login)

			// JWT payload login matches the user fetched from the mock OAuth provider.
			// The /api/me handler is exercised by its own dedicated tests; this test
			// stops at the JWT round-trip to keep the unit-under-test scoped to the
			// callback handler.
			require.Equal(t, "42", payload.Sub)
			require.Equal(t, "alice", payload.Login)

			// Direct repo assertion: the user must have been persisted via Upsert.
			persisted, err := userRepo.GetByID(r.Context(), "42")
			require.NoError(t, err, "GetByID must find the upserted user")
			require.Equal(t, "42", persisted.ID)
			require.Equal(t, "alice", persisted.ProviderLogin)
		})
	}
}

// stubOAuthProvider is a trivial OAuthProvider that satisfies the interface in tests.
type stubOAuthProvider struct{}

func (s *stubOAuthProvider) BuildAuthURL(_ context.Context, _, _ string) (string, error) {
	return "http://example.com", nil
}
func (s *stubOAuthProvider) ExchangeCode(_ context.Context, _, _ string) (string, error) {
	return "tok", nil
}
func (s *stubOAuthProvider) GetUser(_ context.Context, _ string) (*authpkg.OAuthUserProfile, error) {
	return &authpkg.OAuthUserProfile{ID: "1", Login: "u"}, nil
}

// openTestDB opens an in-memory SQLite DB and registers a cleanup.
func openTestDB(t *testing.T) repo.UserRepo {
	t.Helper()
	bundle, err := db.Open(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { _ = bundle.Client.Close() })
	return repo.NewUserRepo(bundle.Client)
}

func TestHandler_Callback_InPluginMode(t *testing.T) {
	h := apiauth.NewHandler(apiauth.Deps{
		JWTSecret:      "test-secret",
		PluginLoginURL: "http://127.0.0.1:19001/login",
	})
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/api/auth/callback?code=x&state=y", nil)
	err := h.Callback(w, r)
	require.Error(t, err)
	var appErr *apierr.AppError
	require.True(t, errors.As(err, &appErr))
	require.Equal(t, http.StatusNotFound, appErr.Status)
}

func TestHandler_Callback_NoOAuthProvider(t *testing.T) {
	h := apiauth.NewHandler(apiauth.Deps{
		JWTSecret:     "test-secret",
		OAuthProvider: nil,
	})
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/api/auth/callback", nil)
	err := h.Callback(w, r)
	require.Error(t, err)
	var appErr *apierr.AppError
	require.True(t, errors.As(err, &appErr))
	require.Equal(t, http.StatusServiceUnavailable, appErr.Status)
}

func TestHandler_Callback_MissingStateCookie(t *testing.T) {
	userRepo := openTestDB(t)
	h := apiauth.NewHandler(apiauth.Deps{
		JWTSecret:     "test-secret",
		OAuthProvider: &stubOAuthProvider{},
		UserRepo:      userRepo,
	})
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/api/auth/callback", nil)
	// No oauth_state cookie set
	err := h.Callback(w, r)
	require.Error(t, err)
	var appErr *apierr.AppError
	require.True(t, errors.As(err, &appErr))
	require.Equal(t, http.StatusBadRequest, appErr.Status)
}

func TestHandler_Callback_InvalidStateJWT(t *testing.T) {
	userRepo := openTestDB(t)
	h := apiauth.NewHandler(apiauth.Deps{
		JWTSecret:     "test-secret",
		OAuthProvider: &stubOAuthProvider{},
		UserRepo:      userRepo,
	})
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/api/auth/callback", nil)
	r.AddCookie(&http.Cookie{Name: "oauth_state", Value: "invalid.jwt.here"})
	err := h.Callback(w, r)
	require.Error(t, err)
	var appErr *apierr.AppError
	require.True(t, errors.As(err, &appErr))
	require.Equal(t, http.StatusUnauthorized, appErr.Status)
}

func TestHandler_Callback_StateMismatch(t *testing.T) {
	const jwtSecret = "test-secret"
	userRepo := openTestDB(t)
	h := apiauth.NewHandler(apiauth.Deps{
		JWTSecret:     jwtSecret,
		OAuthProvider: &stubOAuthProvider{},
		UserRepo:      userRepo,
	})

	validState, err := authpkg.SignOAuthState(jwtSecret)
	require.NoError(t, err)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/api/auth/callback?state=different-value", nil)
	r.AddCookie(&http.Cookie{Name: "oauth_state", Value: validState})

	callbackErr := h.Callback(w, r)
	require.Error(t, callbackErr)
	var appErr *apierr.AppError
	require.True(t, errors.As(callbackErr, &appErr))
	require.Equal(t, http.StatusUnauthorized, appErr.Status)
}

func TestHandler_Callback_MissingCode(t *testing.T) {
	const jwtSecret = "test-secret"
	userRepo := openTestDB(t)
	h := apiauth.NewHandler(apiauth.Deps{
		JWTSecret:     jwtSecret,
		OAuthProvider: &stubOAuthProvider{},
		UserRepo:      userRepo,
	})

	validState, err := authpkg.SignOAuthState(jwtSecret)
	require.NoError(t, err)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/api/auth/callback?state="+validState, nil)
	r.AddCookie(&http.Cookie{Name: "oauth_state", Value: validState})

	callbackErr := h.Callback(w, r)
	require.Error(t, callbackErr)
	var appErr *apierr.AppError
	require.True(t, errors.As(callbackErr, &appErr))
	require.Equal(t, http.StatusBadRequest, appErr.Status)
}
