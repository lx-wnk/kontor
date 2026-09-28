package repo

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/lx-wnk/agent-dashboard/server/internal/db/ent"
)

// ReconcilePluginResources gives every plugin row a registry identity. A plugin
// primary key is its manifest id — a human-authored value — so the registry row
// records it in origin_ref and the generated UUID becomes the stable identity
// that survives a manifest rename.
//
// Runs over every plugin on each boot, not only unlinked ones: the registry row
// carries the manifest's name and version, and skipping linked rows froze both
// at whatever the first boot saw — a manifest that gained a name kept an empty
// one forever. Upsert refreshes exactly that metadata and leaves state and
// origin alone, so re-projecting a settled tree changes nothing and the return
// value still counts only newly linked plugins.
//
// A plugin whose id fails the registry's slug validation (e.g. predates the
// 64-character cap) is logged and skipped rather than aborting the whole
// reconcile — one bad row must not stop every plugin ordered after it from
// ever receiving a registry identity.
func ReconcilePluginResources(ctx context.Context, resources ResourceRepo, client *ent.Client) (int, error) {
	rows, err := client.Plugin.Query().All(ctx)
	if err != nil {
		return 0, fmt.Errorf("reconcile plugins: query: %w", err)
	}

	linked, skipped := 0, 0
	for _, p := range rows {
		state := ResourceStateDiscovered
		switch {
		case p.Active:
			state = ResourceStateEnabled
		case p.InstalledAt != nil:
			state = ResourceStateDisabled
		}

		res, err := resources.Upsert(ctx, UpsertResourceInput{
			Kind:      ResourceKindApplication,
			Slug:      p.ID,
			Name:      p.Name,
			Scope:     GlobalScope(),
			State:     state,
			Version:   p.Version,
			Origin:    ResourceOriginLocal,
			OriginRef: p.ID,
		})
		if err != nil {
			skipped++
			slog.Warn("reconcile plugin: skipped", "plugin_id", p.ID, "err", err)
			continue
		}
		if p.ResourceID == res.ID {
			continue
		}
		if err := client.Plugin.UpdateOneID(p.ID).SetResourceID(res.ID).Exec(ctx); err != nil {
			skipped++
			slog.Warn("reconcile plugin: skipped", "plugin_id", p.ID, "err", err)
			continue
		}
		linked++
	}
	if skipped > 0 {
		slog.Warn("reconcile plugins: some plugins were not linked", "linked", linked, "skipped", skipped)
	}
	return linked, nil
}
