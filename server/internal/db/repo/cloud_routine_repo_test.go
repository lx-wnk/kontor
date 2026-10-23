package repo_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/lx-wnk/kontor/server/internal/db/ent"
	"github.com/lx-wnk/kontor/server/internal/db/repo"
)

func newRoutineInput(name, token string) repo.CreateCloudRoutineInput {
	return repo.CreateCloudRoutineInput{
		Name:      name,
		FireURL:   "https://api.anthropic.com/v1/claude_code/routines/trig_1/fire",
		SpawnerID: "spawner-1",
		Token:     token,
	}
}

func TestCloudRoutineRepo_RoundTripEncryptsToken(t *testing.T) {
	client := openDB(t)
	routines := repo.NewCloudRoutineRepo(client, newBox(t))
	ctx := context.Background()

	created, err := routines.Create(ctx, newRoutineInput("nightly", "sk-secret"))
	require.NoError(t, err)
	require.True(t, repo.CloudRoutineHasToken(created))

	plain, err := routines.Token(ctx, created.ID)
	require.NoError(t, err)
	require.Equal(t, "sk-secret", plain)

	row, err := client.CloudRoutine.Get(ctx, created.ID)
	require.NoError(t, err)
	require.NotContains(t, row.TokenCiphertext, "sk-secret")
	require.NotEmpty(t, row.TokenNonce)
}

func TestCloudRoutineRepo_ListOrdersByName(t *testing.T) {
	routines := repo.NewCloudRoutineRepo(openDB(t), newBox(t))
	ctx := context.Background()
	for _, name := range []string{"b", "c", "a"} {
		_, err := routines.Create(ctx, newRoutineInput(name, "t"))
		require.NoError(t, err)
	}

	list, err := routines.List(ctx)
	require.NoError(t, err)
	require.Len(t, list, 3)
	require.Equal(t, []string{"a", "b", "c"}, []string{list[0].Name, list[1].Name, list[2].Name})
}

func TestCloudRoutineRepo_UpdateKeepsTokenWhenEmptyOrNil(t *testing.T) {
	routines := repo.NewCloudRoutineRepo(openDB(t), newBox(t))
	ctx := context.Background()
	created, err := routines.Create(ctx, newRoutineInput("nightly", "old"))
	require.NoError(t, err)

	name, empty := "renamed", ""
	updated, err := routines.Update(ctx, created.ID, repo.UpdateCloudRoutineInput{Name: &name, Token: &empty})
	require.NoError(t, err)
	require.Equal(t, "renamed", updated.Name)

	_, err = routines.Update(ctx, created.ID, repo.UpdateCloudRoutineInput{})
	require.NoError(t, err)

	plain, err := routines.Token(ctx, created.ID)
	require.NoError(t, err)
	require.Equal(t, "old", plain)
}

func TestCloudRoutineRepo_UpdateReplacesTokenAndFireURL(t *testing.T) {
	routines := repo.NewCloudRoutineRepo(openDB(t), newBox(t))
	ctx := context.Background()
	created, err := routines.Create(ctx, newRoutineInput("nightly", "old"))
	require.NoError(t, err)

	token, url := "new", "https://api.anthropic.com/v1/claude_code/routines/trig_2/fire"
	updated, err := routines.Update(ctx, created.ID, repo.UpdateCloudRoutineInput{Token: &token, FireURL: &url})
	require.NoError(t, err)
	require.Equal(t, url, updated.FireURL)

	plain, err := routines.Token(ctx, created.ID)
	require.NoError(t, err)
	require.Equal(t, "new", plain)
}

func TestCloudRoutineRepo_WithoutBoxRefusesToStoreOrReveal(t *testing.T) {
	client := openDB(t)
	ctx := context.Background()
	withBox, err := repo.NewCloudRoutineRepo(client, newBox(t)).Create(ctx, newRoutineInput("nightly", "x"))
	require.NoError(t, err)

	routines := repo.NewCloudRoutineRepo(client, nil)
	_, err = routines.Create(ctx, newRoutineInput("other", "x"))
	require.ErrorIs(t, err, repo.ErrSecretsUnavailable)
	_, err = routines.Token(ctx, withBox.ID)
	require.ErrorIs(t, err, repo.ErrSecretsUnavailable)

	token := "new"
	_, err = routines.Update(ctx, withBox.ID, repo.UpdateCloudRoutineInput{Token: &token})
	require.ErrorIs(t, err, repo.ErrSecretsUnavailable)
}

func TestCloudRoutineRepo_DeleteAndUnknownID(t *testing.T) {
	routines := repo.NewCloudRoutineRepo(openDB(t), newBox(t))
	ctx := context.Background()
	created, err := routines.Create(ctx, newRoutineInput("nightly", "x"))
	require.NoError(t, err)

	require.NoError(t, routines.Delete(ctx, created.ID))
	_, err = routines.Get(ctx, created.ID)
	require.True(t, ent.IsNotFound(err))

	require.True(t, ent.IsNotFound(routines.Delete(ctx, "missing")))
	_, err = routines.Token(ctx, "missing")
	require.True(t, ent.IsNotFound(err))
	_, err = routines.Update(ctx, "missing", repo.UpdateCloudRoutineInput{})
	require.True(t, ent.IsNotFound(err))
}
