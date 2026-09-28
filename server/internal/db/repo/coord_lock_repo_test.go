package repo_test

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lx-wnk/kontor/server/internal/db/repo"
	"github.com/stretchr/testify/require"
)

func TestCoordLock_Semantics(t *testing.T) {
	client := openDB(t)
	ctx := context.Background()
	r := repo.NewCoordLockRepo(client)

	// acquire free lock as A → acquired=true, owner=A
	ok, owner, _, err := r.Acquire(ctx, "ns", "key1", "A", time.Minute)
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, "A", owner)

	// acquire held lock as B → acquired=false, owner still A
	ok, owner, _, err = r.Acquire(ctx, "ns", "key1", "B", time.Minute)
	require.NoError(t, err)
	require.False(t, ok)
	require.Equal(t, "A", owner)

	// re-entrant: A acquires again → true
	ok, owner, _, err = r.Acquire(ctx, "ns", "key1", "A", time.Minute)
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, "A", owner)

	// B tries to release → error
	err = r.Release(ctx, "ns", "key1", "B")
	require.Error(t, err)

	// A releases → ok
	require.NoError(t, r.Release(ctx, "ns", "key1", "A"))

	// B acquires now-free lock → true, owner B
	ok, owner, _, err = r.Acquire(ctx, "ns", "key1", "B", time.Minute)
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, "B", owner)
}

func TestCoordLock_AcquireAfterExpiry(t *testing.T) {
	client := openDB(t)
	ctx := context.Background()
	r := repo.NewCoordLockRepo(client)

	// A acquires with negative TTL → already expired
	ok, _, _, err := r.Acquire(ctx, "ns", "key2", "A", -time.Second)
	require.NoError(t, err)
	require.True(t, ok)

	// B acquires expired lock → true, owner B
	ok, owner, _, err := r.Acquire(ctx, "ns", "key2", "B", time.Minute)
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, "B", owner)
}

func TestCoordLock_FreeRace(t *testing.T) {
	client := openDB(t)
	ctx := context.Background()
	r := repo.NewCoordLockRepo(client)

	const goroutines = 8
	var acquired atomic.Int64
	var wg sync.WaitGroup
	wg.Add(goroutines)

	for i := range goroutines {
		go func(i int) {
			defer wg.Done()
			ok, _, _, err := r.Acquire(ctx, "race-ns", "race-key", fmt.Sprintf("owner-%d", i), time.Minute)
			if err == nil && ok {
				acquired.Add(1)
			}
		}(i)
	}

	wg.Wait()
	require.Equal(t, int64(1), acquired.Load(), "exactly one goroutine must acquire the lock")
}
