package repo

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/lx-wnk/kontor/server/internal/db/ent"
	"github.com/lx-wnk/kontor/server/internal/db/ent/materialization"
)

// RecordMaterializationInput is the named input for MaterializationRepo.Record.
// A ContentHash of "" means the row records something this node did not write.
type RecordMaterializationInput struct {
	ResourceID  string
	TargetKey   string
	Path        string
	ContentHash string
	Outcome     string
}

// MaterializationRepo persists what was produced where.
type MaterializationRepo interface {
	// Get returns (nil, nil) when the target has never been recorded. Absence
	// is the ordinary first state of every target, not a failure — and forcing
	// each call site to tell ent.IsNotFound from a storage fault is how an
	// outage ends up classified as "foreign", the one outcome that stops
	// writing permanently.
	Get(ctx context.Context, resourceID, targetKey string) (*ent.Materialization, error)
	Record(ctx context.Context, in RecordMaterializationInput) (*ent.Materialization, error)
	ListForResource(ctx context.Context, resourceID string) ([]*ent.Materialization, error)
}

type entMaterializationRepo struct{ client *ent.Client }

// NewMaterializationRepo returns a MaterializationRepo backed by the ent client.
func NewMaterializationRepo(client *ent.Client) MaterializationRepo {
	return &entMaterializationRepo{client: client}
}

func (r *entMaterializationRepo) Get(ctx context.Context, resourceID, targetKey string) (*ent.Materialization, error) {
	row, err := r.client.Materialization.Query().
		Where(materialization.ResourceID(resourceID), materialization.TargetKey(targetKey)).
		Only(ctx)
	if ent.IsNotFound(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("materialization.Get %s/%s: %w", resourceID, targetKey, err)
	}
	return row, nil
}

func (r *entMaterializationRepo) Record(ctx context.Context, in RecordMaterializationInput) (*ent.Materialization, error) {
	if in.ResourceID == "" || in.TargetKey == "" {
		return nil, fmt.Errorf("materialization.Record: resource id and target key are required")
	}
	existing, err := r.Get(ctx, in.ResourceID, in.TargetKey)
	if err != nil {
		return nil, err
	}
	if existing != nil {
		row, uerr := existing.Update().
			SetPath(in.Path).
			SetContentHash(in.ContentHash).
			SetOutcome(in.Outcome).
			Save(ctx)
		if uerr != nil {
			return nil, fmt.Errorf("materialization.Record update: %w", uerr)
		}
		return row, nil
	}
	row, cerr := r.client.Materialization.Create().
		SetID(uuid.NewString()).
		SetResourceID(in.ResourceID).
		SetTargetKey(in.TargetKey).
		SetPath(in.Path).
		SetContentHash(in.ContentHash).
		SetOutcome(in.Outcome).
		Save(ctx)
	if cerr != nil {
		return nil, fmt.Errorf("materialization.Record insert: %w", cerr)
	}
	return row, nil
}

func (r *entMaterializationRepo) ListForResource(ctx context.Context, resourceID string) ([]*ent.Materialization, error) {
	rows, err := r.client.Materialization.Query().
		Where(materialization.ResourceID(resourceID)).
		Order(ent.Asc(materialization.FieldTargetKey)).
		All(ctx)
	if err != nil {
		return nil, fmt.Errorf("materialization.ListForResource %s: %w", resourceID, err)
	}
	return rows, nil
}
