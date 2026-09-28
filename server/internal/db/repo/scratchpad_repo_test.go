package repo_test

import (
	"context"
	"testing"

	"github.com/lx-wnk/kontor/server/internal/db/repo"
	"github.com/stretchr/testify/require"
)

func TestScratchpad_WriteReadList(t *testing.T) {
	client := openDB(t)
	ctx := context.Background()
	r := repo.NewScratchpadRepo(client)

	// Write(ns1, k1, v1, task-A) then Read → value v1
	require.NoError(t, r.Write(ctx, "ns1", "k1", "v1", "task-A"))
	row, err := r.Read(ctx, "ns1", "k1")
	require.NoError(t, err)
	require.NotNil(t, row)
	require.Equal(t, "v1", row.Value)
	require.Equal(t, "task-A", row.UpdatedByTaskID)

	// Write(ns1, k1, v2, task-B) → upsert overwrites value + UpdatedByTaskID
	require.NoError(t, r.Write(ctx, "ns1", "k1", "v2", "task-B"))
	row, err = r.Read(ctx, "ns1", "k1")
	require.NoError(t, err)
	require.NotNil(t, row)
	require.Equal(t, "v2", row.Value)
	require.Equal(t, "task-B", row.UpdatedByTaskID)

	// Write(ns1, k2, ...) then List(ns1) → 2 entries
	require.NoError(t, r.Write(ctx, "ns1", "k2", "vX", "task-C"))
	list, err := r.List(ctx, "ns1")
	require.NoError(t, err)
	require.Len(t, list, 2)

	// Read(ns1, missing) → nil, no error
	missing, err := r.Read(ctx, "ns1", "no-such-key")
	require.NoError(t, err)
	require.Nil(t, missing)
}
