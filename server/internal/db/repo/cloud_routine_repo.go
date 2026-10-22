package repo

import (
	"context"
	"fmt"

	"github.com/google/uuid"

	"github.com/lx-wnk/kontor/server/internal/db/ent"
	"github.com/lx-wnk/kontor/server/internal/db/ent/cloudroutine"
	"github.com/lx-wnk/kontor/server/internal/secretbox"
)

// CreateCloudRoutineInput carries the fields of a new routine; Token is the
// plaintext bearer token and is stored encrypted.
type CreateCloudRoutineInput struct {
	Name      string
	FireURL   string
	SpawnerID string
	Token     string
}

// UpdateCloudRoutineInput carries the optional changes to a routine. A nil or
// empty Token keeps the stored one.
type UpdateCloudRoutineInput struct {
	Name    *string
	FireURL *string
	Token   *string
}

// CloudRoutineRepo manages claude.ai routines and their encrypted tokens.
type CloudRoutineRepo interface {
	Create(ctx context.Context, in CreateCloudRoutineInput) (*ent.CloudRoutine, error)
	// List returns the routines ordered by name.
	List(ctx context.Context) ([]*ent.CloudRoutine, error)
	Get(ctx context.Context, id string) (*ent.CloudRoutine, error)
	Update(ctx context.Context, id string, in UpdateCloudRoutineInput) (*ent.CloudRoutine, error)
	Delete(ctx context.Context, id string) error
	// Token decrypts the routine's bearer token; it is the only way to read it.
	Token(ctx context.Context, id string) (string, error)
}

// CloudRoutineHasToken reports whether a token is stored, without decrypting it.
func CloudRoutineHasToken(r *ent.CloudRoutine) bool {
	return r.TokenCiphertext != ""
}

type entCloudRoutineRepo struct {
	client *ent.Client
	box    *secretbox.Box
}

// NewCloudRoutineRepo returns a CloudRoutineRepo; a nil box makes every
// operation that touches the token fail with ErrSecretsUnavailable.
func NewCloudRoutineRepo(client *ent.Client, box *secretbox.Box) CloudRoutineRepo {
	return &entCloudRoutineRepo{client: client, box: box}
}

func (r *entCloudRoutineRepo) Create(ctx context.Context, in CreateCloudRoutineInput) (*ent.CloudRoutine, error) {
	if r.box == nil {
		return nil, ErrSecretsUnavailable
	}
	ciphertext, nonce, err := r.box.Encrypt(in.Token)
	if err != nil {
		return nil, fmt.Errorf("cloudRoutine.Create: encrypt: %w", err)
	}
	routine, err := r.client.CloudRoutine.Create().
		SetID(uuid.New().String()).
		SetName(in.Name).
		SetFireURL(in.FireURL).
		SetSpawnerID(in.SpawnerID).
		SetTokenCiphertext(ciphertext).
		SetTokenNonce(nonce).
		Save(ctx)
	if err != nil {
		return nil, fmt.Errorf("cloudRoutine.Create: %w", err)
	}
	return routine, nil
}

func (r *entCloudRoutineRepo) List(ctx context.Context) ([]*ent.CloudRoutine, error) {
	routines, err := r.client.CloudRoutine.Query().Order(ent.Asc(cloudroutine.FieldName)).All(ctx)
	if err != nil {
		return nil, fmt.Errorf("cloudRoutine.List: %w", err)
	}
	return routines, nil
}

func (r *entCloudRoutineRepo) Get(ctx context.Context, id string) (*ent.CloudRoutine, error) {
	routine, err := r.client.CloudRoutine.Get(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("cloudRoutine.Get: %w", err)
	}
	return routine, nil
}

func (r *entCloudRoutineRepo) Update(ctx context.Context, id string, in UpdateCloudRoutineInput) (*ent.CloudRoutine, error) {
	u := r.client.CloudRoutine.UpdateOneID(id)
	if in.Name != nil {
		u.SetName(*in.Name)
	}
	if in.FireURL != nil {
		u.SetFireURL(*in.FireURL)
	}
	if in.Token != nil && *in.Token != "" {
		if r.box == nil {
			return nil, ErrSecretsUnavailable
		}
		ciphertext, nonce, err := r.box.Encrypt(*in.Token)
		if err != nil {
			return nil, fmt.Errorf("cloudRoutine.Update: encrypt: %w", err)
		}
		u.SetTokenCiphertext(ciphertext).SetTokenNonce(nonce)
	}
	routine, err := u.Save(ctx)
	if err != nil {
		return nil, fmt.Errorf("cloudRoutine.Update: %w", err)
	}
	return routine, nil
}

func (r *entCloudRoutineRepo) Delete(ctx context.Context, id string) error {
	if err := r.client.CloudRoutine.DeleteOneID(id).Exec(ctx); err != nil {
		return fmt.Errorf("cloudRoutine.Delete: %w", err)
	}
	return nil
}

func (r *entCloudRoutineRepo) Token(ctx context.Context, id string) (string, error) {
	if r.box == nil {
		return "", ErrSecretsUnavailable
	}
	routine, err := r.client.CloudRoutine.Get(ctx, id)
	if err != nil {
		return "", fmt.Errorf("cloudRoutine.Token: %w", err)
	}
	plain, err := r.box.Decrypt(routine.TokenCiphertext, routine.TokenNonce)
	if err != nil {
		return "", fmt.Errorf("cloudRoutine.Token: decrypt: %w", err)
	}
	return plain, nil
}
