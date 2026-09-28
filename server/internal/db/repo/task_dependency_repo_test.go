package repo_test

import (
	"context"
	"strings"
	"testing"

	"github.com/lx-wnk/kontor/server/internal/db/repo"
	"github.com/stretchr/testify/require"
)

func TestDependencyRepo_AddAndRemove(t *testing.T) {
	client := openDB(t)
	ctx := context.Background()

	tr := repo.NewTaskRepo(client)
	dr := repo.NewDependencyRepo(client)

	parentID := createTask(t, tr, "parent-task")
	childID := createTask(t, tr, "child-task")

	dep, err := dr.Add(ctx, childID, parentID, "done", "on_hold")
	require.NoError(t, err)
	require.Equal(t, childID, dep.TaskID)
	require.Equal(t, parentID, dep.DependsOnID)
	require.Equal(t, "done", dep.RequiredStage)
	require.Equal(t, "on_hold", dep.OnCancelAction)

	removed, err := dr.Remove(ctx, childID, parentID)
	require.NoError(t, err)
	require.True(t, removed)
}

func TestDependencyRepo_DuplicateAdd(t *testing.T) {
	client := openDB(t)
	ctx := context.Background()

	tr := repo.NewTaskRepo(client)
	dr := repo.NewDependencyRepo(client)

	parentID := createTask(t, tr, "parent-dup")
	childID := createTask(t, tr, "child-dup")

	_, err := dr.Add(ctx, childID, parentID, "done", "on_hold")
	require.NoError(t, err)

	_, err = dr.Add(ctx, childID, parentID, "done", "on_hold")
	require.Error(t, err)
	require.True(t, strings.Contains(err.Error(), "dependency.Add"))
}

func TestDependencyRepo_RemoveNonExistent(t *testing.T) {
	client := openDB(t)
	ctx := context.Background()

	dr := repo.NewDependencyRepo(client)

	removed, err := dr.Remove(ctx, "nonexistent-task", "nonexistent-dep")
	require.NoError(t, err)
	require.False(t, removed)
}
