package admin_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/require"

	"github.com/lx-wnk/kontor/server/internal/api/admin"
	"github.com/lx-wnk/kontor/server/internal/auth"
)

const testAdminJWTSecret = "test-sp8-admin-secret"

// TestRestart_AnyAuthenticatedUserIsAccepted pins what protects the restart
// endpoint now that the admin gate is gone: authentication, and nothing else.
//
// This assertion used to be its inverse — a non-admin got 403. That contract
// was never real: no code path ever set is_admin, so the gate rejected every
// authenticated user and admitted the endpoint to nobody, while passing
// everything through in bypass mode. Removing it turned "nobody" into "any
// authenticated caller", which is what this test now states out loud.
func TestRestart_AnyAuthenticatedUserIsAccepted(t *testing.T) {
	triggered := make(chan struct{}, 1)
	h := admin.New(fakeValidator{}, "reexec", &fakeRestarter{}, func() { triggered <- struct{}{} })

	r := chi.NewRouter()
	r.Use(auth.RequireAuth(testAdminJWTSecret))
	h.Mount(r)

	token, err := auth.SignJWT(
		auth.JWTPayload{Sub: "u2", Login: "viewer"},
		testAdminJWTSecret, 3600,
	)
	require.NoError(t, err)
	req := httptest.NewRequest(http.MethodPost, "/api/admin/restart", nil)
	req.AddCookie(&http.Cookie{Name: "auth_token", Value: token})

	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	require.Equal(t, http.StatusAccepted, rec.Code)
	select {
	case <-triggered:
	default:
		t.Fatal("trigger must fire for an authenticated caller")
	}
}

// TestRestart_UnauthenticatedIsRejected is the assertion that still carries the
// security weight: with auth enabled, no token means no restart.
func TestRestart_UnauthenticatedIsRejected(t *testing.T) {
	h := admin.New(fakeValidator{}, "reexec", &fakeRestarter{}, func() { t.Fatal("must not trigger") })

	r := chi.NewRouter()
	r.Use(auth.RequireAuth(testAdminJWTSecret))
	h.Mount(r)

	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/admin/restart", nil))
	require.Equal(t, http.StatusUnauthorized, rec.Code)
}

// TestRestart_BypassModeAllowsAnyRequest documents the loopback single-user
// mode, where no JWT middleware is mounted at all.
func TestRestart_BypassModeAllowsAnyRequest(t *testing.T) {
	triggered := make(chan struct{}, 1)
	h := admin.New(fakeValidator{}, "reexec", &fakeRestarter{}, func() { triggered <- struct{}{} })

	r := chi.NewRouter()
	h.Mount(r)

	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/admin/restart", nil))
	require.Equal(t, http.StatusAccepted, rec.Code)
	select {
	case <-triggered:
	default:
		t.Fatal("trigger must fire in bypass mode")
	}
}
