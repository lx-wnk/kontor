package repo

import (
	"context"
	"fmt"

	"github.com/google/uuid"

	"github.com/lx-wnk/kontor/server/internal/db/ent"
	"github.com/lx-wnk/kontor/server/internal/db/ent/pluginsetting"
)

type PluginSettingInput struct {
	PluginID, Key, Value, Nonce string
	Secret                      bool
}

type PluginSettingRepo interface {
	ListByPlugin(ctx context.Context, pluginID string) ([]*ent.PluginSetting, error)
	Upsert(ctx context.Context, in PluginSettingInput) (*ent.PluginSetting, error)
	DeleteByPlugin(ctx context.Context, pluginID string) error
}

type entPluginSettingRepo struct{ client *ent.Client }

func NewPluginSettingRepo(client *ent.Client) PluginSettingRepo {
	return &entPluginSettingRepo{client: client}
}

func (r *entPluginSettingRepo) ListByPlugin(ctx context.Context, pluginID string) ([]*ent.PluginSetting, error) {
	rows, err := r.client.PluginSetting.Query().
		Where(pluginsetting.PluginIDEQ(pluginID)).All(ctx)
	if err != nil {
		return nil, fmt.Errorf("pluginsetting.ListByPlugin: %w", err)
	}
	return rows, nil
}

func (r *entPluginSettingRepo) Upsert(ctx context.Context, in PluginSettingInput) (*ent.PluginSetting, error) {
	err := r.client.PluginSetting.Create().
		SetID(uuid.New().String()).
		SetPluginID(in.PluginID).SetKey(in.Key).SetValue(in.Value).
		SetSecret(in.Secret).SetNonce(in.Nonce).
		OnConflictColumns(pluginsetting.FieldPluginID, pluginsetting.FieldKey).
		// Refresh only the mutable columns; id/created_at stay untouched.
		UpdateValue().
		UpdateSecret().
		UpdateNonce().
		UpdateUpdatedAt().
		Exec(ctx)
	if err != nil {
		return nil, fmt.Errorf("pluginsetting.Upsert: %w", err)
	}
	row, err := r.client.PluginSetting.Query().
		Where(pluginsetting.PluginIDEQ(in.PluginID), pluginsetting.KeyEQ(in.Key)).Only(ctx)
	if err != nil {
		return nil, fmt.Errorf("pluginsetting.Upsert reload: %w", err)
	}
	return row, nil
}

func (r *entPluginSettingRepo) DeleteByPlugin(ctx context.Context, pluginID string) error {
	_, err := r.client.PluginSetting.Delete().Where(pluginsetting.PluginIDEQ(pluginID)).Exec(ctx)
	return err
}
