package pipeline_test

// Integration tests for the plan-usage spawn gate (approvedPlan PR2-5).
//
// Three scenarios:
//  1. Gate fires → stage_run lands in rate_limited, NextRetryAt=resets_at,
//     RateLimitRetryCount unchanged, audit action stage_usage_gated recorded.
//  2. sweepRequeueableRuns with clock past resets_at promotes the run to pending.
//  3. 429 backstop: when PlanUsageResets returns a time after the normal backoff,
//     nextRetryAt is pushed out to resets_at.

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/lx-wnk/kontor/server/internal/db"
	"github.com/lx-wnk/kontor/server/internal/db/ent"
	"github.com/lx-wnk/kontor/server/internal/db/repo"
	"github.com/lx-wnk/kontor/server/internal/pipeline"
)

// makeGateOrch builds an orchestrator with CheckUsageGate and PlanUsageResets
// wired alongside the standard repos. Returns the orchestrator, taskRepo,
// stageRunRepo, and auditRepo so callers can assert audit events.
func makeGateOrch(
	t *testing.T,
	checkGate func(string) pipeline.UsageGateDecision,
	planResets func(string) *time.Time,
) (*pipeline.PipelineOrchestrator, repo.TaskRepo, repo.StageRunRepo, repo.AuditEventRepo) {
	t.Helper()
	bundle, err := db.Open(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { _ = bundle.Client.Close() })

	c := bundle.Client
	taskRepo := repo.NewTaskRepo(c)
	srRepo := repo.NewStageRunRepo(c)
	permRepo := repo.NewPermissionRepo(c)
	auditRepo := repo.NewAuditEventRepo(c)
	cfgRepo := repo.NewPipelineConfigRepo(c)

	orch, err := pipeline.NewOrchestrator(pipeline.OrchestratorOptions{
		TaskRepo:        taskRepo,
		StageRunRepo:    srRepo,
		PermissionRepo:  permRepo,
		AuditRepo:       auditRepo,
		ConfigRepo:      cfgRepo,
		CheckUsageGate:  checkGate,
		PlanUsageResets: planResets,
	})
	require.NoError(t, err)
	return orch, taskRepo, srRepo, auditRepo
}

// makeGateTask creates a task at the implementation stage ready for ProgressTask.
func makeGateTask(t *testing.T, ctx context.Context, taskRepo repo.TaskRepo, slug string) string {
	t.Helper()
	task, err := taskRepo.Create(ctx, repo.CreateTaskInput{
		Slug:          slug,
		Title:         slug,
		Cwd:           "/tmp",
		CurrentStage:  "implementation",
		Priority:      "medium",
		MaxIterations: 3,
	})
	require.NoError(t, err)
	return task.ID
}

// TestUsageGate_BlockedSpawn_ProducesRateLimitedTransition verifies that when
// CheckUsageGate returns Block:true the orchestrator emits a RateLimitedTransition
// (status rate_limited, NextRetryAt=resets_at, RateLimitRetryCount unchanged at 0)
// and records an audit event with action "stage_usage_gated".
func TestUsageGate_BlockedSpawn_ProducesRateLimitedTransition(t *testing.T) {
	ctx := context.Background()
	resets := time.Now().Add(3 * time.Hour).Truncate(time.Second)

	orch, taskRepo, srRepo, auditRepo := makeGateOrch(t,
		func(_ string) pipeline.UsageGateDecision {
			return pipeline.UsageGateDecision{Block: true, Until: resets, Reason: "5-hour at 92.0% (threshold 90.0%)"}
		},
		nil,
	)

	// Use a real agentStageHandler so the gate path inside Execute is exercised.
	// The spawnFn must not be called — fail loudly if it is.
	orch.SetHandlerOverride("implementation",
		pipeline.NewAgentStageHandlerForTest("implementation",
			func(_ pipeline.SpawnAgentOptions) (pipeline.SpawnResult, error) {
				t.Fatal("spawnFn must not be called when gate is blocking")
				return pipeline.SpawnResult{}, nil
			},
		),
	)

	taskID := makeGateTask(t, ctx, taskRepo, "gate-block-test")

	_, err := orch.ProgressTask(ctx, taskID, nil)
	require.NoError(t, err)

	// Retrieve the stage run created by ProgressTask.
	runs, err := srRepo.ListForTask(ctx, taskID)
	require.NoError(t, err)
	require.Len(t, runs, 1, "exactly one stage run must be created")
	sr := runs[0]

	require.Equal(t, "rate_limited", sr.Status,
		"gate-blocked run must be rate_limited")
	require.Equal(t, 0, sr.RateLimitRetryCount,
		"gate block must not increment RateLimitRetryCount")
	require.NotNil(t, sr.NextRetryAt,
		"NextRetryAt must be set to resets_at")
	require.WithinDuration(t, resets, *sr.NextRetryAt, time.Second,
		"NextRetryAt must equal the gate's Until (resets_at)")

	// Verify the audit event was recorded.
	events, err := auditRepo.ListForTask(ctx, taskID)
	require.NoError(t, err)
	var found bool
	for _, e := range events {
		if e.Action == "stage_usage_gated" {
			found = true
			break
		}
	}
	require.True(t, found, "audit event with action stage_usage_gated must be recorded")
}

// TestUsageGate_SweepPastResetsAt_PromotesToPending verifies that
// sweepRequeueableRuns promotes a rate_limited run to pending once NextRetryAt
// has passed (i.e. the rate-limit window has reset).
func TestUsageGate_SweepPastResetsAt_PromotesToPending(t *testing.T) {
	ctx := context.Background()

	orch, taskRepo, srRepo, _ := makeGateOrch(t, nil, nil)

	taskID := makeGateTask(t, ctx, taskRepo, "gate-sweep-test")

	// Seed a rate_limited run with NextRetryAt in the past.
	sr, err := srRepo.Create(ctx, repo.CreateStageRunInput{
		TaskID:      taskID,
		Stage:       "implementation",
		Iteration:   0,
		SessionName: "gate-sweep-test-0",
	})
	require.NoError(t, err)

	past := time.Now().Add(-time.Minute)
	_, err = srRepo.Update(ctx, sr.ID, repo.UpdateStageRunInput{
		Status:      strPtr("rate_limited"),
		NextRetryAt: &past,
	})
	require.NoError(t, err)

	err = orch.SweepRequeueableRunsForTest(ctx)
	require.NoError(t, err)

	updated, err := srRepo.GetByID(ctx, sr.ID)
	require.NoError(t, err)
	require.Equal(t, "pending", updated.Status,
		"rate_limited run with elapsed NextRetryAt must be promoted to pending")
	require.Nil(t, updated.NextRetryAt, "NextRetryAt must be cleared after promotion")
}

// TestUsageGate_429Backstop_ExtendsNextRetryAtToResetsAt verifies that when a
// stage run completes with RateLimited=true and PlanUsageResets returns a time
// after the normal backoff, nextRetryAt is pushed out to resets_at.
func TestUsageGate_429Backstop_ExtendsNextRetryAtToResetsAt(t *testing.T) {
	ctx := context.Background()

	// resets_at is 10 minutes from now — well beyond the default 600 s backoff.
	resetsAt := time.Now().Add(10 * time.Minute).Truncate(time.Second)

	orch, taskRepo, srRepo, _ := makeGateOrch(t,
		nil,
		func(_ string) *time.Time { return &resetsAt },
	)

	// Build a running stage run with a dead PID so finalizeCompletedAsyncRuns
	// considers it eligible for completion detection.
	task, err := taskRepo.Create(ctx, repo.CreateTaskInput{
		Slug:          "backstop-test",
		Title:         "Backstop Test",
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
		SessionName: "backstop-test-0",
	})
	require.NoError(t, err)

	deadPID := -1
	now := time.Now()
	sr, err = srRepo.Update(ctx, sr.ID, repo.UpdateStageRunInput{
		Status:    strPtr("running"),
		PID:       &deadPID,
		StartedAt: &now,
	})
	require.NoError(t, err)

	// Inject a completion detector that reports a 429 rate-limit failure.
	orch.SetCompletionDetector(func(_ *ent.StageRun, _ string, _ pipeline.CompletionDeps) (pipeline.CompletionResult, error) {
		return pipeline.CompletionResult{Kind: "failed", Error: "rate limited by API", RateLimited: true}, nil
	})

	err = orch.FinalizeCompletedAsyncRunsForTest(ctx, []*ent.StageRun{sr})
	require.NoError(t, err)

	updated, err := srRepo.GetByID(ctx, sr.ID)
	require.NoError(t, err)
	require.Equal(t, "rate_limited", updated.Status)
	require.NotNil(t, updated.NextRetryAt,
		"429 backstop must set NextRetryAt")
	require.False(t, updated.NextRetryAt.Before(resetsAt),
		"NextRetryAt must be >= resets_at; got %v, want >= %v", updated.NextRetryAt, resetsAt)
}
