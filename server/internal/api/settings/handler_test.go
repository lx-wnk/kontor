package settings_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	settingsapi "github.com/lx-wnk/kontor/server/internal/api/settings"
	"github.com/lx-wnk/kontor/server/internal/secretbox"
	settingssvc "github.com/lx-wnk/kontor/server/internal/settings"
)

type memRepo struct {
	m      map[string]string
	setErr error
}

func (r *memRepo) Get(_ context.Context, k string) (string, bool, error) {
	v, ok := r.m[k]
	return v, ok, nil
}
func (r *memRepo) Set(_ context.Context, k, v string) error {
	if r.setErr != nil {
		return r.setErr
	}
	r.m[k] = v
	return nil
}
func (r *memRepo) SetSecret(_ context.Context, k, ciphertext, _ string) error {
	if r.setErr != nil {
		return r.setErr
	}
	r.m[k] = ciphertext
	return nil
}
func (r *memRepo) GetSecret(_ context.Context, k string) (string, string, bool, error) {
	v, ok := r.m[k]
	return v, "", ok, nil
}
func (r *memRepo) ListAll(_ context.Context) (map[string]string, error) { return r.m, nil }

func newRouter(t *testing.T) (http.Handler, *settingssvc.Service) {
	t.Helper()
	return newRouterWithRepo(t, &memRepo{m: map[string]string{}})
}

func newRouterWithRepo(t *testing.T, repo settingssvc.Repo) (http.Handler, *settingssvc.Service) {
	t.Helper()
	box, err := secretbox.New(make([]byte, 32))
	require.NoError(t, err)
	svc := settingssvc.New(repo, box)
	require.NoError(t, svc.Load(context.Background()))
	h := settingsapi.NewHandler(svc)
	r := chi.NewRouter()
	h.MountRead(r)
	h.MountWrite(r)
	return r, svc
}

func TestSettingsAPI_ListAndPatch(t *testing.T) {
	r, _ := newRouter(t)

	// GET returns definitions with current values
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/settings", nil))
	require.Equal(t, http.StatusOK, rec.Code)
	var list []map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &list))
	assert.NotEmpty(t, list)

	// each entry exposes value, default, and apply
	first := list[0]
	assert.Contains(t, first, "value")
	assert.Contains(t, first, "default")
	assert.Contains(t, first, "apply")

	// PATCH a restart key -> applied:"restart"
	rec = httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPatch, "/api/settings/spawn.rateLimit", strings.NewReader(`{"value":"9"}`))
	r.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)
	var resp map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	assert.Equal(t, "restart", resp["applied"])
	assert.Equal(t, "9", resp["value"])

	// PATCH invalid value -> 400
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPatch, "/api/settings/spawn.rateLimit", strings.NewReader(`{"value":"abc"}`))
	r.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusBadRequest, rec.Code)

	// PATCH unknown key -> 400
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPatch, "/api/settings/nope", strings.NewReader(`{"value":"1"}`))
	r.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestSettingsAPI_PatchPersistenceFailure(t *testing.T) {
	r, _ := newRouterWithRepo(t, &memRepo{m: map[string]string{}, setErr: errors.New("db down")})

	// valid key/value but the repo fails to persist -> 500, not 400
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPatch, "/api/settings/spawn.rateLimit", strings.NewReader(`{"value":"9"}`))
	r.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusInternalServerError, rec.Code)
}

func TestList_MasksSecretValues(t *testing.T) {
	h, svc := newRouter(t)
	require.NoError(t, svc.Set(t.Context(), "obsidian.apiKey", "sk-live-123"))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/settings", nil)
	h.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)

	var got []map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
	for _, row := range got {
		if row["key"] == "obsidian.apiKey" {
			assert.Equal(t, secretbox.MaskedSentinel, row["value"])
			assert.NotContains(t, rec.Body.String(), "sk-live-123")
			return
		}
	}
	t.Fatal("obsidian.apiKey not present in the settings list")
}

// TestPatch_MasksSecretValueInResponse guards against the PATCH response
// echoing a submitted secret in clear. assert.NotContains runs against the
// whole response body, not just the "value" field, so it still fails if the
// plaintext leaks through a field this test did not think to check.
func TestPatch_MasksSecretValueInResponse(t *testing.T) {
	h, _ := newRouter(t)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPatch, "/api/settings/obsidian.apiKey", strings.NewReader(`{"value":"sk-live-456"}`))
	h.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)

	assert.NotContains(t, rec.Body.String(), "sk-live-456")

	var resp map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	assert.Equal(t, secretbox.MaskedSentinel, resp["value"])
}

func TestPatch_ValidationErrorMessageInResponse(t *testing.T) {
	h, svc := newRouter(t)

	// Register a pre-save hook that rejects a specific value.
	svc.OnPreSave(func(_ context.Context, key, value string) error {
		if key == "obsidian.vaultRoot" && value == "bad-root" {
			return fmt.Errorf("folder %q not found in vault", value)
		}
		return nil
	})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPatch, "/api/settings/obsidian.vaultRoot", strings.NewReader(`{"value":"bad-root"}`))
	h.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusBadRequest, rec.Code)
	var body map[string]string
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	assert.Contains(t, body["error"], "bad-root")
	assert.Contains(t, body["error"], "not found")
}
