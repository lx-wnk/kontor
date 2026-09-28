package repo

import (
	"context"
	"fmt"
	"sort"

	"github.com/google/uuid"
	"github.com/lx-wnk/kontor/server/internal/db/ent"
	"github.com/lx-wnk/kontor/server/internal/db/ent/permissionpreset"
)

// PermissionPresetRepo is the data-access interface for permission presets.
type PermissionPresetRepo interface {
	// Upsert inserts a preset entry idempotently (INSERT OR IGNORE semantics).
	Upsert(ctx context.Context, input UpsertPresetInput) error
	// UpsertBatch inserts multiple preset entries idempotently in a single pass.
	UpsertBatch(ctx context.Context, inputs []UpsertPresetInput) error
	// ListSummaries returns all presets grouped by projectCwd for the given userID.
	// userID == nil matches rows where user_id IS NULL.
	ListSummaries(ctx context.Context, userID *string) ([]PresetProjectSummary, error)
	// ListForCwd returns all presets that apply to projectCwd for the given userID.
	// Follows the same scoping as ListSummaries: nil userID returns only global
	// (user_id IS NULL) presets; non-nil userID returns global + user-scoped rows.
	ListForCwd(ctx context.Context, userID *string, projectCwd string) ([]*ent.PermissionPreset, error)
	// DeleteForProject removes all presets for the given cwd scoped to userID.
	DeleteForProject(ctx context.Context, userID *string, projectCwd string) error
}

// UpsertPresetInput holds the fields for an idempotent preset write.
type UpsertPresetInput struct {
	UserID     *string
	ProjectCwd string
	Tool       string
	Pattern    *string
}

// PresetProjectSummary groups preset entries by project directory.
type PresetProjectSummary struct {
	ProjectCwd string        `json:"projectCwd"`
	Entries    []PresetEntry `json:"entries"`
}

// PresetEntry is a single tool + optional pattern pair inside a project summary.
type PresetEntry struct {
	Tool    string  `json:"tool"`
	Pattern *string `json:"pattern"`
}

type entPermissionPresetRepo struct{ client *ent.Client }

// NewPermissionPresetRepo returns a PermissionPresetRepo backed by ent.
func NewPermissionPresetRepo(client *ent.Client) PermissionPresetRepo {
	return &entPermissionPresetRepo{client: client}
}

func (r *entPermissionPresetRepo) Upsert(ctx context.Context, input UpsertPresetInput) error {
	// Check-then-insert inside a transaction to handle the SQLite NULL-UNIQUE caveat:
	// SQLite treats two NULL values as distinct, so the UNIQUE index cannot deduplicate
	// rows where user_id or pattern is NULL. We do a manual existence check instead.
	tx, err := r.client.Tx(ctx)
	if err != nil {
		return fmt.Errorf("permissionPreset.Upsert: begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	q := tx.PermissionPreset.Query().
		Where(permissionpreset.ProjectCwd(input.ProjectCwd), permissionpreset.Tool(input.Tool))
	if input.UserID == nil {
		q = q.Where(permissionpreset.UserIDIsNil())
	} else {
		q = q.Where(permissionpreset.UserIDEQ(*input.UserID))
	}
	if input.Pattern == nil {
		q = q.Where(permissionpreset.PatternIsNil())
	} else {
		q = q.Where(permissionpreset.PatternEQ(*input.Pattern))
	}
	exists, err := q.Exist(ctx)
	if err != nil {
		return fmt.Errorf("permissionPreset.Upsert: check exists: %w", err)
	}
	if exists {
		return tx.Commit()
	}

	create := tx.PermissionPreset.Create().
		SetID(uuid.New().String()).
		SetProjectCwd(input.ProjectCwd).
		SetTool(input.Tool)
	if input.UserID != nil {
		create = create.SetUserID(*input.UserID)
	}
	if input.Pattern != nil {
		create = create.SetPattern(*input.Pattern)
	}
	if err := create.Exec(ctx); err != nil {
		return fmt.Errorf("permissionPreset.Upsert: insert: %w", err)
	}
	return tx.Commit()
}

func (r *entPermissionPresetRepo) UpsertBatch(ctx context.Context, inputs []UpsertPresetInput) error {
	for _, input := range inputs {
		if err := r.Upsert(ctx, input); err != nil {
			return fmt.Errorf("permissionPreset.UpsertBatch: %w", err)
		}
	}
	return nil
}

func (r *entPermissionPresetRepo) ListSummaries(ctx context.Context, userID *string) ([]PresetProjectSummary, error) {
	q := r.client.PermissionPreset.Query()
	if userID == nil {
		q = q.Where(permissionpreset.UserIDIsNil())
	} else {
		q = q.Where(
			permissionpreset.Or(
				permissionpreset.UserIDIsNil(),
				permissionpreset.UserIDEQ(*userID),
			),
		)
	}
	rows, err := q.All(ctx)
	if err != nil {
		return nil, fmt.Errorf("permissionPreset.ListSummaries: %w", err)
	}

	// Group by projectCwd in Go.
	grouped := make(map[string][]PresetEntry)
	for _, row := range rows {
		grouped[row.ProjectCwd] = append(grouped[row.ProjectCwd], PresetEntry{
			Tool:    row.Tool,
			Pattern: row.Pattern,
		})
	}

	summaries := make([]PresetProjectSummary, 0, len(grouped))
	for cwd, entries := range grouped {
		summaries = append(summaries, PresetProjectSummary{
			ProjectCwd: cwd,
			Entries:    entries,
		})
	}
	sort.Slice(summaries, func(i, j int) bool {
		return summaries[i].ProjectCwd < summaries[j].ProjectCwd
	})
	return summaries, nil
}

func (r *entPermissionPresetRepo) ListForCwd(ctx context.Context, userID *string, projectCwd string) ([]*ent.PermissionPreset, error) {
	q := r.client.PermissionPreset.Query().
		Where(permissionpreset.ProjectCwd(projectCwd))
	if userID == nil {
		q = q.Where(permissionpreset.UserIDIsNil())
	} else {
		q = q.Where(
			permissionpreset.Or(
				permissionpreset.UserIDIsNil(),
				permissionpreset.UserIDEQ(*userID),
			),
		)
	}
	rows, err := q.All(ctx)
	if err != nil {
		return nil, fmt.Errorf("permissionPreset.ListForCwd: %w", err)
	}
	return rows, nil
}

func (r *entPermissionPresetRepo) DeleteForProject(ctx context.Context, userID *string, projectCwd string) error {
	q := r.client.PermissionPreset.Delete().
		Where(permissionpreset.ProjectCwd(projectCwd))
	if userID == nil {
		q = q.Where(permissionpreset.UserIDIsNil())
	} else {
		q = q.Where(permissionpreset.UserIDEQ(*userID))
	}
	_, err := q.Exec(ctx)
	if err != nil {
		return fmt.Errorf("permissionPreset.DeleteForProject: %w", err)
	}
	return nil
}
