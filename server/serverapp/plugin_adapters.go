package serverapp

import (
	"context"
	"time"

	"github.com/lx-wnk/kontor/server/internal/db/repo"
	"github.com/lx-wnk/kontor/server/internal/plugin"
)

// pluginSettingRepoAdapter maps the ent-backed PluginSettingRepo onto the
// plugin.Repo interface (Stored ↔ PluginSettingInput/ent.PluginSetting).
type pluginSettingRepoAdapter struct{ inner repo.PluginSettingRepo }

func (a pluginSettingRepoAdapter) ListByPlugin(ctx context.Context, pluginID string) ([]plugin.Stored, error) {
	rows, err := a.inner.ListByPlugin(ctx, pluginID)
	if err != nil {
		return nil, err
	}
	out := make([]plugin.Stored, len(rows))
	for i, r := range rows {
		out[i] = plugin.Stored{Key: r.Key, Value: r.Value, Nonce: r.Nonce, Secret: r.Secret}
	}
	return out, nil
}

func (a pluginSettingRepoAdapter) Upsert(ctx context.Context, pluginID string, s plugin.Stored) error {
	_, err := a.inner.Upsert(ctx, repo.PluginSettingInput{
		PluginID: pluginID, Key: s.Key, Value: s.Value, Nonce: s.Nonce, Secret: s.Secret,
	})
	return err
}

func (a pluginSettingRepoAdapter) DeleteByPlugin(ctx context.Context, pluginID string) error {
	return a.inner.DeleteByPlugin(ctx, pluginID)
}

// pluginStateRepoAdapter maps the ent-backed PluginRepo onto the
// plugin.StateRepo interface (ent.Plugin → plugin.State).
type pluginStateRepoAdapter struct{ inner repo.PluginRepo }

func (a pluginStateRepoAdapter) GetState(ctx context.Context, id string) (plugin.State, error) {
	p, err := a.inner.Get(ctx, id)
	if err != nil {
		return plugin.State{}, err
	}
	return plugin.State{InstalledAt: p.InstalledAt, Active: p.Active, Version: p.Version}, nil
}

func (a pluginStateRepoAdapter) SetInstalledAt(ctx context.Context, id string, at *time.Time) error {
	return a.inner.SetInstalledAt(ctx, id, at)
}

func (a pluginStateRepoAdapter) SetActive(ctx context.Context, id string, active bool) error {
	return a.inner.SetActive(ctx, id, active)
}

func (a pluginStateRepoAdapter) SetVersion(ctx context.Context, id, version string) error {
	return a.inner.SetVersion(ctx, id, version)
}

func (a pluginStateRepoAdapter) SetManifestHash(ctx context.Context, id, hash string) error {
	return a.inner.SetManifestHash(ctx, id, hash)
}

// pluginDiscoverRepoAdapter maps the ent-backed PluginRepo onto the
// plugin.DiscoverRepo interface. It reads the prior manifest hash to
// report update-available, then upserts (the upsert preserves installed_at/active).
type pluginDiscoverRepoAdapter struct {
	inner    repo.PluginRepo
	settings repo.PluginSettingRepo
}

func (a pluginDiscoverRepoAdapter) UpsertDiscovered(ctx context.Context, in plugin.DiscoveredPlugin) (bool, error) {
	var oldHash string
	if existing, err := a.inner.Get(ctx, in.ID); err == nil {
		oldHash = existing.ManifestHash
	} else if !repo.IsNotFound(err) {
		return false, err
	}
	if _, err := a.inner.Upsert(ctx, repo.UpsertPluginInput{
		ID: in.ID, Name: in.Name, Version: in.Version, Path: in.Path, ManifestHash: in.ManifestHash,
	}); err != nil {
		return false, err
	}
	return oldHash != in.ManifestHash, nil
}

func (a pluginDiscoverRepoAdapter) ExistingIDs(ctx context.Context) ([]string, error) {
	rows, err := a.inner.List(ctx)
	if err != nil {
		return nil, err
	}
	ids := make([]string, len(rows))
	for i, p := range rows {
		ids[i] = p.ID
	}
	return ids, nil
}

func (a pluginDiscoverRepoAdapter) IsInstalled(ctx context.Context, id string) (bool, error) {
	p, err := a.inner.Get(ctx, id)
	if err != nil {
		if repo.IsNotFound(err) {
			return false, nil
		}
		return false, err
	}
	return p.InstalledAt != nil, nil
}

func (a pluginDiscoverRepoAdapter) Remove(ctx context.Context, id string) error {
	if err := a.settings.DeleteByPlugin(ctx, id); err != nil {
		return err
	}
	return a.inner.Delete(ctx, id)
}

// pluginProcessAdapter lets the lifecycle engine drive the plugin registry's
// process lifecycle without the plugin package importing plugin.
type pluginProcessAdapter struct{ reg *plugin.Registry }

func (a pluginProcessAdapter) Start(ctx context.Context, id string) error {
	return a.reg.StartOne(ctx, id)
}
func (a pluginProcessAdapter) Stop(_ context.Context, id string) error { return a.reg.StopOne(id) }
func (a pluginProcessAdapter) WithTransient(ctx context.Context, id string, fn func() error) error {
	return a.reg.WithTransient(ctx, id, fn)
}
