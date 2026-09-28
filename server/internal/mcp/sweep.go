package mcp

import (
	"context"
	"log/slog"
	"time"

	"github.com/lx-wnk/kontor/server/internal/db/repo"
)

// SweepExpiredKeys deletes expired stage-run credentials once at boot and then
// on every tick until ctx is cancelled. interval <= 0 runs the boot sweep only.
//
// These rows are deleted rather than deactivated: they carry no audit value —
// the stage run they name has its own record — and one row per stage run per
// retry, kept forever, turns the key table into a log. User keys are untouched;
// they are soft-deleted through ApiKeyRepo.Delete so their hash survives.
func SweepExpiredKeys(ctx context.Context, keys repo.ApiKeyRepo, interval time.Duration) {
	sweep := func() {
		n, err := keys.DeleteExpired(ctx, time.Now())
		if err != nil {
			slog.Warn("mcp.sweep: deleting expired stage-run keys failed", "err", err)
			return
		}
		if n > 0 {
			slog.Info("mcp.sweep: removed expired stage-run keys", "count", n)
		}
	}

	slog.Info("mcp.sweep: starting expired-credential sweeper", "interval", interval)
	sweep()
	if interval <= 0 {
		return
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			sweep()
		}
	}
}
