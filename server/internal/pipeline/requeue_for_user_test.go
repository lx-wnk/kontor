package pipeline_test

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/lx-wnk/kontor/server/internal/db"
	"github.com/lx-wnk/kontor/server/internal/db/repo"
	"github.com/lx-wnk/kontor/server/internal/pipeline"
)

// TestRequeueForUser_FailedRun_CreatesNewPendingRun verifies that when the
// latest stage_run on the current stage is failed, RequeueForUser creates a
// new pending run at iteration+1 with pending_user_prompt set, leaving the
// original failed run untouched.
func TestRequeueForUser_FailedRun_CreatesNewPendingRun(t *testing.T) {
	ctx := context.Background()
	bundle := openSharedBundle(t)
	orch, taskRepo := makeOrchFromBundle(t, bundle)
	srRepo := repo.NewStageRunRepo(bundle.Client)

	task, err := taskRepo.Create(ctx, repo.CreateTaskInput{
		Slug:          "requeue-failed",
		Title:         "Requeue Failed Run",
		Cwd:           "/tmp",
		CurrentStage:  "implementation",
		Priority:      "medium",
		MaxIterations: 5,
	})
	require.NoError(t, err)

	// Seed a failed run at iteration 0.
	priorRun, err := srRepo.Create(ctx, repo.CreateStageRunInput{
		TaskID:      task.ID,
		Stage:       "implementation",
		Iteration:   0,
		SessionName: "requeue-failed-impl-0",
	})
	require.NoError(t, err)
	failed := "failed"
	_, err = srRepo.Update(ctx, priorRun.ID, repo.UpdateStageRunInput{Status: &failed})
	require.NoError(t, err)

	const prompt = "also handle edge cases"
	newRun, err := orch.RequeueForUser(ctx, task.ID, prompt)
	require.NoError(t, err)
	require.NotNil(t, newRun, "expected new pending run to be created")

	require.Equal(t, "pending", newRun.Status, "new run must start as pending")
	require.Equal(t, 1, newRun.Iteration, "new run must have iteration = prior+1")
	require.NotNil(t, newRun.PendingUserPrompt, "pending_user_prompt must be set")
	require.Equal(t, prompt, *newRun.PendingUserPrompt)

	// Original failed run must be untouched.
	refreshed, err := srRepo.GetByID(ctx, priorRun.ID)
	require.NoError(t, err)
	require.Equal(t, "failed", refreshed.Status, "original failed run must remain failed")
}

// TestRequeueForUser_AwaitingUserDeadPID_ReapsAndCreatesNewRun verifies that
// an awaiting_user run whose PID is dead is reaped (marked failed) before the
// new pending run is created.
func TestRequeueForUser_AwaitingUserDeadPID_ReapsAndCreatesNewRun(t *testing.T) {
	ctx := context.Background()
	bundle := openSharedBundle(t)
	orch, taskRepo := makeOrchFromBundle(t, bundle)
	srRepo := repo.NewStageRunRepo(bundle.Client)

	task, err := taskRepo.Create(ctx, repo.CreateTaskInput{
		Slug:          "requeue-await-dead",
		Title:         "Requeue Awaiting Dead PID",
		Cwd:           "/tmp",
		CurrentStage:  "implementation",
		Priority:      "medium",
		MaxIterations: 5,
	})
	require.NoError(t, err)

	// Seed a run that is awaiting_user with a dead PID (extremely high PID that
	// no real process in the test suite will own).
	awaitRun, err := srRepo.Create(ctx, repo.CreateStageRunInput{
		TaskID:      task.ID,
		Stage:       "implementation",
		Iteration:   0,
		SessionName: "requeue-await-dead-impl-0",
	})
	require.NoError(t, err)
	awaitStatus := "awaiting_user"
	deadPID := 999999999
	_, err = srRepo.Update(ctx, awaitRun.ID, repo.UpdateStageRunInput{
		Status: &awaitStatus,
		PID:    &deadPID,
	})
	require.NoError(t, err)

	newRun, err := orch.RequeueForUser(ctx, task.ID, "")
	require.NoError(t, err)
	require.NotNil(t, newRun, "expected new pending run to be created after reap")
	require.Equal(t, "pending", newRun.Status)

	// The awaiting_user run must now be failed (reaped).
	reaped, err := srRepo.GetByID(ctx, awaitRun.ID)
	require.NoError(t, err)
	require.Equal(t, "failed", reaped.Status, "awaiting_user run with dead PID must be reaped to failed")
}

// TestRequeueForUser_TerminalStage_ReturnsNil verifies that RequeueForUser
// returns (nil, nil) when the task's current stage is terminal (done/cancelled).
func TestRequeueForUser_TerminalStage_ReturnsNil(t *testing.T) {
	ctx := context.Background()
	for _, stage := range []string{"done", "cancelled"} {
		t.Run(stage, func(t *testing.T) {
			bundle := openSharedBundle(t)
			orch, taskRepo := makeOrchFromBundle(t, bundle)

			task, err := taskRepo.Create(ctx, repo.CreateTaskInput{
				Slug:          "requeue-terminal-" + stage,
				Title:         "Terminal Stage",
				Cwd:           "/tmp",
				CurrentStage:  stage,
				Priority:      "medium",
				MaxIterations: 5,
			})
			require.NoError(t, err)

			result, err := orch.RequeueForUser(ctx, task.ID, "prompt")
			require.NoError(t, err)
			require.Nil(t, result, "RequeueForUser must return nil for terminal stage %s", stage)
		})
	}
}

// TestRequeueForUser_NoFailedOrRequeuedRun_ReturnsNil verifies that
// RequeueForUser returns (nil, nil) when the latest run for the current stage
// is not in failed or requeued status (e.g. still pending/running).
func TestRequeueForUser_NoFailedOrRequeuedRun_ReturnsNil(t *testing.T) {
	ctx := context.Background()
	bundle := openSharedBundle(t)
	orch, taskRepo := makeOrchFromBundle(t, bundle)
	srRepo := repo.NewStageRunRepo(bundle.Client)

	task, err := taskRepo.Create(ctx, repo.CreateTaskInput{
		Slug:          "requeue-no-failed",
		Title:         "No Failed Run",
		Cwd:           "/tmp",
		CurrentStage:  "implementation",
		Priority:      "medium",
		MaxIterations: 5,
	})
	require.NoError(t, err)

	// Latest run is pending (not failed/requeued).
	_, err = srRepo.Create(ctx, repo.CreateStageRunInput{
		TaskID:      task.ID,
		Stage:       "implementation",
		Iteration:   0,
		SessionName: "requeue-no-failed-impl-0",
	})
	require.NoError(t, err)

	result, err := orch.RequeueForUser(ctx, task.ID, "prompt")
	require.NoError(t, err)
	require.Nil(t, result, "RequeueForUser must return nil when latest run is not failed/requeued")
}

// TestRequeueForUser_RequeuedRun_CreatesNewPendingRun verifies that a run in
// "requeued" status (auto-retry backoff) is also accepted as the prior run,
// since the task has been re-queued for user action.
func TestRequeueForUser_RequeuedRun_CreatesNewPendingRun(t *testing.T) {
	ctx := context.Background()
	bundle := openSharedBundle(t)
	orch, taskRepo := makeOrchFromBundle(t, bundle)
	srRepo := repo.NewStageRunRepo(bundle.Client)

	task, err := taskRepo.Create(ctx, repo.CreateTaskInput{
		Slug:          "requeue-from-requeued",
		Title:         "Requeue From Requeued",
		Cwd:           "/tmp",
		CurrentStage:  "implementation",
		Priority:      "medium",
		MaxIterations: 5,
	})
	require.NoError(t, err)

	priorRun, err := srRepo.Create(ctx, repo.CreateStageRunInput{
		TaskID:      task.ID,
		Stage:       "implementation",
		Iteration:   2,
		SessionName: "requeue-from-requeued-impl-2",
	})
	require.NoError(t, err)
	requeued := "requeued"
	_, err = srRepo.Update(ctx, priorRun.ID, repo.UpdateStageRunInput{Status: &requeued})
	require.NoError(t, err)

	newRun, err := orch.RequeueForUser(ctx, task.ID, "user hint")
	require.NoError(t, err)
	require.NotNil(t, newRun)
	require.Equal(t, "pending", newRun.Status)
	require.Equal(t, 3, newRun.Iteration, "new run must be iteration = prior(2)+1")
}

// TestPicker_AfterRequeueForUser_TaskIsEligible verifies that after
// RequeueForUser the task's latest run is pending, so the picker treats it
// as eligible (not blocked by failed/awaiting_user/requeued guard).
func TestPicker_AfterRequeueForUser_TaskIsEligible(t *testing.T) {
	ctx := context.Background()
	bundle := openSharedBundle(t)
	orch, taskRepo := makeOrchFromBundle(t, bundle)
	srRepo := repo.NewStageRunRepo(bundle.Client)

	// Install a stub so ProgressTask doesn't try to spawn a real agent.
	orch.SetHandlerOverride("implementation", &agentStubHandler{
		stage:      "implementation",
		transition: pipeline.AsyncRunningTransition{PID: pipeline.SyntheticSpawnPIDForTest},
	})

	task, err := taskRepo.Create(ctx, repo.CreateTaskInput{
		Slug:          "picker-after-requeue",
		Title:         "Picker After Requeue",
		Cwd:           "/tmp",
		CurrentStage:  "implementation",
		Priority:      "medium",
		MaxIterations: 5,
	})
	require.NoError(t, err)

	// Seed a failed run so RequeueForUser has something to work with.
	priorRun, err := srRepo.Create(ctx, repo.CreateStageRunInput{
		TaskID:      task.ID,
		Stage:       "implementation",
		Iteration:   0,
		SessionName: "picker-after-requeue-impl-0",
	})
	require.NoError(t, err)
	failed := "failed"
	_, err = srRepo.Update(ctx, priorRun.ID, repo.UpdateStageRunInput{Status: &failed})
	require.NoError(t, err)

	newRun, err := orch.RequeueForUser(ctx, task.ID, "")
	require.NoError(t, err)
	require.NotNil(t, newRun)
	require.Equal(t, "pending", newRun.Status)

	// Drive the picker — it should pick the newly pending run.
	orch.PickNextTasksForFreeSlots(ctx, nil)

	runs, err := srRepo.ListForTask(ctx, task.ID)
	require.NoError(t, err)
	picked := false
	for _, r := range runs {
		if r.Status == "running" {
			picked = true
		}
	}
	require.True(t, picked, "task must be picked by the picker after RequeueForUser creates a pending run")
}

// captureSpawnFn captures the SpawnAgentOptions passed to the spawn function so
// tests can assert which ResumeSessionID and UserAdditionalPrompt were forwarded.
type captureSpawnFn struct {
	mu   sync.Mutex
	opts *pipeline.SpawnAgentOptions
}

func (c *captureSpawnFn) spawn(opts pipeline.SpawnAgentOptions) (pipeline.SpawnResult, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	captured := opts
	c.opts = &captured
	return pipeline.SpawnResult{PID: pipeline.SyntheticSpawnPIDForTest, Cwd: opts.Task.Cwd, Cleanup: func() {}}, nil
}

func (c *captureSpawnFn) capturedOpts() *pipeline.SpawnAgentOptions {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.opts
}

// makeOrchWithCaptureSpawn returns an orchestrator that records spawn args.
func makeOrchWithCaptureSpawn(t *testing.T, spawnCapture *captureSpawnFn) (*pipeline.PipelineOrchestrator, repo.TaskRepo, repo.StageRunRepo, *db.DBBundle) {
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
		SpawnFn:        spawnCapture.spawn,
	})
	require.NoError(t, err)
	return orch, taskRepo, srRepo, bundle
}

// TestSpawnPath_ConsumesPersistedPromptAndDerivesResumeSession drives a
// ProgressTask call on a task whose pending run carries pending_user_prompt
// AND a prior run on the same stage has an existing session JSONL on disk.
// The spawn capture must receive both the persisted prompt and the DB-derived
// resume session ID.
func TestSpawnPath_ConsumesPersistedPromptAndDerivesResumeSession(t *testing.T) {
	ctx := context.Background()
	cwd := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())

	capture := &captureSpawnFn{}
	orch, taskRepo, srRepo, _ := makeOrchWithCaptureSpawn(t, capture)

	task, err := taskRepo.Create(ctx, repo.CreateTaskInput{
		Slug:          "spawn-path-test",
		Title:         "Spawn Path Test",
		Cwd:           cwd,
		CurrentStage:  "implementation",
		Priority:      "medium",
		MaxIterations: 5,
	})
	require.NoError(t, err)

	// Create a prior run with a session JSONL that exists on disk.
	const sessionID = "prior-session-abc"
	priorRun, err := srRepo.Create(ctx, repo.CreateStageRunInput{
		TaskID:      task.ID,
		Stage:       "implementation",
		Iteration:   0,
		SessionName: "spawn-path-impl-0",
	})
	require.NoError(t, err)
	failed := "failed"
	sessionIDVal := sessionID
	_, err = srRepo.Update(ctx, priorRun.ID, repo.UpdateStageRunInput{
		Status:    &failed,
		SessionID: &sessionIDVal,
	})
	require.NoError(t, err)

	// Write the session JSONL to disk so SessionFileExists returns true.
	projectDir, err := pipeline.ResolvedProjectDir(cwd)
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(projectDir, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(projectDir, sessionID+".jsonl"), []byte("{}\n"), 0o600))

	// Create a pending run at iteration 1 with a persisted user prompt — this
	// simulates what RequeueForUser would have produced.
	const userPrompt = "also add logging"
	pendingRun, err := srRepo.Create(ctx, repo.CreateStageRunInput{
		TaskID:            task.ID,
		Stage:             "implementation",
		Iteration:         1,
		SessionName:       "spawn-path-impl-1",
		PendingUserPrompt: userPrompt,
	})
	require.NoError(t, err)
	// The run is already pending by default; mark it explicitly to match the requeue path.
	pending := "pending"
	_, err = srRepo.Update(ctx, pendingRun.ID, repo.UpdateStageRunInput{Status: &pending})
	require.NoError(t, err)

	// ProgressTask with nil opts — orchestrator must read prompt from DB and
	// derive resume session from the prior run on disk.
	sr, err := orch.ProgressTask(ctx, task.ID, nil)
	require.NoError(t, err)
	require.NotNil(t, sr)

	opts := capture.capturedOpts()
	require.NotNil(t, opts, "spawn function must have been called")
	require.Equal(t, sessionID, opts.ResumeSessionID,
		"spawn must receive the DB-derived resume session ID")
	require.Contains(t, opts.Prompt, userPrompt,
		"spawn prompt must include the persisted user additional prompt")
}

// TestSpawnPath_NoSessionFile_FreshSpawn verifies that when the prior run's
// session JSONL does not exist on disk, the spawn receives an empty
// ResumeSessionID (fresh spawn, not a resume).
func TestSpawnPath_NoSessionFile_FreshSpawn(t *testing.T) {
	ctx := context.Background()
	cwd := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())

	capture := &captureSpawnFn{}
	orch, taskRepo, srRepo, _ := makeOrchWithCaptureSpawn(t, capture)

	task, err := taskRepo.Create(ctx, repo.CreateTaskInput{
		Slug:          "spawn-fresh-test",
		Title:         "Spawn Fresh Test",
		Cwd:           cwd,
		CurrentStage:  "implementation",
		Priority:      "medium",
		MaxIterations: 5,
	})
	require.NoError(t, err)

	// Prior run with a session ID, but the JSONL is NOT written to disk.
	priorRun, err := srRepo.Create(ctx, repo.CreateStageRunInput{
		TaskID:      task.ID,
		Stage:       "implementation",
		Iteration:   0,
		SessionName: "spawn-fresh-impl-0",
	})
	require.NoError(t, err)
	failed := "failed"
	gone := "session-gone"
	_, err = srRepo.Update(ctx, priorRun.ID, repo.UpdateStageRunInput{
		Status:    &failed,
		SessionID: &gone,
	})
	require.NoError(t, err)

	// Pending run at iteration 1 with no user prompt.
	pendingRun, err := srRepo.Create(ctx, repo.CreateStageRunInput{
		TaskID:      task.ID,
		Stage:       "implementation",
		Iteration:   1,
		SessionName: "spawn-fresh-impl-1",
	})
	require.NoError(t, err)
	pending := "pending"
	_, err = srRepo.Update(ctx, pendingRun.ID, repo.UpdateStageRunInput{Status: &pending})
	require.NoError(t, err)

	sr, err := orch.ProgressTask(ctx, task.ID, nil)
	require.NoError(t, err)
	require.NotNil(t, sr)

	opts := capture.capturedOpts()
	require.NotNil(t, opts, "spawn function must have been called")
	require.Empty(t, opts.ResumeSessionID,
		"spawn must NOT receive a resume session ID when the JSONL is absent from disk")
}

// TestSpawnPath_NoSessionID_FreshSpawn verifies that when the prior run has no
// session_id at all, the spawn receives an empty ResumeSessionID.
func TestSpawnPath_NoSessionID_FreshSpawn(t *testing.T) {
	ctx := context.Background()
	cwd := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())

	capture := &captureSpawnFn{}
	orch, taskRepo, srRepo, _ := makeOrchWithCaptureSpawn(t, capture)

	task, err := taskRepo.Create(ctx, repo.CreateTaskInput{
		Slug:          "spawn-no-sid-test",
		Title:         "Spawn No SessionID Test",
		Cwd:           cwd,
		CurrentStage:  "implementation",
		Priority:      "medium",
		MaxIterations: 5,
	})
	require.NoError(t, err)

	// Prior run without any session_id.
	priorRun, err := srRepo.Create(ctx, repo.CreateStageRunInput{
		TaskID:      task.ID,
		Stage:       "implementation",
		Iteration:   0,
		SessionName: "spawn-no-sid-impl-0",
	})
	require.NoError(t, err)
	failed := "failed"
	_, err = srRepo.Update(ctx, priorRun.ID, repo.UpdateStageRunInput{Status: &failed})
	require.NoError(t, err)

	pendingRun, err := srRepo.Create(ctx, repo.CreateStageRunInput{
		TaskID:      task.ID,
		Stage:       "implementation",
		Iteration:   1,
		SessionName: "spawn-no-sid-impl-1",
	})
	require.NoError(t, err)
	pending := "pending"
	_, err = srRepo.Update(ctx, pendingRun.ID, repo.UpdateStageRunInput{Status: &pending})
	require.NoError(t, err)

	sr, err := orch.ProgressTask(ctx, task.ID, nil)
	require.NoError(t, err)
	require.NotNil(t, sr)

	opts := capture.capturedOpts()
	require.NotNil(t, opts, "spawn function must have been called")
	require.Empty(t, opts.ResumeSessionID, "spawn must use fresh spawn when no session_id was recorded")
}
