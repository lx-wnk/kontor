package repo

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/lx-wnk/kontor/server/internal/db/ent"
	"github.com/lx-wnk/kontor/server/internal/db/ent/providersetting"
)

// ProviderSettingRepo persists per-provider enable-state.
type ProviderSettingRepo interface {
	List(ctx context.Context) ([]*ent.ProviderSetting, error)
	Upsert(ctx context.Context, providerID string, enabled bool) (*ent.ProviderSetting, error)
}

type entProviderSettingRepo struct {
	client *ent.Client
}

// NewProviderSettingRepo returns a ProviderSettingRepo backed by the ent client.
func NewProviderSettingRepo(client *ent.Client) ProviderSettingRepo {
	return &entProviderSettingRepo{client: client}
}

func (r *entProviderSettingRepo) List(ctx context.Context) ([]*ent.ProviderSetting, error) {
	rows, err := r.client.ProviderSetting.Query().All(ctx)
	if err != nil {
		return nil, fmt.Errorf("providersetting.List: %w", err)
	}
	return rows, nil
}

func (r *entProviderSettingRepo) Upsert(ctx context.Context, providerID string, enabled bool) (*ent.ProviderSetting, error) {
	err := r.client.ProviderSetting.Create().
		SetID(uuid.New().String()).
		SetProviderID(providerID).
		SetEnabled(enabled).
		OnConflictColumns(providersetting.FieldProviderID).
		UpdateNewValues().
		Exec(ctx)
	if err != nil {
		return nil, fmt.Errorf("providersetting.Upsert: %w", err)
	}
	row, err := r.client.ProviderSetting.Query().
		Where(providersetting.ProviderIDEQ(providerID)).
		Only(ctx)
	if err != nil {
		return nil, fmt.Errorf("providersetting.Upsert reload: %w", err)
	}
	return row, nil
}
