package repo

import (
	"context"
	"fmt"
	"time"

	"entgo.io/ent/dialect/sql"
	"github.com/google/uuid"
	"github.com/lx-wnk/kontor/server/internal/db/ent"
	"github.com/lx-wnk/kontor/server/internal/db/ent/task"
	"github.com/lx-wnk/kontor/server/internal/ranking"
)

type TaskRepo interface {
	Create(ctx context.Context, input CreateTaskInput) (*ent.Task, error)
	GetByID(ctx context.Context, id string) (*ent.Task, error)
	GetBySlug(ctx context.Context, slug string) (*ent.Task, error)
	Update(ctx context.Context, id string, input UpdateTaskInput) (*ent.Task, error)
	RerankBetween(ctx context.Context, id, beforeID, afterID string) (*ent.Task, error)
	Delete(ctx context.Context, id string) error
	// ListForUser returns userID's tasks, or every task when unscoped is set —
	// the loopback single-user mode, where there is one implicit user.
	ListForUser(ctx context.Context, userID string, unscoped bool) ([]*ent.Task, error)
	ListPickable(ctx context.Context) ([]*ent.Task, error)
	ListByStage(ctx context.Context, stage string) ([]*ent.Task, error)
	// ListByIDs returns all tasks matching the given IDs in a single query.
	// Used by the eval collector to resolve task metadata (spawner_id, model) in bulk.
	ListByIDs(ctx context.Context, ids []string) ([]*ent.Task, error)
	// CountActiveBySourceBranch counts non-terminal tasks (excluding done/cancelled)
	// that use the given source_branch, optionally excluding one task ID. A branch can
	// back at most one git worktree, so two active tasks must never share one.
	CountActiveBySourceBranch(ctx context.Context, branch, excludeID string) (int, error)
	// ListByRoutine returns routineID's tasks, newest first, at most limit.
	ListByRoutine(ctx context.Context, routineID string, limit int) ([]*ent.Task, error)
}

type CreateTaskInput struct {
	ID              string
	Slug            string
	Title           string
	Description     *string
	Cwd             string
	WorktreePath    *string
	SourceBranch    *string
	TargetBranch    *string
	ParentTaskID    *string
	UserID          *string
	MaxIterations   int
	TokenBudget     *int
	CostBudgetCents *int
	SilverBullet    bool
	PlanMode        *bool
	Priority        string
	CurrentStage    string
	Autonomy        *string
	Metadata        map[string]any
	ProjectID       *string
	SpawnerID       *string
	RoutineID       *string
	Kind            string
	Applications    []string
	Rank            *float64
}

type UpdateTaskInput struct {
	Title             *string
	Description       *string
	CurrentStage      *string
	Priority          *string
	SilverBullet      *bool
	PlanMode          *bool
	MaxIterations     *int
	TokenBudget       *int
	CostBudgetCents   *int
	Metadata          map[string]any
	MetadataClear     bool
	WorktreePath      *string
	SourceBranch      *string
	TargetBranch      *string
	ProjectID         *string
	SpawnerID         *string
	Rank              *float64
	ClearProjectID    bool
	ClearSpawnerID    bool
	ClearWorktreePath bool
	Autonomy          *string
}

// rankGap is the spacing applied when a card is dropped at the top or bottom of
// a column (no neighbor on one side). Between two neighbors the midpoint is used.
const rankGap = 1 << 20

type entTaskRepo struct{ client *ent.Client }

func NewTaskRepo(client *ent.Client) TaskRepo {
	return &entTaskRepo{client: client}
}

func (r *entTaskRepo) Create(ctx context.Context, in CreateTaskInput) (*ent.Task, error) {
	id := in.ID
	if id == "" {
		id = uuid.New().String()
	}
	q := r.client.Task.Create().
		SetID(id).
		SetSlug(in.Slug).
		SetTitle(in.Title).
		SetCwd(in.Cwd).
		SetCurrentStage(in.CurrentStage).
		SetPriority(in.Priority).
		SetMaxIterations(in.MaxIterations).
		SetSilverBullet(in.SilverBullet).
		SetNillablePlanMode(in.PlanMode)
	if in.Description != nil {
		q = q.SetDescription(*in.Description)
	}
	if in.WorktreePath != nil {
		q = q.SetWorktreePath(*in.WorktreePath)
	}
	if in.SourceBranch != nil {
		q = q.SetSourceBranch(*in.SourceBranch)
	}
	if in.TargetBranch != nil {
		q = q.SetTargetBranch(*in.TargetBranch)
	}
	if in.ParentTaskID != nil {
		q = q.SetParentTaskID(*in.ParentTaskID)
	}
	if in.UserID != nil {
		q = q.SetUserID(*in.UserID)
	}
	if in.TokenBudget != nil {
		q = q.SetTokenBudget(*in.TokenBudget)
	}
	if in.CostBudgetCents != nil {
		q = q.SetCostBudgetCents(*in.CostBudgetCents)
	}
	if in.Metadata != nil {
		q = q.SetMetadata(in.Metadata)
	}
	if in.Applications != nil {
		q = q.SetApplications(in.Applications)
	}
	q = q.SetNillableProjectID(in.ProjectID).SetNillableSpawnerID(in.SpawnerID).SetNillableRoutineID(in.RoutineID).SetNillableAutonomy(in.Autonomy)
	if in.Kind != "" {
		q = q.SetKind(in.Kind)
	}
	if in.Rank != nil {
		q = q.SetRank(*in.Rank)
	} else {
		// Seed rank from creation time so new cards land last in their column and
		// sort identically to created_at order until the user drags them.
		q = q.SetRank(float64(time.Now().UnixMicro()))
	}
	t, err := q.Save(ctx)
	if err != nil {
		return nil, fmt.Errorf("task.Create: %w", err)
	}
	return t, nil
}

func (r *entTaskRepo) GetByID(ctx context.Context, id string) (*ent.Task, error) {
	t, err := r.client.Task.Get(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("task.GetByID: %w", err)
	}
	return t, nil
}

func (r *entTaskRepo) GetBySlug(ctx context.Context, slug string) (*ent.Task, error) {
	t, err := r.client.Task.Query().Where(task.Slug(slug)).Only(ctx)
	if err != nil {
		return nil, fmt.Errorf("task.GetBySlug: %w", err)
	}
	return t, nil
}

func (r *entTaskRepo) Update(ctx context.Context, id string, in UpdateTaskInput) (*ent.Task, error) {
	q := r.client.Task.UpdateOneID(id).SetUpdatedAt(time.Now())
	if in.Title != nil {
		q = q.SetTitle(*in.Title)
	}
	if in.Description != nil {
		q = q.SetDescription(*in.Description)
	}
	if in.CurrentStage != nil {
		q = q.SetCurrentStage(*in.CurrentStage)
	}
	if in.Priority != nil {
		q = q.SetPriority(*in.Priority)
	}
	if in.SilverBullet != nil {
		q = q.SetSilverBullet(*in.SilverBullet)
	}
	if in.PlanMode != nil {
		q = q.SetPlanMode(*in.PlanMode)
	}
	if in.MaxIterations != nil {
		q = q.SetMaxIterations(*in.MaxIterations)
	}
	if in.TokenBudget != nil {
		q = q.SetTokenBudget(*in.TokenBudget)
	}
	if in.CostBudgetCents != nil {
		q = q.SetCostBudgetCents(*in.CostBudgetCents)
	}
	if in.MetadataClear {
		q = q.ClearMetadata()
	} else if in.Metadata != nil {
		q = q.SetMetadata(in.Metadata)
	}
	if in.ClearWorktreePath {
		q = q.ClearWorktreePath()
	} else if in.WorktreePath != nil {
		q = q.SetWorktreePath(*in.WorktreePath)
	}
	if in.SourceBranch != nil {
		q = q.SetSourceBranch(*in.SourceBranch)
	}
	if in.TargetBranch != nil {
		q = q.SetTargetBranch(*in.TargetBranch)
	}
	if in.ClearProjectID {
		q = q.ClearProjectID()
	} else if in.ProjectID != nil {
		q = q.SetProjectID(*in.ProjectID)
	}
	if in.ClearSpawnerID {
		q = q.ClearSpawnerID()
	} else if in.SpawnerID != nil {
		q = q.SetSpawnerID(*in.SpawnerID)
	}
	if in.Autonomy != nil {
		q = q.SetAutonomy(*in.Autonomy)
	}
	if in.Rank != nil {
		q = q.SetRank(*in.Rank)
	}
	t, err := q.Save(ctx)
	if err != nil {
		return nil, fmt.Errorf("task.Update: %w", err)
	}
	return t, nil
}

// RerankBetween assigns task id a new rank positioned between beforeID (the card
// above the drop, lower rank) and afterID (the card below, higher rank). Either
// neighbor may be empty when dropping at a column edge. The read of both
// neighbors and the write happen inside one transaction so concurrent drops
// cannot interleave and compute a stale midpoint.
func (r *entTaskRepo) RerankBetween(ctx context.Context, id, beforeID, afterID string) (*ent.Task, error) {
	tx, err := r.client.Tx(ctx)
	if err != nil {
		return nil, fmt.Errorf("task.RerankBetween: begin tx: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()

	neighborRank := func(tid string) (float64, bool, error) {
		if tid == "" {
			return 0, false, nil
		}
		t, err := tx.Task.Get(ctx, tid)
		if err != nil {
			return 0, false, err
		}
		return ranking.EffectiveRank(t), true, nil
	}

	beforeRank, hasBefore, err := neighborRank(beforeID)
	if err != nil {
		return nil, fmt.Errorf("task.RerankBetween: before %q: %w", beforeID, err)
	}
	afterRank, hasAfter, err := neighborRank(afterID)
	if err != nil {
		return nil, fmt.Errorf("task.RerankBetween: after %q: %w", afterID, err)
	}

	var newRank float64
	switch {
	case hasBefore && hasAfter:
		newRank = (beforeRank + afterRank) / 2
	case hasBefore:
		newRank = beforeRank + rankGap
	case hasAfter:
		newRank = afterRank - rankGap
	default:
		newRank = float64(time.Now().UnixMicro())
	}

	t, err := tx.Task.UpdateOneID(id).SetRank(newRank).SetUpdatedAt(time.Now()).Save(ctx)
	if err != nil {
		return nil, fmt.Errorf("task.RerankBetween: update %q: %w", id, err)
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("task.RerankBetween: commit: %w", err)
	}
	committed = true
	return t, nil
}

func (r *entTaskRepo) Delete(ctx context.Context, id string) error {
	if err := r.client.Task.DeleteOneID(id).Exec(ctx); err != nil {
		return fmt.Errorf("task.Delete: %w", err)
	}
	return nil
}

func (r *entTaskRepo) ListForUser(ctx context.Context, userID string, unscoped bool) ([]*ent.Task, error) {
	q := r.client.Task.Query().Order(task.ByCreatedAt())
	if !unscoped {
		q = q.Where(task.UserID(userID))
	}
	tasks, err := q.All(ctx)
	if err != nil {
		return nil, fmt.Errorf("task.ListForUser: %w", err)
	}
	return tasks, nil
}

// ListPickable returns tasks eligible for the runner picker, sorted by SQL for
// the criteria that map cleanly to ORDER BY:
//   - silver_bullet DESC   (true first — binary column, trivially sortable)
//   - priority DESC        (high > medium > low, alphabetic sort matches rank order)
//   - created_at ASC       (FIFO within same tier)
//
// Stage-index ordering cannot be expressed as a simple SQL ORDER BY (requires a
// custom enum rank map) and is applied by sortByStageIndex in runner_picker.go
// after the query returns.
// F-PERF-010: removed the incorrect ascending BySilverBullet() SQL order and
// added the correct DESC direction; created_at ASC is explicit for clarity.
func (r *entTaskRepo) ListPickable(ctx context.Context) ([]*ent.Task, error) {
	tasks, err := r.client.Task.Query().
		Where(
			task.CurrentStageNotIn("done", "cancelled", "on_hold", "backlog"),
		).
		Order(
			task.BySilverBullet(sql.OrderDesc()),
			task.ByPriority(sql.OrderDesc()),
			task.ByRank(sql.OrderAsc()),
			task.ByCreatedAt(sql.OrderAsc()),
		).
		All(ctx)
	if err != nil {
		return nil, fmt.Errorf("task.ListPickable: %w", err)
	}
	return tasks, nil
}

func (r *entTaskRepo) ListByStage(ctx context.Context, stage string) ([]*ent.Task, error) {
	tasks, err := r.client.Task.Query().Where(task.CurrentStage(stage)).All(ctx)
	if err != nil {
		return nil, fmt.Errorf("task.ListByStage: %w", err)
	}
	return tasks, nil
}

func (r *entTaskRepo) ListByIDs(ctx context.Context, ids []string) ([]*ent.Task, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	tasks, err := r.client.Task.Query().Where(task.IDIn(ids...)).All(ctx)
	if err != nil {
		return nil, fmt.Errorf("task.ListByIDs: %w", err)
	}
	return tasks, nil
}

func (r *entTaskRepo) CountActiveBySourceBranch(ctx context.Context, branch, excludeID string) (int, error) {
	q := r.client.Task.Query().Where(
		task.CurrentStageNotIn("done", "cancelled"),
		task.SourceBranch(branch),
	)
	if excludeID != "" {
		q = q.Where(task.IDNEQ(excludeID))
	}
	n, err := q.Count(ctx)
	if err != nil {
		return 0, fmt.Errorf("task.CountActiveBySourceBranch: %w", err)
	}
	return n, nil
}

func (r *entTaskRepo) ListByRoutine(ctx context.Context, routineID string, limit int) ([]*ent.Task, error) {
	tasks, err := r.client.Task.Query().
		Where(task.RoutineID(routineID)).
		Order(ent.Desc(task.FieldCreatedAt)).
		Limit(limit).
		All(ctx)
	if err != nil {
		return nil, fmt.Errorf("task.ListByRoutine: %w", err)
	}
	return tasks, nil
}
