package repo

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/lx-wnk/kontor/server/internal/db/ent"
	"github.com/lx-wnk/kontor/server/internal/db/ent/project"
	"github.com/lx-wnk/kontor/server/internal/db/ent/spawner"
	"github.com/lx-wnk/kontor/server/internal/db/ent/task"
)

// ErrSpawnerBuiltIn is returned when attempting to delete a built-in spawner.
var ErrSpawnerBuiltIn = errors.New("cannot delete a built-in spawner")

// ErrSpawnerInUse is returned when attempting to delete a spawner that is still
// referenced by at least one Task or Project.
var ErrSpawnerInUse = errors.New("spawner is still referenced by one or more tasks or projects")

// ErrSpawnerIsDefault is returned when attempting to delete the spawner that is
// currently marked as the deployment-wide default. Another spawner must be made
// default first so the resolution fallback never has zero targets.
var ErrSpawnerIsDefault = errors.New("cannot delete the default spawner; set another default first")

// SpawnerRepo manages spawner persistence.
type SpawnerRepo interface {
	Create(ctx context.Context, name, slug, command string, args []string, env map[string]string, modelOverride, description *string, adapterType string, adapterConfig map[string]string, builtIn bool) (*ent.Spawner, error)
	GetByID(ctx context.Context, id string) (*ent.Spawner, error)
	GetBySlug(ctx context.Context, slug string) (*ent.Spawner, error)
	// GetDefault returns the row with is_default=true. Returns an ent NotFound
	// error when no default is set (callers fall back to the slug backstop).
	GetDefault(ctx context.Context) (*ent.Spawner, error)
	List(ctx context.Context) ([]*ent.Spawner, error)
	Update(ctx context.Context, id string, name, slug, command *string, args []string, env map[string]string, modelOverride, description *string, adapterType *string, adapterConfig map[string]string, clearModelOverride, clearDescription bool) (*ent.Spawner, error)
	// SetDefault atomically makes id the sole default (clears any existing
	// default in the same transaction). Returns the new default row and the id
	// of the previously-default row ("" if none), so callers can broadcast both.
	SetDefault(ctx context.Context, id string) (*ent.Spawner, string, error)
	Delete(ctx context.Context, id string) error
}

type entSpawnerRepo struct {
	client *ent.Client
}

// NewSpawnerRepo returns a SpawnerRepo backed by the given ent client.
func NewSpawnerRepo(client *ent.Client) SpawnerRepo {
	return &entSpawnerRepo{client: client}
}

func (r *entSpawnerRepo) Create(ctx context.Context, name, slug, command string, args []string, env map[string]string, modelOverride, description *string, adapterType string, adapterConfig map[string]string, builtIn bool) (*ent.Spawner, error) {
	if args == nil {
		args = []string{}
	}
	if env == nil {
		env = map[string]string{}
	}
	if adapterType == "" {
		adapterType = "claude"
	}
	if adapterConfig == nil {
		adapterConfig = map[string]string{}
	}
	s, err := r.client.Spawner.Create().
		SetID(uuid.New().String()).
		SetName(name).
		SetSlug(slug).
		SetCommand(command).
		SetArgs(args).
		SetEnv(env).
		SetAdapterType(adapterType).
		SetAdapterConfig(adapterConfig).
		SetNillableModelOverride(modelOverride).
		SetNillableDescription(description).
		SetBuiltIn(builtIn).
		Save(ctx)
	if err != nil {
		return nil, fmt.Errorf("spawner.Create: %w", err)
	}
	return s, nil
}

func (r *entSpawnerRepo) GetByID(ctx context.Context, id string) (*ent.Spawner, error) {
	s, err := r.client.Spawner.Get(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("spawner.GetByID: %w", err)
	}
	return s, nil
}

func (r *entSpawnerRepo) GetBySlug(ctx context.Context, slug string) (*ent.Spawner, error) {
	s, err := r.client.Spawner.Query().
		Where(spawner.Slug(slug)).
		Only(ctx)
	if err != nil {
		return nil, fmt.Errorf("spawner.GetBySlug: %w", err)
	}
	return s, nil
}

func (r *entSpawnerRepo) GetDefault(ctx context.Context) (*ent.Spawner, error) {
	s, err := r.client.Spawner.Query().
		Where(spawner.IsDefault(true)).
		Only(ctx)
	if err != nil {
		return nil, fmt.Errorf("spawner.GetDefault: %w", err)
	}
	return s, nil
}

// SetDefault makes id the sole default in one transaction: every currently-default
// row is cleared, then id is set. The clear-all-then-set-one ordering preserves
// the exactly-one invariant even if a prior bug left multiple defaults behind.
func (r *entSpawnerRepo) SetDefault(ctx context.Context, id string) (*ent.Spawner, string, error) {
	tx, err := r.client.Tx(ctx)
	if err != nil {
		return nil, "", fmt.Errorf("spawner.SetDefault: begin tx: %w", err)
	}
	// Identify the existing default (at most one expected) before clearing, so
	// the caller can refresh its row in the UI.
	prevID := ""
	if prev, err := tx.Spawner.Query().Where(spawner.IsDefault(true)).First(ctx); err == nil {
		prevID = prev.ID
	} else if !ent.IsNotFound(err) {
		return nil, "", rollback(tx, fmt.Errorf("spawner.SetDefault: query current default: %w", err))
	}

	if _, err := tx.Spawner.Update().
		Where(spawner.IsDefault(true)).
		SetIsDefault(false).
		Save(ctx); err != nil {
		return nil, "", rollback(tx, fmt.Errorf("spawner.SetDefault: clear existing default: %w", err))
	}

	s, err := tx.Spawner.UpdateOneID(id).
		SetIsDefault(true).
		SetUpdatedAt(time.Now()).
		Save(ctx)
	if err != nil {
		return nil, "", rollback(tx, fmt.Errorf("spawner.SetDefault: set %s: %w", id, err))
	}

	if err := tx.Commit(); err != nil {
		return nil, "", fmt.Errorf("spawner.SetDefault: commit: %w", err)
	}
	if prevID == id {
		prevID = ""
	}
	return s, prevID, nil
}

func (r *entSpawnerRepo) List(ctx context.Context) ([]*ent.Spawner, error) {
	spawners, err := r.client.Spawner.Query().
		Order(spawner.ByCreatedAt()).
		All(ctx)
	if err != nil {
		return nil, fmt.Errorf("spawner.List: %w", err)
	}
	return spawners, nil
}

func (r *entSpawnerRepo) Update(ctx context.Context, id string, name, slug, command *string, args []string, env map[string]string, modelOverride, description *string, adapterType *string, adapterConfig map[string]string, clearModelOverride, clearDescription bool) (*ent.Spawner, error) {
	q := r.client.Spawner.UpdateOneID(id).SetUpdatedAt(time.Now())
	if name != nil {
		q = q.SetName(*name)
	}
	if slug != nil {
		q = q.SetSlug(*slug)
	}
	if command != nil {
		q = q.SetCommand(*command)
	}
	if args != nil {
		q = q.SetArgs(args)
	}
	if env != nil {
		q = q.SetEnv(env)
	}
	if adapterType != nil {
		q = q.SetAdapterType(*adapterType)
	}
	if adapterConfig != nil {
		q = q.SetAdapterConfig(adapterConfig)
	}
	if clearModelOverride {
		q = q.ClearModelOverride()
	} else if modelOverride != nil {
		q = q.SetModelOverride(*modelOverride)
	}
	if clearDescription {
		q = q.ClearDescription()
	} else if description != nil {
		q = q.SetDescription(*description)
	}
	s, err := q.Save(ctx)
	if err != nil {
		return nil, fmt.Errorf("spawner.Update: %w", err)
	}
	return s, nil
}

// Delete removes a spawner. Returns ErrSpawnerBuiltIn if the spawner is built-in,
// or ErrSpawnerInUse if any Task or Project still references it.
func (r *entSpawnerRepo) Delete(ctx context.Context, id string) error {
	s, err := r.client.Spawner.Get(ctx, id)
	if err != nil {
		return fmt.Errorf("spawner.Delete: %w", err)
	}
	if s.BuiltIn {
		return ErrSpawnerBuiltIn
	}
	if s.IsDefault {
		return ErrSpawnerIsDefault
	}

	taskCount, err := r.client.Task.Query().
		Where(task.SpawnerID(id)).
		Count(ctx)
	if err != nil {
		return fmt.Errorf("spawner.Delete: count task references: %w", err)
	}
	if taskCount > 0 {
		return ErrSpawnerInUse
	}

	projectCount, err := r.client.Project.Query().
		Where(project.DefaultSpawnerID(id)).
		Count(ctx)
	if err != nil {
		return fmt.Errorf("spawner.Delete: count project references: %w", err)
	}
	if projectCount > 0 {
		return ErrSpawnerInUse
	}

	if err := r.client.Spawner.DeleteOneID(id).Exec(ctx); err != nil {
		return fmt.Errorf("spawner.Delete: %w", err)
	}
	return nil
}
