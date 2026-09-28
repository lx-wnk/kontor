package prompttemplates_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/require"

	"github.com/lx-wnk/kontor/server/internal/api/prompttemplates"
	"github.com/lx-wnk/kontor/server/internal/db"
	"github.com/lx-wnk/kontor/server/internal/db/repo"
)

func newServer(t *testing.T) *httptest.Server {
	t.Helper()
	bundle, err := db.Open(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { _ = bundle.Client.Close() })

	r := chi.NewRouter()
	prompttemplates.NewHandler(repo.NewPromptTemplateRepo(bundle.Client)).Mount(r)
	return httptest.NewServer(r)
}

func TestPromptTemplatesHandler(t *testing.T) {
	srv := newServer(t)
	defer srv.Close()

	t.Run("create then list", func(t *testing.T) {
		body, _ := json.Marshal(map[string]string{"name": "greet", "body": "Hello {{name}}!"})
		resp, err := http.Post(srv.URL+"/api/prompt-templates", "application/json", bytes.NewReader(body))
		require.NoError(t, err)
		require.Equal(t, http.StatusCreated, resp.StatusCode)

		var created map[string]any
		require.NoError(t, json.NewDecoder(resp.Body).Decode(&created))
		require.Equal(t, "greet", created["name"])
		id := created["id"].(string)

		resp2, _ := http.Get(srv.URL + "/api/prompt-templates")
		var list []map[string]any
		require.NoError(t, json.NewDecoder(resp2.Body).Decode(&list))
		require.Len(t, list, 1)

		req, _ := http.NewRequest(http.MethodDelete, srv.URL+"/api/prompt-templates/"+id, nil)
		resp3, _ := http.DefaultClient.Do(req)
		require.Equal(t, http.StatusNoContent, resp3.StatusCode)

		resp4, _ := http.Get(srv.URL + "/api/prompt-templates")
		var list2 []map[string]any
		require.NoError(t, json.NewDecoder(resp4.Body).Decode(&list2))
		require.Len(t, list2, 0)
	})
}
