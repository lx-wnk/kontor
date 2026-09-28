package apikeys_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/require"

	"github.com/lx-wnk/kontor/server/internal/api/apikeys"
	"github.com/lx-wnk/kontor/server/internal/db"
	"github.com/lx-wnk/kontor/server/internal/db/repo"
)

func setupHandler(t *testing.T) (*apikeys.Handler, *chi.Mux) {
	t.Helper()
	bundle, err := db.Open(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { _ = bundle.Client.Close() })

	r := repo.NewApiKeyRepo(bundle.Client)
	h := apikeys.NewHandler(r)

	mux := chi.NewRouter()
	mux.Get("/api/settings/api-keys", apikeys.Wrap(h.List))
	mux.Post("/api/settings/api-keys", apikeys.Wrap(h.Create))
	mux.Delete("/api/settings/api-keys/{id}", apikeys.Wrap(h.Delete))
	return h, mux
}

func TestApiKeyHandler_CreateAndList(t *testing.T) {
	_, mux := setupHandler(t)

	body, _ := json.Marshal(map[string]any{"name": "my-key", "scopes": []string{"tasks:read"}})
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/settings/api-keys", bytes.NewReader(body)))
	require.Equal(t, http.StatusCreated, w.Code)

	var created map[string]any
	require.NoError(t, json.NewDecoder(w.Body).Decode(&created))
	require.Contains(t, created, "token") // raw token shown once
	require.Contains(t, created, "key")
	key, ok := created["key"].(map[string]any)
	require.True(t, ok)
	require.Contains(t, key, "id")
	token, ok := created["token"].(string)
	require.True(t, ok)
	require.True(t, len(token) > 10) // non-empty token

	w2 := httptest.NewRecorder()
	mux.ServeHTTP(w2, httptest.NewRequest(http.MethodGet, "/api/settings/api-keys", nil))
	require.Equal(t, http.StatusOK, w2.Code)

	var list []map[string]any
	require.NoError(t, json.NewDecoder(w2.Body).Decode(&list))
	require.Len(t, list, 1)
	require.NotContains(t, list[0], "key_hash") // hash must never be returned
	require.NotContains(t, list[0], "token")    // raw token only at creation
}

func TestApiKeyHandler_Delete(t *testing.T) {
	_, mux := setupHandler(t)

	body, _ := json.Marshal(map[string]any{"name": "to-delete", "scopes": []string{}})
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/settings/api-keys", bytes.NewReader(body)))
	require.Equal(t, http.StatusCreated, w.Code)

	var created map[string]any
	require.NoError(t, json.NewDecoder(w.Body).Decode(&created))
	key := created["key"].(map[string]any)
	id := key["id"].(string)

	w2 := httptest.NewRecorder()
	mux.ServeHTTP(w2, httptest.NewRequest(http.MethodDelete, "/api/settings/api-keys/"+id, nil))
	require.Equal(t, http.StatusNoContent, w2.Code)

	// After delete, list should be empty
	w3 := httptest.NewRecorder()
	mux.ServeHTTP(w3, httptest.NewRequest(http.MethodGet, "/api/settings/api-keys", nil))
	var list []map[string]any
	require.NoError(t, json.NewDecoder(w3.Body).Decode(&list))
	require.Empty(t, list)
}

func TestApiKeyHandler_Create_EmptyName(t *testing.T) {
	_, mux := setupHandler(t)

	body, _ := json.Marshal(map[string]any{"name": "", "scopes": []string{}})
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/settings/api-keys", bytes.NewReader(body)))
	require.Equal(t, http.StatusBadRequest, w.Code)
}
