package repo_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/lx-wnk/kontor/server/internal/db/ent"
	"github.com/lx-wnk/kontor/server/internal/db/repo"
)

func TestMCPApplicationRepo_FreshRowHasEmptyEntryDefaults(t *testing.T) {
	apps := repo.NewMCPApplicationRepo(openDB(t))
	ctx := context.Background()

	row, err := apps.Upsert(ctx, repo.UpsertMCPApplicationInput{ResourceID: "res-1", ServerName: "mail"})
	require.NoError(t, err)
	require.JSONEq(t, "{}", string(row.Entry))
	require.False(t, row.ExportToClaude)
	require.Equal(t, "", row.ExportedHash)
}

func TestMCPApplicationRepo_SetEntryRoundTripsUnknownFields(t *testing.T) {
	apps := repo.NewMCPApplicationRepo(openDB(t))
	ctx := context.Background()
	_, err := apps.Upsert(ctx, repo.UpsertMCPApplicationInput{ResourceID: "res-1", ServerName: "mail"})
	require.NoError(t, err)

	entry := json.RawMessage(`{"type":"stdio","command":"x","args":["a"],"env":{"A":"b"},"weird":1}`)
	updated, err := apps.SetEntry(ctx, "res-1", entry)
	require.NoError(t, err)
	require.JSONEq(t, string(entry), string(updated.Entry))

	got, err := apps.GetByResourceID(ctx, "res-1")
	require.NoError(t, err)
	require.JSONEq(t, string(entry), string(got.Entry))
}

func TestMCPApplicationRepo_SetExportRoundTrips(t *testing.T) {
	apps := repo.NewMCPApplicationRepo(openDB(t))
	ctx := context.Background()
	_, err := apps.Upsert(ctx, repo.UpsertMCPApplicationInput{ResourceID: "res-1", ServerName: "mail"})
	require.NoError(t, err)

	updated, err := apps.SetExport(ctx, "res-1", true, "abc")
	require.NoError(t, err)
	require.True(t, updated.ExportToClaude)
	require.Equal(t, "abc", updated.ExportedHash)

	got, err := apps.GetByResourceID(ctx, "res-1")
	require.NoError(t, err)
	require.True(t, got.ExportToClaude)
	require.Equal(t, "abc", got.ExportedHash)
}

func TestMCPApplicationRepo_DeleteRemovesRow(t *testing.T) {
	apps := repo.NewMCPApplicationRepo(openDB(t))
	ctx := context.Background()
	_, err := apps.Upsert(ctx, repo.UpsertMCPApplicationInput{ResourceID: "res-1", ServerName: "mail"})
	require.NoError(t, err)

	require.NoError(t, apps.Delete(ctx, "res-1"))

	_, err = apps.GetByResourceID(ctx, "res-1")
	require.True(t, ent.IsNotFound(err))
}

func TestMCPApplicationRepo_UpsertLeavesEntryOfExistingRowUntouched(t *testing.T) {
	apps := repo.NewMCPApplicationRepo(openDB(t))
	ctx := context.Background()

	entry := json.RawMessage(`{"type":"stdio","command":"x"}`)
	_, err := apps.Upsert(ctx, repo.UpsertMCPApplicationInput{ResourceID: "res-1", ServerName: "mail", Entry: entry})
	require.NoError(t, err)
	_, err = apps.SetEntry(ctx, "res-1", entry)
	require.NoError(t, err)

	again, err := apps.Upsert(ctx, repo.UpsertMCPApplicationInput{ResourceID: "res-1", ServerName: "mail", Entry: json.RawMessage(`{"type":"http"}`)})
	require.NoError(t, err)
	require.JSONEq(t, string(entry), string(again.Entry), "an existing row's entry must survive a later reconcile")
}
