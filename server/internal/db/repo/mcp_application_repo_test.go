package repo_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/lx-wnk/kontor/server/internal/db/ent/schema"
	"github.com/lx-wnk/kontor/server/internal/db/repo"
)

func TestMCPApplicationRepo_UpsertKeepsAttachAllOfExistingRow(t *testing.T) {
	apps := repo.NewMCPApplicationRepo(openDB(t))
	ctx := context.Background()

	first, err := apps.Upsert(ctx, repo.UpsertMCPApplicationInput{ResourceID: "res-1", ServerName: "mail", AttachAll: true})
	require.NoError(t, err)
	require.True(t, first.AttachAll)

	again, err := apps.Upsert(ctx, repo.UpsertMCPApplicationInput{ResourceID: "res-1", ServerName: "mail", AttachAll: false})
	require.NoError(t, err)
	require.Equal(t, first.ID, again.ID)
	require.True(t, again.AttachAll, "a later reconcile must not revoke a human's or the first reconcile's choice")
}

func TestMCPApplicationRepo_RecordCatalogueKeepsToolsOnError(t *testing.T) {
	apps := repo.NewMCPApplicationRepo(openDB(t))
	ctx := context.Background()
	_, err := apps.Upsert(ctx, repo.UpsertMCPApplicationInput{ResourceID: "res-1", ServerName: "mail"})
	require.NoError(t, err)

	tools := []schema.CatalogueTool{{Name: "search"}}
	require.NoError(t, apps.RecordCatalogue(ctx, "res-1", tools, "", time.Now()))
	require.NoError(t, apps.RecordCatalogue(ctx, "res-1", nil, "connect: refused", time.Now()))

	got, err := apps.GetByResourceID(ctx, "res-1")
	require.NoError(t, err)
	require.Equal(t, tools, got.Catalogue, "a failed refresh must not erase the last good catalogue")
	require.Equal(t, "connect: refused", got.CatalogueError)
}
