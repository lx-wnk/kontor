package tasks_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
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

// lifecycleOrchestrator tracks which lifecycle methods were called.
type lifecycleOrchestrator struct {
	requeueCalled   bool
	resumeCalled    bool
	resumePrompt    string
	progressCalled  bool
	terminateCalled bool
}

func (o *lifecycleOrchestrator) RequeueForUser(_ context.Context, _ string, _ string) (*ent.StageRun, error) {
	o.requeueCalled = true
	return &ent.StageRun{ID: "requeued"}, nil
}
func (o *lifecycleOrchestrator) ResumeFromUser(_ context.Context, _ string, userPrompt string) (*ent.StageRun, error) {
	o.resumeCalled = true
	o.resumePrompt = userPrompt
	return &ent.StageRun{ID: "resumed"}, nil
}
func (o *lifecycleOrchestrator) ProgressTask(_ context.Context, _ string, _ *pipeline.ProgressOpts) (*ent.StageRun, error) {
	o.progressCalled = true
	return &ent.StageRun{ID: "progressed"}, nil
}
func (o *lifecycleOrchestrator) NotifyTaskTerminated(_ context.Context, _, _ string) {
	o.terminateCalled = true
}
func (o *lifecycleOrchestrator) InvalidateConfigCache()                                   {}
func (o *lifecycleOrchestrator) ClearStalePendingPermissions(_ context.Context, _ string) {}
func (o *lifecycleOrchestrator) EffectiveStageModel(_ context.Context, _ string) string   { return "" }

func newLifecycleHandler(t *testing.T, orch tasks.OrchestratorIface) (*ent.Client, *chi.Mux) {
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

// TestAdvanceREST_FailedTask_Returns200Dispatched verifies POST /advance on a
// failed task returns 200 with dispatched:true and calls RequeueForUser.
func TestAdvanceREST_FailedTask_Returns200Dispatched(t *testing.T) {
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
	orch := &lifecycleOrchestrator{}
	client, r := newLifecycleHandler(t, orch)
	taskID := seedTaskWithRun(t, client, "implementation", "failed")

	req := withAuth(t, httptest.NewRequest(http.MethodPost, "/api/tasks/"+taskID+"/advance", nil))
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var resp map[string]any
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !resp["dispatched"].(bool) {
		t.Errorf("expected dispatched=true")
	}
	if resp["primary"] != "retry" {
		t.Errorf("expected primary=retry, got %v", resp["primary"])
	}
	if !orch.requeueCalled {
		t.Error("expected RequeueForUser to be called")
	}
}

// TestAdvanceREST_ConceptDraft_DoesNotAutoApprove verifies that advance on a
// concept/done task returns dispatched:false and never calls the orchestrator.
func TestAdvanceREST_ConceptDraft_DoesNotAutoApprove(t *testing.T) {
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
	orch := &lifecycleOrchestrator{}
	client, r := newLifecycleHandler(t, orch)
	taskID := seedTaskWithRun(t, client, "backlog", "done")

	req := withAuth(t, httptest.NewRequest(http.MethodPost, "/api/tasks/"+taskID+"/advance", nil))
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var resp map[string]any
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp["dispatched"].(bool) {
		t.Errorf("advance must NOT auto-approve spec")
	}
	if resp["primary"] != "approve_spec" {
		t.Errorf("expected primary=approve_spec, got %v", resp["primary"])
	}
	if orch.requeueCalled || orch.resumeCalled || orch.progressCalled {
		t.Error("no orchestrator must be called for approve_spec path")
	}
}

// TestHoldREST_ActiveTask_Returns200 verifies POST /hold parks the task.
func TestHoldREST_ActiveTask_Returns200(t *testing.T) {
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
	orch := &lifecycleOrchestrator{}
	client, r := newLifecycleHandler(t, orch)
	taskID := seedTaskWithRun(t, client, "implementation", "running")

	req := withAuth(t, httptest.NewRequest(http.MethodPost, "/api/tasks/"+taskID+"/hold", nil))
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	// Stage must now be on_hold.
	task, err := repo.NewTaskRepo(client).GetByID(context.Background(), taskID)
	if err != nil {
		t.Fatalf("get task: %v", err)
	}
	if task.CurrentStage != "on_hold" {
		t.Errorf("expected stage=on_hold, got %q", task.CurrentStage)
	}
	// hold must NOT terminate the task (NotifyTaskTerminated must not be called).
	if orch.terminateCalled {
		t.Error("hold must not call NotifyTaskTerminated")
	}
}

// TestHoldREST_TerminalTask_Returns400 verifies that holding a done/cancelled task fails.
func TestHoldREST_TerminalTask_Returns400(t *testing.T) {
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
	for _, stage := range []string{"done", "cancelled"} {
		t.Run(stage, func(t *testing.T) {
			orch := &lifecycleOrchestrator{}
			client, r := newLifecycleHandler(t, orch)
			taskID := seedTaskWithRun(t, client, "implementation", "failed")
			_, err := repo.NewTaskRepo(client).Update(context.Background(), taskID, repo.UpdateTaskInput{CurrentStage: &stage})
			if err != nil {
				t.Fatalf("update stage: %v", err)
			}

			req := withAuth(t, httptest.NewRequest(http.MethodPost, "/api/tasks/"+taskID+"/hold", nil))
			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)

			if w.Code != http.StatusBadRequest {
				t.Fatalf("expected 400, got %d: %s", w.Code, w.Body.String())
			}
		})
	}
}

// TestResumeREST_OnHoldTask_Returns202AndCallsResume verifies POST /resume on an
// on_hold task unstages it and calls ResumeFromUser.
func TestResumeREST_OnHoldTask_Returns202AndCallsResume(t *testing.T) {
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
	orch := &lifecycleOrchestrator{}
	client, r := newLifecycleHandler(t, orch)
	taskID := seedTaskWithRun(t, client, "implementation", "running")
	onHold := "on_hold"
	if _, err := repo.NewTaskRepo(client).Update(context.Background(), taskID, repo.UpdateTaskInput{CurrentStage: &onHold}); err != nil {
		t.Fatalf("hold task: %v", err)
	}

	req := withAuth(t, httptest.NewRequest(http.MethodPost, "/api/tasks/"+taskID+"/resume", nil))
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d: %s", w.Code, w.Body.String())
	}
	if !orch.resumeCalled {
		t.Error("expected ResumeFromUser to be called")
	}
	// Stage must no longer be on_hold.
	task, err := repo.NewTaskRepo(client).GetByID(context.Background(), taskID)
	if err != nil {
		t.Fatalf("get task: %v", err)
	}
	if task.CurrentStage == "on_hold" {
		t.Errorf("expected stage to leave on_hold after resume, got %q", task.CurrentStage)
	}
}

// TestResumeREST_PassesAdditionalPrompt verifies POST /resume forwards the
// user's additionalPrompt to ResumeFromUser (it was previously dropped).
func TestResumeREST_PassesAdditionalPrompt(t *testing.T) {
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
	orch := &lifecycleOrchestrator{}
	client, r := newLifecycleHandler(t, orch)
	taskID := seedTaskWithRun(t, client, "implementation", "awaiting_user")

	body := strings.NewReader(`{"additionalPrompt":"focus on the parser"}`)
	req := withAuth(t, httptest.NewRequest(http.MethodPost, "/api/tasks/"+taskID+"/resume", body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d: %s", w.Code, w.Body.String())
	}
	if orch.resumePrompt != "focus on the parser" {
		t.Errorf("expected resume prompt forwarded, got %q", orch.resumePrompt)
	}
}

// TestCompleteREST_MarksDoneAndRecordsAudit verifies POST /complete moves a live task to done, terminates it and records the reference.
func TestCompleteREST_MarksDoneAndRecordsAudit(t *testing.T) {
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
	orch := &lifecycleOrchestrator{}
	client, r := newLifecycleHandler(t, orch)
	taskID := seedTaskWithRun(t, client, "implementation", "failed")

	req := withAuth(t, httptest.NewRequest(http.MethodPost, "/api/tasks/"+taskID+"/complete", strings.NewReader(`{"reference":"  lx-wnk/kontor#538 "}`)))
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	task, err := repo.NewTaskRepo(client).GetByID(context.Background(), taskID)
	if err != nil {
		t.Fatalf("get task: %v", err)
	}
	if task.CurrentStage != "done" {
		t.Errorf("expected stage=done, got %q", task.CurrentStage)
	}
	if !orch.terminateCalled {
		t.Error("complete must call NotifyTaskTerminated")
	}
	events, err := repo.NewAuditEventRepo(client).ListForTask(context.Background(), taskID)
	if err != nil {
		t.Fatalf("list audit: %v", err)
	}
	if len(events) != 1 || events[0].Action != repo.AuditActionTaskCompleted {
		t.Fatalf("expected one %q audit event, got %+v", repo.AuditActionTaskCompleted, events)
	}
	meta := events[0].Metadata
	if meta["reference"] != "lx-wnk/kontor#538" || meta["fromStage"] != "implementation" || meta["source"] != repo.AuditSourceUI || meta["actor"] != repo.AuditActorUser {
		t.Errorf("unexpected audit metadata: %+v", meta)
	}
}

// TestCompleteREST_WithoutBody verifies the reference is optional.
func TestCompleteREST_WithoutBody(t *testing.T) {
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
	client, r := newLifecycleHandler(t, &lifecycleOrchestrator{})
	taskID := seedTaskWithRun(t, client, "backlog", "done")

	w := httptest.NewRecorder()
	r.ServeHTTP(w, withAuth(t, httptest.NewRequest(http.MethodPost, "/api/tasks/"+taskID+"/complete", nil)))

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
}

// TestCompleteREST_ReferenceLimitCountsCharacters verifies 500 multi-byte characters pass the 500-character limit.
func TestCompleteREST_ReferenceLimitCountsCharacters(t *testing.T) {
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
	client, r := newLifecycleHandler(t, &lifecycleOrchestrator{})
	taskID := seedTaskWithRun(t, client, "implementation", "failed")

	body := `{"reference":"` + strings.Repeat("ü", 500) + `"}`
	w := httptest.NewRecorder()
	r.ServeHTTP(w, withAuth(t, httptest.NewRequest(http.MethodPost, "/api/tasks/"+taskID+"/complete", strings.NewReader(body))))

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
}

// TestCompleteREST_Rejects verifies terminal tasks, oversized references and malformed bodies are refused without a state change.
func TestCompleteREST_Rejects(t *testing.T) {
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
	cases := []struct {
		name  string
		stage string
		body  string
	}{
		{"done", "done", ""},
		{"cancelled", "cancelled", ""},
		{"reference too long", "implementation", `{"reference":"` + strings.Repeat("x", 501) + `"}`},
		{"malformed body", "implementation", `{`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			orch := &lifecycleOrchestrator{}
			client, r := newLifecycleHandler(t, orch)
			taskID := seedTaskWithRun(t, client, "implementation", "failed")
			if tc.stage != "implementation" {
				if _, err := repo.NewTaskRepo(client).Update(context.Background(), taskID, repo.UpdateTaskInput{CurrentStage: &tc.stage}); err != nil {
					t.Fatalf("update stage: %v", err)
				}
			}

			w := httptest.NewRecorder()
			r.ServeHTTP(w, withAuth(t, httptest.NewRequest(http.MethodPost, "/api/tasks/"+taskID+"/complete", strings.NewReader(tc.body))))

			if w.Code != http.StatusBadRequest {
				t.Fatalf("expected 400, got %d: %s", w.Code, w.Body.String())
			}
			task, _ := repo.NewTaskRepo(client).GetByID(context.Background(), taskID)
			if task.CurrentStage != tc.stage {
				t.Errorf("stage changed to %q", task.CurrentStage)
			}
			if orch.terminateCalled {
				t.Error("a refused completion must not terminate the task")
			}
		})
	}
}
