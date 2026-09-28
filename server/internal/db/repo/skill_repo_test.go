package repo_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/lx-wnk/kontor/server/internal/db"
	"github.com/lx-wnk/kontor/server/internal/db/repo"
)

func newSkillRepo(t *testing.T) (repo.SkillRepo, repo.ResourceRepo, context.Context) {
	t.Helper()
	bundle, err := db.Open(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { _ = bundle.Client.Close() })
	return repo.NewSkillRepo(bundle.Client), repo.NewResourceRepo(bundle.Client), context.Background()
}

func TestSkill_UpsertThenRead(t *testing.T) {
	skills, resources, ctx := newSkillRepo(t)

	res, err := resources.Upsert(ctx, repo.UpsertResourceInput{
		Kind: repo.ResourceKindSkill, Slug: "code-review", Name: "Code Review",
		Scope: repo.GlobalScope(), State: repo.ResourceStateEnabled,
	})
	require.NoError(t, err)

	_, err = skills.Upsert(ctx, repo.UpsertSkillInput{
		ResourceID: res.ID, Description: "Review a diff", Body: "# Code Review\n\nRead the diff.\n",
	})
	require.NoError(t, err)

	got, err := skills.GetByResource(ctx, res.ID)
	require.NoError(t, err)
	require.Equal(t, "Review a diff", got.Description)
	require.Equal(t, "# Code Review\n\nRead the diff.\n", got.Body)
}

func TestSkill_UpsertReplacesBody(t *testing.T) {
	skills, resources, ctx := newSkillRepo(t)

	res, err := resources.Upsert(ctx, repo.UpsertResourceInput{
		Kind: repo.ResourceKindSkill, Slug: "code-review",
		Scope: repo.GlobalScope(), State: repo.ResourceStateEnabled,
	})
	require.NoError(t, err)

	for _, body := range []string{"first", "second"} {
		_, err = skills.Upsert(ctx, repo.UpsertSkillInput{ResourceID: res.ID, Body: body})
		require.NoError(t, err)
	}

	got, err := skills.GetByResource(ctx, res.ID)
	require.NoError(t, err)
	require.Equal(t, "second", got.Body, "a second upsert must replace, never create a second row")
}

func TestSkill_GetByResourceIsNotFoundForUnknownResource(t *testing.T) {
	skills, _, ctx := newSkillRepo(t)
	_, err := skills.GetByResource(ctx, "no-such-resource")
	require.Error(t, err, "a skill resource with no content is a real error: nothing can be materialized from it")
}
