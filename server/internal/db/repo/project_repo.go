package repo

import (
	"context"
	"fmt"
	"time"

	"entgo.io/ent/dialect/sql"
	"github.com/google/uuid"

	"github.com/lx-wnk/kontor/server/internal/db/ent"
	"github.com/lx-wnk/kontor/server/internal/db/ent/project"
	"github.com/lx-wnk/kontor/server/internal/db/ent/projectfolder"
)

// ProjectWithCount pairs a Project with its folder count.
type ProjectWithCount struct {
	*ent.Project
	FolderCount int
}

// ProjectRepo manages project persistence.
type ProjectRepo interface {
	Create(ctx context.Context, name, slug string, description, color, defaultSpawnerID, setupCommand *string) (*ent.Project, error)
	GetByID(ctx context.Context, id string) (*ent.Project, error)
	GetBySlug(ctx context.Context, slug string) (*ent.Project, error)
	GetWithFolders(ctx context.Context, id string) (*ent.Project, error)
	List(ctx context.Context) ([]*ent.Project, error)
	ListWithFolderCount(ctx context.Context) ([]ProjectWithCount, error)
	Update(ctx context.Context, id string, name, slug *string, description, color, defaultSpawnerID, setupCommand *string, clearDescription, clearColor, clearDefaultSpawnerID, clearSetupCommand bool) (*ent.Project, error)
	Delete(ctx context.Context, id string) error
}

type entProjectRepo struct {
	client *ent.Client
}

// NewProjectRepo returns a ProjectRepo backed by the given ent client.
func NewProjectRepo(client *ent.Client) ProjectRepo {
	return &entProjectRepo{client: client}
}

func (r *entProjectRepo) Create(ctx context.Context, name, slug string, description, color, defaultSpawnerID, setupCommand *string) (*ent.Project, error) {
	p, err := r.client.Project.Create().
		SetID(uuid.New().String()).
		SetName(name).
		SetSlug(slug).
		SetNillableDescription(description).
		SetNillableColor(color).
		SetNillableDefaultSpawnerID(defaultSpawnerID).
		SetNillableSetupCommand(setupCommand).
		Save(ctx)
	if err != nil {
		return nil, fmt.Errorf("project.Create: %w", err)
	}
	return p, nil
}

func (r *entProjectRepo) GetByID(ctx context.Context, id string) (*ent.Project, error) {
	p, err := r.client.Project.Get(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("project.GetByID: %w", err)
	}
	return p, nil
}

func (r *entProjectRepo) GetBySlug(ctx context.Context, slug string) (*ent.Project, error) {
	p, err := r.client.Project.Query().
		Where(project.Slug(slug)).
		Only(ctx)
	if err != nil {
		return nil, fmt.Errorf("project.GetBySlug: %w", err)
	}
	return p, nil
}

func (r *entProjectRepo) GetWithFolders(ctx context.Context, id string) (*ent.Project, error) {
	p, err := r.client.Project.Query().
		Where(project.ID(id)).
		WithFolders(func(q *ent.ProjectFolderQuery) {
			q.Order(projectfolder.ByIsDefault(sql.OrderDesc()), projectfolder.ByLabel())
		}).
		Only(ctx)
	if err != nil {
		return nil, fmt.Errorf("project.GetWithFolders: %w", err)
	}
	return p, nil
}

func (r *entProjectRepo) List(ctx context.Context) ([]*ent.Project, error) {
	projects, err := r.client.Project.Query().
		Order(project.ByCreatedAt()).
		All(ctx)
	if err != nil {
		return nil, fmt.Errorf("project.List: %w", err)
	}
	return projects, nil
}

func (r *entProjectRepo) ListWithFolderCount(ctx context.Context) ([]ProjectWithCount, error) {
	projects, err := r.client.Project.Query().
		WithFolders().
		Order(project.ByCreatedAt()).
		All(ctx)
	if err != nil {
		return nil, fmt.Errorf("project.ListWithFolderCount: %w", err)
	}
	result := make([]ProjectWithCount, len(projects))
	for i, p := range projects {
		folders, _ := p.Edges.FoldersOrErr()
		result[i] = ProjectWithCount{
			Project:     p,
			FolderCount: len(folders),
		}
	}
	return result, nil
}

func (r *entProjectRepo) Update(ctx context.Context, id string, name, slug *string, description, color, defaultSpawnerID, setupCommand *string, clearDescription, clearColor, clearDefaultSpawnerID, clearSetupCommand bool) (*ent.Project, error) {
	q := r.client.Project.UpdateOneID(id).SetUpdatedAt(time.Now())
	if name != nil {
		q = q.SetName(*name)
	}
	if slug != nil {
		q = q.SetSlug(*slug)
	}
	if clearDescription {
		q = q.ClearDescription()
	} else if description != nil {
		q = q.SetDescription(*description)
	}
	if clearColor {
		q = q.ClearColor()
	} else if color != nil {
		q = q.SetColor(*color)
	}
	if clearDefaultSpawnerID {
		q = q.ClearDefaultSpawnerID()
	} else if defaultSpawnerID != nil {
		q = q.SetDefaultSpawnerID(*defaultSpawnerID)
	}
	if clearSetupCommand {
		q = q.ClearSetupCommand()
	} else if setupCommand != nil {
		q = q.SetSetupCommand(*setupCommand)
	}
	p, err := q.Save(ctx)
	if err != nil {
		return nil, fmt.Errorf("project.Update: %w", err)
	}
	return p, nil
}

func (r *entProjectRepo) Delete(ctx context.Context, id string) error {
	if err := r.client.Project.DeleteOneID(id).Exec(ctx); err != nil {
		return fmt.Errorf("project.Delete: %w", err)
	}
	return nil
}
