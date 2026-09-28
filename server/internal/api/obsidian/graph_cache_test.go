package obsidian

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	obsidianapp "github.com/lx-wnk/kontor/server/internal/apps/obsidian"
)

var markerGraph = obsidianapp.Graph{Notes: []obsidianapp.GraphNote{{Path: "a.md", MtimeMs: 1}}}

func TestGraphCacheRebuildsOnlyOnceTheTTLHasPassed(t *testing.T) {
	clock := time.Unix(1_700_000_000, 0)
	cache := &graphCache{now: func() time.Time { return clock }}
	var builds int
	build := func(context.Context) (obsidianapp.Graph, error) {
		builds++
		return markerGraph, nil
	}

	_, err := cache.get(context.Background(), build)
	require.NoError(t, err)
	clock = clock.Add(59 * time.Second)
	_, err = cache.get(context.Background(), build)
	require.NoError(t, err)
	assert.Equal(t, 1, builds, "a graph younger than 60 s is served from the cache")

	clock = clock.Add(2 * time.Second)
	_, err = cache.get(context.Background(), build)
	require.NoError(t, err)
	assert.Equal(t, 2, builds, "a graph older than 60 s is rebuilt")
}

func TestGraphCacheSharesOneRebuildAmongConcurrentCallers(t *testing.T) {
	cache := &graphCache{now: time.Now}
	var builds atomic.Int32
	started := make(chan struct{})
	release := make(chan struct{})
	build := func(context.Context) (obsidianapp.Graph, error) {
		if builds.Add(1) == 1 {
			close(started)
		}
		<-release
		return markerGraph, nil
	}

	results := make([]obsidianapp.Graph, 10)
	errs := make([]error, 10)
	var wg sync.WaitGroup
	for i := range results {
		wg.Go(func() { results[i], errs[i] = cache.get(context.Background(), build) })
	}
	<-started
	time.Sleep(50 * time.Millisecond)
	close(release)
	wg.Wait()

	assert.Equal(t, int32(1), builds.Load())
	for i := range results {
		require.NoError(t, errs[i])
		assert.Equal(t, markerGraph, results[i])
	}
}

func TestGraphCacheReturnsAFailedRebuildToEveryWaiterAndRetriesNextTime(t *testing.T) {
	cache := &graphCache{now: time.Now}
	upstream := errors.New("vault down")
	var builds atomic.Int32
	started := make(chan struct{})
	release := make(chan struct{})
	failing := func(context.Context) (obsidianapp.Graph, error) {
		if builds.Add(1) == 1 {
			close(started)
		}
		<-release
		return obsidianapp.Graph{}, upstream
	}

	errs := make([]error, 5)
	var wg sync.WaitGroup
	for i := range errs {
		wg.Go(func() { _, errs[i] = cache.get(context.Background(), failing) })
	}
	<-started
	time.Sleep(50 * time.Millisecond)
	close(release)
	wg.Wait()
	for _, err := range errs {
		assert.ErrorIs(t, err, upstream)
	}

	g, err := cache.get(context.Background(), func(context.Context) (obsidianapp.Graph, error) { return markerGraph, nil })
	require.NoError(t, err)
	assert.Equal(t, markerGraph, g, "a failed rebuild is not cached")
}

func TestGraphCacheReusesAGraphStoredWhileACallerWaitedToRebuild(t *testing.T) {
	cache := &graphCache{now: time.Now}
	var builds int
	build := func(context.Context) (obsidianapp.Graph, error) {
		builds++
		return markerGraph, nil
	}

	_, err := cache.get(context.Background(), build)
	require.NoError(t, err)
	// A caller that missed the cache just before the store reaches the rebuild step next.
	g, err := cache.rebuild(context.Background(), build, true)
	require.NoError(t, err)
	assert.Equal(t, markerGraph, g)
	assert.Equal(t, 1, builds)
}

func TestGraphCacheRefreshRebuildsDespiteAFreshGraphAndStoresIt(t *testing.T) {
	cache := &graphCache{now: time.Now}
	refreshed := obsidianapp.Graph{Notes: []obsidianapp.GraphNote{{Path: "b.md", MtimeMs: 2}}}
	_, err := cache.get(context.Background(), func(context.Context) (obsidianapp.Graph, error) { return markerGraph, nil })
	require.NoError(t, err)

	g, err := cache.refresh(context.Background(), func(context.Context) (obsidianapp.Graph, error) { return refreshed, nil })
	require.NoError(t, err)
	assert.Equal(t, refreshed, g)

	g, err = cache.get(context.Background(), func(context.Context) (obsidianapp.Graph, error) {
		t.Fatal("the refreshed graph is cached, so get must not rebuild")
		return obsidianapp.Graph{}, nil
	})
	require.NoError(t, err)
	assert.Equal(t, refreshed, g)
}

func TestGraphCacheRequestDuringARunningRefreshSharesIt(t *testing.T) {
	cache := &graphCache{now: time.Now}
	var builds atomic.Int32
	started := make(chan struct{})
	release := make(chan struct{})
	build := func(context.Context) (obsidianapp.Graph, error) {
		if builds.Add(1) == 1 {
			close(started)
		}
		<-release
		return markerGraph, nil
	}

	var wg sync.WaitGroup
	var refreshed, got obsidianapp.Graph
	wg.Go(func() { refreshed, _ = cache.refresh(context.Background(), build) })
	<-started
	wg.Go(func() { got, _ = cache.get(context.Background(), build) })
	time.Sleep(50 * time.Millisecond)
	close(release)
	wg.Wait()

	assert.Equal(t, int32(1), builds.Load())
	assert.Equal(t, markerGraph, refreshed)
	assert.Equal(t, markerGraph, got)
}

func TestGraphCacheBuildsOnAContextTheCallerCannotCancel(t *testing.T) {
	cache := &graphCache{now: time.Now}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := cache.get(ctx, func(buildCtx context.Context) (obsidianapp.Graph, error) {
		return markerGraph, buildCtx.Err()
	})
	assert.NoError(t, err)
}
