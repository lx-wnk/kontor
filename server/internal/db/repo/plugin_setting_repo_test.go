package repo_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lx-wnk/kontor/server/internal/db/repo"
)

func TestPluginSettingRepo_CRUD(t *testing.T) {
	client := openTestDB(t)
	r := repo.NewPluginSettingRepo(client)
	ctx := t.Context()

	first, err := r.Upsert(ctx, repo.PluginSettingInput{PluginID: "p1", Key: "endpoint", Value: "https://x", Secret: false})
	require.NoError(t, err)
	_, err = r.Upsert(ctx, repo.PluginSettingInput{PluginID: "p1", Key: "apiKey", Value: "ciph", Secret: true, Nonce: "n"})
	require.NoError(t, err)
	// upsert same key updates, no dup
	second, err := r.Upsert(ctx, repo.PluginSettingInput{PluginID: "p1", Key: "endpoint", Value: "https://y", Secret: false})
	require.NoError(t, err)

	// re-upsert updates the value but preserves the row identity and created_at
	assert.Equal(t, "https://y", second.Value)
	assert.Equal(t, first.ID, second.ID)
	assert.Equal(t, first.CreatedAt, second.CreatedAt)

	rows, err := r.ListByPlugin(ctx, "p1")
	require.NoError(t, err)
	assert.Len(t, rows, 2)

	require.NoError(t, r.DeleteByPlugin(ctx, "p1"))
	rows, _ = r.ListByPlugin(ctx, "p1")
	assert.Empty(t, rows)
}
