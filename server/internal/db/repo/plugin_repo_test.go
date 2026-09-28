package repo_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lx-wnk/kontor/server/internal/db/repo"
)

func TestPluginRepo_Lifecycle(t *testing.T) {
	client := openTestDB(t)
	r := repo.NewPluginRepo(client)
	ctx := t.Context()

	// Upsert (discover) a plugin
	_, err := r.Upsert(ctx, repo.UpsertPluginInput{ID: "p1", Name: "P1", Version: "1.0.0", Path: "/x", ManifestHash: "h1"})
	require.NoError(t, err)

	got, err := r.Get(ctx, "p1")
	require.NoError(t, err)
	assert.Nil(t, got.InstalledAt) // discovered
	assert.False(t, got.Active)

	now := time.Now()
	require.NoError(t, r.SetInstalledAt(ctx, "p1", &now))
	require.NoError(t, r.SetActive(ctx, "p1", true))
	got, _ = r.Get(ctx, "p1")
	require.NotNil(t, got.InstalledAt)
	assert.True(t, got.Active)

	all, err := r.List(ctx)
	require.NoError(t, err)
	assert.Len(t, all, 1)

	require.NoError(t, r.Delete(ctx, "p1"))
	_, err = r.Get(ctx, "p1")
	assert.True(t, repo.IsNotFound(err))
}
