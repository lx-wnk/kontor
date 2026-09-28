package repo

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/lx-wnk/kontor/server/internal/db/ent"
	"github.com/lx-wnk/kontor/server/internal/db/ent/checkpoint"
)

// CheckpointRepo persists per-turn worktree snapshots.
type CheckpointRepo interface {
	Create(ctx context.Context, in CreateCheckpointInput) (*ent.Checkpoint, error)
	GetByID(ctx context.Context, id string) (*ent.Checkpoint, error)
	GetLatestByTask(ctx context.Context, taskID string) (*ent.Checkpoint, error)
	ListByTask(ctx context.Context, taskID string) ([]*ent.Checkpoint, error)
	CountByTask(ctx context.Context, taskID string) (int, error)
	PruneOldest(ctx context.Context, taskID string, keep int) ([]int, error)
	DeleteByTask(ctx context.Context, taskID string) error
}

type CreateCheckpointInput struct {
	TaskID       string
	StageRunID   *string
	Seq          int
	CommitSHA    string
	TreeSHA      string
	FilesChanged int
	PreRevert    bool
}

type entCheckpointRepo struct{ client *ent.Client }

func NewCheckpointRepo(client *ent.Client) CheckpointRepo {
	return &entCheckpointRepo{client: client}
}

func (r *entCheckpointRepo) Create(ctx context.Context, in CreateCheckpointInput) (*ent.Checkpoint, error) {
	q := r.client.Checkpoint.Create().
		SetID(uuid.New().String()).
		SetTaskID(in.TaskID).
		SetSeq(in.Seq).
		SetCommitSha(in.CommitSHA).
		SetTreeSha(in.TreeSHA).
		SetFilesChanged(in.FilesChanged).
		SetPreRevert(in.PreRevert)
	if in.StageRunID != nil {
		q = q.SetStageRunID(*in.StageRunID)
	}
	cp, err := q.Save(ctx)
	if err != nil {
		return nil, fmt.Errorf("checkpoint.Create: %w", err)
	}
	return cp, nil
}

func (r *entCheckpointRepo) GetByID(ctx context.Context, id string) (*ent.Checkpoint, error) {
	cp, err := r.client.Checkpoint.Get(ctx, id)
	if ent.IsNotFound(err) {
		return nil, nil
	}
	return cp, err
}

func (r *entCheckpointRepo) GetLatestByTask(ctx context.Context, taskID string) (*ent.Checkpoint, error) {
	cp, err := r.client.Checkpoint.Query().
		Where(checkpoint.TaskID(taskID)).
		Order(ent.Desc(checkpoint.FieldSeq)).
		First(ctx)
	if ent.IsNotFound(err) {
		return nil, nil
	}
	return cp, err
}

func (r *entCheckpointRepo) ListByTask(ctx context.Context, taskID string) ([]*ent.Checkpoint, error) {
	return r.client.Checkpoint.Query().
		Where(checkpoint.TaskID(taskID)).
		Order(ent.Desc(checkpoint.FieldSeq)).
		All(ctx)
}

func (r *entCheckpointRepo) CountByTask(ctx context.Context, taskID string) (int, error) {
	return r.client.Checkpoint.Query().Where(checkpoint.TaskID(taskID)).Count(ctx)
}

// PruneOldest keeps the newest `keep` checkpoints for taskID and deletes the rest,
// returning the seqs of the deleted rows so the caller can drop their git refs.
func (r *entCheckpointRepo) PruneOldest(ctx context.Context, taskID string, keep int) ([]int, error) {
	all, err := r.client.Checkpoint.Query().
		Where(checkpoint.TaskID(taskID)).
		Order(ent.Asc(checkpoint.FieldSeq)).
		All(ctx)
	if err != nil || len(all) <= keep {
		return nil, err
	}
	toDelete := all[:len(all)-keep]
	ids := make([]string, len(toDelete))
	seqs := make([]int, len(toDelete))
	for i, cp := range toDelete {
		ids[i] = cp.ID
		seqs[i] = cp.Seq
	}
	if _, err := r.client.Checkpoint.Delete().Where(checkpoint.IDIn(ids...)).Exec(ctx); err != nil {
		return nil, err
	}
	return seqs, nil
}

func (r *entCheckpointRepo) DeleteByTask(ctx context.Context, taskID string) error {
	_, err := r.client.Checkpoint.Delete().Where(checkpoint.TaskID(taskID)).Exec(ctx)
	return err
}
