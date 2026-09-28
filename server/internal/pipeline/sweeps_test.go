package pipeline_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/lx-wnk/kontor/server/internal/db"
	"github.com/lx-wnk/kontor/server/internal/db/repo"
	"github.com/lx-wnk/kontor/server/internal/pipeline"
)

// makeOrchestratorFull returns an orchestrator plus all repos for sweep tests.
func makeOrchestratorFull(t *testing.T) (*pipeline.PipelineOrchestrator, repo.TaskRepo, repo.StageRunRepo) {
	t.Helper()
	bundle, err := db.Open(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { _ = bundle.Client.Close() })

	taskRepo := repo.NewTaskRepo(bundle.Client)
	srRepo := repo.NewStageRunRepo(bundle.Client)
	permRepo := repo.NewPermissionRepo(bundle.Client)
	auditRepo := repo.NewAuditEventRepo(bundle.Client)
	cfgRepo := repo.NewPipelineConfigRepo(bundle.Client)

	orch, err := pipeline.NewOrchestrator(pipeline.OrchestratorOptions{
		TaskRepo:       taskRepo,
		StageRunRepo:   srRepo,
		PermissionRepo: permRepo,
		AuditRepo:      auditRepo,
		ConfigRepo:     cfgRepo,
	})
	require.NoError(t, err)
	return orch, taskRepo, srRepo
}

// makeRequeuedRun creates a task + stage_run in "requeued" status with the given nextRetryAt.
func makeRequeuedRun(t *testing.T, ctx context.Context, taskRepo repo.TaskRepo, srRepo repo.StageRunRepo, nextRetryAt time.Time) (*repo.CreateTaskInput, string) {
	t.Helper()
	task, err := taskRepo.Create(ctx, repo.CreateTaskInput{
		Slug:          "requeue-sweep-test",
		Title:         "Requeue Sweep Test",
		Cwd:           "/tmp",
		CurrentStage:  "implementation",
		Priority:      "medium",
		MaxIterations: 3,
	})
	require.NoError(t, err)

	sr, err := srRepo.Create(ctx, repo.CreateStageRunInput{
		TaskID:      task.ID,
		Stage:       "implementation",
		Iteration:   0,
		SessionName: "requeue-sweep-impl-0",
	})
	require.NoError(t, err)

	retryCount := 1
	startedAt := time.Now().Add(-5 * time.Minute)
	deadPID := 99999
	sr, err = srRepo.Update(ctx, sr.ID, repo.UpdateStageRunInput{
		Status:      strPtr("requeued"),
		PID:         &deadPID,
		StartedAt:   &startedAt,
		RetryCount:  &retryCount,
		NextRetryAt: &nextRetryAt,
	})
	require.NoError(t, err)

	_ = sr
	return nil, sr.ID
}

func TestSweepRequeueableRuns_PastCooldown_PromotesToPending(t *testing.T) {
	ctx := context.Background()
	orch, taskRepo, srRepo := makeOrchestratorFull(t)

	past := time.Now().Add(-1 * time.Minute)
	_, runID := makeRequeuedRun(t, ctx, taskRepo, srRepo, past)

	before, err := srRepo.GetByID(ctx, runID)
	require.NoError(t, err)
	require.Equal(t, 1, before.RetryCount)

	err = orch.SweepRequeueableRunsForTest(ctx)
	require.NoError(t, err)

	updated, err := srRepo.GetByID(ctx, runID)
	require.NoError(t, err)
	require.Equal(t, "pending", updated.Status)
	require.Nil(t, updated.Pid, "PID must be cleared")
	require.Nil(t, updated.StartedAt, "started_at must be cleared")
	require.Nil(t, updated.NextRetryAt, "next_retry_at must be cleared")
	require.Equal(t, 1, updated.RetryCount, "retry_count must be preserved")
}

func TestSweepRequeueableRuns_FutureCooldown_Unchanged(t *testing.T) {
	ctx := context.Background()
	orch, taskRepo, srRepo := makeOrchestratorFull(t)

	future := time.Now().Add(10 * time.Minute)
	_, runID := makeRequeuedRun(t, ctx, taskRepo, srRepo, future)

	err := orch.SweepRequeueableRunsForTest(ctx)
	require.NoError(t, err)

	updated, err := srRepo.GetByID(ctx, runID)
	require.NoError(t, err)
	require.Equal(t, "requeued", updated.Status, "run with future cooldown must not be promoted")
}

func TestSweepOrphanRuns_RequeuedRunOnParkedTask_Reaped(t *testing.T) {
	ctx := context.Background()
	orch, taskRepo, srRepo := makeOrchestratorFull(t)

	// Create a task in "cancelled" state (parked).
	task, err := taskRepo.Create(ctx, repo.CreateTaskInput{
		Slug:          "orphan-requeued-test",
		Title:         "Orphan Requeued Test",
		Cwd:           "/tmp",
		CurrentStage:  "cancelled",
		Priority:      "medium",
		MaxIterations: 3,
	})
	require.NoError(t, err)

	sr, err := srRepo.Create(ctx, repo.CreateStageRunInput{
		TaskID:      task.ID,
		Stage:       "implementation",
		Iteration:   0,
		SessionName: "orphan-requeued-impl-0",
	})
	require.NoError(t, err)

	retryCount := 1
	future := time.Now().Add(5 * time.Minute)
	_, err = srRepo.Update(ctx, sr.ID, repo.UpdateStageRunInput{
		Status:      strPtr("requeued"),
		RetryCount:  &retryCount,
		NextRetryAt: &future,
	})
	require.NoError(t, err)

	err = orch.SweepOrphanRunsForTest(ctx, nil)
	require.NoError(t, err)

	updated, err := srRepo.GetByID(ctx, sr.ID)
	require.NoError(t, err)
	require.Equal(t, "failed", updated.Status, "requeued run on parked task must be reaped to failed")
}

// makeRunningRun creates a task + "running" stage_run with the given PID (nil
// allowed) and StartedAt, for sweepOrphanRuns case-4 (orphaned running run) tests.
func makeRunningRun(t *testing.T, ctx context.Context, taskRepo repo.TaskRepo, srRepo repo.StageRunRepo, slug string, pid *int, startedAt time.Time) string {
	t.Helper()
	task, err := taskRepo.Create(ctx, repo.CreateTaskInput{
		Slug:          slug,
		Title:         "Running Orphan Test",
		Cwd:           "/tmp",
		CurrentStage:  "implementation",
		Priority:      "medium",
		MaxIterations: 3,
	})
	require.NoError(t, err)

	sr, err := srRepo.Create(ctx, repo.CreateStageRunInput{
		TaskID:      task.ID,
		Stage:       "implementation",
		Iteration:   0,
		SessionName: slug + "-impl-0",
	})
	require.NoError(t, err)

	sr, err = srRepo.Update(ctx, sr.ID, repo.UpdateStageRunInput{
		Status:    strPtr("running"),
		PID:       pid,
		StartedAt: &startedAt,
	})
	require.NoError(t, err)
	return sr.ID
}

// TestSweepOrphanRuns_RunningWithSessionStartedBeforeOrchestrator_Requeued
// pins case 4 to the same verdict recoverRunningStageRuns reaches for this row
// state at startup. A run with a resumable session must be re-queued, not
// failed — deriving liveness here independently is what made the tick-driven
// sweep and the startup pass disagree about identical rows.
func TestSweepOrphanRuns_RunningWithSessionStartedBeforeOrchestrator_Requeued(t *testing.T) {
	ctx := context.Background()
	orch, taskRepo, srRepo := makeOrchestratorFull(t)

	startedAt := time.Now().Add(-1 * time.Minute)
	runID := makeRunningRun(t, ctx, taskRepo, srRepo, "running-orphan-case4-session", nil, startedAt)

	sessionID := "sess-abc123"
	_, err := srRepo.Update(ctx, runID, repo.UpdateStageRunInput{SessionID: &sessionID})
	require.NoError(t, err)

	running, err := srRepo.ListByStatus(ctx, "running")
	require.NoError(t, err)
	require.NoError(t, orch.SweepOrphanRunsForTest(ctx, running))

	updated, err := srRepo.GetByID(ctx, runID)
	require.NoError(t, err)
	require.Equal(t, "pending", updated.Status,
		"a run with a session to resume from must be re-queued, not failed")
}

// TestSweepOrphanRuns_RequeuedRunSurvivesTheNextTick is the regression guard
// for the interaction between case 4's re-queue and case 3's stale-pending
// reaper. Case 4 only fires for a run whose started_at predates this
// orchestrator, so once the process has been up longer than
// pendingStaleDuration that timestamp is always older than the case 3 window.
// Re-queueing without refreshing started_at therefore hands the run straight
// to case 3 on the following tick, which fails it under the misleading reason
// "pending stage_run never promoted to running" — the resume never happens.
func TestSweepOrphanRuns_RequeuedRunSurvivesTheNextTick(t *testing.T) {
	ctx := context.Background()
	orch, taskRepo, srRepo := makeOrchestratorFull(t)

	// Older than pendingStaleDuration (5m), which is the normal case: any run
	// predating a long-lived orchestrator qualifies.
	startedAt := time.Now().Add(-30 * time.Minute)
	runID := makeRunningRun(t, ctx, taskRepo, srRepo, "running-orphan-requeue-survives", nil, startedAt)

	sessionID := "sess-survives"
	_, err := srRepo.Update(ctx, runID, repo.UpdateStageRunInput{SessionID: &sessionID})
	require.NoError(t, err)

	running, err := srRepo.ListByStatus(ctx, "running")
	require.NoError(t, err)
	require.NoError(t, orch.SweepOrphanRunsForTest(ctx, running))

	afterFirst, err := srRepo.GetByID(ctx, runID)
	require.NoError(t, err)
	require.Equal(t, "pending", afterFirst.Status, "case 4 must re-queue a resumable run")

	// Second tick with nothing having picked the run up in between — exactly
	// what happens when the picker skipped it because the tick snapshot still
	// showed it as running.
	stillRunning, err := srRepo.ListByStatus(ctx, "running")
	require.NoError(t, err)
	require.NoError(t, orch.SweepOrphanRunsForTest(ctx, stillRunning))

	afterSecond, err := srRepo.GetByID(ctx, runID)
	require.NoError(t, err)
	require.Equal(t, "pending", afterSecond.Status,
		"re-queued run must survive the next tick's stale-pending reaper, not be failed by it")
}

func TestSweepOrphanRuns_RunningNilPIDStartedBeforeOrchestrator_Reaped(t *testing.T) {
	ctx := context.Background()
	orch, taskRepo, srRepo := makeOrchestratorFull(t)

	// Orchestrator's startedAt is set at construction, just above. A run that
	// claims to have started before that can't belong to this process.
	startedAt := time.Now().Add(-1 * time.Minute)
	runID := makeRunningRun(t, ctx, taskRepo, srRepo, "running-orphan-case4-before", nil, startedAt)

	// sweepOrphanRuns only sees "running" runs via the allRunning param passed
	// down from tick() — mirror that here instead of passing nil.
	running, err := srRepo.ListByStatus(ctx, "running")
	require.NoError(t, err)
	err = orch.SweepOrphanRunsForTest(ctx, running)
	require.NoError(t, err)

	updated, err := srRepo.GetByID(ctx, runID)
	require.NoError(t, err)
	require.Equal(t, "failed", updated.Status, "running run predating the orchestrator with no PID must be reaped")
}

// TestSweepOrphanRuns_RunningNilPIDStartedAfterOrchestrator_LeftAlone is the
// regression test for the live async HTTP-adapter spawn path
// (stage_handlers.go AsyncRunningTransition{PID: 0}): a run legitimately has
// no local PID for the lifetime of the HTTP call. If this test does not exist
// or does not pass, the sweep silently destroys in-flight LLM stage work.
func TestSweepOrphanRuns_RunningNilPIDStartedAfterOrchestrator_LeftAlone(t *testing.T) {
	ctx := context.Background()
	orch, taskRepo, srRepo := makeOrchestratorFull(t)

	startedAt := time.Now().Add(1 * time.Minute)
	runID := makeRunningRun(t, ctx, taskRepo, srRepo, "running-orphan-case4-after", nil, startedAt)

	running, err := srRepo.ListByStatus(ctx, "running")
	require.NoError(t, err)
	err = orch.SweepOrphanRunsForTest(ctx, running)
	require.NoError(t, err)

	updated, err := srRepo.GetByID(ctx, runID)
	require.NoError(t, err)
	require.Equal(t, "running", updated.Status, "run started after the orchestrator must not be reaped, even with no PID")
}

func TestSweepOrphanRuns_RunningLivePIDStartedBeforeOrchestrator_LeftAlone(t *testing.T) {
	ctx := context.Background()
	orch, taskRepo, srRepo := makeOrchestratorFull(t)

	livePID := os.Getpid() // current test process is alive — a subprocess agent that survived a restart
	startedAt := time.Now().Add(-1 * time.Minute)
	runID := makeRunningRun(t, ctx, taskRepo, srRepo, "running-orphan-case4-livepid", &livePID, startedAt)

	running, err := srRepo.ListByStatus(ctx, "running")
	require.NoError(t, err)
	err = orch.SweepOrphanRunsForTest(ctx, running)
	require.NoError(t, err)

	updated, err := srRepo.GetByID(ctx, runID)
	require.NoError(t, err)
	require.Equal(t, "running", updated.Status, "run with a live PID must not be reaped even if it predates the orchestrator")
}

func TestSweepOrphanRuns_RunningDeadPIDStartedBeforeOrchestrator_Reaped(t *testing.T) {
	ctx := context.Background()
	orch, taskRepo, srRepo := makeOrchestratorFull(t)

	deadPID := -1
	startedAt := time.Now().Add(-1 * time.Minute)
	runID := makeRunningRun(t, ctx, taskRepo, srRepo, "running-orphan-case4-deadpid", &deadPID, startedAt)

	running, err := srRepo.ListByStatus(ctx, "running")
	require.NoError(t, err)
	err = orch.SweepOrphanRunsForTest(ctx, running)
	require.NoError(t, err)

	updated, err := srRepo.GetByID(ctx, runID)
	require.NoError(t, err)
	require.Equal(t, "failed", updated.Status, "running run predating the orchestrator with a dead PID must be reaped")
}
