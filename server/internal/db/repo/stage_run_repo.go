package repo

import (
	"context"
	"fmt"
	"time"

	"entgo.io/ent/dialect/sql"
	"github.com/google/uuid"
	"github.com/lx-wnk/kontor/server/internal/db/ent"
	"github.com/lx-wnk/kontor/server/internal/db/ent/stagerun"
)

type StageRunRepo interface {
	Create(ctx context.Context, input CreateStageRunInput) (*ent.StageRun, error)
	GetByID(ctx context.Context, id string) (*ent.StageRun, error)
	GetBySessionID(ctx context.Context, sessionID string) (*ent.StageRun, error)
	// ListBySessionIDs returns all stage_runs whose session_id matches one of
	// the given IDs in a single query. Used by the broadcast enricher to
	// resolve stage runs for a whole tick's worth of agents instead of one
	// GetBySessionID call per agent per tick.
	ListBySessionIDs(ctx context.Context, sessionIDs []string) ([]*ent.StageRun, error)
	GetLatestForTask(ctx context.Context, taskID string) (*ent.StageRun, error)
	GetLatestByTaskAndStage(ctx context.Context, taskID, stage string) (*ent.StageRun, error)
	GetByTaskStageIteration(ctx context.Context, taskID, stage string, iteration int) (*ent.StageRun, error)
	ListForTask(ctx context.Context, taskID string) ([]*ent.StageRun, error)
	// ListStageRunsByTaskIDs returns all stage_runs for the given task IDs
	// grouped by task_id. Eliminates N+1 queries compared to per-task ListForTask.
	ListStageRunsByTaskIDs(ctx context.Context, taskIDs []string) (map[string][]*ent.StageRun, error)
	ListByStatus(ctx context.Context, statuses ...string) ([]*ent.StageRun, error)
	ListPending(ctx context.Context) ([]*ent.StageRun, error)
	Update(ctx context.Context, id string, input UpdateStageRunInput) (*ent.StageRun, error)
	SumCompletedCostCents(ctx context.Context, taskID string) (int64, error)
	SumCompletedTokens(ctx context.Context, taskID string) (int64, error)
	// GetLatestForTasks returns the most-recent stage_run per task using a
	// ROW_NUMBER() window function — correctness is exact regardless of iteration
	// count, unlike the former Go-side heuristic limit of len(ids)*20+20.
	GetLatestForTasks(ctx context.Context, taskIDs []string) (map[string]*ent.StageRun, error)
	// ListInWindow returns all stage_runs whose created_at is in the half-open range [from, to).
	ListInWindow(ctx context.Context, from, to time.Time) ([]*ent.StageRun, error)
}

type CreateStageRunInput struct {
	TaskID            string
	Stage             string
	Iteration         int
	SessionName       string
	PendingUserPrompt string
}

type UpdateStageRunInput struct {
	Status            *string
	PID               *int
	PIDClear          bool
	SessionID         *string
	Output            map[string]any
	TokensUsed        *int
	CostCents         *int
	StartedAt         *time.Time
	EndedAt           *time.Time
	LastGrantAt       *time.Time
	RetryCount        *int
	NextRetryAt       *time.Time
	NextRetryAtClear  bool
	StartedAtClear    bool
	PendingUserPrompt *string
}

type entStageRunRepo struct {
	client *ent.Client
}

// NewStageRunRepo returns a StageRunRepo backed by the ent client.
// Bulk window-function queries (GetLatestForTasks, ListStageRunsByTaskIDs)
// use an ent-based heuristic fallback. Callers that need exact window-function
// semantics should inject rawrepo.StageRunBulkRepo directly via DI.
func NewStageRunRepo(client *ent.Client) StageRunRepo {
	return &entStageRunRepo{client: client}
}

func (r *entStageRunRepo) Create(ctx context.Context, in CreateStageRunInput) (*ent.StageRun, error) {
	q := r.client.StageRun.Create().
		SetID(uuid.New().String()).
		SetTaskID(in.TaskID).
		SetStage(in.Stage).
		SetIteration(in.Iteration).
		SetStatus("pending")
	if in.SessionName != "" {
		q = q.SetSessionName(in.SessionName)
	}
	if in.PendingUserPrompt != "" {
		q = q.SetPendingUserPrompt(in.PendingUserPrompt)
	}
	sr, err := q.Save(ctx)
	if err != nil {
		return nil, fmt.Errorf("stagerun.Create: %w", err)
	}
	return sr, nil
}

func (r *entStageRunRepo) GetByID(ctx context.Context, id string) (*ent.StageRun, error) {
	sr, err := r.client.StageRun.Get(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("stagerun.GetByID: %w", err)
	}
	return sr, nil
}

func (r *entStageRunRepo) GetBySessionID(ctx context.Context, sessionID string) (*ent.StageRun, error) {
	sr, err := r.client.StageRun.Query().
		Where(stagerun.SessionID(sessionID)).
		Only(ctx)
	if err != nil {
		return nil, fmt.Errorf("stagerun.GetBySessionID: %w", err)
	}
	return sr, nil
}

func (r *entStageRunRepo) ListBySessionIDs(ctx context.Context, sessionIDs []string) ([]*ent.StageRun, error) {
	if len(sessionIDs) == 0 {
		return nil, nil
	}
	runs, err := r.client.StageRun.Query().
		Where(stagerun.SessionIDIn(sessionIDs...)).
		All(ctx)
	if err != nil {
		return nil, fmt.Errorf("stagerun.ListBySessionIDs: %w", err)
	}
	return runs, nil
}

func (r *entStageRunRepo) GetLatestForTask(ctx context.Context, taskID string) (*ent.StageRun, error) {
	sr, err := r.client.StageRun.Query().
		Where(stagerun.TaskID(taskID)).
		Order(stagerun.ByCreatedAt(sql.OrderDesc())).
		First(ctx)
	if err != nil {
		return nil, fmt.Errorf("stagerun.GetLatestForTask: %w", err)
	}
	return sr, nil
}

func (r *entStageRunRepo) GetLatestByTaskAndStage(ctx context.Context, taskID, stage string) (*ent.StageRun, error) {
	sr, err := r.client.StageRun.Query().
		Where(stagerun.TaskID(taskID), stagerun.Stage(stage)).
		Order(stagerun.ByIteration(sql.OrderDesc())).
		First(ctx)
	if err != nil {
		return nil, fmt.Errorf("stagerun.GetLatestByTaskAndStage: %w", err)
	}
	return sr, nil
}

func (r *entStageRunRepo) GetByTaskStageIteration(ctx context.Context, taskID, stage string, iteration int) (*ent.StageRun, error) {
	sr, err := r.client.StageRun.Query().
		Where(stagerun.TaskID(taskID), stagerun.Stage(stage), stagerun.Iteration(iteration)).
		Only(ctx)
	if err != nil {
		return nil, fmt.Errorf("stagerun.GetByTaskStageIteration: %w", err)
	}
	return sr, nil
}

func (r *entStageRunRepo) ListForTask(ctx context.Context, taskID string) ([]*ent.StageRun, error) {
	runs, err := r.client.StageRun.Query().
		Where(stagerun.TaskID(taskID)).
		Order(stagerun.ByIteration()).
		All(ctx)
	if err != nil {
		return nil, fmt.Errorf("stagerun.ListForTask: %w", err)
	}
	return runs, nil
}

func (r *entStageRunRepo) ListByStatus(ctx context.Context, statuses ...string) ([]*ent.StageRun, error) {
	runs, err := r.client.StageRun.Query().
		Where(stagerun.StatusIn(statuses...)).
		All(ctx)
	if err != nil {
		return nil, fmt.Errorf("stagerun.ListByStatus: %w", err)
	}
	return runs, nil
}

func (r *entStageRunRepo) ListPending(ctx context.Context) ([]*ent.StageRun, error) {
	return r.ListByStatus(ctx, "pending")
}

func (r *entStageRunRepo) Update(ctx context.Context, id string, in UpdateStageRunInput) (*ent.StageRun, error) {
	q := r.client.StageRun.UpdateOneID(id)
	if in.Status != nil {
		q = q.SetStatus(*in.Status)
	}
	if in.PIDClear {
		q = q.ClearPid()
	} else if in.PID != nil {
		q = q.SetPid(*in.PID)
	}
	if in.SessionID != nil {
		q = q.SetSessionID(*in.SessionID)
	}
	if in.Output != nil {
		q = q.SetOutput(in.Output)
	}
	if in.TokensUsed != nil {
		q = q.SetTokensUsed(*in.TokensUsed)
	}
	if in.CostCents != nil {
		q = q.SetCostCents(*in.CostCents)
	}
	if in.StartedAtClear {
		q = q.ClearStartedAt()
	} else if in.StartedAt != nil {
		q = q.SetStartedAt(*in.StartedAt)
	}
	if in.EndedAt != nil {
		q = q.SetEndedAt(*in.EndedAt)
	}
	if in.LastGrantAt != nil {
		q = q.SetLastGrantAt(*in.LastGrantAt)
	}
	if in.RetryCount != nil {
		q = q.SetRetryCount(*in.RetryCount)
	}
	if in.NextRetryAtClear {
		q = q.ClearNextRetryAt()
	} else if in.NextRetryAt != nil {
		q = q.SetNextRetryAt(*in.NextRetryAt)
	}
	if in.PendingUserPrompt != nil {
		q = q.SetPendingUserPrompt(*in.PendingUserPrompt)
	}
	sr, err := q.Save(ctx)
	if err != nil {
		return nil, fmt.Errorf("stagerun.Update: %w", err)
	}
	return sr, nil
}

func (r *entStageRunRepo) SumCompletedCostCents(ctx context.Context, taskID string) (int64, error) {
	type aggResult struct {
		Sum int `json:"sum"`
	}
	var result []aggResult
	err := r.client.StageRun.Query().
		Where(stagerun.TaskID(taskID), stagerun.StatusIn("done", "failed")).
		Aggregate(ent.Sum(stagerun.FieldCostCents)).
		Scan(ctx, &result)
	if err != nil {
		return 0, fmt.Errorf("stagerun.SumCompletedCostCents: %w", err)
	}
	if len(result) == 0 {
		return 0, nil
	}
	return int64(result[0].Sum), nil
}

func (r *entStageRunRepo) SumCompletedTokens(ctx context.Context, taskID string) (int64, error) {
	type aggResult struct {
		Sum int `json:"sum"`
	}
	var result []aggResult
	err := r.client.StageRun.Query().
		Where(stagerun.TaskID(taskID), stagerun.StatusIn("done", "failed")).
		Aggregate(ent.Sum(stagerun.FieldTokensUsed)).
		Scan(ctx, &result)
	if err != nil {
		return 0, fmt.Errorf("stagerun.SumCompletedTokens: %w", err)
	}
	if len(result) == 0 {
		return 0, nil
	}
	return int64(result[0].Sum), nil
}

// GetLatestForTasks returns the most-recent stage_run per task using an
// ent-based heuristic query.
// Fallback for transaction contexts where bulkRepo is unavailable; heuristic
// limit acceptable because transaction-scoped calls are always single-task.
func (r *entStageRunRepo) GetLatestForTasks(ctx context.Context, taskIDs []string) (map[string]*ent.StageRun, error) {
	limit := len(taskIDs)*20 + 20
	runs, err := r.client.StageRun.Query().
		Where(stagerun.TaskIDIn(taskIDs...)).
		Order(stagerun.ByCreatedAt(sql.OrderDesc())).
		Limit(limit).
		All(ctx)
	if err != nil {
		return nil, fmt.Errorf("stagerun.GetLatestForTasks: %w", err)
	}
	result := make(map[string]*ent.StageRun, len(taskIDs))
	for _, run := range runs {
		if _, exists := result[run.TaskID]; !exists {
			result[run.TaskID] = run
		}
	}
	return result, nil
}

// ListInWindow returns all stage_runs whose created_at is in the half-open
// range [from, to). The exclusive upper bound keeps adjacent scan windows
// disjoint so a boundary run is never counted in both.
func (r *entStageRunRepo) ListInWindow(ctx context.Context, from, to time.Time) ([]*ent.StageRun, error) {
	runs, err := r.client.StageRun.Query().
		Where(
			stagerun.CreatedAtGTE(from),
			stagerun.CreatedAtLT(to),
		).
		All(ctx)
	if err != nil {
		return nil, fmt.Errorf("stagerun.ListInWindow: %w", err)
	}
	return runs, nil
}

// ListStageRunsByTaskIDs returns all stage_runs for the given task IDs in a
// single ent bulk query, grouped by task_id. Callers that need the exact
// window-function implementation should inject rawrepo.StageRunBulkRepo
// directly and call AllForTaskIDs.
func (r *entStageRunRepo) ListStageRunsByTaskIDs(ctx context.Context, taskIDs []string) (map[string][]*ent.StageRun, error) {
	if len(taskIDs) == 0 {
		return map[string][]*ent.StageRun{}, nil
	}
	runs, err := r.client.StageRun.Query().
		Where(stagerun.TaskIDIn(taskIDs...)).
		Order(stagerun.ByIteration()).
		All(ctx)
	if err != nil {
		return nil, fmt.Errorf("stagerun.ListStageRunsByTaskIDs: %w", err)
	}
	result := make(map[string][]*ent.StageRun, len(taskIDs))
	for _, run := range runs {
		result[run.TaskID] = append(result[run.TaskID], run)
	}
	return result, nil
}
