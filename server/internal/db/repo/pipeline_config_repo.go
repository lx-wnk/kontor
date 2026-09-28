package repo

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"

	"github.com/google/uuid"
	"github.com/lx-wnk/kontor/server/internal/db/ent"
	"github.com/lx-wnk/kontor/server/internal/db/ent/pipelineconfig"
)

type PipelineConfigRepo interface {
	// Global (project_id "") getters — kept for existing callers.
	GetNumber(ctx context.Context, key string, fallback float64) float64
	GetString(ctx context.Context, key, fallback string) string

	// GetStringScoped returns the project-scoped row when projectID is non-nil and a
	// row exists, else falls back to the global ("" project_id) row, else fallback.
	// Pass nil projectID for a global-only lookup (same as GetString).
	GetStringScoped(ctx context.Context, projectID *string, key, fallback string) string

	// Set writes a global (project_id "") row for key.
	Set(ctx context.Context, key, value string) error

	// SetScoped upserts a row keyed on (projectID, key). nil projectID → global scope.
	SetScoped(ctx context.Context, projectID *string, key, value string) error

	// Delete removes the global row for key. Missing key is a no-op.
	Delete(ctx context.Context, key string) error

	// DeleteScoped removes the (projectID, key) row. Missing row is a no-op.
	DeleteScoped(ctx context.Context, projectID *string, key string) error

	// GetStringForScope returns the row for EXACTLY the given scope without
	// cross-scope fallback. nil projectID → global ("") scope. Returns fallback
	// when no row exists for that exact scope.
	GetStringForScope(ctx context.Context, projectID *string, key, fallback string) string

	// GetAll returns global (project_id "") rows only.
	GetAll(ctx context.Context) (map[string]string, error)

	// GetAllScoped returns globals merged with project rows; project rows win on conflict.
	// Pass nil for global-only (equivalent to GetAll).
	GetAllScoped(ctx context.Context, projectID *string) (map[string]string, error)
}

type entPipelineConfigRepo struct{ client *ent.Client }

func NewPipelineConfigRepo(client *ent.Client) PipelineConfigRepo {
	return &entPipelineConfigRepo{client: client}
}

// scopeID maps a *string projectID to the storage sentinel: "" for global scope,
// the dereferenced value for project scope.
func scopeID(projectID *string) string {
	if projectID == nil || *projectID == "" {
		return ""
	}
	return *projectID
}

// --- thin global wrappers for existing callers ---

func (r *entPipelineConfigRepo) GetNumber(ctx context.Context, key string, fallback float64) float64 {
	s := r.GetStringScoped(ctx, nil, key, "")
	if s == "" {
		return fallback
	}
	n, err := strconv.ParseFloat(s, 64)
	if err != nil {
		slog.Error("pipeline_config: parse float", "key", key, "err", err)
		return fallback
	}
	return n
}

func (r *entPipelineConfigRepo) GetString(ctx context.Context, key, fallback string) string {
	return r.GetStringScoped(ctx, nil, key, fallback)
}

func (r *entPipelineConfigRepo) Set(ctx context.Context, key, value string) error {
	return r.SetScoped(ctx, nil, key, value)
}

func (r *entPipelineConfigRepo) Delete(ctx context.Context, key string) error {
	return r.DeleteScoped(ctx, nil, key)
}

func (r *entPipelineConfigRepo) GetAll(ctx context.Context) (map[string]string, error) {
	return r.GetAllScoped(ctx, nil)
}

// --- scoped implementations ---

func (r *entPipelineConfigRepo) GetStringScoped(ctx context.Context, projectID *string, key, fallback string) string {
	scope := scopeID(projectID)

	if scope != "" {
		cfg, err := r.client.PipelineConfig.Query().
			Where(pipelineconfig.ProjectID(scope), pipelineconfig.Key(key)).
			Only(ctx)
		if err == nil {
			return cfg.Value
		}
		if !ent.IsNotFound(err) {
			slog.Error("pipeline_config: scoped lookup", "key", key, "project_id", scope, "err", err)
			return fallback
		}
		// not found → fall through to global
	}

	cfg, err := r.client.PipelineConfig.Query().
		Where(pipelineconfig.ProjectID(""), pipelineconfig.Key(key)).
		Only(ctx)
	if err != nil {
		if !ent.IsNotFound(err) {
			slog.Error("pipeline_config: global lookup", "key", key, "err", err)
		}
		return fallback
	}
	return cfg.Value
}

func (r *entPipelineConfigRepo) SetScoped(ctx context.Context, projectID *string, key, value string) error {
	scope := scopeID(projectID)
	err := r.client.PipelineConfig.Create().
		SetID(uuid.New().String()).
		SetKey(key).
		SetProjectID(scope).
		SetValue(value).
		OnConflictColumns(pipelineconfig.FieldProjectID, pipelineconfig.FieldKey).
		UpdateNewValues().
		Exec(ctx)
	if err != nil {
		return fmt.Errorf("pipelineconfig.Set: %w", err)
	}
	return nil
}

func (r *entPipelineConfigRepo) DeleteScoped(ctx context.Context, projectID *string, key string) error {
	scope := scopeID(projectID)
	_, err := r.client.PipelineConfig.Delete().
		Where(pipelineconfig.ProjectID(scope), pipelineconfig.Key(key)).
		Exec(ctx)
	if err != nil {
		return fmt.Errorf("pipelineconfig.Delete: %w", err)
	}
	return nil
}

func (r *entPipelineConfigRepo) GetStringForScope(ctx context.Context, projectID *string, key, fallback string) string {
	scope := scopeID(projectID)
	cfg, err := r.client.PipelineConfig.Query().
		Where(pipelineconfig.ProjectID(scope), pipelineconfig.Key(key)).
		Only(ctx)
	if err != nil {
		if !ent.IsNotFound(err) {
			slog.Error("pipeline_config: GetStringForScope", "key", key, "scope", scope, "err", err)
		}
		return fallback
	}
	return cfg.Value
}

func (r *entPipelineConfigRepo) GetAllScoped(ctx context.Context, projectID *string) (map[string]string, error) {
	globals, err := r.client.PipelineConfig.Query().Where(pipelineconfig.ProjectID("")).All(ctx)
	if err != nil {
		return nil, fmt.Errorf("pipelineconfig.GetAll: %w", err)
	}
	result := make(map[string]string, len(globals))
	for _, cfg := range globals {
		result[cfg.Key] = cfg.Value
	}

	scope := scopeID(projectID)
	if scope == "" {
		return result, nil
	}

	// Project rows override globals for the same key.
	scoped, err := r.client.PipelineConfig.Query().Where(pipelineconfig.ProjectID(scope)).All(ctx)
	if err != nil {
		return nil, fmt.Errorf("pipelineconfig.GetAll(scoped): %w", err)
	}
	for _, cfg := range scoped {
		result[cfg.Key] = cfg.Value
	}
	return result, nil
}
