package repo_test

import (
	"context"
	"testing"

	"github.com/lx-wnk/kontor/server/internal/db/repo"
	"github.com/stretchr/testify/require"
)

func TestPipelineConfigRepo_SetAndGet(t *testing.T) {
	client := openDB(t)
	ctx := context.Background()
	cr := repo.NewPipelineConfigRepo(client)

	err := cr.Set(ctx, "maxParallelOrchestrators", "3")
	require.NoError(t, err)

	val := cr.GetNumber(ctx, "maxParallelOrchestrators", 1.0)
	require.Equal(t, 3.0, val)
}

func TestPipelineConfigRepo_Set_Upsert(t *testing.T) {
	client := openDB(t)
	ctx := context.Background()
	cr := repo.NewPipelineConfigRepo(client)

	err := cr.Set(ctx, "stageTimeoutSeconds", "1800")
	require.NoError(t, err)

	err = cr.Set(ctx, "stageTimeoutSeconds", "3600")
	require.NoError(t, err)

	val := cr.GetNumber(ctx, "stageTimeoutSeconds", 0.0)
	require.Equal(t, 3600.0, val)
}

func TestPipelineConfigRepo_GetAll(t *testing.T) {
	client := openDB(t)
	ctx := context.Background()
	cr := repo.NewPipelineConfigRepo(client)

	require.NoError(t, cr.Set(ctx, "key1", "val1"))
	require.NoError(t, cr.Set(ctx, "key2", "val2"))

	all, err := cr.GetAll(ctx)
	require.NoError(t, err)
	require.Equal(t, "val1", all["key1"])
	require.Equal(t, "val2", all["key2"])
}

func TestPipelineConfigRepo_GetNumber_Missing(t *testing.T) {
	client := openDB(t)
	ctx := context.Background()
	cr := repo.NewPipelineConfigRepo(client)

	val := cr.GetNumber(ctx, "nonexistent", 42.0)
	require.Equal(t, 42.0, val)
}

func TestPipelineConfigRepo_GetString_SetAndGet(t *testing.T) {
	client := openDB(t)
	ctx := context.Background()
	cr := repo.NewPipelineConfigRepo(client)

	require.NoError(t, cr.Set(ctx, "extraSafeBashCommands", "gh jq"))
	val := cr.GetString(ctx, "extraSafeBashCommands", "")
	require.Equal(t, "gh jq", val)
}

func TestPipelineConfigRepo_GetString_Missing(t *testing.T) {
	client := openDB(t)
	ctx := context.Background()
	cr := repo.NewPipelineConfigRepo(client)

	val := cr.GetString(ctx, "nonexistent", "default-val")
	require.Equal(t, "default-val", val)
}
