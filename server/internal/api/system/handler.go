// Package system provides system status HTTP handlers.
package system

import (
	"context"
	"encoding/json"
	"net/http"
	"runtime"
	"sync"
	"time"

	"github.com/lx-wnk/kontor/server/internal/version"
)

var startTime = time.Now()

// sourceVersionTTL bounds the age of the cached "git describe" result. The
// frontend polls /api/system/health continuously; without a cache, every poll
// would shell out to git for a value that only changes when a new commit or
// edit lands. A TTL (rather than a one-shot sync.Once) is deliberate: the
// process can run for days while its source tree moves on underneath it --
// exactly the staleness this endpoint exists to surface -- so the lookup must
// keep refreshing for the life of the process, not just once at boot.
const sourceVersionTTL = 10 * time.Second

var sourceVersionCache struct {
	mu      sync.Mutex
	value   string
	fetched time.Time
}

// sourceVersion runs "git describe --tags --always --dirty" in the process's
// working directory (the directory the server was started from) and returns
// its trimmed output, cached for sourceVersionTTL.
//
// It never errors: an empty result means the directory isn't a git repo, git
// isn't installed, or the command failed for any other reason. Health must
// answer regardless -- a broken version check is not a broken server.
func sourceVersion() string {
	sourceVersionCache.mu.Lock()
	defer sourceVersionCache.mu.Unlock()

	if time.Since(sourceVersionCache.fetched) < sourceVersionTTL {
		return sourceVersionCache.value
	}

	v := version.DescribeIn(context.Background(), "")
	sourceVersionCache.value = v
	sourceVersionCache.fetched = time.Now()
	return v
}

// sourceVersionFn is sourceVersion behind a package var so tests can stub it
// without shelling out to git or waiting out sourceVersionTTL.
var sourceVersionFn = sourceVersion

// HealthHandler handles GET /api/system/health.
func HealthHandler(w http.ResponseWriter, r *http.Request) {
	v := version.Version
	sv := sourceVersionFn()
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"status":        "ok",
		"uptime":        time.Since(startTime).Seconds(),
		"go":            runtime.Version(),
		"platform":      runtime.GOOS,
		"version":       v,
		"sourceVersion": sv,
		"stale":         sv != "" && v != "" && sv != v,
	})
}
