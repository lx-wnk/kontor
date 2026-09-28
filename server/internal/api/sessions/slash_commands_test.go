package sessions

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/lx-wnk/kontor/sdk"
	"github.com/lx-wnk/kontor/server/internal/db/ent"
	"github.com/stretchr/testify/require"
)

type fakeSpawners struct{ agent *ent.Spawner }

func (f fakeSpawners) GetByID(_ context.Context, _ string) (*ent.Spawner, error) {
	return nil, errors.New("not used")
}
func (f fakeSpawners) GetBySlug(_ context.Context, _ string) (*ent.Spawner, error) {
	if f.agent != nil {
		return f.agent, nil
	}
	return nil, errors.New("not found")
}
func (f fakeSpawners) GetDefault(_ context.Context) (*ent.Spawner, error) {
	if f.agent != nil && f.agent.IsDefault {
		return f.agent, nil
	}
	return nil, errors.New("not found")
}

func TestSlashCommands_SessionScopedEnvelope(t *testing.T) {
	cfg := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(cfg, "commands"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(cfg, "commands", "ship.md"), []byte("---\ndescription: Ship it\n---"), 0o644))

	agents := func(_ context.Context) ([]sdk.Agent, error) {
		return []sdk.Agent{{SessionID: "s1", CWD: "/proj", ClaudeConfigDir: cfg}}, nil
	}
	h := NewCommandsHandler(fakeSpawners{}, agents)

	rec := httptest.NewRecorder()
	h.SlashCommands(rec, httptest.NewRequest(http.MethodGet, "/api/slash-commands?sessionId=s1", nil))

	require.Equal(t, http.StatusOK, rec.Code)
	var resp slashCommandsResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	require.Equal(t, "session", resp.ScopeSource)
	require.Equal(t, "session:s1", resp.ScopeLabel)

	names := map[string]string{}
	for _, c := range resp.Commands {
		names[c.Name] = c.Source
	}
	require.Equal(t, "builtin", names["/help"], "builtins present")
	require.Equal(t, "user", names["/ship"], "session config dir command present")
}
