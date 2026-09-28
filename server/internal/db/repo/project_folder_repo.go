package repo

import (
	"context"
	"fmt"

	"entgo.io/ent/dialect/sql"
	"github.com/google/uuid"

	"github.com/lx-wnk/kontor/server/internal/db/ent"
	"github.com/lx-wnk/kontor/server/internal/db/ent/project"
	"github.com/lx-wnk/kontor/server/internal/db/ent/projectfolder"
)

// ProjectFolderRepo manages project folder persistence.
type ProjectFolderRepo interface {
	Create(ctx context.Context, projectID, path string, label *string, isDefault bool) (*ent.ProjectFolder, error)
	GetByID(ctx context.Context, id string) (*ent.ProjectFolder, error)
	ListByProject(ctx context.Context, projectID string) ([]*ent.ProjectFolder, error)
	// ListAll returns every folder across all projects, with the owning project eagerly loaded.
	ListAll(ctx context.Context) ([]*ent.ProjectFolder, error)
	Update(ctx context.Context, id string, path, label *string, clearLabel bool, isDefault *bool) (*ent.ProjectFolder, error)
	Delete(ctx context.Context, id string) error
	Suggest(ctx context.Context, projectID string) ([]*ent.ProjectFolder, error)
}

type entProjectFolderRepo struct {
	client *ent.Client
}

// NewProjectFolderRepo returns a ProjectFolderRepo backed by the given ent client.
func NewProjectFolderRepo(client *ent.Client) ProjectFolderRepo {
	return &entProjectFolderRepo{client: client}
}

// Create creates a new folder for the given project.
// INVARIANT: at most one folder per project may have is_default=true.
// When isDefault is true, this method clears is_default on all sibling folders in the same transaction.
func (r *entProjectFolderRepo) Create(ctx context.Context, projectID, path string, label *string, isDefault bool) (*ent.ProjectFolder, error) {
	tx, err := r.client.Tx(ctx)
	if err != nil {
		return nil, fmt.Errorf("projectfolder.Create: begin tx: %w", err)
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()

	if isDefault {
		if err = clearSiblingDefaults(ctx, tx, projectID, ""); err != nil {
			return nil, fmt.Errorf("projectfolder.Create: clear siblings: %w", err)
		}
	}

	f, err := tx.ProjectFolder.Create().
		SetID(uuid.New().String()).
		SetProjectID(projectID).
		SetPath(path).
		SetNillableLabel(label).
		SetIsDefault(isDefault).
		Save(ctx)
	if err != nil {
		return nil, fmt.Errorf("projectfolder.Create: %w", err)
	}

	if err = tx.Commit(); err != nil {
		return nil, fmt.Errorf("projectfolder.Create: commit: %w", err)
	}
	return f, nil
}

func (r *entProjectFolderRepo) GetByID(ctx context.Context, id string) (*ent.ProjectFolder, error) {
	f, err := r.client.ProjectFolder.Get(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("projectfolder.GetByID: %w", err)
	}
	return f, nil
}

func (r *entProjectFolderRepo) ListByProject(ctx context.Context, projectID string) ([]*ent.ProjectFolder, error) {
	folders, err := r.client.ProjectFolder.Query().
		Where(projectfolder.HasProjectWith(project.ID(projectID))).
		Order(projectfolder.ByIsDefault(sql.OrderDesc()), projectfolder.ByLabel()).
		All(ctx)
	if err != nil {
		return nil, fmt.Errorf("projectfolder.ListByProject: %w", err)
	}
	return folders, nil
}

// ListAll returns every project folder across all projects with the owning
// project edge eagerly loaded. Used by the cost project resolver to map session
// cwds to project grouping keys without an N+1 query.
func (r *entProjectFolderRepo) ListAll(ctx context.Context) ([]*ent.ProjectFolder, error) {
	folders, err := r.client.ProjectFolder.Query().
		WithProject().
		All(ctx)
	if err != nil {
		return nil, fmt.Errorf("projectfolder.ListAll: %w", err)
	}
	return folders, nil
}

// Update updates mutable fields on a folder.
// When isDefault is true, clears is_default on all sibling folders in the same transaction.
func (r *entProjectFolderRepo) Update(ctx context.Context, id string, path, label *string, clearLabel bool, isDefault *bool) (*ent.ProjectFolder, error) {
	tx, err := r.client.Tx(ctx)
	if err != nil {
		return nil, fmt.Errorf("projectfolder.Update: begin tx: %w", err)
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()

	if isDefault != nil && *isDefault {
		// Load the folder to find its project.
		existing, lookupErr := tx.ProjectFolder.Get(ctx, id)
		if lookupErr != nil {
			return nil, fmt.Errorf("projectfolder.Update: load folder: %w", lookupErr)
		}
		// Determine project ID from the loaded edge or re-query.
		proj, projErr := existing.QueryProject().Only(ctx)
		if projErr != nil {
			return nil, fmt.Errorf("projectfolder.Update: load project: %w", projErr)
		}
		if err = clearSiblingDefaults(ctx, tx, proj.ID, id); err != nil {
			return nil, fmt.Errorf("projectfolder.Update: clear siblings: %w", err)
		}
	}

	q := tx.ProjectFolder.UpdateOneID(id)
	if path != nil {
		q = q.SetPath(*path)
	}
	if clearLabel {
		q = q.ClearLabel()
	} else if label != nil {
		q = q.SetLabel(*label)
	}
	if isDefault != nil {
		q = q.SetIsDefault(*isDefault)
	}
	f, err := q.Save(ctx)
	if err != nil {
		return nil, fmt.Errorf("projectfolder.Update: %w", err)
	}

	if err = tx.Commit(); err != nil {
		return nil, fmt.Errorf("projectfolder.Update: commit: %w", err)
	}
	return f, nil
}

func (r *entProjectFolderRepo) Delete(ctx context.Context, id string) error {
	if err := r.client.ProjectFolder.DeleteOneID(id).Exec(ctx); err != nil {
		return fmt.Errorf("projectfolder.Delete: %w", err)
	}
	return nil
}

// Suggest returns folders for a project ordered by is_default DESC, label ASC.
func (r *entProjectFolderRepo) Suggest(ctx context.Context, projectID string) ([]*ent.ProjectFolder, error) {
	folders, err := r.client.ProjectFolder.Query().
		Where(projectfolder.HasProjectWith(project.ID(projectID))).
		Order(projectfolder.ByIsDefault(sql.OrderDesc()), projectfolder.ByLabel()).
		All(ctx)
	if err != nil {
		return nil, fmt.Errorf("projectfolder.Suggest: %w", err)
	}
	return folders, nil
}

// clearSiblingDefaults sets is_default=false on all folders belonging to projectID,
// excluding the folder identified by excludeID (pass "" to clear all).
func clearSiblingDefaults(ctx context.Context, tx *ent.Tx, projectID, excludeID string) error {
	pred := projectfolder.HasProjectWith(project.ID(projectID))
	q := tx.ProjectFolder.Update().
		Where(pred, projectfolder.IsDefault(true))
	if excludeID != "" {
		q = q.Where(projectfolder.IDNEQ(excludeID))
	}
	if err := q.SetIsDefault(false).Exec(ctx); err != nil {
		return fmt.Errorf("clear sibling defaults: %w", err)
	}
	return nil
}
