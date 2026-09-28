package api_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/lx-wnk/kontor/server/internal/api"
	authpkg "github.com/lx-wnk/kontor/server/internal/auth"
	"github.com/lx-wnk/kontor/server/internal/merger"
	"github.com/lx-wnk/kontor/server/internal/sse"
)

// TestRouter_BypassAuth_LoopbackNoOAuth verifies that when BypassAuth is true
// (loopback host + no GitHub OAuth configured), protected routes are accessible
// without a JWT token.
func TestRouter_BypassAuth_LoopbackNoOAuth(t *testing.T) {
	deps := api.RouterDeps{
		Config: api.RouterConfig{
			JWTSecret:   "test-secret-minimum-32-characters-x",
			HooksSecret: "test-hooks-secret",
			IsLoopback:  true,
			BypassAuth:  true,
		},
		AgentBroadcaster: sse.NewBroadcaster(),
		Merger:           merger.New(),
	}
	router := api.NewRouter(deps)

	// /api/agents is a protected route — must be accessible without auth when bypass is on.
	req := httptest.NewRequest(http.MethodGet, "/api/agents", nil)
	req.Header.Set("Origin", "http://127.0.0.1")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	// Verify the bypass behavior: the request must not be rejected with 401.
	// We do not assert 200 specifically because ps/lsof may be unavailable in CI.
	assert.NotEqual(t, http.StatusUnauthorized, rec.Code)
}

// TestRouter_RequireAuth_Returns401WhenUnauthenticated verifies that when BypassAuth is false,
// protected routes reject unauthenticated requests with 401.
func TestRouter_RequireAuth_Returns401WhenUnauthenticated(t *testing.T) {
	deps := api.RouterDeps{
		Config: api.RouterConfig{
			JWTSecret:   "test-secret-minimum-32-characters-x",
			HooksSecret: "test-hooks-secret",
			IsLoopback:  true,
			BypassAuth:  false,
		},
		AgentBroadcaster: sse.NewBroadcaster(),
		Merger:           merger.New(),
		// OAuthProvider set to non-nil triggers auth
		OAuthProvider: &stubOAuth{},
	}
	router := api.NewRouter(deps)

	req := httptest.NewRequest(http.MethodGet, "/api/agents", nil)
	req.Host = "127.0.0.1:13120"
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusUnauthorized, rec.Code)
}

// stubOAuth implements authpkg.OAuthProvider with no-op stubs.
type stubOAuth struct{}

func (s *stubOAuth) BuildAuthURL(_ context.Context, state, redirectURI string) (string, error) {
	return "http://stub", nil
}

func (s *stubOAuth) ExchangeCode(_ context.Context, code, redirectURI string) (string, error) {
	return "stub-token", nil
}

func (s *stubOAuth) GetUser(_ context.Context, accessToken string) (*authpkg.OAuthUserProfile, error) {
	return &authpkg.OAuthUserProfile{ID: "1", Login: "stub"}, nil
}
