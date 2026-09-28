package tasks_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/lx-wnk/kontor/server/internal/api/tasks"
	"github.com/lx-wnk/kontor/server/internal/auth"
	"github.com/lx-wnk/kontor/server/internal/db"
	"github.com/lx-wnk/kontor/server/internal/db/ent"
	"github.com/lx-wnk/kontor/server/internal/db/repo"
	"github.com/lx-wnk/kontor/server/internal/pipeline"
	"github.com/lx-wnk/kontor/server/internal/sse"
)

// captureOrchestrator records the most recent RequeueForUser call so handler
// tests can assert which taskID and prompt were passed. ProgressTask is kept
// for interface compliance but its opts field is no longer inspected at the
// handler level (session derivation moved into the real orchestrator).
type captureOrchestrator struct {
	opts          *pipeline.ProgressOpts
	requeueTaskID string
	requeuePrompt string
}

func (c *captureOrchestrator) ProgressTask(_ context.Context, taskID string, opts *pipeline.ProgressOpts) (*ent.StageRun, error) {
	c.opts = opts
	return &ent.StageRun{ID: taskID + "-run"}, nil
}
func (c *captureOrchestrator) ResumeFromUser(_ context.Context, _ string, _ string) (*ent.StageRun, error) {
	return nil, nil
}
func (c *captureOrchestrator) RequeueForUser(_ context.Context, taskID, userPrompt string) (*ent.StageRun, error) {
	c.requeueTaskID = taskID
	c.requeuePrompt = userPrompt
	return &ent.StageRun{ID: taskID + "-run"}, nil
}

func (c *captureOrchestrator) EffectiveStageModel(_ context.Context, _ string) string {
	return ""
}

func (c *captureOrchestrator) NotifyTaskTerminated(_ context.Context, _, _ string)      {}
func (c *captureOrchestrator) InvalidateConfigCache()                                   {}
func (c *captureOrchestrator) ClearStalePendingPermissions(_ context.Context, _ string) {}

// seedFailedRun creates a task at the given stage with one failed stage_run.
// When sessionID is non-empty it is stamped onto the run; when writeFile is
// true the matching session JSONL is created on disk under cwd's project dir.
func seedFailedRun(t *testing.T, client *ent.Client, cwd, stage, sessionID string, writeFile bool) string {
	t.Helper()
	ctx := context.Background()
	taskRepo := repo.NewTaskRepo(client)
	srRepo := repo.NewStageRunRepo(client)

	task, err := taskRepo.Create(ctx, repo.CreateTaskInput{
		Slug:          "retry-resume",
		Title:         "Retry Resume",
		Cwd:           cwd,
		MaxIterations: 5,
		Priority:      "normal",
		CurrentStage:  stage,
	})
	if err != nil {
		t.Fatalf("create task: %v", err)
	}

	run, err := srRepo.Create(ctx, repo.CreateStageRunInput{TaskID: task.ID, Stage: stage, Iteration: 0})
	if err != nil {
		t.Fatalf("create stage run: %v", err)
	}
	failed := "failed"
	upd := repo.UpdateStageRunInput{Status: &failed}
	if sessionID != "" {
		sid := sessionID
		upd.SessionID = &sid
	}
	if _, err := srRepo.Update(ctx, run.ID, upd); err != nil {
		t.Fatalf("update stage run: %v", err)
	}

	if writeFile && sessionID != "" {
		projectDir, derr := pipeline.ResolvedProjectDir(cwd)
		if derr != nil {
			t.Fatalf("resolve project dir: %v", derr)
		}
		if err := os.MkdirAll(projectDir, 0o700); err != nil {
			t.Fatalf("mkdir project: %v", err)
		}
		p := filepath.Join(projectDir, sessionID+".jsonl")
		if err := os.WriteFile(p, []byte("{}\n"), 0o600); err != nil {
			t.Fatalf("write session: %v", err)
		}
		_ = os.Chtimes(p, time.Now(), time.Now())
	}
	return task.ID
}

func newRetryHandler(t *testing.T, orch tasks.OrchestratorIface) (*ent.Client, *chi.Mux) {
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

func postRetry(t *testing.T, r *chi.Mux, taskID string) *httptest.ResponseRecorder {
	t.Helper()
	req := withAuth(t, httptest.NewRequest(http.MethodPost, "/api/tasks/"+taskID+"/retry", nil))
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

// TestRetry_Returns202AndCallsRequeueForUser verifies the handler now returns
// 202 Accepted and delegates to RequeueForUser with the correct task ID.
// Session-resolution is no longer the handler's responsibility — those
// assertions were moved to the orchestrator-level tests.
func TestRetry_Returns202AndCallsRequeueForUser(t *testing.T) {
	cwd := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
	orch := &captureOrchestrator{}
	client, r := newRetryHandler(t, orch)
	taskID := seedFailedRun(t, client, cwd, "implementation", "sess-live", true)

	w := postRetry(t, r, taskID)
	if w.Code != http.StatusAccepted {
		t.Fatalf("expected 202 Accepted, got %d: %s", w.Code, w.Body.String())
	}
	if orch.requeueTaskID != taskID {
		t.Fatalf("expected RequeueForUser called with taskID=%q, got %q", taskID, orch.requeueTaskID)
	}
}

// TestRetry_Returns202_NoSessionID confirms 202 is returned even when the
// task has no prior session (fresh spawn path, resolved later by orchestrator).
func TestRetry_Returns202_NoSessionID(t *testing.T) {
	cwd := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
	orch := &captureOrchestrator{}
	client, r := newRetryHandler(t, orch)
	taskID := seedFailedRun(t, client, cwd, "implementation", "", false)

	w := postRetry(t, r, taskID)
	if w.Code != http.StatusAccepted {
		t.Fatalf("expected 202 Accepted, got %d: %s", w.Code, w.Body.String())
	}
	if orch.requeueTaskID != taskID {
		t.Fatalf("expected RequeueForUser called with taskID=%q, got %q", taskID, orch.requeueTaskID)
	}
}

// TestRetry_Returns202_SessionFileMissing confirms 202 even when the session
// file is gone from disk (orchestrator handles derive-or-fresh at spawn time).
func TestRetry_Returns202_SessionFileMissing(t *testing.T) {
	cwd := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
	orch := &captureOrchestrator{}
	client, r := newRetryHandler(t, orch)
	// session_id recorded in DB but no JSONL on disk — handler must not care.
	taskID := seedFailedRun(t, client, cwd, "implementation", "sess-gone", false)

	w := postRetry(t, r, taskID)
	if w.Code != http.StatusAccepted {
		t.Fatalf("expected 202 Accepted, got %d: %s", w.Code, w.Body.String())
	}
	if orch.requeueTaskID != taskID {
		t.Fatalf("expected RequeueForUser called with taskID=%q, got %q", taskID, orch.requeueTaskID)
	}
}

// TestRetry_PassesAdditionalPrompt verifies the additional prompt from the
// request body is forwarded to RequeueForUser unchanged.
func TestRetry_PassesAdditionalPrompt(t *testing.T) {
	cwd := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
	orch := &captureOrchestrator{}
	client, r := newRetryHandler(t, orch)
	taskID := seedFailedRun(t, client, cwd, "implementation", "", false)

	b, _ := json.Marshal(map[string]any{"additionalPrompt": "please also fix the lint errors"})
	req := withAuth(t, httptest.NewRequest(http.MethodPost, "/api/tasks/"+taskID+"/retry", bytes.NewReader(b)))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d: %s", w.Code, w.Body.String())
	}
	if orch.requeuePrompt != "please also fix the lint errors" {
		t.Fatalf("expected prompt forwarded, got %q", orch.requeuePrompt)
	}
}
