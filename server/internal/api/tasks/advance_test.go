package tasks_test

import (
	"context"
	"testing"

	"github.com/lx-wnk/kontor/server/internal/api/tasks"
	"github.com/lx-wnk/kontor/server/internal/db"
	"github.com/lx-wnk/kontor/server/internal/db/ent"
	"github.com/lx-wnk/kontor/server/internal/db/repo"
	"github.com/lx-wnk/kontor/server/internal/pipeline"
)

// advanceOrchestrator is a test stub that tracks which Advance-dispatcher
// branches were exercised. All methods satisfy OrchestratorIface.
type advanceOrchestrator struct {
	requeueCalled  bool
	resumeCalled   bool
	progressCalled bool
}

func (a *advanceOrchestrator) RequeueForUser(_ context.Context, _ string, _ string) (*ent.StageRun, error) {
	a.requeueCalled = true
	return &ent.StageRun{ID: "requeued"}, nil
}
func (a *advanceOrchestrator) ResumeFromUser(_ context.Context, _ string, _ string) (*ent.StageRun, error) {
	a.resumeCalled = true
	return &ent.StageRun{ID: "resumed"}, nil
}
func (a *advanceOrchestrator) ProgressTask(_ context.Context, _ string, _ *pipeline.ProgressOpts) (*ent.StageRun, error) {
	a.progressCalled = true
	return &ent.StageRun{ID: "progressed"}, nil
}
func (a *advanceOrchestrator) NotifyTaskTerminated(_ context.Context, _, _ string)      {}
func (a *advanceOrchestrator) InvalidateConfigCache()                                   {}
func (a *advanceOrchestrator) ClearStalePendingPermissions(_ context.Context, _ string) {}
func (a *advanceOrchestrator) EffectiveStageModel(_ context.Context, _ string) string   { return "" }

func openAdvanceDB(t *testing.T) *ent.Client {
	t.Helper()
	bundle, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { _ = bundle.Client.Close() })
	return bundle.Client
}

func advanceDeps(t *testing.T, client *ent.Client, orch tasks.OrchestratorIface) tasks.AdvanceDeps {
	t.Helper()
	return tasks.AdvanceDeps{
		TaskRepo:     repo.NewTaskRepo(client),
		SRRepo:       repo.NewStageRunRepo(client),
		PermRepo:     repo.NewPermissionRepo(client),
		AuditRepo:    repo.NewAuditEventRepo(client),
		Orchestrator: orch,
	}
}

// seedTaskWithRun creates a task at the given stage with a stage run at the given status.
// When status is "", no run is created.
func seedTaskWithRun(t *testing.T, client *ent.Client, stage, runStatus string) string {
	t.Helper()
	ctx := context.Background()
	task, err := repo.NewTaskRepo(client).Create(ctx, repo.CreateTaskInput{
		Slug:          "adv-" + stage + "-" + runStatus,
		Title:         "Advance test",
		Cwd:           t.TempDir(),
		MaxIterations: 5,
		Priority:      "normal",
		CurrentStage:  stage,
	})
	if err != nil {
		t.Fatalf("create task: %v", err)
	}
	if runStatus != "" {
		srRepo := repo.NewStageRunRepo(client)
		run, err := srRepo.Create(ctx, repo.CreateStageRunInput{
			TaskID:    task.ID,
			Stage:     stage,
			Iteration: 0,
		})
		if err != nil {
			t.Fatalf("create run: %v", err)
		}
		if _, err := srRepo.Update(ctx, run.ID, repo.UpdateStageRunInput{Status: &runStatus}); err != nil {
			t.Fatalf("update run status: %v", err)
		}
	}
	return task.ID
}

// TestAdvance_FailedTask_CallsRequeue verifies that a failed task dispatches RequeueForUser.
func TestAdvance_FailedTask_CallsRequeue(t *testing.T) {
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
	client := openAdvanceDB(t)
	orch := &advanceOrchestrator{}
	taskID := seedTaskWithRun(t, client, "implementation", "failed")

	res, err := tasks.Advance(context.Background(), advanceDeps(t, client, orch), taskID)
	if err != nil {
		t.Fatalf("Advance error: %v", err)
	}
	if !res.Dispatched {
		t.Fatalf("expected dispatched=true, got false (primary=%q)", res.Primary)
	}
	if res.Primary != "retry" {
		t.Errorf("expected primary=retry, got %q", res.Primary)
	}
	if !orch.requeueCalled {
		t.Error("expected RequeueForUser to be called")
	}
}

// TestAdvance_AwaitingWithPending_CallsApproveAll verifies the approve_all_pending branch.
func TestAdvance_AwaitingWithPending_CallsApproveAll(t *testing.T) {
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
	client := openAdvanceDB(t)
	orch := &advanceOrchestrator{}

	// Seed task with awaiting_user run + a pending permission request.
	taskID := seedTaskWithRun(t, client, "implementation", "awaiting_user")
	permRepo := repo.NewPermissionRepo(client)
	srRepo := repo.NewStageRunRepo(client)
	runs, _ := srRepo.ListForTask(context.Background(), taskID)
	if len(runs) == 0 {
		t.Fatal("expected at least one run")
	}
	pattern := "echo hi"
	if _, err := permRepo.CreatePermissionRequest(context.Background(), repo.CreatePermissionRequestInput{
		StageRunID: runs[0].ID,
		Tool:       "Bash",
		Pattern:    &pattern,
	}); err != nil {
		t.Fatalf("create perm request: %v", err)
	}

	res, err := tasks.Advance(context.Background(), advanceDeps(t, client, orch), taskID)
	if err != nil {
		t.Fatalf("Advance error: %v", err)
	}
	if !res.Dispatched {
		t.Fatalf("expected dispatched=true, primary=%q", res.Primary)
	}
	if res.Primary != "approve_all_pending" {
		t.Errorf("expected primary=approve_all_pending, got %q", res.Primary)
	}
}

// TestAdvance_ConceptWithDraftSpec_DoesNotAutoApproveSpec verifies that a task
// in concept stage with a spec-ready run returns dispatched=false and does NOT
// call any orchestrator method.
func TestAdvance_ConceptWithDraftSpec_DoesNotAutoApproveSpec(t *testing.T) {
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
	client := openAdvanceDB(t)
	orch := &advanceOrchestrator{}
	// concept stage, run.done → primary is approve_spec
	taskID := seedTaskWithRun(t, client, "backlog", "done")

	res, err := tasks.Advance(context.Background(), advanceDeps(t, client, orch), taskID)
	if err != nil {
		t.Fatalf("Advance error: %v", err)
	}
	if res.Dispatched {
		t.Errorf("advance must NOT auto-approve spec (dispatched=true is wrong)")
	}
	if res.Primary != "approve_spec" {
		t.Errorf("expected primary=approve_spec, got %q", res.Primary)
	}
	if res.Message == "" {
		t.Error("expected non-empty message for approve_spec no-op")
	}
	if orch.requeueCalled || orch.resumeCalled || orch.progressCalled {
		t.Error("no orchestrator method must be called for approve_spec path")
	}
}

// TestAdvance_StageDone_CallsProgressTask verifies the advance branch (stage run done).
func TestAdvance_StageDone_CallsProgressTask(t *testing.T) {
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
	client := openAdvanceDB(t)
	orch := &advanceOrchestrator{}
	// implementation/done → primary is "advance" → ProgressTask
	taskID := seedTaskWithRun(t, client, "implementation", "done")

	res, err := tasks.Advance(context.Background(), advanceDeps(t, client, orch), taskID)
	if err != nil {
		t.Fatalf("Advance error: %v", err)
	}
	if !res.Dispatched {
		t.Fatalf("expected dispatched=true, primary=%q", res.Primary)
	}
	if res.Primary != "advance" {
		t.Errorf("expected primary=advance, got %q", res.Primary)
	}
	if !orch.progressCalled {
		t.Error("expected ProgressTask to be called")
	}
}

// TestAdvance_TerminalTask_NoDispatch verifies that done/cancelled tasks return dispatched=false.
func TestAdvance_TerminalTask_NoDispatch(t *testing.T) {
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
	for _, stage := range []string{"done", "cancelled"} {
		t.Run(stage, func(t *testing.T) {
			client := openAdvanceDB(t)
			orch := &advanceOrchestrator{}
			// Terminal stages: update the task's CurrentStage directly.
			taskID := seedTaskWithRun(t, client, "implementation", "failed")
			// Move task to terminal stage.
			_, err := repo.NewTaskRepo(client).Update(context.Background(), taskID, repo.UpdateTaskInput{CurrentStage: &stage})
			if err != nil {
				t.Fatalf("update stage: %v", err)
			}

			res, err := tasks.Advance(context.Background(), advanceDeps(t, client, orch), taskID)
			if err != nil {
				t.Fatalf("Advance error: %v", err)
			}
			if res.Dispatched {
				t.Errorf("expected dispatched=false for terminal stage %q", stage)
			}
			if orch.requeueCalled || orch.resumeCalled || orch.progressCalled {
				t.Error("no orchestrator method must be called for terminal stage")
			}
		})
	}
}

// stubRefineReader feeds a fixed refine status into Advance.
type stubRefineReader struct{ status string }

func (s stubRefineReader) State(_ string) (string, string) { return s.status, "" }

// TestAdvance_ConceptRefining_Dispatches verifies that when the refine runner
// reports "refining" on a concept task, Advance computes primary=advance (and
// dispatches ProgressTask) rather than the approve_spec no-op.
func TestAdvance_ConceptRefining_Dispatches(t *testing.T) {
	client := openAdvanceDB(t)
	orch := &advanceOrchestrator{}
	taskID := seedTaskWithRun(t, client, "backlog", "")
	deps := advanceDeps(t, client, orch)
	deps.RefineReader = stubRefineReader{status: "refining"}

	res, err := tasks.Advance(context.Background(), deps, taskID)
	if err != nil {
		t.Fatalf("Advance: %v", err)
	}
	if res.Primary != "advance" || !res.Dispatched {
		t.Errorf("expected refining concept to dispatch advance, got primary=%q dispatched=%v", res.Primary, res.Dispatched)
	}
	if !orch.progressCalled {
		t.Error("expected ProgressTask to be called")
	}
}
