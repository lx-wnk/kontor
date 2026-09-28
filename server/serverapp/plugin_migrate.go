package serverapp

import (
	"context"
	"fmt"
	"time"

	"github.com/lx-wnk/kontor/server/internal/db/repo"
	"github.com/lx-wnk/kontor/server/internal/settings"
)

// seedPluginsFromEnabledList migrates the legacy #230 "plugins.enabled" setting
// into the plugin table, which is the new source of truth for enablement. For
// each id in the list it ensures a row exists and marks it active+installed.
// Idempotent: rows already active are skipped, installed_at is set only when
// absent, and Upsert preserves existing metadata — so re-runs and later
// discovery never clobber state.
func seedPluginsFromEnabledList(ctx context.Context, settingsSvc *settings.Service, pluginRepo repo.PluginRepo) error {
	if settingsSvc == nil || pluginRepo == nil {
		return nil
	}
	// One-shot: only seed on the very first boot, while the plugin table is still
	// empty (this runs before discovery populates it). On later boots the table is
	// non-empty, so a plugin the user has since disabled is never re-activated from
	// the frozen legacy list.
	existingRows, err := pluginRepo.List(ctx)
	if err != nil {
		return fmt.Errorf("seedPluginsFromEnabledList: list: %w", err)
	}
	if len(existingRows) > 0 {
		return nil
	}
	for _, id := range settingsSvc.StringSlice("plugins.enabled") {
		existing, err := pluginRepo.Get(ctx, id)
		if err != nil && !repo.IsNotFound(err) {
			return fmt.Errorf("seedPluginsFromEnabledList: get %q: %w", id, err)
		}
		if existing != nil && existing.Active {
			continue
		}
		if _, err := pluginRepo.Upsert(ctx, repo.UpsertPluginInput{ID: id}); err != nil {
			return fmt.Errorf("seedPluginsFromEnabledList: upsert %q: %w", id, err)
		}
		if existing == nil || existing.InstalledAt == nil {
			now := time.Now()
			if err := pluginRepo.SetInstalledAt(ctx, id, &now); err != nil {
				return fmt.Errorf("seedPluginsFromEnabledList: set installed_at %q: %w", id, err)
			}
		}
		if err := pluginRepo.SetActive(ctx, id, true); err != nil {
			return fmt.Errorf("seedPluginsFromEnabledList: set active %q: %w", id, err)
		}
	}
	return nil
}
