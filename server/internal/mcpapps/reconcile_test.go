package mcpapps_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/lx-wnk/kontor/server/internal/channelconfig"
	"github.com/lx-wnk/kontor/server/internal/db"
	"github.com/lx-wnk/kontor/server/internal/db/repo"
	"github.com/lx-wnk/kontor/server/internal/mcpapps"
)

type reconcileFixture struct {
	resources repo.ResourceRepo
	apps      repo.MCPApplicationRepo
	markers   db.MarkerStore
}

func newReconcileFixture(t *testing.T) reconcileFixture {
	t.Helper()
	bundle, err := db.Open(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { _ = bundle.Client.Close() })
	return reconcileFixture{
		resources: repo.NewResourceRepo(bundle.Client),
		apps:      repo.NewMCPApplicationRepo(bundle.Client),
		markers:   db.MarkerStore{DB: bundle.DB},
	}
}

func servers(names ...string) map[string]json.RawMessage {
	out := map[string]json.RawMessage{}
	for _, n := range names {
		out[n] = json.RawMessage(`{"command":"true"}`)
	}
	return out
}

func TestReconcile_FirstRunAttachesToAllLaterServersDoNot(t *testing.T) {
	f := newReconcileFixture(t)
	ctx := context.Background()

	n, err := mcpapps.Reconcile(ctx, servers("obsidian"), f.resources, f.apps, f.markers)
	require.NoError(t, err)
	require.Equal(t, 1, n)

	n, err = mcpapps.Reconcile(ctx, servers("obsidian", "mail"), f.resources, f.apps, f.markers)
	require.NoError(t, err)
	require.Equal(t, 2, n)

	rows, err := f.apps.List(ctx)
	require.NoError(t, err)
	byName := map[string]bool{}
	for _, r := range rows {
		byName[r.ServerName] = r.AttachAll
	}
	require.True(t, byName["obsidian"], "a server present before this feature keeps reaching every run")
	require.False(t, byName["mail"], "a server added later must never reach a run nobody attached it to")
}

func TestReconcile_EmptyFirstRunStillRecordsMarker(t *testing.T) {
	f := newReconcileFixture(t)
	ctx := context.Background()

	_, err := mcpapps.Reconcile(ctx, nil, f.resources, f.apps, f.markers)
	require.NoError(t, err)
	_, err = mcpapps.Reconcile(ctx, servers("mail"), f.resources, f.apps, f.markers)
	require.NoError(t, err)

	row, err := f.apps.List(ctx)
	require.NoError(t, err)
	require.Len(t, row, 1)
	require.False(t, row[0].AttachAll)
}

func TestReconcile_SkipsReservedNamesAndIsIdempotent(t *testing.T) {
	f := newReconcileFixture(t)
	ctx := context.Background()
	reserved := channelconfig.ChannelServerName
	require.True(t, channelconfig.IsReservedServerName(reserved))

	for range 2 {
		_, err := mcpapps.Reconcile(ctx, servers(reserved, "mail"), f.resources, f.apps, f.markers)
		require.NoError(t, err)
	}
	rows, err := f.apps.List(ctx)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.Equal(t, "mail", rows[0].ServerName)

	res, err := f.resources.Get(ctx, repo.ResourceKindApplication, repo.GlobalScope(), mcpapps.ResourceSlug("mail"))
	require.NoError(t, err)
	require.Equal(t, rows[0].ResourceID, res.ID)
}
