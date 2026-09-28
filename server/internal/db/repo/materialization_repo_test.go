package repo_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/lx-wnk/kontor/server/internal/db"
	"github.com/lx-wnk/kontor/server/internal/db/repo"
)

func newMaterializationRepo(t *testing.T) (repo.MaterializationRepo, context.Context) {
	t.Helper()
	bundle, err := db.Open(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { _ = bundle.Client.Close() })
	return repo.NewMaterializationRepo(bundle.Client), context.Background()
}

func TestMaterialization_GetAbsentIsNotAnError(t *testing.T) {
	r, ctx := newMaterializationRepo(t)

	got, err := r.Get(ctx, "res-1", "claude|user|/tmp/cfg")
	require.NoError(t, err, "never materialized here is an ordinary state, not a failure")
	require.Nil(t, got)
}

func TestMaterialization_RecordThenGet(t *testing.T) {
	r, ctx := newMaterializationRepo(t)

	_, err := r.Record(ctx, repo.RecordMaterializationInput{
		ResourceID: "res-1", TargetKey: "claude|user|/tmp/cfg",
		Path: "/tmp/cfg/skills/review/SKILL.md", ContentHash: "abc", Outcome: "created",
	})
	require.NoError(t, err)

	got, err := r.Get(ctx, "res-1", "claude|user|/tmp/cfg")
	require.NoError(t, err)
	require.NotNil(t, got)
	require.Equal(t, "/tmp/cfg/skills/review/SKILL.md", got.Path)
	require.Equal(t, "abc", got.ContentHash)
	require.Equal(t, "created", got.Outcome)
}

func TestMaterialization_RecordIsIdempotentPerTarget(t *testing.T) {
	r, ctx := newMaterializationRepo(t)

	for _, hash := range []string{"abc", "def"} {
		_, err := r.Record(ctx, repo.RecordMaterializationInput{
			ResourceID: "res-1", TargetKey: "claude|user|/tmp/cfg",
			Path: "/tmp/cfg/skills/review/SKILL.md", ContentHash: hash, Outcome: "repaired",
		})
		require.NoError(t, err)
	}

	rows, err := r.ListForResource(ctx, "res-1")
	require.NoError(t, err)
	require.Len(t, rows, 1, "one row per (resource, target) — a second row would orphan the first hash")
	require.Equal(t, "def", rows[0].ContentHash)
}

func TestMaterialization_TargetsAreIndependent(t *testing.T) {
	r, ctx := newMaterializationRepo(t)

	for _, key := range []string{"claude|user|/tmp/a", "claude|user|/tmp/b"} {
		_, err := r.Record(ctx, repo.RecordMaterializationInput{
			ResourceID: "res-1", TargetKey: key, Path: key + "/SKILL.md", ContentHash: "h", Outcome: "created",
		})
		require.NoError(t, err)
	}

	rows, err := r.ListForResource(ctx, "res-1")
	require.NoError(t, err)
	require.Len(t, rows, 2, "one skill, two config dirs, two records")
}

func TestMaterialization_ForeignRecordCarriesAnEmptyHash(t *testing.T) {
	r, ctx := newMaterializationRepo(t)

	_, err := r.Record(ctx, repo.RecordMaterializationInput{
		ResourceID: "res-1", TargetKey: "claude|user|/tmp/cfg",
		Path: "/tmp/cfg/skills/review/SKILL.md", Outcome: "foreign",
	})
	require.NoError(t, err)

	got, err := r.Get(ctx, "res-1", "claude|user|/tmp/cfg")
	require.NoError(t, err)
	require.Equal(t, "", got.ContentHash, "an empty hash is what keeps a foreign file foreign on the next run")
}
