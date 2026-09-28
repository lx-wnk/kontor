package repo

import (
	"context"
	"fmt"

	"entgo.io/ent/dialect/sql"
	"github.com/google/uuid"
	"github.com/lx-wnk/kontor/server/internal/db/ent"
	"github.com/lx-wnk/kontor/server/internal/db/ent/refinementturn"
)

// RefinementTurnRepo is the data-access interface for refinement chat turns.
type RefinementTurnRepo interface {
	Create(ctx context.Context, in CreateTurnInput) (*ent.RefinementTurn, error)
	// ListForTask returns all turns for a task ordered by created_at ASC.
	// Limit <= 0 means no limit.
	ListForTask(ctx context.Context, taskID string, limit int) ([]*ent.RefinementTurn, error)
	// ListForTaskNewest returns the most-recent limit turns for a task, ordered ASC (oldest first).
	// This is the correct method to use for building windowed conversation history.
	// Limit <= 0 means no limit (equivalent to ListForTask).
	ListForTaskNewest(ctx context.Context, taskID string, limit int) ([]*ent.RefinementTurn, error)
	// DeleteForTask removes all turns for a task (for cleanup / reset).
	DeleteForTask(ctx context.Context, taskID string) error
}

// CreateTurnInput holds the fields needed to create a RefinementTurn.
type CreateTurnInput struct {
	TaskID  string
	Role    string // "user" or "assistant"
	Content string
	Phase   *string
	Options []string // prepared answers; nil = none
}

type entRefinementTurnRepo struct{ client *ent.Client }

// NewRefinementTurnRepo returns a RefinementTurnRepo backed by ent.
func NewRefinementTurnRepo(client *ent.Client) RefinementTurnRepo {
	return &entRefinementTurnRepo{client: client}
}

func (r *entRefinementTurnRepo) Create(ctx context.Context, in CreateTurnInput) (*ent.RefinementTurn, error) {
	b := r.client.RefinementTurn.Create().
		SetID(uuid.New().String()).
		SetTaskID(in.TaskID).
		SetRole(refinementturn.Role(in.Role)).
		SetContent(in.Content).
		SetNillablePhase(in.Phase)
	if len(in.Options) > 0 {
		b = b.SetOptions(in.Options)
	}
	turn, err := b.Save(ctx)
	if err != nil {
		return nil, fmt.Errorf("refinementTurn.Create: %w", err)
	}
	return turn, nil
}

func (r *entRefinementTurnRepo) ListForTask(ctx context.Context, taskID string, limit int) ([]*ent.RefinementTurn, error) {
	q := r.client.RefinementTurn.Query().
		Where(refinementturn.TaskID(taskID)).
		Order(refinementturn.ByCreatedAt())
	if limit > 0 {
		q = q.Limit(limit)
	}
	turns, err := q.All(ctx)
	if err != nil {
		return nil, fmt.Errorf("refinementTurn.ListForTask: %w", err)
	}
	return turns, nil
}

func (r *entRefinementTurnRepo) ListForTaskNewest(ctx context.Context, taskID string, limit int) ([]*ent.RefinementTurn, error) {
	q := r.client.RefinementTurn.Query().
		Where(refinementturn.TaskID(taskID)).
		Order(refinementturn.ByCreatedAt(sql.OrderDesc()))
	if limit > 0 {
		q = q.Limit(limit)
	}
	turns, err := q.All(ctx)
	if err != nil {
		return nil, fmt.Errorf("refinementTurn.ListForTaskNewest: %w", err)
	}
	// Reverse to restore ASC order (oldest first) for the caller.
	for i, j := 0, len(turns)-1; i < j; i, j = i+1, j-1 {
		turns[i], turns[j] = turns[j], turns[i]
	}
	return turns, nil
}

func (r *entRefinementTurnRepo) DeleteForTask(ctx context.Context, taskID string) error {
	_, err := r.client.RefinementTurn.Delete().
		Where(refinementturn.TaskID(taskID)).
		Exec(ctx)
	if err != nil {
		return fmt.Errorf("refinementTurn.DeleteForTask: %w", err)
	}
	return nil
}
