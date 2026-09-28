package repo_test

import (
	"context"
	"testing"

	"github.com/lx-wnk/kontor/server/internal/db/repo"
	"github.com/stretchr/testify/require"
)

func TestCreateTask_DefaultsToPipelineKind(t *testing.T) {
	client := openDB(t)
	r := repo.NewTaskRepo(client)
	ctx := context.Background()

	got, err := r.Create(ctx, repo.CreateTaskInput{
		Slug:          "kind-default",
		Title:         "Kind Default",
		Cwd:           "/tmp",
		CurrentStage:  "backlog",
		Priority:      "medium",
		MaxIterations: 20,

		Kind: "",
	})
	require.NoError(t, err)
	require.Equal(t, "pipeline", got.Kind)
}

func TestCreateTask_StoresJobKind(t *testing.T) {
	client := openDB(t)
	r := repo.NewTaskRepo(client)
	ctx := context.Background()

	got, err := r.Create(ctx, repo.CreateTaskInput{
		Slug:          "kind-job",
		Title:         "Kind Job",
		Cwd:           "/tmp",
		CurrentStage:  "backlog",
		Priority:      "medium",
		MaxIterations: 20,

		Kind: "job",
	})
	require.NoError(t, err)
	require.Equal(t, "job", got.Kind)

	reread, err := r.GetByID(ctx, got.ID)
	require.NoError(t, err)
	require.Equal(t, "job", reread.Kind)
}
