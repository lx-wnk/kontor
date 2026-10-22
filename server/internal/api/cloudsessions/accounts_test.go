package cloudsessions

import (
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/lx-wnk/kontor/server/internal/cloud"
)

type accountRow struct {
	ConfigDir string `json:"configDir"`
	Spawners  []struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	} `json:"spawners"`
	Auth  *cloud.AuthStatus `json:"auth"`
	Error string            `json:"error"`
}

func (f *fixture) accounts(t *testing.T, query string) []accountRow {
	t.Helper()
	rr := f.do("GET", "/api/cloud-accounts"+query, "")
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	var rows []accountRow
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &rows), rr.Body.String())
	return rows
}

func (f *fixture) addSpawner(t *testing.T, name, slug, adapter, dir string) {
	t.Helper()
	_, err := f.h.spawners.Create(t.Context(), name, slug, "claude", nil,
		map[string]string{"CLAUDE_CONFIG_DIR": dir}, nil, nil, adapter, nil, false)
	require.NoError(t, err)
}

func TestCloudAccounts_SymlinkedDirCountsOnce(t *testing.T) {
	f := newFixture(t)
	link := filepath.Join(t.TempDir(), "link")
	require.NoError(t, os.Symlink(f.dir, link))
	f.addSpawner(t, "Alias", "alias", "claude", link)

	rows := f.accounts(t, "")

	require.Len(t, rows, 1)
	require.Equal(t, link, rows[0].ConfigDir)
	require.Len(t, rows[0].Spawners, 2)
	require.Equal(t, "Alias", rows[0].Spawners[0].Name)
	require.Equal(t, "Work", rows[0].Spawners[1].Name)
	require.NotNil(t, rows[0].Auth)
	require.True(t, rows[0].Auth.LoggedIn)
	require.Equal(t, "claude.ai", rows[0].Auth.AuthMethod)
	require.Equal(t, 1, f.cli.authCount())
}

func TestCloudAccounts_ExcludesNonClaudeSpawners(t *testing.T) {
	f := newFixture(t)
	f.addSpawner(t, "Local", "local", "ollama", t.TempDir())

	rows := f.accounts(t, "")

	require.Len(t, rows, 1)
	require.Len(t, rows[0].Spawners, 1)
	require.Equal(t, "Work", rows[0].Spawners[0].Name)
}

func TestCloudAccounts_RowsSortedByConfigDir(t *testing.T) {
	f := newFixture(t)
	other := t.TempDir()
	f.addSpawner(t, "Other", "other", "claude", other)

	rows := f.accounts(t, "")

	require.Len(t, rows, 2)
	require.Less(t, rows[0].ConfigDir, rows[1].ConfigDir)
}

func TestCloudAccounts_FailingLookupGivesNullAuthAndStill200(t *testing.T) {
	f := newFixture(t)
	f.cli.authErr = errors.New("claude auth status exploded")

	rr := f.do("GET", "/api/cloud-accounts", "")
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	var raw []map[string]any
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &raw))

	require.Len(t, raw, 1)
	require.Contains(t, raw[0], "auth")
	require.Nil(t, raw[0]["auth"])
	require.Equal(t, "claude auth status exploded", raw[0]["error"])
}

func TestCloudAccounts_ReusesReadyResultWithoutRefresh(t *testing.T) {
	f := newFixture(t)

	f.accounts(t, "")
	f.accounts(t, "")

	require.Equal(t, 1, f.cli.authCount())
}

func TestCloudAccounts_RefreshBypassesCache(t *testing.T) {
	f := newFixture(t)
	f.accounts(t, "")

	f.accounts(t, "?refresh=1")

	require.Equal(t, 2, f.cli.authCount())
}
