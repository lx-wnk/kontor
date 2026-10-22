package agentbroadcast

import (
	"sync"
	"time"

	"github.com/lx-wnk/kontor/server/internal/pathutil"
)

// dirCacheTTL bounds how long a canonicalised config dir is reused. The answer
// only changes when someone retargets a symlink or moves a profile directory,
// which is rare enough that a short window costs nothing and long enough to
// absorb the enricher's real call rate.
const dirCacheTTL = 30 * time.Second

// dirResolver memoises pathutil.Canonical.
//
// Spawner attribution runs on every agent read — the broadcast tick plus seven
// other call sites, one of them a hook rescan debounced at 100ms — and resolves
// one config dir per spawner and per agent each time. Every one of those is an
// EvalSymlinks walk of a path that has not changed since the last read seconds
// earlier. Keyed by the raw directory string rather than by PID: attribution no
// longer looks at processes at all, and agents sharing a profile share one entry.
//
// Safe for concurrent use: the SSE loop and the HTTP read paths enrich at the
// same time.
type dirResolver struct {
	mu  sync.Mutex
	ttl time.Duration
	now func() time.Time
	// resolve is pathutil.Canonical; a seam so tests can count filesystem walks.
	resolve func(string) string
	entries map[string]dirEntry
}

type dirEntry struct {
	canonical string
	expires   time.Time
}

func newDirResolver(ttl time.Duration, now func() time.Time) *dirResolver {
	return &dirResolver{ttl: ttl, now: now, resolve: pathutil.Canonical, entries: make(map[string]dirEntry)}
}

// canonical is pathutil.Canonical, answered from the cache while the entry is fresh.
func (r *dirResolver) canonical(dir string) string {
	if dir == "" {
		return ""
	}
	r.mu.Lock()
	defer r.mu.Unlock()

	now := r.now()
	if e, ok := r.entries[dir]; ok && now.Before(e.expires) {
		return e.canonical
	}
	// Only on a miss, so the sweep cost lands on the rare path and the map
	// cannot accumulate dirs of profiles that are no longer running.
	for k, e := range r.entries {
		if !now.Before(e.expires) {
			delete(r.entries, k)
		}
	}

	resolved := r.resolve(dir)
	r.entries[dir] = dirEntry{canonical: resolved, expires: now.Add(r.ttl)}
	return resolved
}
