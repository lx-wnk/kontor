package presets_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/require"

	"github.com/lx-wnk/kontor/server/internal/api/presets"
	"github.com/lx-wnk/kontor/server/internal/auth"
	"github.com/lx-wnk/kontor/server/internal/db"
	"github.com/lx-wnk/kontor/server/internal/db/repo"
)

// contextKey must match auth.contextKey — we use a signed JWT to inject auth.
// Instead, we sign a real JWT and pass it via cookie so RequireAuth populates the context.
//
// But to avoid depending on a running JWT secret, we use a test-only JWT and a test router
// that mounts RequireAuth with the same secret.

const testJWTSecret = "test-secret-for-presets"

// jwtMiddleware signs a JWT for "user-1" and injects it so handlers can call
// auth.PayloadFromContext. We obtain a real signed token and pass it via cookie.
func jwtMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tok, err := auth.SignJWT(auth.JWTPayload{Sub: "user-1"}, testJWTSecret, 3600)
		if err != nil {
			http.Error(w, "test setup: sign jwt: "+err.Error(), http.StatusInternalServerError)
			return
		}
		r = r.WithContext(context.WithValue(r.Context(), struct{ name string }{"jwt_for_test"}, tok))
		// Add cookie so RequireAuth picks it up.
		r.AddCookie(&http.Cookie{Name: "auth_token", Value: tok})
		auth.RequireAuth(testJWTSecret)(next).ServeHTTP(w, r)
	})
}

func setupPresetsHandler(t *testing.T) (*presets.Handler, *chi.Mux) {
	t.Helper()
	bundle, err := db.Open(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { _ = bundle.Client.Close() })

	r := repo.NewPermissionPresetRepo(bundle.Client)
	h := presets.NewHandler(r)

	mux := chi.NewRouter()
	// Mount handler behind jwtMiddleware so requests carry a valid JWT payload.
	mux.Group(func(r chi.Router) {
		r.Use(jwtMiddleware)
		h.Mount(r)
	})
	return h, mux
}

func TestPresetsHandler_DeleteBody_EmptyCwd(t *testing.T) {
	_, mux := setupPresetsHandler(t)

	// Empty cwd should be rejected with 400.
	body, _ := json.Marshal(map[string]any{"cwd": ""})
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest(http.MethodDelete, "/api/settings/permission-presets", bytes.NewReader(body)))
	require.Equal(t, http.StatusBadRequest, w.Code)
}

func TestPresetsHandler_DeleteBody_MissingCwd(t *testing.T) {
	_, mux := setupPresetsHandler(t)

	// Missing cwd key should be rejected with 400.
	body, _ := json.Marshal(map[string]any{})
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest(http.MethodDelete, "/api/settings/permission-presets", bytes.NewReader(body)))
	require.Equal(t, http.StatusBadRequest, w.Code)
}

func TestPresetsHandler_DeleteBody_InvalidJSON(t *testing.T) {
	_, mux := setupPresetsHandler(t)

	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest(http.MethodDelete, "/api/settings/permission-presets", bytes.NewReader([]byte("not-json"))))
	require.Equal(t, http.StatusBadRequest, w.Code)
}

func TestPresetsHandler_ListEmpty(t *testing.T) {
	_, mux := setupPresetsHandler(t)

	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/settings/permission-presets", nil))
	require.Equal(t, http.StatusOK, w.Code)

	var list []repo.PresetProjectSummary
	require.NoError(t, json.NewDecoder(w.Body).Decode(&list))
	require.Empty(t, list)
}

func TestPresetsHandler_DeleteValidCwd(t *testing.T) {
	_, mux := setupPresetsHandler(t)

	body, _ := json.Marshal(map[string]any{"cwd": "/some/project"})
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest(http.MethodDelete, "/api/settings/permission-presets", bytes.NewReader(body)))
	require.Equal(t, http.StatusOK, w.Code)

	var resp map[string]bool
	require.NoError(t, json.NewDecoder(w.Body).Decode(&resp))
	require.True(t, resp["ok"])
}

// TestPresetsHandler_BypassAuth tests handlers in bypass mode (no JWT, loopback only).
// This simulates the scenario where BypassAuth=true and no JWT middleware runs.
func TestPresetsHandler_BypassAuth_List(t *testing.T) {
	_, mux := setupBypassAuthHandler(t)

	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/settings/permission-presets", nil))
	require.Equal(t, http.StatusOK, w.Code)

	var list []repo.PresetProjectSummary
	require.NoError(t, json.NewDecoder(w.Body).Decode(&list))
	require.Empty(t, list)
}

func TestPresetsHandler_BypassAuth_Delete(t *testing.T) {
	_, mux := setupBypassAuthHandler(t)

	body, _ := json.Marshal(map[string]any{"cwd": "/some/project"})
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest(http.MethodDelete, "/api/settings/permission-presets", bytes.NewReader(body)))
	require.Equal(t, http.StatusOK, w.Code)

	var resp map[string]bool
	require.NoError(t, json.NewDecoder(w.Body).Decode(&resp))
	require.True(t, resp["ok"])
}

// setupBypassAuthHandler creates a handler without JWT middleware (bypass mode).
func setupBypassAuthHandler(t *testing.T) (*presets.Handler, *chi.Mux) {
	t.Helper()
	bundle, err := db.Open(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { _ = bundle.Client.Close() })

	r := repo.NewPermissionPresetRepo(bundle.Client)
	h := presets.NewHandler(r)

	mux := chi.NewRouter()
	// Mount handler WITHOUT JWT middleware — simulates bypass auth mode.
	h.Mount(mux)
	return h, mux
}
