package repo

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/lx-wnk/kontor/server/internal/db"
	"github.com/lx-wnk/kontor/server/internal/db/ent"
	"github.com/lx-wnk/kontor/server/internal/db/ent/attentionmark"
)

const attentionBaselineID = "attention"

// AttentionMarkRepo manages attention marks and the attention baseline.
type AttentionMarkRepo interface {
	// List returns the marks ordered by item_key.
	List(ctx context.Context) ([]*ent.AttentionMark, error)
	// Mark raises the mark for key to seenThrough. changed is false when the
	// stored mark is already at or past seenThrough.
	Mark(ctx context.Context, key string, seenThrough time.Time) (mark *ent.AttentionMark, changed bool, err error)
	// Baseline returns when marking started, creating the row on first call.
	Baseline(ctx context.Context) (time.Time, error)
}

type entAttentionMarkRepo struct {
	client      *ent.Client
	writeClient db.WriteClient
}

// NewAttentionMarkRepo returns an AttentionMarkRepo backed by the ent client.
// writeClient must be db.DBBundle.WriteClient — see WithWriteTx's doc comment
// for why Mark and Baseline need it.
func NewAttentionMarkRepo(client *ent.Client, writeClient db.WriteClient) AttentionMarkRepo {
	return &entAttentionMarkRepo{client: client, writeClient: writeClient}
}

func (r *entAttentionMarkRepo) List(ctx context.Context) ([]*ent.AttentionMark, error) {
	marks, err := r.client.AttentionMark.Query().
		Order(ent.Asc(attentionmark.FieldItemKey)).
		All(ctx)
	if err != nil {
		return nil, fmt.Errorf("attentionmark.List: %w", err)
	}
	return marks, nil
}

func (r *entAttentionMarkRepo) Mark(ctx context.Context, key string, seenThrough time.Time) (*ent.AttentionMark, bool, error) {
	var (
		mark    *ent.AttentionMark
		changed bool
	)
	err := WithWriteTx(ctx, r.writeClient, func(tx *ent.Tx) error {
		existing, err := tx.AttentionMark.Query().Where(attentionmark.ItemKey(key)).Only(ctx)
		switch {
		case err == nil:
			if !seenThrough.After(existing.SeenThrough) {
				mark = existing
				return nil
			}
		case !ent.IsNotFound(err):
			return err
		}
		id, err := tx.AttentionMark.Create().
			SetID(uuid.New().String()).
			SetItemKey(key).
			SetSeenThrough(seenThrough).
			OnConflictColumns(attentionmark.FieldItemKey).
			UpdateNewValues().
			ID(ctx)
		if err != nil {
			return err
		}
		mark, err = tx.AttentionMark.Query().Where(attentionmark.ItemKey(key)).Only(ctx)
		if err != nil {
			return fmt.Errorf("reload %s: %w", id, err)
		}
		changed = true
		return nil
	})
	if err != nil {
		return nil, false, fmt.Errorf("attentionmark.Mark: %w", err)
	}
	return mark, changed, nil
}

func (r *entAttentionMarkRepo) Baseline(ctx context.Context) (time.Time, error) {
	row, err := r.client.AttentionBaseline.Get(ctx, attentionBaselineID)
	if err == nil {
		return row.StartedAt, nil
	}
	if !ent.IsNotFound(err) {
		return time.Time{}, fmt.Errorf("attentionbaseline.Baseline: %w", err)
	}
	var startedAt time.Time
	err = WithWriteTx(ctx, r.writeClient, func(tx *ent.Tx) error {
		row, err := tx.AttentionBaseline.Get(ctx, attentionBaselineID)
		if ent.IsNotFound(err) {
			row, err = tx.AttentionBaseline.Create().
				SetID(attentionBaselineID).
				SetStartedAt(time.Now().UTC()).
				Save(ctx)
		}
		if err != nil {
			return err
		}
		startedAt = row.StartedAt
		return nil
	})
	if err != nil {
		return time.Time{}, fmt.Errorf("attentionbaseline.Baseline: %w", err)
	}
	return startedAt, nil
}
