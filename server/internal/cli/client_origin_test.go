package cli

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/lx-wnk/kontor/server/internal/api"
)

func TestClientMutationPassesSameOriginCheck(t *testing.T) {
	srv := httptest.NewServer(api.RequireSameOriginForMutations(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{}`))
	})))
	t.Cleanup(srv.Close)

	c := newClient(CLIConfig{Host: srv.URL})
	require.NoError(t, c.post("/api/tasks", map[string]string{"slug": "x"}, nil))
}
