package repo

import (
	"context"
	"fmt"
	"time"

	"github.com/lx-wnk/kontor/server/internal/db/ent"
	"github.com/lx-wnk/kontor/server/internal/db/ent/plugin"
)

type UpsertPluginInput struct {
	ID, Name, Version, Path, ManifestHash string
}

type PluginRepo interface {
	Get(ctx context.Context, id string) (*ent.Plugin, error)
	List(ctx context.Context) ([]*ent.Plugin, error)
	Upsert(ctx context.Context, in UpsertPluginInput) (*ent.Plugin, error)
	SetInstalledAt(ctx context.Context, id string, at *time.Time) error
	SetActive(ctx context.Context, id string, active bool) error
	SetVersion(ctx context.Context, id, version string) error
	SetManifestHash(ctx context.Context, id, hash string) error
	Delete(ctx context.Context, id string) error
}

type entPluginRepo struct{ client *ent.Client }

func NewPluginRepo(client *ent.Client) PluginRepo { return &entPluginRepo{client: client} }

func (r *entPluginRepo) Get(ctx context.Context, id string) (*ent.Plugin, error) {
	p, err := r.client.Plugin.Get(ctx, id)
	if err != nil {
		return nil, err // includes ent.NotFoundError; callers use IsNotFound
	}
	return p, nil
}

func (r *entPluginRepo) List(ctx context.Context) ([]*ent.Plugin, error) {
	rows, err := r.client.Plugin.Query().All(ctx)
	if err != nil {
		return nil, fmt.Errorf("plugin.List: %w", err)
	}
	return rows, nil
}

func (r *entPluginRepo) Upsert(ctx context.Context, in UpsertPluginInput) (*ent.Plugin, error) {
	err := r.client.Plugin.Create().
		SetID(in.ID).SetName(in.Name).SetVersion(in.Version).
		SetPath(in.Path).SetManifestHash(in.ManifestHash).
		OnConflictColumns(plugin.FieldID).
		// On re-discovery, refresh metadata but DO NOT reset installed_at/active.
		UpdateName().UpdateVersion().UpdatePath().UpdateManifestHash().
		Exec(ctx)
	if err != nil {
		return nil, fmt.Errorf("plugin.Upsert: %w", err)
	}
	return r.Get(ctx, in.ID)
}

func (r *entPluginRepo) SetInstalledAt(ctx context.Context, id string, at *time.Time) error {
	upd := r.client.Plugin.UpdateOneID(id)
	if at == nil {
		upd = upd.ClearInstalledAt()
	} else {
		upd = upd.SetInstalledAt(*at)
	}
	return upd.Exec(ctx)
}

func (r *entPluginRepo) SetActive(ctx context.Context, id string, active bool) error {
	return r.client.Plugin.UpdateOneID(id).SetActive(active).Exec(ctx)
}

func (r *entPluginRepo) SetVersion(ctx context.Context, id, version string) error {
	return r.client.Plugin.UpdateOneID(id).SetVersion(version).Exec(ctx)
}

func (r *entPluginRepo) SetManifestHash(ctx context.Context, id, hash string) error {
	return r.client.Plugin.UpdateOneID(id).SetManifestHash(hash).Exec(ctx)
}

func (r *entPluginRepo) Delete(ctx context.Context, id string) error {
	return r.client.Plugin.DeleteOneID(id).Exec(ctx)
}
