package repo

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/lx-wnk/kontor/server/internal/db/ent"
	"github.com/lx-wnk/kontor/server/internal/db/ent/scratchpad"
)

type ScratchpadRepo interface {
	Write(ctx context.Context, namespace, key, value, updatedByTaskID string) error
	Read(ctx context.Context, namespace, key string) (*ent.Scratchpad, error)
	List(ctx context.Context, namespace string) ([]*ent.Scratchpad, error)
}

type entScratchpadRepo struct{ client *ent.Client }

func NewScratchpadRepo(client *ent.Client) ScratchpadRepo { return &entScratchpadRepo{client: client} }

func (r *entScratchpadRepo) Write(ctx context.Context, namespace, key, value, updatedBy string) error {
	err := r.client.Scratchpad.Create().
		SetID(uuid.New().String()).
		SetNamespace(namespace).SetKey(key).SetValue(value).SetUpdatedByTaskID(updatedBy).
		OnConflictColumns(scratchpad.FieldNamespace, scratchpad.FieldKey).
		UpdateNewValues().
		Exec(ctx)
	if err != nil {
		return fmt.Errorf("scratchpad.Write: %w", err)
	}
	return nil
}

func (r *entScratchpadRepo) Read(ctx context.Context, namespace, key string) (*ent.Scratchpad, error) {
	row, err := r.client.Scratchpad.Query().Where(scratchpad.Namespace(namespace), scratchpad.Key(key)).Only(ctx)
	if ent.IsNotFound(err) {
		return nil, nil
	}
	return row, err
}

func (r *entScratchpadRepo) List(ctx context.Context, namespace string) ([]*ent.Scratchpad, error) {
	return r.client.Scratchpad.Query().Where(scratchpad.Namespace(namespace)).Order(ent.Asc(scratchpad.FieldKey)).All(ctx)
}
