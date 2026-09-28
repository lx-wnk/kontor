package pipeline_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/lx-wnk/kontor/server/internal/db"
	"github.com/lx-wnk/kontor/server/internal/db/ent"
	"github.com/lx-wnk/kontor/server/internal/db/repo"
	"github.com/lx-wnk/kontor/server/internal/pipeline"
)

// TestOrchestrator_UserRetryClearsStalePending proves that ClearStalePendingPermissions
// expires stale requests so a user-initiated retry is not blocked by the lingering-pending gate.
func TestOrchestrator_UserRetryClearsStalePending(t *testing.T) {
	ctx := context.Background()

	bundle := openSharedBundle(t)
	orch, taskRepo := makeOrchFromBundle(t, bundle)
	srRepo := repo.NewStageRunRepo(bundle.Client)
	permRepo := repo.NewPermissionRepo(bundle.Client)

	orch.SetHandlerOverride("implementation", &agentStubHandler{
		stage:      "implementation",
		transition: pipeline.FailTransition{Reason: "handler reached"},
	})

	task, err := taskRepo.Create(ctx, repo.CreateTaskInput{
		Slug:                "retry-clears-stale",
		Title:               "Retry Clears Stale Pending",
		Cwd:                 "/tmp",
		CurrentStage:        "implementation",
		Priority:            "medium",
		MaxIterations:       3,
		StageTimeoutSeconds: 1800,
	})
	require.NoError(t, err)

	// Seed a prior stage_run that is failed and has one pending permission_request.
	priorRun, err := srRepo.Create(ctx, repo.CreateStageRunInput{
		TaskID:      task.ID,
		Stage:       "implementation",
		Iteration:   0,
		SessionName: "seeded-failed-run",
	})
	require.NoError(t, err)
	failed := "failed"
	priorRun, err = srRepo.Update(ctx, priorRun.ID, repo.UpdateStageRunInput{Status: &failed})
	require.NoError(t, err)
	_, err = permRepo.CreatePermissionRequest(ctx, repo.CreatePermissionRequestInput{
		StageRunID: priorRun.ID,
		Tool:       "Bash",
	})
	require.NoError(t, err)

	countBefore := len(listRunsForTask(t, srRepo, ctx, task.ID))

	// Assert the pre-fix behavior: gate blocks ProgressTask → nil result, no new run.
	result, err := orch.ProgressTask(ctx, task.ID, nil)
	require.NoError(t, err)
	require.Nil(t, result, "gate must block when stale pending requests exist")
	require.Equal(t, countBefore, len(listRunsForTask(t, srRepo, ctx, task.ID)),
		"no new stage_run should be created while gate is blocking")

	// Simulate the user-initiated retry: clear stale requests.
	orch.ClearStalePendingPermissions(ctx, task.ID)

	pending, err := permRepo.CountForStageRun(ctx, priorRun.ID)
	require.NoError(t, err)
	require.Equal(t, 0, pending, "pending requests must be cleared before retry")

	// ProgressTask must now succeed and create a new run.
	_, err = orch.ProgressTask(ctx, task.ID, nil)
	require.NoError(t, err)
	require.Greater(t, len(listRunsForTask(t, srRepo, ctx, task.ID)), countBefore,
		"a new stage_run must be created after stale requests are cleared")
}

// agentStubHandler is a StageHandler test double that claims RequiresAgent()==true.
// Its Execute method returns the preset transition so tests can confirm whether
// the handler was reached (gate did not block) or not (gate blocked).
type agentStubHandler struct {
	stage      string
	transition pipeline.StageTransition
}

func (h *agentStubHandler) Stage() string       { return h.stage }
func (h *agentStubHandler) RequiresAgent() bool { return true }
func (h *agentStubHandler) Execute(_ *pipeline.StageContext) (pipeline.StageTransition, error) {
	return h.transition, nil
}

// openSharedBundle opens an in-memory SQLite DB and registers a test cleanup.
// Use this when a test needs direct repo access alongside the orchestrator.
func openSharedBundle(t *testing.T) *db.DBBundle {
	t.Helper()
	bundle, err := db.Open(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { _ = bundle.Client.Close() })
	return bundle
}

// makeOrchFromBundle constructs an orchestrator + TaskRepo backed by the given bundle.
func makeOrchFromBundle(t *testing.T, bundle *db.DBBundle) (*pipeline.PipelineOrchestrator, repo.TaskRepo) {
	t.Helper()
	c := bundle.Client
	orch, err := pipeline.NewOrchestrator(pipeline.OrchestratorOptions{
		TaskRepo:       repo.NewTaskRepo(c),
		StageRunRepo:   repo.NewStageRunRepo(c),
		PermissionRepo: repo.NewPermissionRepo(c),
		AuditRepo:      repo.NewAuditEventRepo(c),
		ConfigRepo:     repo.NewPipelineConfigRepo(c),
	})
	require.NoError(t, err)
	return orch, repo.NewTaskRepo(c)
}

func listRunsForTask(t *testing.T, srRepo repo.StageRunRepo, ctx context.Context, taskID string) []*ent.StageRun {
	t.Helper()
	runs, err := srRepo.ListForTask(ctx, taskID)
	require.NoError(t, err)
	return runs
}

// TestOrchestrator_LingeringPendingGate verifies that runProgressTaskLocked returns nil
// and creates no new stage_run when the latest run on the current stage is terminal (done/failed)
// OR is awaiting_user with a dead PID, AND unresolved permission_request rows exist.
// This prevents cascaded respawns after a killed agent leaves pending permission rows behind.
func TestOrchestrator_LingeringPendingGate(t *testing.T) {
	tests := []struct {
		name           string
		latestStatus   string
		pid            *int // nil = no PID column set; nonzero = set PID
		hasPendingReqs bool
		wantBlocked    bool
	}{
		{
			name:           "failed run with pending permission_requests blocks respawn",
			latestStatus:   "failed",
			hasPendingReqs: true,
			wantBlocked:    true,
		},
		{
			name:           "done run with pending permission_requests blocks respawn",
			latestStatus:   "done",
			hasPendingReqs: true,
			wantBlocked:    true,
		},
		{
			name:           "awaiting_user with dead PID and pending requests blocks respawn",
			latestStatus:   "awaiting_user",
			pid:            ptr(999999999), // extremely high PID — never alive in test process
			hasPendingReqs: true,
			wantBlocked:    true,
		},
		{
			name:           "failed run without pending requests allows respawn",
			latestStatus:   "failed",
			hasPendingReqs: false,
			wantBlocked:    false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()

			// Shared bundle so orchestrator and direct repo calls see the same DB.
			bundle := openSharedBundle(t)
			orch, taskRepo := makeOrchFromBundle(t, bundle)
			srRepo := repo.NewStageRunRepo(bundle.Client)
			permRepo := repo.NewPermissionRepo(bundle.Client)

			// Install an agent-requiring handler so RequiresAgent()==true triggers the gate.
			// The transition is FailTransition so even if the gate doesn't fire, the test
			// remains deterministic (no real claude spawn).
			orch.SetHandlerOverride("implementation", &agentStubHandler{
				stage:      "implementation",
				transition: pipeline.FailTransition{Reason: "handler reached — gate did not block"},
			})

			task, err := taskRepo.Create(ctx, repo.CreateTaskInput{
				Slug:                "gate-" + tc.name,
				Title:               "Lingering Pending Gate Test",
				Cwd:                 "/tmp",
				CurrentStage:        "implementation",
				Priority:            "medium",
				MaxIterations:       3,
				StageTimeoutSeconds: 1800,
			})
			require.NoError(t, err)

			// Seed a prior stage_run in the requested terminal/zombie status.
			priorRun, err := srRepo.Create(ctx, repo.CreateStageRunInput{
				TaskID:      task.ID,
				Stage:       "implementation",
				Iteration:   0,
				SessionName: "seeded-prior-run",
			})
			require.NoError(t, err)

			runStatus := tc.latestStatus
			updateIn := repo.UpdateStageRunInput{Status: &runStatus}
			if tc.pid != nil {
				updateIn.PID = tc.pid
			}
			priorRun, err = srRepo.Update(ctx, priorRun.ID, updateIn)
			require.NoError(t, err)

			// Optionally attach a pending permission_request (outcome IS NULL = pending).
			if tc.hasPendingReqs {
				_, err = permRepo.CreatePermissionRequest(ctx, repo.CreatePermissionRequestInput{
					StageRunID: priorRun.ID,
					Tool:       "Bash",
				})
				require.NoError(t, err)
			}

			runsBefore := listRunsForTask(t, srRepo, ctx, task.ID)
			countBefore := len(runsBefore)

			result, err := orch.ProgressTask(ctx, task.ID, nil)
			require.NoError(t, err)

			runsAfter := listRunsForTask(t, srRepo, ctx, task.ID)
			countAfter := len(runsAfter)

			if tc.wantBlocked {
				require.Nil(t, result,
					"gate should return nil when latest run is terminal/zombie with unresolved permission_requests")
				require.Equal(t, countBefore, countAfter,
					"no new stage_run row should be created when gate blocks; before=%d after=%d",
					countBefore, countAfter)
			} else {
				// Gate did not fire: handler ran → FailTransition → a new run was created and marked failed.
				require.Greater(t, countAfter, countBefore,
					"gate should not block when there are no pending permission_requests")
			}
		})
	}
}

// TestOrchestrator_LingeringPendingGate_ThreeCases covers the three scenarios
// specified in the task brief:
//  1. Gate-positive TERMINAL: failed run + unresolved perm row → blocked.
//  2. Gate-positive ZOMBIE-AWAIT: awaiting_user + dead PID + unresolved perm row → blocked.
//  3. Gate-negative RESOLVED: failed run + resolved perm row → new run created.
//     This proves that the OutcomeIsNil filter (not mere row presence) is the gate condition.
func TestOrchestrator_LingeringPendingGate_ThreeCases(t *testing.T) {
	tests := []struct {
		name         string
		latestStatus string
		pid          *int
		resolvedPerm bool // true = resolve the perm row before calling ProgressTask
		wantBlocked  bool
	}{
		{
			name:         "terminal-failed with unresolved perm blocks respawn",
			latestStatus: "failed",
			wantBlocked:  true,
		},
		{
			name:         "zombie-awaiting with dead PID and unresolved perm blocks respawn",
			latestStatus: "awaiting_user",
			pid:          ptr(999999999),
			wantBlocked:  true,
		},
		{
			name:         "resolved perm row on failed run allows respawn",
			latestStatus: "failed",
			resolvedPerm: true,
			wantBlocked:  false,
		},
	}

	for i, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()

			bundle := openSharedBundle(t)
			orch, taskRepo := makeOrchFromBundle(t, bundle)
			srRepo := repo.NewStageRunRepo(bundle.Client)
			permRepo := repo.NewPermissionRepo(bundle.Client)

			orch.SetHandlerOverride("implementation", &agentStubHandler{
				stage:      "implementation",
				transition: pipeline.FailTransition{Reason: "handler reached — gate did not block"},
			})

			task, err := taskRepo.Create(ctx, repo.CreateTaskInput{
				Slug:                fmt.Sprintf("three-cases-%d", i),
				Title:               "ThreeCases Gate Test",
				Cwd:                 "/tmp",
				CurrentStage:        "implementation",
				Priority:            "medium",
				MaxIterations:       3,
				StageTimeoutSeconds: 1800,
			})
			require.NoError(t, err)

			priorRun, err := srRepo.Create(ctx, repo.CreateStageRunInput{
				TaskID:      task.ID,
				Stage:       "implementation",
				Iteration:   0,
				SessionName: "seeded-three-cases",
			})
			require.NoError(t, err)

			updateIn := repo.UpdateStageRunInput{Status: &tc.latestStatus}
			if tc.pid != nil {
				updateIn.PID = tc.pid
			}
			priorRun, err = srRepo.Update(ctx, priorRun.ID, updateIn)
			require.NoError(t, err)

			// Always seed one permission_request row; resolve it only for the control case.
			req, err := permRepo.CreatePermissionRequest(ctx, repo.CreatePermissionRequestInput{
				StageRunID: priorRun.ID,
				Tool:       "Bash",
			})
			require.NoError(t, err)

			if tc.resolvedPerm {
				err = permRepo.ResolvePermissionRequest(ctx, req.ID, "allow")
				require.NoError(t, err)
			}

			runsBefore := listRunsForTask(t, srRepo, ctx, task.ID)
			countBefore := len(runsBefore)

			result, err := orch.ProgressTask(ctx, task.ID, nil)
			require.NoError(t, err)

			runsAfter := listRunsForTask(t, srRepo, ctx, task.ID)
			countAfter := len(runsAfter)

			if tc.wantBlocked {
				require.Nil(t, result,
					"gate should return nil when latest run is terminal/zombie with unresolved permission_requests")
				require.Equal(t, countBefore, countAfter,
					"no new stage_run should be created when gate blocks; before=%d after=%d",
					countBefore, countAfter)
			} else {
				require.Greater(t, countAfter, countBefore,
					"gate should not block when the only perm row is resolved; before=%d after=%d",
					countBefore, countAfter)
			}
		})
	}
}
