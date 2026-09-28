package serverapp

import (
	"context"
	"fmt"

	"github.com/lx-wnk/kontor/server/internal/db/ent"
	"github.com/lx-wnk/kontor/server/internal/db/ent/task"
)

// taskProjectOps adapts the ent client to api/projects.TaskProjectOps so the
// projects handler can guard DELETE /api/projects/{id} against active tasks
// without depending on the full TaskRepo interface.
type taskProjectOps struct {
	client *ent.Client
}

func newTaskProjectOps(client *ent.Client) *taskProjectOps {
	if client == nil {
		return nil
	}
	return &taskProjectOps{client: client}
}

// terminalStages are the task.current_stage values considered terminal — a
// task in one of these may be detached from its project before deletion.
var terminalStages = []string{"done", "cancelled"}

// CountActiveByProject counts non-terminal tasks linked to projectID.
func (o *taskProjectOps) CountActiveByProject(ctx context.Context, projectID string) (int, error) {
	n, err := o.client.Task.Query().
		Where(
			task.ProjectID(projectID),
			task.CurrentStageNotIn(terminalStages...),
		).
		Count(ctx)
	if err != nil {
		return 0, fmt.Errorf("CountActiveByProject: %w", err)
	}
	return n, nil
}

// ClearProjectForTerminalTasks sets project_id=NULL on all tasks linked to
// projectID whose current_stage is in terminalStages.
func (o *taskProjectOps) ClearProjectForTerminalTasks(ctx context.Context, projectID string) error {
	_, err := o.client.Task.Update().
		Where(
			task.ProjectID(projectID),
			task.CurrentStageIn(terminalStages...),
		).
		ClearProjectID().
		Save(ctx)
	if err != nil {
		return fmt.Errorf("ClearProjectForTerminalTasks: %w", err)
	}
	return nil
}
