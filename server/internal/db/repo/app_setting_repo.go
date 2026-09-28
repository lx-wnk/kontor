package repo

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/lx-wnk/kontor/server/internal/db/ent"
	"github.com/lx-wnk/kontor/server/internal/db/ent/appsetting"
)

// AppSettingRepo persists generic key/value configuration.
type AppSettingRepo interface {
	Get(ctx context.Context, key string) (string, bool, error)
	List(ctx context.Context) ([]*ent.AppSetting, error)
	Upsert(ctx context.Context, key, value string) (*ent.AppSetting, error)
	// UpsertSecret stores an encrypted value. ciphertext and nonce are both
	// base64, exactly as internal/secretbox.Box.Encrypt returns them.
	UpsertSecret(ctx context.Context, key, ciphertext, nonce string) (*ent.AppSetting, error)
	// GetSecret returns the stored ciphertext and nonce. A row written by
	// Upsert rather than UpsertSecret comes back with an empty nonce, which
	// is how a caller distinguishes the two without consulting the settings
	// registry.
	GetSecret(ctx context.Context, key string) (ciphertext, nonce string, found bool, err error)
}

type entAppSettingRepo struct{ client *ent.Client }

// NewAppSettingRepo returns an AppSettingRepo backed by the ent client.
func NewAppSettingRepo(client *ent.Client) AppSettingRepo {
	return &entAppSettingRepo{client: client}
}

func (r *entAppSettingRepo) Get(ctx context.Context, key string) (string, bool, error) {
	row, err := r.client.AppSetting.Query().Where(appsetting.KeyEQ(key)).Only(ctx)
	if ent.IsNotFound(err) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("appsetting.Get: %w", err)
	}
	return row.Value, true, nil
}

func (r *entAppSettingRepo) List(ctx context.Context) ([]*ent.AppSetting, error) {
	rows, err := r.client.AppSetting.Query().All(ctx)
	if err != nil {
		return nil, fmt.Errorf("appsetting.List: %w", err)
	}
	return rows, nil
}

func (r *entAppSettingRepo) Upsert(ctx context.Context, key, value string) (*ent.AppSetting, error) {
	err := r.client.AppSetting.Create().
		SetID(uuid.New().String()).
		SetKey(key).
		SetValue(value).
		OnConflictColumns(appsetting.FieldKey).
		UpdateNewValues().
		Exec(ctx)
	if err != nil {
		return nil, fmt.Errorf("appsetting.Upsert: %w", err)
	}
	return r.byKey(ctx, key)
}

// UpsertSecret stores an encrypted value. ciphertext and nonce are both
// base64, exactly as internal/secretbox.Box.Encrypt returns them.
func (r *entAppSettingRepo) UpsertSecret(ctx context.Context, key, ciphertext, nonce string) (*ent.AppSetting, error) {
	err := r.client.AppSetting.Create().
		SetID(uuid.New().String()).
		SetKey(key).
		SetValue(ciphertext).
		SetSecret(true).
		SetNonce(nonce).
		OnConflictColumns(appsetting.FieldKey).
		UpdateNewValues().
		Exec(ctx)
	if err != nil {
		return nil, fmt.Errorf("appsetting.UpsertSecret: %w", err)
	}
	return r.byKey(ctx, key)
}

// GetSecret returns the stored ciphertext and nonce. A row written by Upsert
// rather than UpsertSecret comes back with an empty nonce, which is how a
// caller distinguishes the two without consulting the settings registry.
func (r *entAppSettingRepo) GetSecret(ctx context.Context, key string) (string, string, bool, error) {
	row, err := r.client.AppSetting.Query().Where(appsetting.KeyEQ(key)).Only(ctx)
	if ent.IsNotFound(err) {
		return "", "", false, nil
	}
	if err != nil {
		return "", "", false, fmt.Errorf("appsetting.GetSecret: %w", err)
	}
	return row.Value, row.Nonce, true, nil
}

// byKey reloads a row by key after a create/upsert. It shares the query
// Upsert and UpsertSecret both need to return the persisted row instead of
// whatever the OnConflict clause last saw.
func (r *entAppSettingRepo) byKey(ctx context.Context, key string) (*ent.AppSetting, error) {
	row, err := r.client.AppSetting.Query().Where(appsetting.KeyEQ(key)).Only(ctx)
	if err != nil {
		return nil, fmt.Errorf("appsetting reload %q: %w", key, err)
	}
	return row, nil
}
