package pipeline_test

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

func TestOrchestrator_ReadyTransitionsToImplementation(t *testing.T) {
	orch, taskRepo := makeTestOrchestratorWithRepos(t)
	ctx := context.Background()

	task, err := taskRepo.Create(ctx, repo.CreateTaskInput{
		Slug:                "backlog-test",
		Title:               "Backlog Test",
		Cwd:                 "/tmp",
		CurrentStage:        "ready",
		Priority:            "medium",
		MaxIterations:       3,
		StageTimeoutSeconds: 1800,
	})
	require.NoError(t, err)

	sr, err := orch.ProgressTask(ctx, task.ID, nil)
	require.NoError(t, err)
	require.NotNil(t, sr)
	require.Equal(t, "done", sr.Status)

	updated, err := taskRepo.GetByID(ctx, task.ID)
	require.NoError(t, err)
	require.Equal(t, "implementation", updated.CurrentStage)
}

func TestOrchestrator_AsyncRunningTransition_RecordsPI(t *testing.T) {
	orch, taskRepo := makeTestOrchestratorWithRepos(t)
	orch.SetHandlerOverride("implementation", &stubStageHandler{
		stage:      "implementation",
		transition: pipeline.AsyncRunningTransition{PID: 42},
	})

	task, err := taskRepo.Create(context.Background(), repo.CreateTaskInput{
		Slug:                "impl-test",
		Title:               "Impl Test",
		Cwd:                 "/tmp",
		CurrentStage:        "implementation",
		Priority:            "medium",
		MaxIterations:       3,
		StageTimeoutSeconds: 1800,
	})
	require.NoError(t, err)

	sr, err := orch.ProgressTask(context.Background(), task.ID, nil)
	require.NoError(t, err)
	require.NotNil(t, sr)
	require.Equal(t, "running", sr.Status)
	require.NotNil(t, sr.Pid)
	require.Equal(t, 42, *sr.Pid)
}

func TestOrchestrator_FailTransition_TaskStageUnchanged(t *testing.T) {
	orch, taskRepo := makeTestOrchestratorWithRepos(t)
	orch.SetHandlerOverride("implementation", &stubStageHandler{
		stage:      "implementation",
		transition: pipeline.FailTransition{Reason: "test failure"},
	})

	task, err := taskRepo.Create(context.Background(), repo.CreateTaskInput{
		Slug:                "fail-test",
		Title:               "Fail Test",
		Cwd:                 "/tmp",
		CurrentStage:        "implementation",
		Priority:            "medium",
		MaxIterations:       3,
		StageTimeoutSeconds: 1800,
	})
	require.NoError(t, err)

	sr, err := orch.ProgressTask(context.Background(), task.ID, nil)
	require.NoError(t, err)
	require.Equal(t, "failed", sr.Status)

	updated, err := taskRepo.GetByID(context.Background(), task.ID)
	require.NoError(t, err)
	require.Equal(t, "implementation", updated.CurrentStage)
}

func TestOrchestrator_RequeueTransition(t *testing.T) {
	ctx := context.Background()
	onStageFailed := false

	bundle, err := db.Open(":memory:")
	require.NoError(t, err)
	client := bundle.Client
	t.Cleanup(func() { _ = client.Close() })

	taskRepo := repo.NewTaskRepo(client)
	srRepo := repo.NewStageRunRepo(client)
	permRepo := repo.NewPermissionRepo(client)
	auditRepo := repo.NewAuditEventRepo(client)
	cfgRepo := repo.NewPipelineConfigRepo(client)

	futureTime := time.Now().Add(5 * time.Minute)

	orch, err := pipeline.NewOrchestrator(pipeline.OrchestratorOptions{
		TaskRepo:       taskRepo,
		StageRunRepo:   srRepo,
		PermissionRepo: permRepo,
		AuditRepo:      auditRepo,
		ConfigRepo:     cfgRepo,
		OnStageFailed:  func(_ string, _ pipeline.StageFailedInfo) { onStageFailed = true },
	})
	require.NoError(t, err)

	orch.SetHandlerOverride("implementation", &stubStageHandler{
		stage: "implementation",
		transition: pipeline.RequeueTransition{
			Reason:      "quota",
			Attempt:     1,
			NextRetryAt: futureTime,
			Output:      map[string]any{"extra": "data"},
		},
	})

	task, err := taskRepo.Create(ctx, repo.CreateTaskInput{
		Slug:                "requeue-test",
		Title:               "Requeue Test",
		Cwd:                 "/tmp",
		CurrentStage:        "implementation",
		Priority:            "medium",
		MaxIterations:       3,
		StageTimeoutSeconds: 1800,
	})
	require.NoError(t, err)

	sr, err := orch.ProgressTask(ctx, task.ID, nil)
	require.NoError(t, err)
	require.Equal(t, "requeued", sr.Status)
	require.Equal(t, 1, sr.RetryCount)
	require.NotNil(t, sr.NextRetryAt)
	require.WithinDuration(t, futureTime, *sr.NextRetryAt, time.Second)
	require.Equal(t, "quota", sr.Output["requeue_reason"])
	require.EqualValues(t, 1, sr.Output["attempt"])

	updated, err := taskRepo.GetByID(ctx, task.ID)
	require.NoError(t, err)
	require.Equal(t, "implementation", updated.CurrentStage)

	require.False(t, onStageFailed, "OnStageFailed must not be called for requeue")
}

// --- sortPickCandidates tests ---

func TestSortPickCandidates_SilverBulletFirst(t *testing.T) {
	now := time.Now()
	normal := &ent.Task{ID: "normal", CurrentStage: "implementation", Priority: "high", SilverBullet: false, CreatedAt: now}
	bullet := &ent.Task{ID: "bullet", CurrentStage: "implementation", Priority: "high", SilverBullet: true, CreatedAt: now}

	// Place normal first to confirm sort moves bullet to front.
	tasks := []*ent.Task{normal, bullet}
	pipeline.SortPickCandidatesForTest(tasks)

	require.Equal(t, "bullet", tasks[0].ID, "silver-bullet task must be first")
	require.Equal(t, "normal", tasks[1].ID)
}

func TestSortPickCandidates_FurtherStageFirst(t *testing.T) {
	now := time.Now()
	early := &ent.Task{ID: "early", CurrentStage: "implementation", Priority: "medium", SilverBullet: false, CreatedAt: now}
	later := &ent.Task{ID: "later", CurrentStage: "self_review", Priority: "medium", SilverBullet: false, CreatedAt: now}

	tasks := []*ent.Task{early, later}
	pipeline.SortPickCandidatesForTest(tasks)

	require.Equal(t, "later", tasks[0].ID, "task at further stage must be first")
	require.Equal(t, "early", tasks[1].ID)
}

func TestSortPickCandidates_HigherPriorityFirst(t *testing.T) {
	now := time.Now()
	low := &ent.Task{ID: "low", CurrentStage: "implementation", Priority: "low", SilverBullet: false, CreatedAt: now}
	med := &ent.Task{ID: "med", CurrentStage: "implementation", Priority: "medium", SilverBullet: false, CreatedAt: now}
	high := &ent.Task{ID: "high", CurrentStage: "implementation", Priority: "high", SilverBullet: false, CreatedAt: now}

	tasks := []*ent.Task{low, med, high}
	pipeline.SortPickCandidatesForTest(tasks)

	require.Equal(t, "high", tasks[0].ID, "high priority must be first")
	require.Equal(t, "med", tasks[1].ID, "medium priority must be second")
	require.Equal(t, "low", tasks[2].ID, "low priority must be last")
}

func TestSortPickCandidates_OlderFirst(t *testing.T) {
	older := &ent.Task{ID: "older", CurrentStage: "implementation", Priority: "medium", SilverBullet: false, CreatedAt: time.Now().Add(-10 * time.Minute)}
	newer := &ent.Task{ID: "newer", CurrentStage: "implementation", Priority: "medium", SilverBullet: false, CreatedAt: time.Now()}

	tasks := []*ent.Task{newer, older}
	pipeline.SortPickCandidatesForTest(tasks)

	require.Equal(t, "older", tasks[0].ID, "older task must be first")
	require.Equal(t, "newer", tasks[1].ID)
}

func TestSortPickCandidates_LowerRankFirst(t *testing.T) {
	// Give the lower-rank task a NEWER created_at to prove rank beats created_at.
	lowRank := ptr(100.0)
	highRank := ptr(900.0)
	newerTime := time.Now()
	olderTime := newerTime.Add(-10 * time.Minute)

	lowRankTask := &ent.Task{ID: "low-rank", CurrentStage: "implementation", Priority: "medium", SilverBullet: false, Rank: lowRank, CreatedAt: newerTime}
	highRankTask := &ent.Task{ID: "high-rank", CurrentStage: "implementation", Priority: "medium", SilverBullet: false, Rank: highRank, CreatedAt: olderTime}

	// Place high-rank task first to confirm sort moves low-rank to front.
	tasks := []*ent.Task{highRankTask, lowRankTask}
	pipeline.SortPickCandidatesForTest(tasks)

	require.Equal(t, "low-rank", tasks[0].ID, "lower rank must sort first regardless of created_at")
	require.Equal(t, "high-rank", tasks[1].ID)
}

func TestSortPickCandidates_PriorityBeatsRank(t *testing.T) {
	// High priority + high rank must still sort before low priority + low rank.
	highRank := ptr(999999.0)
	lowRank := ptr(1.0)
	now := time.Now()

	highPriority := &ent.Task{ID: "high-prio", CurrentStage: "implementation", Priority: "high", SilverBullet: false, Rank: highRank, CreatedAt: now}
	lowPriority := &ent.Task{ID: "low-prio", CurrentStage: "implementation", Priority: "low", SilverBullet: false, Rank: lowRank, CreatedAt: now}

	tasks := []*ent.Task{lowPriority, highPriority}
	pipeline.SortPickCandidatesForTest(tasks)

	require.Equal(t, "high-prio", tasks[0].ID, "high priority must sort first even with a worse (higher) rank")
	require.Equal(t, "low-prio", tasks[1].ID)
}

// --- decideCompletedTransition tests ---

func TestDecideCompletedTransition_Finalization(t *testing.T) {
	ctx := context.Background()
	orch := makeTestOrchestrator(t)

	task := &ent.Task{ID: "t1", CurrentStage: "finalization"}
	run := &ent.StageRun{ID: "r1", Stage: "finalization"}

	transition := orch.DecideCompletedTransitionForTest(ctx, task, run, map[string]any{})

	_, ok := transition.(pipeline.DoneTransition)
	require.True(t, ok, "finalization stage must produce DoneTransition, got %T", transition)
}

func TestDecideCompletedTransition_SelfReview_Passed(t *testing.T) {
	ctx := context.Background()
	orch := makeTestOrchestrator(t)

	task := &ent.Task{ID: "t2", CurrentStage: "self_review"}
	run := &ent.StageRun{ID: "r2", Stage: "self_review"}
	output := map[string]any{"passed": true}

	transition := orch.DecideCompletedTransitionForTest(ctx, task, run, output)

	next, ok := transition.(pipeline.NextTransition)
	require.True(t, ok, "passed self_review must produce NextTransition, got %T", transition)
	require.Equal(t, "finalization", next.Stage)
}

func TestDecideCompletedTransition_SelfReview_Passed_ClearsFeedback(t *testing.T) {
	ctx := context.Background()
	orch := makeTestOrchestrator(t)

	task := &ent.Task{
		ID:           "t3",
		CurrentStage: "self_review",
		Metadata:     map[string]any{"review_feedback": "old feedback", "review_cycles": float64(1)},
	}
	run := &ent.StageRun{ID: "r3", Stage: "self_review"}
	output := map[string]any{"passed": true}

	transition := orch.DecideCompletedTransitionForTest(ctx, task, run, output)

	next, ok := transition.(pipeline.NextTransition)
	require.True(t, ok, "passed self_review must produce NextTransition, got %T", transition)
	require.Equal(t, "finalization", next.Stage)

	// review_feedback must not appear in the metadata patch
	if next.MetadataPatch != nil {
		_, hasFeedback := next.MetadataPatch["review_feedback"]
		require.False(t, hasFeedback, "review_feedback must be cleared after passing review")
	}
}

func TestDecideCompletedTransition_SelfReview_Failed_FirstCycle(t *testing.T) {
	ctx := context.Background()
	orch := makeTestOrchestrator(t)

	task := &ent.Task{ID: "t4", CurrentStage: "self_review", Metadata: nil}
	run := &ent.StageRun{ID: "r4", Stage: "self_review"}
	// Provide structured findings so SummarizeReviewFindings returns a non-empty string.
	output := map[string]any{
		"passed":  false,
		"summary": "missing tests and error handling",
		"findings": []any{
			map[string]any{
				"severity":    "ERROR",
				"description": "no unit tests for service layer",
				"file":        "service/foo.go",
			},
		},
	}

	transition := orch.DecideCompletedTransitionForTest(ctx, task, run, output)

	next, ok := transition.(pipeline.NextTransition)
	require.True(t, ok, "failed self_review (first cycle) must produce NextTransition, got %T", transition)
	require.Equal(t, "implementation", next.Stage)
	require.NotEmpty(t, next.MetadataPatch["review_feedback"], "review_feedback must be set in patch")
	require.Equal(t, 1, next.MetadataPatch["review_cycles"], "review_cycles must be 1 after first failure")
}

func TestDecideCompletedTransition_SelfReview_MaxCyclesReached(t *testing.T) {
	ctx := context.Background()
	orch := makeTestOrchestrator(t)

	// defaultMaxReviewCycles is 3; cycles = prevCycles + 1 = 2 + 1 = 3 >= 3 → WaitUser
	task := &ent.Task{
		ID:           "t5",
		CurrentStage: "self_review",
		Metadata:     map[string]any{"review_cycles": float64(2)},
	}
	run := &ent.StageRun{ID: "r5", Stage: "self_review"}
	output := map[string]any{"passed": false}

	transition := orch.DecideCompletedTransitionForTest(ctx, task, run, output)

	_, ok := transition.(pipeline.WaitUserTransition)
	require.True(t, ok, "self_review at max cycles must produce WaitUserTransition, got %T", transition)
}

// --- infra-failure auto-requeue branch tests ---

// makeRunningStageRun creates a task + stage_run already in "running" status with the given retryCount.
func makeRunningStageRun(t *testing.T, ctx context.Context, taskRepo repo.TaskRepo, srRepo repo.StageRunRepo, retryCount int) (*ent.Task, *ent.StageRun) {
	t.Helper()
	task, err := taskRepo.Create(ctx, repo.CreateTaskInput{
		Slug:                "infra-test",
		Title:               "Infra Test",
		Cwd:                 "/tmp",
		CurrentStage:        "implementation",
		Priority:            "medium",
		MaxIterations:       3,
		StageTimeoutSeconds: 1800,
	})
	require.NoError(t, err)

	sr, err := srRepo.Create(ctx, repo.CreateStageRunInput{
		TaskID:      task.ID,
		Stage:       "implementation",
		Iteration:   0,
		SessionName: "infra-test-impl-0",
	})
	require.NoError(t, err)

	// Set to running with a dead PID so finalizeCompletedAsyncRuns processes it.
	deadPID := -1
	now := time.Now()
	sr, err = srRepo.Update(ctx, sr.ID, repo.UpdateStageRunInput{
		Status:     strPtr("running"),
		PID:        &deadPID,
		StartedAt:  &now,
		RetryCount: &retryCount,
	})
	require.NoError(t, err)
	return task, sr
}

func strPtr(s string) *string { return &s }

func makeOrchestratorWithSRRepo(t *testing.T) (*pipeline.PipelineOrchestrator, repo.TaskRepo, repo.StageRunRepo) {
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

func TestFinalizeCompletedAsyncRuns_InfraFailure_Requeues(t *testing.T) {
	ctx := context.Background()
	orch, taskRepo, srRepo := makeOrchestratorWithSRRepo(t)

	_, run := makeRunningStageRun(t, ctx, taskRepo, srRepo, 0)

	orch.SetCompletionDetector(func(_ *ent.StageRun, _ string, _ pipeline.CompletionDeps) (pipeline.CompletionResult, error) {
		return pipeline.CompletionResult{Kind: "failed", Error: "quota exceeded", Infra: true}, nil
	})

	before := time.Now()
	err := orch.FinalizeCompletedAsyncRunsForTest(ctx, []*ent.StageRun{run})
	require.NoError(t, err)

	updated, err := srRepo.GetByID(ctx, run.ID)
	require.NoError(t, err)
	require.Equal(t, "requeued", updated.Status)
	require.Equal(t, 1, updated.RetryCount)
	require.NotNil(t, updated.NextRetryAt)
	require.True(t, updated.NextRetryAt.After(before), "NextRetryAt must be in the future")
	require.Equal(t, "quota exceeded", updated.Output["requeue_reason"])
}

func TestFinalizeCompletedAsyncRuns_InfraFailure_ExhaustedBudget_HardFails(t *testing.T) {
	ctx := context.Background()
	orch, taskRepo, srRepo := makeOrchestratorWithSRRepo(t)

	// defaultMaxAutoRetries is 3; start with retryCount == 3 → budget exhausted.
	_, run := makeRunningStageRun(t, ctx, taskRepo, srRepo, 3)

	orch.SetCompletionDetector(func(_ *ent.StageRun, _ string, _ pipeline.CompletionDeps) (pipeline.CompletionResult, error) {
		return pipeline.CompletionResult{Kind: "failed", Error: "overloaded_error", Infra: true}, nil
	})

	err := orch.FinalizeCompletedAsyncRunsForTest(ctx, []*ent.StageRun{run})
	require.NoError(t, err)

	updated, err := srRepo.GetByID(ctx, run.ID)
	require.NoError(t, err)
	require.Equal(t, "failed", updated.Status)
	require.NotNil(t, updated.Output["auto_retries_exhausted"], "output must contain auto_retries_exhausted key")
	require.EqualValues(t, 3, updated.Output["auto_retries_exhausted"])
}

func TestFinalizeCompletedAsyncRuns_SchemaReject_Iter0_Iterates(t *testing.T) {
	ctx := context.Background()
	orch, taskRepo, srRepo := makeOrchestratorWithSRRepo(t)

	_, run := makeRunningStageRun(t, ctx, taskRepo, srRepo, 0)

	orch.SetCompletionDetector(func(_ *ent.StageRun, _ string, _ pipeline.CompletionDeps) (pipeline.CompletionResult, error) {
		return pipeline.CompletionResult{Kind: "failed", Error: "missing field: passed", Retryable: true}, nil
	})

	err := orch.FinalizeCompletedAsyncRunsForTest(ctx, []*ent.StageRun{run})
	require.NoError(t, err)

	updated, err := srRepo.GetByID(ctx, run.ID)
	require.NoError(t, err)
	// IterateTransition marks the original run "done" and creates a new one
	require.Equal(t, "done", updated.Status)
}

func TestFinalizeCompletedAsyncRuns_SchemaReject_Iter1_WaitsUser(t *testing.T) {
	ctx := context.Background()
	orch, taskRepo, srRepo := makeOrchestratorWithSRRepo(t)

	task, err := taskRepo.Create(ctx, repo.CreateTaskInput{
		Slug:                "schema-iter1",
		Title:               "Schema Iter1 Test",
		Cwd:                 "/tmp",
		CurrentStage:        "implementation",
		Priority:            "medium",
		MaxIterations:       3,
		StageTimeoutSeconds: 1800,
	})
	require.NoError(t, err)

	// Create iteration 0 as "done" so the partial unique index on running is free.
	iter0, err := srRepo.Create(ctx, repo.CreateStageRunInput{
		TaskID:      task.ID,
		Stage:       "implementation",
		Iteration:   0,
		SessionName: "schema-iter1-impl-0",
	})
	require.NoError(t, err)
	_, err = srRepo.Update(ctx, iter0.ID, repo.UpdateStageRunInput{Status: strPtr("done")})
	require.NoError(t, err)

	// Create iteration 1 in running state.
	iter1, err := srRepo.Create(ctx, repo.CreateStageRunInput{
		TaskID:      task.ID,
		Stage:       "implementation",
		Iteration:   1,
		SessionName: "schema-iter1-impl-1",
	})
	require.NoError(t, err)
	deadPID := -1
	now := time.Now()
	iter1, err = srRepo.Update(ctx, iter1.ID, repo.UpdateStageRunInput{
		Status:    strPtr("running"),
		PID:       &deadPID,
		StartedAt: &now,
	})
	require.NoError(t, err)

	orch.SetCompletionDetector(func(_ *ent.StageRun, _ string, _ pipeline.CompletionDeps) (pipeline.CompletionResult, error) {
		return pipeline.CompletionResult{Kind: "failed", Error: "missing field: passed", Retryable: true}, nil
	})

	err = orch.FinalizeCompletedAsyncRunsForTest(ctx, []*ent.StageRun{iter1})
	require.NoError(t, err)

	updated, err := srRepo.GetByID(ctx, iter1.ID)
	require.NoError(t, err)
	require.Equal(t, "awaiting_user", updated.Status)
}

func TestFinalizeCompletedAsyncRuns_HardFail_NeitherInfraNorRetryable(t *testing.T) {
	ctx := context.Background()
	orch, taskRepo, srRepo := makeOrchestratorWithSRRepo(t)

	_, run := makeRunningStageRun(t, ctx, taskRepo, srRepo, 0)

	orch.SetCompletionDetector(func(_ *ent.StageRun, _ string, _ pipeline.CompletionDeps) (pipeline.CompletionResult, error) {
		return pipeline.CompletionResult{Kind: "failed", Error: "agent panicked", Infra: false, Retryable: false}, nil
	})

	err := orch.FinalizeCompletedAsyncRunsForTest(ctx, []*ent.StageRun{run})
	require.NoError(t, err)

	updated, err := srRepo.GetByID(ctx, run.ID)
	require.NoError(t, err)
	require.Equal(t, "failed", updated.Status)
	require.Equal(t, "agent panicked", updated.Output["error"])
	require.Nil(t, updated.Output["auto_retries_exhausted"], "hard fail must not set auto_retries_exhausted")
}

// --- picker guard tests ---

// makePickerTask creates a pending task at "implementation" stage for picker tests.
func makePickerTask(t *testing.T, ctx context.Context, taskRepo repo.TaskRepo, slug string) *ent.Task {
	t.Helper()
	task, err := taskRepo.Create(ctx, repo.CreateTaskInput{
		Slug:                slug,
		Title:               slug,
		Cwd:                 "/tmp",
		CurrentStage:        "implementation",
		Priority:            "medium",
		MaxIterations:       3,
		StageTimeoutSeconds: 1800,
	})
	require.NoError(t, err)
	return task
}

func TestFinalizeCompletedAsyncRuns_RateLimited_Requeues(t *testing.T) {
	ctx := context.Background()
	orch, taskRepo, srRepo := makeOrchestratorWithSRRepo(t)

	_, run := makeRunningStageRun(t, ctx, taskRepo, srRepo, 0)

	orch.SetCompletionDetector(func(_ *ent.StageRun, _ string, _ pipeline.CompletionDeps) (pipeline.CompletionResult, error) {
		return pipeline.CompletionResult{
			Kind:        "failed",
			Error:       "agent hit API rate/usage limit (status 429)",
			Infra:       true,
			RateLimited: true,
		}, nil
	})

	before := time.Now()
	err := orch.FinalizeCompletedAsyncRunsForTest(ctx, []*ent.StageRun{run})
	require.NoError(t, err)

	updated, err := srRepo.GetByID(ctx, run.ID)
	require.NoError(t, err)
	require.Equal(t, "requeued", updated.Status)
	require.Equal(t, 1, updated.RetryCount)
	require.NotNil(t, updated.NextRetryAt)
	// Rate-limit backoff is 600s (defaultRateLimitBackoff), much larger than infra backoff.
	require.True(t, updated.NextRetryAt.After(before.Add(500*time.Second)),
		"rate-limit requeue NextRetryAt must be ~600s out")
}

func TestFinalizeCompletedAsyncRuns_RateLimited_ExhaustedBudget_HardFails(t *testing.T) {
	ctx := context.Background()
	orch, taskRepo, srRepo := makeOrchestratorWithSRRepo(t)

	// defaultMaxRateLimitRetries is 36; start with retryCount == 36 → exhausted.
	_, run := makeRunningStageRun(t, ctx, taskRepo, srRepo, 36)

	orch.SetCompletionDetector(func(_ *ent.StageRun, _ string, _ pipeline.CompletionDeps) (pipeline.CompletionResult, error) {
		return pipeline.CompletionResult{
			Kind:        "failed",
			Error:       "agent hit API rate/usage limit (status 429)",
			Infra:       true,
			RateLimited: true,
		}, nil
	})

	err := orch.FinalizeCompletedAsyncRunsForTest(ctx, []*ent.StageRun{run})
	require.NoError(t, err)

	updated, err := srRepo.GetByID(ctx, run.ID)
	require.NoError(t, err)
	require.Equal(t, "failed", updated.Status)
	require.NotNil(t, updated.Output["rate_limit_retries_exhausted"], "output must contain rate_limit_retries_exhausted key")
	require.EqualValues(t, 36, updated.Output["rate_limit_retries_exhausted"])
}

func TestPicker_RequeuedRun_NotPicked(t *testing.T) {
	ctx := context.Background()
	orch, taskRepo, srRepo := makeOrchestratorWithSRRepo(t)

	task := makePickerTask(t, ctx, taskRepo, "requeued-guard-test")

	sr, err := srRepo.Create(ctx, repo.CreateStageRunInput{
		TaskID:      task.ID,
		Stage:       "implementation",
		Iteration:   0,
		SessionName: "requeued-guard-impl-0",
	})
	require.NoError(t, err)

	future := time.Now().Add(5 * time.Minute)
	retryCount := 1
	_, err = srRepo.Update(ctx, sr.ID, repo.UpdateStageRunInput{
		Status:      strPtr("requeued"),
		RetryCount:  &retryCount,
		NextRetryAt: &future,
	})
	require.NoError(t, err)

	orch.PickNextTasksForFreeSlots(ctx, nil)

	runs, err := srRepo.ListForTask(ctx, task.ID)
	require.NoError(t, err)
	for _, r := range runs {
		require.NotEqual(t, "running", r.Status, "requeued run must not be promoted to running during cooldown")
	}
}

func TestPicker_AfterSweepFlipsToPending_TaskIsPicked(t *testing.T) {
	ctx := context.Background()
	orch, taskRepo, srRepo := makeOrchestratorWithSRRepo(t)

	task := makePickerTask(t, ctx, taskRepo, "sweep-then-pick-test")

	sr, err := srRepo.Create(ctx, repo.CreateStageRunInput{
		TaskID:      task.ID,
		Stage:       "implementation",
		Iteration:   0,
		SessionName: "sweep-then-pick-impl-0",
	})
	require.NoError(t, err)

	future := time.Now().Add(5 * time.Minute)
	retryCount := 1
	_, err = srRepo.Update(ctx, sr.ID, repo.UpdateStageRunInput{
		Status:      strPtr("requeued"),
		RetryCount:  &retryCount,
		NextRetryAt: &future,
	})
	require.NoError(t, err)

	// Simulate sweep: flip the run to pending (as sweepRequeueableRuns would do).
	_, err = srRepo.Update(ctx, sr.ID, repo.UpdateStageRunInput{
		Status:           strPtr("pending"),
		PIDClear:         true,
		StartedAtClear:   true,
		NextRetryAtClear: true,
	})
	require.NoError(t, err)

	orch.PickNextTasksForFreeSlots(ctx, nil)

	runs, err := srRepo.ListForTask(ctx, task.ID)
	require.NoError(t, err)
	picked := false
	for _, r := range runs {
		if r.Status == "running" {
			picked = true
		}
	}
	require.True(t, picked, "task must be picked after sweep flips run to pending")
}

func TestPicker_UniqueRunningInvariant_AtMostOneRunning(t *testing.T) {
	ctx := context.Background()
	orch, taskRepo, srRepo := makeOrchestratorWithSRRepo(t)

	task := makePickerTask(t, ctx, taskRepo, "unique-running-test")

	sr, err := srRepo.Create(ctx, repo.CreateStageRunInput{
		TaskID:      task.ID,
		Stage:       "implementation",
		Iteration:   0,
		SessionName: "unique-running-impl-0",
	})
	require.NoError(t, err)

	future := time.Now().Add(5 * time.Minute)
	retryCount := 1
	_, err = srRepo.Update(ctx, sr.ID, repo.UpdateStageRunInput{
		Status:      strPtr("requeued"),
		RetryCount:  &retryCount,
		NextRetryAt: &future,
	})
	require.NoError(t, err)

	// Simulate sweep: promote to pending.
	_, err = srRepo.Update(ctx, sr.ID, repo.UpdateStageRunInput{
		Status:           strPtr("pending"),
		PIDClear:         true,
		StartedAtClear:   true,
		NextRetryAtClear: true,
	})
	require.NoError(t, err)

	orch.PickNextTasksForFreeSlots(ctx, nil)

	runs, err := srRepo.ListForTask(ctx, task.ID)
	require.NoError(t, err)
	runningCount := 0
	for _, r := range runs {
		if r.Status == "running" {
			runningCount++
		}
	}
	require.LessOrEqual(t, runningCount, 1, "at most one stage_run may be in running status for the same task")
}
