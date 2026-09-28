package mcpapps_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/lx-wnk/kontor/server/internal/channelconfig"
	"github.com/lx-wnk/kontor/server/internal/db"
	"github.com/lx-wnk/kontor/server/internal/db/ent"
	"github.com/lx-wnk/kontor/server/internal/db/repo"
	"github.com/lx-wnk/kontor/server/internal/mcp"
	"github.com/lx-wnk/kontor/server/internal/mcpapps"
)

type importFixture struct {
	apps    repo.MCPApplicationRepo
	markers db.MarkerStore
}

func newImportFixture(t *testing.T) importFixture {
	t.Helper()
	bundle, err := db.Open(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { _ = bundle.Client.Close() })
	return importFixture{
		apps:    repo.NewMCPApplicationRepo(bundle.Client),
		markers: db.MarkerStore{DB: bundle.DB},
	}
}

func (f importFixture) addApp(t *testing.T, resourceID, server string) {
	t.Helper()
	_, err := f.apps.Upsert(context.Background(), repo.UpsertMCPApplicationInput{
		ResourceID: resourceID, ServerName: server, AttachAll: true,
	})
	require.NoError(t, err)
}

func importServers(names ...string) map[string]json.RawMessage {
	out := map[string]json.RawMessage{}
	for _, n := range names {
		out[n] = json.RawMessage(`{"command":"` + n + `"}`)
	}
	return out
}

func TestImportEntries_CopiesEntrySkipsReservedReturnsCount(t *testing.T) {
	f := newImportFixture(t)
	ctx := context.Background()
	f.addApp(t, "res-mail", "mail")
	f.addApp(t, "res-notes", "notes")

	servers := importServers("mail", "notes", channelconfig.ChannelServerName, mcp.ServerName)

	n, err := mcpapps.ImportEntries(ctx, servers, f.apps, f.markers)
	require.NoError(t, err)
	require.Equal(t, 2, n)

	rows, err := f.apps.List(ctx)
	require.NoError(t, err)
	require.Len(t, rows, 2)
	for _, row := range rows {
		require.JSONEq(t, string(servers[row.ServerName]), string(row.Entry))
		require.True(t, row.ExportToClaude)
		require.Equal(t, mcpapps.EntryHash(servers[row.ServerName]), row.ExportedHash)
		require.True(t, row.AttachAll, "AttachAll must stay untouched")
	}

	seen, err := f.markers.Has(ctx, mcpapps.EntryImportMarker)
	require.NoError(t, err)
	require.True(t, seen)
}

func TestImportEntries_SecondCallImportsNothing(t *testing.T) {
	f := newImportFixture(t)
	ctx := context.Background()
	f.addApp(t, "res-mail", "mail")

	first := importServers("mail")
	n, err := mcpapps.ImportEntries(ctx, first, f.apps, f.markers)
	require.NoError(t, err)
	require.Equal(t, 1, n)

	changed := map[string]json.RawMessage{"mail": json.RawMessage(`{"command":"changed"}`)}
	n, err = mcpapps.ImportEntries(ctx, changed, f.apps, f.markers)
	require.NoError(t, err)
	require.Equal(t, 0, n)

	row, err := f.apps.GetByResourceID(ctx, "res-mail")
	require.NoError(t, err)
	require.JSONEq(t, string(first["mail"]), string(row.Entry))
}

func TestImportEntries_ServerWithNoApplicationRowIsSkipped(t *testing.T) {
	f := newImportFixture(t)
	ctx := context.Background()
	f.addApp(t, "res-mail", "mail")

	servers := importServers("mail", "orphan")

	n, err := mcpapps.ImportEntries(ctx, servers, f.apps, f.markers)
	require.NoError(t, err)
	require.Equal(t, 1, n)
}

type erroringAppsRepo struct {
	repo.MCPApplicationRepo
	failServer string
}

func (r erroringAppsRepo) SetEntry(ctx context.Context, resourceID string, entry json.RawMessage) (*ent.MCPApplication, error) {
	row, err := r.GetByResourceID(ctx, resourceID)
	if err == nil && row.ServerName == r.failServer {
		return nil, errors.New("boom")
	}
	return r.MCPApplicationRepo.SetEntry(ctx, resourceID, entry)
}

func TestImportEntries_ErrorAbortsWithoutRecordingMarker(t *testing.T) {
	f := newImportFixture(t)
	ctx := context.Background()
	f.addApp(t, "res-mail", "mail")
	failing := erroringAppsRepo{MCPApplicationRepo: f.apps, failServer: "mail"}

	_, err := mcpapps.ImportEntries(ctx, importServers("mail"), failing, f.markers)
	require.Error(t, err)

	seen, err := f.markers.Has(ctx, mcpapps.EntryImportMarker)
	require.NoError(t, err)
	require.False(t, seen)
}

func TestEntryHash_IgnoresFormattingAndKeyOrder(t *testing.T) {
	indented := json.RawMessage("{\n  \"command\": \"uvx\",\n  \"args\": [\n    \"x\"\n  ]\n}")
	compact := json.RawMessage(`{"args":["x"],"command":"uvx"}`)
	require.Equal(t, mcpapps.EntryHash(compact), mcpapps.EntryHash(indented),
		"the same entry written two ways must not look like a change")
	require.NotEqual(t, mcpapps.EntryHash(compact), mcpapps.EntryHash(json.RawMessage(`{"command":"other"}`)))
	require.Equal(t, "", mcpapps.EntryHash(nil))
}
