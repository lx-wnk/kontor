package repo_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lx-wnk/kontor/server/internal/db/repo"
)

func TestAppSettingRepo_UpsertGetList(t *testing.T) {
	client := openTestDB(t)
	r := repo.NewAppSettingRepo(client)
	ctx := t.Context()

	_, err := r.Upsert(ctx, "auth.mode", "plugin")
	require.NoError(t, err)
	// upsert again updates value, not duplicates
	_, err = r.Upsert(ctx, "auth.mode", "none")
	require.NoError(t, err)

	v, ok, err := r.Get(ctx, "auth.mode")
	require.NoError(t, err)
	assert.True(t, ok)
	assert.Equal(t, "none", v)

	_, ok, err = r.Get(ctx, "missing.key")
	require.NoError(t, err)
	assert.False(t, ok)

	all, err := r.List(ctx)
	require.NoError(t, err)
	assert.Len(t, all, 1)
}

func TestAppSettingRepo_SecretRoundTrip(t *testing.T) {
	client := openTestDB(t)
	r := repo.NewAppSettingRepo(client)
	ctx := t.Context()

	_, err := r.UpsertSecret(ctx, "obsidian.apiKey", "Y2lwaGVy", "bm9uY2U=")
	require.NoError(t, err)

	ct, nonce, ok, err := r.GetSecret(ctx, "obsidian.apiKey")
	require.NoError(t, err)
	assert.True(t, ok)
	assert.Equal(t, "Y2lwaGVy", ct)
	assert.Equal(t, "bm9uY2U=", nonce)

	// A plaintext row reports no nonce, so a reader can tell the two apart
	// without consulting the registry.
	_, err = r.Upsert(ctx, "git.allowPush", "true")
	require.NoError(t, err)
	_, nonce2, ok2, err := r.GetSecret(ctx, "git.allowPush")
	require.NoError(t, err)
	assert.True(t, ok2)
	assert.Empty(t, nonce2)
}
