package repo

import (
	"context"
	"fmt"
	"time"

	"entgo.io/ent/dialect/sql"
	"github.com/google/uuid"
	"github.com/lx-wnk/kontor/server/internal/db/ent"
	"github.com/lx-wnk/kontor/server/internal/db/ent/systemprompt"
	"github.com/lx-wnk/kontor/server/internal/domainerr"
)

// SystemPromptRepo defines CRUD for SystemPrompt entities.
type SystemPromptRepo interface {
	Create(ctx context.Context, in CreateSystemPromptInput) (*ent.SystemPrompt, error)
	List(ctx context.Context) ([]*ent.SystemPrompt, error)
	ListForStage(ctx context.Context, stage string) ([]*ent.SystemPrompt, error)
	Update(ctx context.Context, id string, in UpdateSystemPromptInput) (*ent.SystemPrompt, error)
	Delete(ctx context.Context, id string) error
}

// CreateSystemPromptInput holds fields for creating a SystemPrompt.
type CreateSystemPromptInput struct {
	Scope     string  `json:"scope"`
	Stage     *string `json:"stage"`
	Content   string  `json:"content"`
	Priority  int     `json:"priority"`
	CreatedBy *string `json:"created_by"`
}

// UpdateSystemPromptInput holds mutable fields for SystemPrompt updates.
type UpdateSystemPromptInput struct {
	Content  *string `json:"content"`
	Priority *int    `json:"priority"`
	Stage    *string `json:"stage"`
}

type entSystemPromptRepo struct{ client *ent.Client }

// NewSystemPromptRepo returns a SystemPromptRepo backed by ent.
func NewSystemPromptRepo(client *ent.Client) SystemPromptRepo {
	return &entSystemPromptRepo{client: client}
}

func (r *entSystemPromptRepo) Create(ctx context.Context, in CreateSystemPromptInput) (*ent.SystemPrompt, error) {
	scope := in.Scope
	if scope == "" {
		scope = "global"
	}
	q := r.client.SystemPrompt.Create().
		SetID(uuid.New().String()).
		SetScope(scope).
		SetContent(in.Content).
		SetPriority(in.Priority).
		SetCreatedAt(time.Now()).
		SetUpdatedAt(time.Now())
	if in.Stage != nil && *in.Stage != "" {
		q = q.SetStage(*in.Stage)
	}
	if in.CreatedBy != nil {
		q = q.SetCreatedBy(*in.CreatedBy)
	}
	sp, err := q.Save(ctx)
	if err != nil {
		return nil, fmt.Errorf("systemPrompt.Create: %w", err)
	}
	return sp, nil
}

func (r *entSystemPromptRepo) List(ctx context.Context) ([]*ent.SystemPrompt, error) {
	rows, err := r.client.SystemPrompt.Query().
		Order(systemprompt.ByPriority(sql.OrderDesc())).
		All(ctx)
	if err != nil {
		return nil, fmt.Errorf("systemPrompt.List: %w", err)
	}
	return rows, nil
}

func (r *entSystemPromptRepo) ListForStage(ctx context.Context, stage string) ([]*ent.SystemPrompt, error) {
	rows, err := r.client.SystemPrompt.Query().
		Where(
			systemprompt.ScopeEQ("global"),
			systemprompt.Or(
				systemprompt.StageIsNil(),
				systemprompt.StageEQ(stage),
			),
		).
		Order(systemprompt.ByPriority(sql.OrderDesc())).
		All(ctx)
	if err != nil {
		return nil, fmt.Errorf("systemPrompt.ListForStage: %w", err)
	}
	return rows, nil
}

func (r *entSystemPromptRepo) Update(ctx context.Context, id string, in UpdateSystemPromptInput) (*ent.SystemPrompt, error) {
	q := r.client.SystemPrompt.UpdateOneID(id).SetUpdatedAt(time.Now())
	if in.Content != nil {
		q = q.SetContent(*in.Content)
	}
	if in.Priority != nil {
		q = q.SetPriority(*in.Priority)
	}
	if in.Stage != nil {
		if *in.Stage == "" {
			q = q.ClearStage()
		} else {
			q = q.SetStage(*in.Stage)
		}
	}
	sp, err := q.Save(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return nil, fmt.Errorf("%w: system prompt %s", domainerr.ErrNotFound, id)
		}
		return nil, fmt.Errorf("systemPrompt.Update(%s): %w", id, err)
	}
	return sp, nil
}

func (r *entSystemPromptRepo) Delete(ctx context.Context, id string) error {
	if err := r.client.SystemPrompt.DeleteOneID(id).Exec(ctx); err != nil {
		if ent.IsNotFound(err) {
			return fmt.Errorf("%w: system prompt %s", domainerr.ErrNotFound, id)
		}
		return fmt.Errorf("systemPrompt.Delete(%s): %w", id, err)
	}
	return nil
}
