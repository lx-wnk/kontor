package repo

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/lx-wnk/kontor/server/internal/db/ent"
	"github.com/lx-wnk/kontor/server/internal/db/ent/skill"
)

// UpsertSkillInput is the named input for SkillRepo.Upsert.
type UpsertSkillInput struct {
	ResourceID  string
	Description string
	Body        string
}

// SkillRepo persists the content half of a skill resource.
type SkillRepo interface {
	Upsert(ctx context.Context, in UpsertSkillInput) (*ent.Skill, error)
	// GetByResource returns an error when the resource has no content row.
	// Unlike MaterializationRepo.Get, absence here is a real fault: a skill
	// resource with nothing to render cannot be materialized at all.
	GetByResource(ctx context.Context, resourceID string) (*ent.Skill, error)
}

type entSkillRepo struct{ client *ent.Client }

// NewSkillRepo returns a SkillRepo backed by the ent client.
func NewSkillRepo(client *ent.Client) SkillRepo { return &entSkillRepo{client: client} }

func (r *entSkillRepo) Upsert(ctx context.Context, in UpsertSkillInput) (*ent.Skill, error) {
	if in.ResourceID == "" {
		return nil, fmt.Errorf("skill.Upsert: resource id is required")
	}
	existing, err := r.client.Skill.Query().Where(skill.ResourceID(in.ResourceID)).Only(ctx)
	switch {
	case err == nil:
		row, uerr := existing.Update().
			SetDescription(in.Description).
			SetBody(in.Body).
			Save(ctx)
		if uerr != nil {
			return nil, fmt.Errorf("skill.Upsert update: %w", uerr)
		}
		return row, nil
	case ent.IsNotFound(err):
		row, cerr := r.client.Skill.Create().
			SetID(uuid.NewString()).
			SetResourceID(in.ResourceID).
			SetDescription(in.Description).
			SetBody(in.Body).
			Save(ctx)
		if cerr != nil {
			return nil, fmt.Errorf("skill.Upsert insert: %w", cerr)
		}
		return row, nil
	default:
		return nil, fmt.Errorf("skill.Upsert query: %w", err)
	}
}

func (r *entSkillRepo) GetByResource(ctx context.Context, resourceID string) (*ent.Skill, error) {
	row, err := r.client.Skill.Query().Where(skill.ResourceID(resourceID)).Only(ctx)
	if err != nil {
		return nil, fmt.Errorf("skill.GetByResource %s: %w", resourceID, err)
	}
	return row, nil
}
