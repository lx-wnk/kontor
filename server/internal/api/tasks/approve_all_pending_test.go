package tasks_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/lx-wnk/kontor/server/internal/api/tasks"
	"github.com/lx-wnk/kontor/server/internal/auth"
	"github.com/lx-wnk/kontor/server/internal/db"
	"github.com/lx-wnk/kontor/server/internal/db/ent"
	"github.com/lx-wnk/kontor/server/internal/db/repo"
	"github.com/lx-wnk/kontor/server/internal/pipeline"
	"github.com/lx-wnk/kontor/server/internal/sse"
)

// requeueCapture records whether RequeueForUser was called and for which task.
type requeueCapture struct {
	called bool
	taskID string
}

func (c *requeueCapture) ProgressTask(_ context.Context, _ string, _ *pipeline.ProgressOpts) (*ent.StageRun, error) {
	return nil, nil
}
func (c *requeueCapture) ResumeFromUser(_ context.Context, _ string, _ string) (*ent.StageRun, error) {
	return nil, nil
}
func (c *requeueCapture) RequeueForUser(_ context.Context, taskID, _ string) (*ent.StageRun, error) {
	c.called = true
	c.taskID = taskID
	return &ent.StageRun{ID: taskID + "-requeued"}, nil
}
func (c *requeueCapture) NotifyTaskTerminated(_ context.Context, _, _ string)      {}
func (c *requeueCapture) InvalidateConfigCache()                                   {}
func (c *requeueCapture) ClearStalePendingPermissions(_ context.Context, _ string) {}
func (c *requeueCapture) EffectiveStageModel(_ context.Context, _ string) string   { return "" }

func newApproveAllHandler(t *testing.T, orch tasks.OrchestratorIface) (*ent.Client, *chi.Mux) {
	t.Helper()
	bundle, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	client := bundle.Client
	t.Cleanup(func() { _ = client.Close() })

	h := tasks.NewHandler(tasks.Deps{
		TaskRepo:     repo.NewTaskRepo(client),
		SRRepo:       repo.NewStageRunRepo(client),
		PermRepo:     repo.NewPermissionRepo(client),
		AuditRepo:    repo.NewAuditEventRepo(client),
		CfgRepo:      repo.NewPipelineConfigRepo(client),
		Orchestrator: orch,
		Broadcaster:  sse.NewTaskBroadcaster(sse.NewBroadcaster()),
	})
	r := chi.NewRouter()
	r.Use(auth.RequireAuth(testJWTSecret))
	h.Mount(r)
	return client, r
}

// seedAwaitingWithPermissions creates a task whose latest stage run is
// awaiting_user and has n pending permission_requests attached to it.
func seedAwaitingWithPermissions(t *testing.T, client *ent.Client, n int) (taskID, stageRunID string) {
	t.Helper()
	ctx := context.Background()
	taskRepo := repo.NewTaskRepo(client)
	srRepo := repo.NewStageRunRepo(client)
	permRepo := repo.NewPermissionRepo(client)

	task, err := taskRepo.Create(ctx, repo.CreateTaskInput{
		Slug:          "approve-pending",
		Title:         "Approve Pending",
		Cwd:           t.TempDir(),
		MaxIterations: 5,
		Priority:      "normal",
		CurrentStage:  "implementation",
	})
	if err != nil {
		t.Fatalf("create task: %v", err)
	}

	run, err := srRepo.Create(ctx, repo.CreateStageRunInput{
		TaskID:    task.ID,
		Stage:     "implementation",
		Iteration: 0,
	})
	if err != nil {
		t.Fatalf("create stage run: %v", err)
	}
	awaiting := "awaiting_user"
	if _, err := srRepo.Update(ctx, run.ID, repo.UpdateStageRunInput{Status: &awaiting}); err != nil {
		t.Fatalf("update stage run: %v", err)
	}

	for i := range n {
		_, err := permRepo.CreatePermissionRequest(ctx, repo.CreatePermissionRequestInput{
			StageRunID: run.ID,
			Tool:       "Bash",
			Pattern:    strPtr(t, "echo "+string(rune('a'+i))),
		})
		if err != nil {
			t.Fatalf("create perm request: %v", err)
		}
	}
	return task.ID, run.ID
}

func strPtr(_ *testing.T, s string) *string { return &s }

func postApproveAll(t *testing.T, r *chi.Mux, taskID string) *httptest.ResponseRecorder {
	t.Helper()
	req := withAuth(t, httptest.NewRequest(http.MethodPost, "/api/tasks/"+taskID+"/approve-all-pending", nil))
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

// TestApproveAllPending_ApprovesAndRequeues verifies the happy path:
// two pending requests → both approved, awaiting_user task → requeued:true.
func TestApproveAllPending_ApprovesAndRequeues(t *testing.T) {
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
	orch := &requeueCapture{}
	client, r := newApproveAllHandler(t, orch)
	taskID, runID := seedAwaitingWithPermissions(t, client, 2)
	_ = runID

	w := postApproveAll(t, r, taskID)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp map[string]any
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if got := resp["approved"].(float64); got != 2 {
		t.Errorf("expected approved=2, got %v", got)
	}
	if !resp["requeued"].(bool) {
		t.Errorf("expected requeued=true")
	}
	if !orch.called || orch.taskID != taskID {
		t.Errorf("expected RequeueForUser called with taskID=%q, called=%v taskID=%q", taskID, orch.called, orch.taskID)
	}

	// Verify requests are now resolved.
	permRepo := repo.NewPermissionRepo(client)
	srRepo := repo.NewStageRunRepo(client)
	runs, _ := srRepo.ListForTask(context.Background(), taskID)
	runIDs := make([]string, len(runs))
	for i, sr := range runs {
		runIDs[i] = sr.ID
	}
	still, _ := permRepo.ListPendingForTask(context.Background(), taskID, runIDs)
	if len(still) != 0 {
		t.Errorf("expected 0 pending after approve-all, got %d", len(still))
	}
}

// TestApproveAllPending_NoPendingPerms verifies the handler returns approved=0
// and requeued=false when there are no pending permission requests.
func TestApproveAllPending_NoPendingPerms(t *testing.T) {
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
	orch := &requeueCapture{}
	client, r := newApproveAllHandler(t, orch)

	// Seed a task with no permission requests.
	taskID, _ := seedAwaitingWithPermissions(t, client, 0)

	w := postApproveAll(t, r, taskID)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp map[string]any
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if got := resp["approved"].(float64); got != 0 {
		t.Errorf("expected approved=0, got %v", got)
	}
	if resp["requeued"].(bool) {
		t.Errorf("expected requeued=false when no pending perms")
	}
}

// TestApproveAllPending_UnknownTask returns 404.
func TestApproveAllPending_UnknownTask(t *testing.T) {
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
	_, r := newApproveAllHandler(t, &requeueCapture{})
	w := postApproveAll(t, r, "nonexistent-task-id")
	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", w.Code)
	}
}

// TestApproveAllPending_RecordsDecidedBy verifies the fourth, distinct grant
// path (ApproveAllPending, shared by the REST endpoint and the MCP tool)
// stamps decided_by/decided_at on the resulting task_permission.
func TestApproveAllPending_RecordsDecidedBy(t *testing.T) {
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
	client, r := newApproveAllHandler(t, &requeueCapture{})
	taskID, _ := seedAwaitingWithPermissions(t, client, 1)

	w := postApproveAll(t, r, taskID)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	grants, err := repo.NewPermissionRepo(client).ListTaskPermissions(context.Background(), taskID)
	if err != nil {
		t.Fatalf("list task permissions: %v", err)
	}
	if len(grants) == 0 {
		t.Fatal("expected a task_permission after approve-all, got none")
	}
	// "user-1" is the Sub withAuth signs into postApproveAll's request JWT.
	if grants[0].DecidedBy == nil || *grants[0].DecidedBy != "user-1" {
		t.Errorf("expected decided_by=%q, got %v", "user-1", grants[0].DecidedBy)
	}
	if grants[0].DecidedAt == nil {
		t.Error("expected decided_at to be recorded, got nil")
	}
}

// TestApproveAllPending_WritesGrantedOutcome pins the value, not just the count.
//
// ApproveAllPending used to write the outcome "approved" while every other
// resolver wrote "granted". The ACP permission gate authorizes on
// repo.OutcomeGranted alone and reads anything else as a refusal, so approving
// through this path denied the call for an ACP-backed agent. The pre-existing
// happy-path test only asserted that nothing stayed pending, which "approved"
// satisfied — hence this assertion on the stored value.
func TestApproveAllPending_WritesGrantedOutcome(t *testing.T) {
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
	client, r := newApproveAllHandler(t, &requeueCapture{})
	taskID, runID := seedAwaitingWithPermissions(t, client, 1)

	ctx := context.Background()
	permRepo := repo.NewPermissionRepo(client)
	pending, err := permRepo.ListPendingForStageRun(ctx, runID)
	if err != nil || len(pending) != 1 {
		t.Fatalf("expected 1 pending request, got %d (err=%v)", len(pending), err)
	}
	reqID := pending[0].ID

	if w := postApproveAll(t, r, taskID); w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	resolved, err := permRepo.GetPermissionRequest(ctx, reqID)
	if err != nil {
		t.Fatalf("get resolved request: %v", err)
	}
	if resolved.Outcome == nil {
		t.Fatalf("outcome is nil — request was not resolved")
	}
	if *resolved.Outcome != repo.OutcomeGranted {
		t.Errorf("outcome = %q, want %q (the only value the ACP gate authorizes on)",
			*resolved.Outcome, repo.OutcomeGranted)
	}
}
