package agentbroadcast

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	sdk "github.com/lx-wnk/kontor/sdk"
	"github.com/lx-wnk/kontor/server/internal/db/ent"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// countingSymlink builds a directory whose canonical form differs from its path
// and counts how often the filesystem is walked to establish that, by moving the
// symlink target and observing which answer comes back.
func symlinkTo(t *testing.T, target string) string {
	t.Helper()
	link := filepath.Join(t.TempDir(), "profile")
	require.NoError(t, os.Symlink(target, link))
	return link
}

// Resolving a config dir is an EvalSymlinks walk, and attribution runs on every
// agent read — the broadcast tick plus a hook rescan debounced at 100ms. The
// answer must be reused instead of re-walked once per spawner and per agent
// each time.
func TestResolvesAConfigDirOnceWithinTheCacheWindow(t *testing.T) {
	first := t.TempDir()
	link := symlinkTo(t, first)
	r := newDirResolver(time.Minute, time.Now)

	require.Equal(t, canonicalDir(first), r.canonical(link))

	// Re-point the symlink. A resolver that walks the filesystem again would
	// follow it; a cached one answers with what it already read.
	second := t.TempDir()
	require.NoError(t, os.Remove(link))
	require.NoError(t, os.Symlink(second, link))

	assert.Equal(t, canonicalDir(first), r.canonical(link), "the resolver walked the filesystem again inside the cache window")
}

// Cached is not frozen: a profile that gets moved or re-linked must be picked up
// within a bounded window, or attribution stays wrong until the server restarts.
func TestReResolvesAConfigDirOnceTheCacheWindowPasses(t *testing.T) {
	first := t.TempDir()
	link := symlinkTo(t, first)

	now := time.Now()
	r := newDirResolver(30*time.Second, func() time.Time { return now })
	require.Equal(t, canonicalDir(first), r.canonical(link))

	second := t.TempDir()
	require.NoError(t, os.Remove(link))
	require.NoError(t, os.Symlink(second, link))

	now = now.Add(31 * time.Second)

	assert.Equal(t, canonicalDir(second), r.canonical(link), "an expired entry must be resolved again")
}

// An entry nobody asks for again must not be kept forever: config dirs come from
// whatever profiles happen to be running, so the map would otherwise accumulate
// one entry per profile ever seen.
func TestDropsEntriesNobodyAsksForAgainAfterTheyExpire(t *testing.T) {
	now := time.Now()
	r := newDirResolver(30*time.Second, func() time.Time { return now })

	gone := t.TempDir()
	r.canonical(gone)
	require.Len(t, r.entries, 1)

	now = now.Add(31 * time.Second)
	r.canonical(t.TempDir())

	assert.NotContains(t, r.entries, gone, "the expired entry was never swept")
	assert.Len(t, r.entries, 1)
}

// The resolver is shared across enrichments, so a roster of agents on one
// profile costs one resolution per distinct directory per window — not one per
// agent per call, on all eight call sites.
func TestReusesResolutionsAcrossEnrichmentsAndAgents(t *testing.T) {
	store := t.TempDir()
	link := symlinkTo(t, store)
	rows := []*ent.Spawner{spawner("work-id", "Claude Work", link, false)}

	r := newDirResolver(time.Minute, time.Now)
	walk := r.resolve
	walks := 0
	r.resolve = func(dir string) string {
		walks++
		return walk(dir)
	}
	enricher := newSpawnerEnricher(stubSpawnerRepo{rows: rows}, nil, r)

	agents := []sdk.Agent{
		{PID: 1, ClaudeConfigDir: store, ClaudeConfigDirKnown: true},
		{PID: 2, ClaudeConfigDir: store, ClaudeConfigDirKnown: true},
		{PID: 3, ClaudeConfigDir: store, ClaudeConfigDirKnown: true},
	}
	enricher(t.Context(), agents)
	enricher(t.Context(), agents)

	for i := range agents {
		require.Equal(t, "work-id", agents[i].SpawnerID)
	}
	// The spawner's symlink and the agents' shared store: two walks, however
	// many agents and enrichments there are.
	assert.Equal(t, 2, walks)
}
