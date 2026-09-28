package checkpoint

import (
	"context"
	"fmt"
	"log/slog"
	"sync"

	"github.com/lx-wnk/kontor/server/internal/apierr"
	"github.com/lx-wnk/kontor/server/internal/db/ent"
	"github.com/lx-wnk/kontor/server/internal/db/repo"
	"github.com/lx-wnk/kontor/server/internal/sse"
)

const defaultMaxPerTask = 50

// KillFn kills the live agent for taskID (see orchestrator.KillRunningStage).
// May be nil in tests / no-DB paths.
type KillFn func(ctx context.Context, taskID string) error

// ParkFn parks a task as awaiting_user with the given reason. May be nil in tests.
type ParkFn func(ctx context.Context, taskID, reason string) error

// ServiceOptions configures a Service.
type ServiceOptions struct {
	Repo        repo.CheckpointRepo
	MaxPerTask  int
	Broadcaster *sse.TaskBroadcaster // nil → no SSE
	KillFn      KillFn
	ParkFn      ParkFn
}

// Service orchestrates snapshot+DB+SSE and the revert flow. It serializes
// snapshots and reverts per task.
type Service struct {
	opts    ServiceOptions
	states  sync.Map // taskID → *taskState
	reverts sync.Map // taskID → *sync.Mutex
}

// taskState tracks the last snapshot of a task for identical-tree skipping and
// checkpoint-commit chaining.
type taskState struct {
	mu         sync.Mutex
	seeded     bool
	lastTree   string
	lastSeq    int
	lastCommit string
	// restoring blocks debounce snapshots while a revert rewrites the worktree,
	// so a snapshot can't capture a half-restored tree.
	restoring bool
}

// NewService creates a Service.
func NewService(opts ServiceOptions) *Service {
	if opts.MaxPerTask <= 0 {
		opts.MaxPerTask = defaultMaxPerTask
	}
	return &Service{opts: opts}
}

func (s *Service) state(taskID string) *taskState {
	v, _ := s.states.LoadOrStore(taskID, &taskState{})
	return v.(*taskState)
}

func (s *Service) revertMu(taskID string) *sync.Mutex {
	v, _ := s.reverts.LoadOrStore(taskID, &sync.Mutex{})
	return v.(*sync.Mutex)
}

// seedFromDB lazily initialises the in-memory snapshot cursor from the latest
// persisted checkpoint, so seq numbering survives a watcher restart. Caller holds st.mu.
func (s *Service) seedFromDB(ctx context.Context, taskID string, st *taskState) {
	if st.seeded {
		return
	}
	st.seeded = true
	if latest, err := s.opts.Repo.GetLatestByTask(ctx, taskID); err == nil && latest != nil {
		st.lastSeq = latest.Seq
		st.lastTree = latest.TreeSha
		st.lastCommit = latest.CommitSha
	}
}

// TakeSnapshot captures the current worktree state and persists a checkpoint row.
// Best-effort: any error is logged as Warn and the task continues unaffected.
func (s *Service) TakeSnapshot(ctx context.Context, taskID, worktreePath string) error {
	st := s.state(taskID)
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.restoring {
		return nil
	}
	s.seedFromDB(ctx, taskID, st)

	cp, err := s.snapshotLocked(ctx, taskID, worktreePath, st, false)
	if err != nil || cp == nil {
		return err
	}

	if n, _ := s.opts.Repo.CountByTask(ctx, taskID); n > s.opts.MaxPerTask {
		prunedSeqs, err := s.opts.Repo.PruneOldest(ctx, taskID, s.opts.MaxPerTask)
		if err != nil {
			slog.Warn("checkpoint: prune failed", "taskID", taskID, "err", err)
		} else if len(prunedSeqs) > 0 {
			if err := DeleteCheckpointRefSeqs(ctx, worktreePath, taskID, prunedSeqs); err != nil {
				slog.Warn("checkpoint: prune refs failed", "taskID", taskID, "err", err)
			}
		}
	}
	return nil
}

// snapshotLocked captures one checkpoint and persists it. Caller holds st.mu.
// Returns (nil, nil) when the tree is unchanged (skipped) or on a best-effort
// failure that should not abort the caller.
func (s *Service) snapshotLocked(ctx context.Context, taskID, worktreePath string, st *taskState, preRevert bool) (*ent.Checkpoint, error) {
	nextSeq := st.lastSeq + 1
	res, err := SnapshotWithParent(ctx, worktreePath, taskID, nextSeq, st.lastTree, st.lastCommit)
	if err != nil {
		slog.Warn("checkpoint: snapshot failed", "taskID", taskID, "err", err)
		return nil, nil
	}
	if res.Skipped {
		return nil, nil
	}

	cp, err := s.opts.Repo.Create(ctx, repo.CreateCheckpointInput{
		TaskID:       taskID,
		Seq:          nextSeq,
		CommitSHA:    res.CommitSHA,
		TreeSHA:      res.TreeSHA,
		FilesChanged: res.FilesChanged,
		PreRevert:    preRevert,
	})
	if err != nil {
		slog.Warn("checkpoint: persist row failed", "taskID", taskID, "err", err)
		return nil, nil
	}

	st.lastSeq = nextSeq
	st.lastTree = res.TreeSHA
	st.lastCommit = res.CommitSHA

	if s.opts.Broadcaster != nil {
		s.opts.Broadcaster.Broadcast(sse.TaskEvent{
			Type:    "checkpoint_added",
			TaskID:  taskID,
			Payload: toView(cp),
		})
	}
	return cp, nil
}

// Revert reverts the task's worktree to the given checkpoint and parks the task.
// It kills the live agent first, snapshots the current state as a pre-revert
// checkpoint (so the revert is itself undoable), restores the worktree, then parks.
func (s *Service) Revert(ctx context.Context, taskID, checkpointID, worktreePath string) error {
	mu := s.revertMu(taskID)
	mu.Lock()
	defer mu.Unlock()

	cp, err := s.opts.Repo.GetByID(ctx, checkpointID)
	if err != nil || cp == nil {
		return fmt.Errorf("revert: checkpoint %s not found: %w", checkpointID, apierr.ErrNotFound)
	}
	// Guard against reverting task A's worktree to task B's checkpoint.
	if cp.TaskID != taskID {
		return fmt.Errorf("revert: checkpoint %s not found for task %s: %w", checkpointID, taskID, apierr.ErrNotFound)
	}

	if s.opts.KillFn != nil {
		if err := s.opts.KillFn(ctx, taskID); err != nil {
			return fmt.Errorf("revert: kill agent: %w", err)
		}
	}

	// Pre-revert snapshot so the revert can itself be undone, then block any
	// debounce snapshot until the restore finishes rewriting the worktree.
	st := s.state(taskID)
	st.mu.Lock()
	s.seedFromDB(ctx, taskID, st)
	_, _ = s.snapshotLocked(ctx, taskID, worktreePath, st, true)
	st.restoring = true
	st.mu.Unlock()
	defer func() {
		st.mu.Lock()
		st.restoring = false
		st.mu.Unlock()
	}()

	if err := Restore(ctx, worktreePath, worktreePath, cp.TreeSha); err != nil {
		return fmt.Errorf("revert: restore: %w", err)
	}

	if s.opts.ParkFn != nil {
		reason := fmt.Sprintf("reverted to checkpoint #%d", cp.Seq)
		if err := s.opts.ParkFn(ctx, taskID, reason); err != nil {
			slog.Warn("revert: park task failed", "taskID", taskID, "err", err)
		}
	}
	return nil
}

// List returns the task's checkpoints, newest first.
func (s *Service) List(ctx context.Context, taskID string) ([]CheckpointView, error) {
	rows, err := s.opts.Repo.ListByTask(ctx, taskID)
	if err != nil {
		return nil, err
	}
	views := make([]CheckpointView, len(rows))
	for i, r := range rows {
		views[i] = toView(r)
	}
	return views, nil
}

// PruneRefs deletes all refs/checkpoints/<taskID>/* from the repo at repoDir.
func (s *Service) PruneRefs(ctx context.Context, taskID, repoDir string) error {
	return DeleteCheckpointRefs(ctx, repoDir, taskID)
}

// CheckpointView is the JSON shape returned by the API.
type CheckpointView struct {
	ID           string `json:"id"`
	TaskID       string `json:"taskId"`
	Seq          int    `json:"seq"`
	CommitSha    string `json:"commitSha"`
	FilesChanged int    `json:"filesChanged"`
	PreRevert    bool   `json:"preRevert"`
	CreatedAt    string `json:"createdAt"`
}

func toView(cp *ent.Checkpoint) CheckpointView {
	return CheckpointView{
		ID:           cp.ID,
		TaskID:       cp.TaskID,
		Seq:          cp.Seq,
		CommitSha:    cp.CommitSha,
		FilesChanged: cp.FilesChanged,
		PreRevert:    cp.PreRevert,
		CreatedAt:    cp.CreatedAt.UTC().Format("2006-01-02T15:04:05Z"),
	}
}
