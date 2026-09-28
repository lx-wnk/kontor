package pipeline

import (
	"context"
	"sync"
	"time"

	"github.com/lx-wnk/kontor/server/internal/db/repo"
)

type cachedConfig struct {
	value     int
	expiresAt time.Time
}

type cachedConfigStr struct {
	value     string
	expiresAt time.Time
}

// configCache is a 60s TTL cache in front of repo.PipelineConfigRepo reads,
// used by the orchestrator's hot poll loop to avoid a DB round trip per tick.
type configCache struct {
	repo repo.PipelineConfigRepo
	m    sync.Map // map[key string]cachedConfig | cachedConfigStr
}

// newConfigCache constructs a configCache backed by repo.
func newConfigCache(cfgRepo repo.PipelineConfigRepo) *configCache {
	return &configCache{repo: cfgRepo}
}

// Number returns the cached numeric config value for key, fetching and
// caching it via repo.GetNumber on a cache miss or expiry.
func (c *configCache) Number(ctx context.Context, key string, fallback int) int {
	if v, ok := c.m.Load(key); ok {
		cc, ok := v.(cachedConfig)
		if ok && time.Now().Before(cc.expiresAt) {
			return cc.value
		}
	}
	n := int(c.repo.GetNumber(ctx, key, float64(fallback)))
	c.m.Store(key, cachedConfig{value: n, expiresAt: time.Now().Add(60 * time.Second)})
	return n
}

// String returns the cached string config value for key, fetching and
// caching it via repo.GetString on a cache miss or expiry.
func (c *configCache) String(ctx context.Context, key string, fallback string) string {
	if v, ok := c.m.Load(key); ok {
		cc, ok := v.(cachedConfigStr)
		if ok && time.Now().Before(cc.expiresAt) {
			return cc.value
		}
	}
	s := c.repo.GetString(ctx, key, fallback)
	c.m.Store(key, cachedConfigStr{value: s, expiresAt: time.Now().Add(60 * time.Second)})
	return s
}

// Invalidate clears all cached entries — call after REST writes to pipeline_config.
func (c *configCache) Invalidate() {
	c.m.Range(func(k, _ any) bool { c.m.Delete(k); return true })
}
