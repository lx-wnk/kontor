package repo_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/lx-wnk/kontor/server/internal/db/repo"
	"github.com/lx-wnk/kontor/server/internal/secretbox"
)

func newBox(t *testing.T) *secretbox.Box {
	t.Helper()
	box, err := secretbox.New(make([]byte, 32))
	require.NoError(t, err)
	return box
}

func TestApplicationSecretRepo_RoundTripAndOverwrite(t *testing.T) {
	client := openDB(t)
	secrets := repo.NewApplicationSecretRepo(client, newBox(t))
	ctx := context.Background()

	require.NoError(t, secrets.Set(ctx, "res-1", "IMAP_PASSWORD", "first"))
	require.NoError(t, secrets.Set(ctx, "res-1", "IMAP_PASSWORD", "second"))
	require.NoError(t, secrets.Set(ctx, "res-2", "IMAP_PASSWORD", "other"))

	values, err := secrets.Values(ctx, "res-1")
	require.NoError(t, err)
	require.Equal(t, map[string]string{"IMAP_PASSWORD": "second"}, values)

	rows, err := client.ApplicationSecret.Query().All(ctx)
	require.NoError(t, err)
	require.Len(t, rows, 2)
	for _, row := range rows {
		for _, plain := range []string{"first", "second", "other"} {
			require.NotContains(t, row.Ciphertext, plain, "stored value must be encrypted")
		}
	}

	meta, err := secrets.List(ctx, "res-1")
	require.NoError(t, err)
	require.Len(t, meta, 1)
	require.Equal(t, "IMAP_PASSWORD", meta[0].EnvName)

	require.NoError(t, secrets.Delete(ctx, "res-1", "IMAP_PASSWORD"))
	values, err = secrets.Values(ctx, "res-1")
	require.NoError(t, err)
	require.Empty(t, values)
}

func TestApplicationSecretRepo_WithoutBoxRefusesToStore(t *testing.T) {
	secrets := repo.NewApplicationSecretRepo(openDB(t), nil)
	err := secrets.Set(context.Background(), "res-1", "IMAP_PASSWORD", "x")
	require.ErrorIs(t, err, repo.ErrSecretsUnavailable)
}
