package obsidian

import (
	"context"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"

	obsidianapp "github.com/lx-wnk/kontor/server/internal/apps/obsidian"
)

const graphTTL = 60 * time.Second

type graphBuilder func(context.Context) (obsidianapp.Graph, error)

type graphCache struct {
	now   func() time.Time
	group singleflight.Group
	mu    sync.Mutex
	at    time.Time
	graph *obsidianapp.Graph
}

func (c *graphCache) get(ctx context.Context, build graphBuilder) (obsidianapp.Graph, error) {
	if g, ok := c.cached(); ok {
		return g, nil
	}
	return c.rebuild(ctx, build, true)
}

// refresh joins a rebuild already running, rather than starting a second one.
func (c *graphCache) refresh(ctx context.Context, build graphBuilder) (obsidianapp.Graph, error) {
	return c.rebuild(ctx, build, false)
}

func (c *graphCache) cached() (obsidianapp.Graph, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.graph == nil || c.now().Sub(c.at) >= graphTTL {
		return obsidianapp.Graph{}, false
	}
	return *c.graph, true
}

func (c *graphCache) rebuild(ctx context.Context, build graphBuilder, reuseFresh bool) (obsidianapp.Graph, error) {
	v, err, _ := c.group.Do("graph", func() (any, error) {
		// A rebuild that finished after this caller's cache check has already stored a fresh graph.
		if reuseFresh {
			if g, ok := c.cached(); ok {
				return g, nil
			}
		}
		// Shared by every waiter, so one caller hanging up must not cancel it.
		g, err := build(context.WithoutCancel(ctx))
		if err != nil {
			return nil, err
		}
		c.mu.Lock()
		c.graph, c.at = &g, c.now()
		c.mu.Unlock()
		return g, nil
	})
	if err != nil {
		return obsidianapp.Graph{}, err
	}
	return v.(obsidianapp.Graph), nil
}
