package repo

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/lx-wnk/kontor/server/internal/db/ent"
	"github.com/lx-wnk/kontor/server/internal/db/ent/apikey"
)

// API key kinds. A user key is one a person created and manages; a stage_run
// key is minted by the pipeline for one agent process and swept when it expires.
const (
	ApiKeyKindUser     = "user"
	ApiKeyKindStageRun = "stage_run"
	// ApiKeyKindModule is minted for one module process and revoked when the
	// module is deactivated. Its scopes are the module manifest's `uses` list.
	ApiKeyKindModule = "module"
)

// ModuleKeyName returns the key name that identifies a module's credential.
// The module id lives in the name rather than in a column of its own: the kind
// already separates these rows, and RevokeForModule queries both, so the
// lookup stays a single indexed-ish predicate rather than a migration.
func ModuleKeyName(moduleID string) string {
	return "module:" + moduleID
}

// CreateApiKeyInput is the named input for Create. Named rather than
// positional because the call now has more than four parameters, which is
// where this codebase's convention switches. A zero Kind means ApiKeyKindUser.
type CreateApiKeyInput struct {
	Name       string
	Hash       string
	Scopes     []string
	Kind       string
	StageRunID string
	ExpiresAt  *time.Time
}

// ApiKeyRepo manages API key persistence.
type ApiKeyRepo interface {
	Create(ctx context.Context, in CreateApiKeyInput) (*ent.ApiKey, error)
	GetByHash(ctx context.Context, hash string) (*ent.ApiKey, error)
	GetByID(ctx context.Context, id string) (*ent.ApiKey, error)
	List(ctx context.Context) ([]*ent.ApiKey, error)
	Delete(ctx context.Context, id string) error
	TouchLastUsed(ctx context.Context, id string) error
	Rotate(ctx context.Context, id, newHash string) (*ent.ApiKey, error)
	// RevokeForStageRun deactivates every key issued for stageRunID and
	// returns how many rows it touched.
	RevokeForStageRun(ctx context.Context, stageRunID string) (int, error)
	// RevokeForModule deactivates every credential issued to a module and
	// returns how many rows it touched.
	RevokeForModule(ctx context.Context, moduleID string) (int, error)
	// DeleteExpired hard-deletes stage_run keys whose expires_at is before
	// the given instant. User keys are never deleted here: they are soft-
	// deleted through Delete so their hash stays available for audit.
	DeleteExpired(ctx context.Context, before time.Time) (int, error)
}

type entApiKeyRepo struct {
	client *ent.Client
}

// NewApiKeyRepo returns an ApiKeyRepo backed by the given ent client.
func NewApiKeyRepo(client *ent.Client) ApiKeyRepo {
	return &entApiKeyRepo{client: client}
}

func (r *entApiKeyRepo) Create(ctx context.Context, in CreateApiKeyInput) (*ent.ApiKey, error) {
	scopes := in.Scopes
	if scopes == nil {
		scopes = []string{}
	}
	kind := in.Kind
	if kind == "" {
		kind = ApiKeyKindUser
	}
	q := r.client.ApiKey.Create().
		SetID(uuid.New().String()).
		SetName(in.Name).
		SetKeyHash(in.Hash).
		SetScopes(scopes).
		SetKind(kind).
		SetStageRunID(in.StageRunID).
		SetNillableExpiresAt(in.ExpiresAt)
	k, err := q.Save(ctx)
	if err != nil {
		return nil, fmt.Errorf("apikey.Create: %w", err)
	}
	return k, nil
}

func (r *entApiKeyRepo) GetByID(ctx context.Context, id string) (*ent.ApiKey, error) {
	k, err := r.client.ApiKey.Get(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("apikey.GetByID: %w", err)
	}
	return k, nil
}

// GetByHash resolves a usable key: active, and either without an expiry or
// not yet expired. The expiry rule lives here rather than at the call site so
// no future caller can forget it — "usable" is decided in one place.
func (r *entApiKeyRepo) GetByHash(ctx context.Context, hash string) (*ent.ApiKey, error) {
	k, err := r.client.ApiKey.Query().
		Where(
			apikey.KeyHash(hash),
			apikey.Active(true),
			apikey.Or(
				apikey.ExpiresAtIsNil(),
				apikey.ExpiresAtGT(time.Now()),
			),
		).
		Only(ctx)
	if err != nil {
		return nil, fmt.Errorf("apikey.GetByHash: %w", err)
	}
	return k, nil
}

// List returns the keys a person manages. Ephemeral stage_run keys are
// excluded: one row per stage run per retry would turn this list into a log.
func (r *entApiKeyRepo) List(ctx context.Context) ([]*ent.ApiKey, error) {
	keys, err := r.client.ApiKey.Query().
		Where(apikey.Active(true), apikey.KindEQ(ApiKeyKindUser)).
		Order(apikey.ByCreatedAt()).
		All(ctx)
	if err != nil {
		return nil, fmt.Errorf("apikey.List: %w", err)
	}
	return keys, nil
}

// RevokeForStageRun deactivates every key whose stage_run_id equals
// stageRunID. A user key carries stage_run_id = "" like every other user
// key, so an empty stageRunID would match all of them; guarded here rather
// than trusted to the caller, since the boundary is where that guarantee
// belongs.
func (r *entApiKeyRepo) RevokeForStageRun(ctx context.Context, stageRunID string) (int, error) {
	if stageRunID == "" {
		return 0, nil
	}
	n, err := r.client.ApiKey.Update().
		Where(apikey.StageRunIDEQ(stageRunID), apikey.Active(true)).
		SetActive(false).
		Save(ctx)
	if err != nil {
		return 0, fmt.Errorf("apikey.RevokeForStageRun: %w", err)
	}
	return n, nil
}

// RevokeForModule deactivates every credential issued to moduleID. It mirrors
// RevokeForStageRun, including its guard: an empty id must not match the keys
// that carry no module name.
func (r *entApiKeyRepo) RevokeForModule(ctx context.Context, moduleID string) (int, error) {
	if moduleID == "" {
		return 0, nil
	}
	n, err := r.client.ApiKey.Update().
		Where(
			apikey.KindEQ(ApiKeyKindModule),
			apikey.NameEQ(ModuleKeyName(moduleID)),
			apikey.Active(true),
		).
		SetActive(false).
		Save(ctx)
	if err != nil {
		return 0, fmt.Errorf("apikey.RevokeForModule: %w", err)
	}
	return n, nil
}

func (r *entApiKeyRepo) DeleteExpired(ctx context.Context, before time.Time) (int, error) {
	n, err := r.client.ApiKey.Delete().
		Where(
			apikey.KindEQ(ApiKeyKindStageRun),
			apikey.ExpiresAtNotNil(),
			apikey.ExpiresAtLT(before),
		).
		Exec(ctx)
	if err != nil {
		return 0, fmt.Errorf("apikey.DeleteExpired: %w", err)
	}
	return n, nil
}

func (r *entApiKeyRepo) Delete(ctx context.Context, id string) error {
	// Soft-delete: set active = false so hash remains in DB for audit.
	if err := r.client.ApiKey.UpdateOneID(id).SetActive(false).Exec(ctx); err != nil {
		return fmt.Errorf("apikey.Delete: %w", err)
	}
	return nil
}

func (r *entApiKeyRepo) TouchLastUsed(ctx context.Context, id string) error {
	if err := r.client.ApiKey.UpdateOneID(id).SetLastUsedAt(time.Now()).Exec(ctx); err != nil {
		return fmt.Errorf("apikey.TouchLastUsed: %w", err)
	}
	return nil
}

func (r *entApiKeyRepo) Rotate(ctx context.Context, id, newHash string) (*ent.ApiKey, error) {
	k, err := r.client.ApiKey.UpdateOneID(id).
		SetKeyHash(newHash).
		SetNillableLastUsedAt(nil).
		Save(ctx)
	if err != nil {
		return nil, fmt.Errorf("apikey.Rotate: %w", err)
	}
	return k, nil
}
