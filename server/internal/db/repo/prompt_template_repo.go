package repo

import (
	"context"
	"fmt"

	"github.com/google/uuid"

	"github.com/lx-wnk/kontor/server/internal/db/ent"
	"github.com/lx-wnk/kontor/server/internal/db/ent/prompttemplate"
)

// PromptTemplateRepo manages prompt template persistence.
type PromptTemplateRepo interface {
	Create(ctx context.Context, name, body string) (*ent.PromptTemplate, error)
	List(ctx context.Context) ([]*ent.PromptTemplate, error)
	Delete(ctx context.Context, id string) error
}

type entPromptTemplateRepo struct{ client *ent.Client }

// NewPromptTemplateRepo returns a PromptTemplateRepo backed by the given ent client.
func NewPromptTemplateRepo(client *ent.Client) PromptTemplateRepo {
	return &entPromptTemplateRepo{client: client}
}

func (r *entPromptTemplateRepo) Create(ctx context.Context, name, body string) (*ent.PromptTemplate, error) {
	tpl, err := r.client.PromptTemplate.Create().
		SetID(uuid.New().String()).
		SetName(name).
		SetBody(body).
		Save(ctx)
	if err != nil {
		return nil, fmt.Errorf("prompt_template.Create: %w", err)
	}
	return tpl, nil
}

func (r *entPromptTemplateRepo) List(ctx context.Context) ([]*ent.PromptTemplate, error) {
	tpls, err := r.client.PromptTemplate.Query().
		Order(prompttemplate.ByCreatedAt()).
		All(ctx)
	if err != nil {
		return nil, fmt.Errorf("prompt_template.List: %w", err)
	}
	return tpls, nil
}

func (r *entPromptTemplateRepo) Delete(ctx context.Context, id string) error {
	if err := r.client.PromptTemplate.DeleteOneID(id).Exec(ctx); err != nil {
		return fmt.Errorf("prompt_template.Delete: %w", err)
	}
	return nil
}
