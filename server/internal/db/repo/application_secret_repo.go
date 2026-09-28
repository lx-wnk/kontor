package repo

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/lx-wnk/kontor/server/internal/db/ent"
	"github.com/lx-wnk/kontor/server/internal/db/ent/applicationsecret"
	"github.com/lx-wnk/kontor/server/internal/secretbox"
)

var ErrSecretsUnavailable = errors.New("application secrets: no encryption key is configured")

type SecretMeta struct {
	EnvName   string
	UpdatedAt time.Time
}

type ApplicationSecretRepo interface {
	Set(ctx context.Context, resourceID, envName, value string) error
	Delete(ctx context.Context, resourceID, envName string) error
	List(ctx context.Context, resourceID string) ([]SecretMeta, error)
	Values(ctx context.Context, resourceID string) (map[string]string, error)
}

type entApplicationSecretRepo struct {
	client *ent.Client
	box    *secretbox.Box
}

func NewApplicationSecretRepo(client *ent.Client, box *secretbox.Box) ApplicationSecretRepo {
	return &entApplicationSecretRepo{client: client, box: box}
}

func (r *entApplicationSecretRepo) Set(ctx context.Context, resourceID, envName, value string) error {
	if r.box == nil {
		return ErrSecretsUnavailable
	}
	ciphertext, nonce, err := r.box.Encrypt(value)
	if err != nil {
		return fmt.Errorf("applicationsecret.Set: encrypt: %w", err)
	}
	existing, err := r.client.ApplicationSecret.Query().
		Where(applicationsecret.ResourceID(resourceID), applicationsecret.EnvName(envName)).
		Only(ctx)
	switch {
	case err == nil:
		return existing.Update().SetCiphertext(ciphertext).SetNonce(nonce).Exec(ctx)
	case ent.IsNotFound(err):
		return r.client.ApplicationSecret.Create().
			SetID(uuid.New().String()).
			SetResourceID(resourceID).
			SetEnvName(envName).
			SetCiphertext(ciphertext).
			SetNonce(nonce).
			Exec(ctx)
	default:
		return fmt.Errorf("applicationsecret.Set: %w", err)
	}
}

func (r *entApplicationSecretRepo) Delete(ctx context.Context, resourceID, envName string) error {
	_, err := r.client.ApplicationSecret.Delete().
		Where(applicationsecret.ResourceID(resourceID), applicationsecret.EnvName(envName)).
		Exec(ctx)
	return err
}

func (r *entApplicationSecretRepo) List(ctx context.Context, resourceID string) ([]SecretMeta, error) {
	rows, err := r.client.ApplicationSecret.Query().
		Where(applicationsecret.ResourceID(resourceID)).
		Order(ent.Asc(applicationsecret.FieldEnvName)).
		All(ctx)
	if err != nil {
		return nil, fmt.Errorf("applicationsecret.List: %w", err)
	}
	out := make([]SecretMeta, 0, len(rows))
	for _, row := range rows {
		out = append(out, SecretMeta{EnvName: row.EnvName, UpdatedAt: row.UpdatedAt})
	}
	return out, nil
}

func (r *entApplicationSecretRepo) Values(ctx context.Context, resourceID string) (map[string]string, error) {
	rows, err := r.client.ApplicationSecret.Query().Where(applicationsecret.ResourceID(resourceID)).All(ctx)
	if err != nil {
		return nil, fmt.Errorf("applicationsecret.Values: %w", err)
	}
	out := make(map[string]string, len(rows))
	if len(rows) == 0 {
		return out, nil
	}
	if r.box == nil {
		return nil, ErrSecretsUnavailable
	}
	for _, row := range rows {
		plain, err := r.box.Decrypt(row.Ciphertext, row.Nonce)
		if err != nil {
			return nil, fmt.Errorf("applicationsecret.Values: decrypt %s: %w", row.EnvName, err)
		}
		out[row.EnvName] = plain
	}
	return out, nil
}
