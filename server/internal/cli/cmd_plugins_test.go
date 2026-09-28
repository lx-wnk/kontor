package cli

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/lx-wnk/kontor/server/internal/db/repo"
)

func seedPlugin(t *testing.T, dbPath, id string, active bool) {
	t.Helper()
	store, err := openDBStore(dbPath)
	require.NoError(t, err)
	defer func() { _ = store.Close() }()
	pr := repo.NewPluginRepo(store.client)
	_, err = pr.Upsert(context.Background(), repo.UpsertPluginInput{ID: id})
	require.NoError(t, err)
	require.NoError(t, pr.SetActive(context.Background(), id, active))
}

func pluginActive(t *testing.T, dbPath, id string) bool {
	t.Helper()
	store, err := openDBStore(dbPath)
	require.NoError(t, err)
	defer func() { _ = store.Close() }()
	row, err := repo.NewPluginRepo(store.client).Get(context.Background(), id)
	require.NoError(t, err)
	return row.Active
}

func TestPluginsDisableSetsInactive(t *testing.T) {
	dbPath := t.TempDir() + "/test.db"
	seedPlugin(t, dbPath, "github-oauth", true)

	cmd := newPluginsCmd()
	cmd.SetArgs([]string{"disable", "github-oauth", "--db", dbPath})
	require.NoError(t, cmd.Execute())

	require.False(t, pluginActive(t, dbPath, "github-oauth"))
}

func TestPluginsEnableSetsActive(t *testing.T) {
	dbPath := t.TempDir() + "/test.db"
	seedPlugin(t, dbPath, "github-oauth", false)

	cmd := newPluginsCmd()
	cmd.SetArgs([]string{"enable", "github-oauth", "--db", dbPath})
	require.NoError(t, cmd.Execute())

	require.True(t, pluginActive(t, dbPath, "github-oauth"))
}

func TestPluginsDisableUnknownErrors(t *testing.T) {
	dbPath := t.TempDir() + "/test.db"
	seedPlugin(t, dbPath, "other", true) // ensures DB exists

	cmd := newPluginsCmd()
	cmd.SetArgs([]string{"disable", "nope", "--db", dbPath})
	require.Error(t, cmd.Execute())
}

func TestPluginsEnableUnknownErrors(t *testing.T) {
	dbPath := t.TempDir() + "/test.db"
	seedPlugin(t, dbPath, "other", false) // ensures DB is initialised

	cmd := newPluginsCmd()
	cmd.SetArgs([]string{"enable", "nope", "--db", dbPath})
	require.Error(t, cmd.Execute())
}
