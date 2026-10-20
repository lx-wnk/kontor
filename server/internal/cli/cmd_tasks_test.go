package cli

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestTasksDonePostsReferenceToComplete(t *testing.T) {
	var path string
	var body map[string]string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&body)
		_, _ = w.Write([]byte(`{}`))
	}))
	t.Cleanup(srv.Close)

	cmd := newTasksCmd(&CLIConfig{Host: srv.URL})
	cmd.SetArgs([]string{"done", "task-1", "--ref", "lx-wnk/kontor#538"})
	require.NoError(t, cmd.Execute())

	require.Equal(t, "/api/tasks/task-1/complete", path)
	require.Equal(t, map[string]string{"reference": "lx-wnk/kontor#538"}, body)
}
