package pipeline

import (
	"context"
	"log/slog"
	"time"

	"github.com/lx-wnk/kontor/server/internal/db/ent"
	"github.com/lx-wnk/kontor/server/internal/db/repo"
)

// terminalStageRunStatuses are the statuses after which a stage run's agent can
// make no further calls. awaiting_user is deliberately absent: such a run is
// resumable and its agent may still be alive; expires_at caps it instead.
var terminalStageRunStatuses = map[string]bool{
	"done": true, "failed": true, "cancelled": true,
}

// stageRunService is the orchestrator's seam onto repo.StageRunRepo — all
// stage_run persistence goes through here instead of opts.StageRunRepo directly.
type stageRunService struct {
	repo repo.StageRunRepo
	// revoke invalidates the MCP credentials issued for a stage run. Nil in
	// tests and in any composition that wires no issuer, which disables
	// revocation without changing any call site.
	revoke func(ctx context.Context, stageRunID string) error
	// releaseSpawn removes the on-disk artifacts of a stage run's spawn — the
	// temp --mcp-config file holding its bearer token above all. Nil disables
	// the removal the same way a nil revoke disables revocation.
	releaseSpawn func(stageRunID string)
}

// newStageRunService constructs a stageRunService backed by r, releasing a
// stage run's credentials and spawn artifacts on every terminal status write.
// revoke and releaseSpawn may each be nil.
func newStageRunService(r repo.StageRunRepo, revoke func(context.Context, string) error, releaseSpawn func(string)) *stageRunService {
	return &stageRunService{repo: r, revoke: revoke, releaseSpawn: releaseSpawn}
}

func (s *stageRunService) GetByID(ctx context.Context, id string) (*ent.StageRun, error) {
	return s.repo.GetByID(ctx, id)
}

func (s *stageRunService) Update(ctx context.Context, id string, input repo.UpdateStageRunInput) (*ent.StageRun, error) {
	run, err := s.repo.Update(ctx, id, input)
	if err != nil {
		return nil, err
	}
	// Revoke after the write, and never let its failure surface: the status
	// change has already happened, so returning an error here would report
	// that nothing happened when something did. expires_at is the second net.
	if input.Status != nil && terminalStageRunStatuses[*input.Status] {
		s.releaseStageRun(ctx, id)
	}
	return run, nil
}

// releaseStageRun hands back everything issued to id's agent: its MCP
// credentials via revoke, and the on-disk spawn artifacts via releaseSpawn.
// Both failures are logged, never returned — the status change has already
// happened, so an error here would report that nothing happened when something
// did. expires_at is the second net under revocation.
//
// Shared by Update above and by applyTransitionWrites' post-commit hooks
// (transitions.go). Call it wherever a stage run's agent is known to be gone,
// terminal status or not.
func (s *stageRunService) releaseStageRun(ctx context.Context, id string) {
	if s.revoke != nil {
		if err := s.revoke(ctx, id); err != nil {
			slog.Warn("pipeline: revoking stage-run credentials failed", "stageRun", id, "err", err)
		}
	}
	if s.releaseSpawn != nil {
		s.releaseSpawn(id)
	}
}

func (s *stageRunService) ListByStatus(ctx context.Context, statuses ...string) ([]*ent.StageRun, error) {
	return s.repo.ListByStatus(ctx, statuses...)
}

func (s *stageRunService) GetLatestByTaskAndStage(ctx context.Context, taskID, stage string) (*ent.StageRun, error) {
	return s.repo.GetLatestByTaskAndStage(ctx, taskID, stage)
}

func (s *stageRunService) Create(ctx context.Context, input repo.CreateStageRunInput) (*ent.StageRun, error) {
	return s.repo.Create(ctx, input)
}

func (s *stageRunService) ListForTask(ctx context.Context, taskID string) ([]*ent.StageRun, error) {
	return s.repo.ListForTask(ctx, taskID)
}

func (s *stageRunService) GetByTaskStageIteration(ctx context.Context, taskID, stage string, iteration int) (*ent.StageRun, error) {
	return s.repo.GetByTaskStageIteration(ctx, taskID, stage, iteration)
}

func (s *stageRunService) SumCompletedCostCents(ctx context.Context, taskID string) (int64, error) {
	return s.repo.SumCompletedCostCents(ctx, taskID)
}

func (s *stageRunService) SumCompletedTokens(ctx context.Context, taskID string) (int64, error) {
	return s.repo.SumCompletedTokens(ctx, taskID)
}

// MarkPending resets a recovered stage_run to pending with its PID cleared,
// so the tick-loop picker respawns it. started_at is moved to now: the row it
// recovers carries the dead process's timestamp, and sweepOrphanRuns case 3
// fails any pending run whose started_at is older than pendingStaleDuration —
// so leaving the old value re-queues the run straight into the stale-pending
// reaper. Clearing started_at instead is not an option: RequeueForUser
// documents that a pending run with a nil started_at never spawns and is never
// reaped, which trades a premature kill for a permanent zombie.
func (s *stageRunService) MarkPending(ctx context.Context, id string) (*ent.StageRun, error) {
	now := time.Now()
	return s.repo.Update(ctx, id, repo.UpdateStageRunInput{Status: strPtr("pending"), PIDClear: true, StartedAt: &now})
}

// MarkFailed marks a stage_run failed with an end timestamp and the given
// output, routed through Update so the terminal write also revokes the run's
// MCP credentials.
func (s *stageRunService) MarkFailed(ctx context.Context, id string, output map[string]any) (*ent.StageRun, error) {
	now := time.Now()
	return s.Update(ctx, id, repo.UpdateStageRunInput{Status: strPtr("failed"), EndedAt: &now, Output: output})
}
