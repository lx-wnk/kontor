package repo_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/lx-wnk/kontor/server/internal/db"
	"github.com/lx-wnk/kontor/server/internal/db/repo"
)

func TestPromptTemplateRepo(t *testing.T) {
	bundle, err := db.Open(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { _ = bundle.Client.Close() })
	r := repo.NewPromptTemplateRepo(bundle.Client)
	ctx := context.Background()

	t.Run("create and list", func(t *testing.T) {
		tpl, err := r.Create(ctx, "greeting", "Hello {{name}}, welcome to {{place}}!")
		require.NoError(t, err)
		require.NotEmpty(t, tpl.ID)
		require.Equal(t, "greeting", tpl.Name)

		list, err := r.List(ctx)
		require.NoError(t, err)
		require.Len(t, list, 1)
		require.Equal(t, tpl.ID, list[0].ID)
	})

	t.Run("delete", func(t *testing.T) {
		tpl, err := r.Create(ctx, "bye", "Goodbye {{name}}!")
		require.NoError(t, err)

		require.NoError(t, r.Delete(ctx, tpl.ID))

		list, err := r.List(ctx)
		require.NoError(t, err)
		// only the "greeting" from the prior sub-test remains
		for _, item := range list {
			require.NotEqual(t, tpl.ID, item.ID)
		}
	})
}
