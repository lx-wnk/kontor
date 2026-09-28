package repo

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/lx-wnk/kontor/server/internal/db/ent"
	entdep "github.com/lx-wnk/kontor/server/internal/db/ent/taskdependency"
)

// DependencyRepo manages task dependency records.
type DependencyRepo interface {
	Add(ctx context.Context, taskID, dependsOnID, requiredStage, onCancelAction string) (*ent.TaskDependency, error)
	Remove(ctx context.Context, taskID, dependsOnID string) (bool, error)
	// ListUpstream returns rows where task_id == taskID (this task depends on others).
	ListUpstream(ctx context.Context, taskID string) ([]*ent.TaskDependency, error)
	// ListDownstream returns rows where depends_on_id == taskID (others depend on this task).
	ListDownstream(ctx context.Context, taskID string) ([]*ent.TaskDependency, error)
	// RemoveByID removes a single dependency row by its primary key.
	RemoveByID(ctx context.Context, id string) error
}

type entDependencyRepo struct{ client *ent.Client }

// NewDependencyRepo returns a DependencyRepo backed by the given ent client.
func NewDependencyRepo(client *ent.Client) DependencyRepo {
	return &entDependencyRepo{client: client}
}

func (r *entDependencyRepo) Add(ctx context.Context, taskID, dependsOnID, requiredStage, onCancelAction string) (*ent.TaskDependency, error) {
	dep, err := r.client.TaskDependency.Create().
		SetID(uuid.New().String()).
		SetTaskID(taskID).
		SetDependsOnID(dependsOnID).
		SetRequiredStage(requiredStage).
		SetOnCancelAction(onCancelAction).
		Save(ctx)
	if err != nil {
		return nil, fmt.Errorf("dependency.Add: %w", err)
	}
	return dep, nil
}

func (r *entDependencyRepo) Remove(ctx context.Context, taskID, dependsOnID string) (bool, error) {
	n, err := r.client.TaskDependency.Delete().
		Where(entdep.TaskID(taskID), entdep.DependsOnID(dependsOnID)).
		Exec(ctx)
	if err != nil {
		return false, fmt.Errorf("dependency.Remove: %w", err)
	}
	return n > 0, nil
}

func (r *entDependencyRepo) ListUpstream(ctx context.Context, taskID string) ([]*ent.TaskDependency, error) {
	deps, err := r.client.TaskDependency.Query().
		Where(entdep.TaskID(taskID)).
		All(ctx)
	if err != nil {
		return nil, fmt.Errorf("dependency.ListUpstream: %w", err)
	}
	return deps, nil
}

func (r *entDependencyRepo) ListDownstream(ctx context.Context, taskID string) ([]*ent.TaskDependency, error) {
	deps, err := r.client.TaskDependency.Query().
		Where(entdep.DependsOnID(taskID)).
		All(ctx)
	if err != nil {
		return nil, fmt.Errorf("dependency.ListDownstream: %w", err)
	}
	return deps, nil
}

func (r *entDependencyRepo) RemoveByID(ctx context.Context, id string) error {
	if err := r.client.TaskDependency.DeleteOneID(id).Exec(ctx); err != nil {
		return fmt.Errorf("dependency.RemoveByID: %w", err)
	}
	return nil
}
