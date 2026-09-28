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
	"github.com/lx-wnk/kontor/server/internal/checkpoint"
	"github.com/lx-wnk/kontor/server/internal/db"
	"github.com/lx-wnk/kontor/server/internal/db/repo"
	"github.com/lx-wnk/kontor/server/internal/sse"
)

type fakeCheckpointSvc struct {
	listResult  []checkpoint.CheckpointView
	revertErr   error
	revertedID  string
	revertedCPs string
}

func (f *fakeCheckpointSvc) List(_ context.Context, _ string) ([]checkpoint.CheckpointView, error) {
	return f.listResult, nil
}

func (f *fakeCheckpointSvc) Revert(_ context.Context, taskID, cpID, _ string) error {
	f.revertedID = taskID
	f.revertedCPs = cpID
	return f.revertErr
}

func newCheckpointHandler(t *testing.T, svc tasks.CheckpointServiceIface) (repo.TaskRepo, *chi.Mux) {
	t.Helper()
	bundle, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	client := bundle.Client
	t.Cleanup(func() { _ = client.Close() })

	taskRepo := repo.NewTaskRepo(client)
	h := tasks.NewHandler(tasks.Deps{
		Client:        client,
		TaskRepo:      taskRepo,
		SRRepo:        repo.NewStageRunRepo(client),
		PermRepo:      repo.NewPermissionRepo(client),
		AuditRepo:     repo.NewAuditEventRepo(client),
		CfgRepo:       repo.NewPipelineConfigRepo(client),
		DepRepo:       repo.NewDependencyRepo(client),
		Orchestrator:  &noopOrchestrator{},
		Broadcaster:   sse.NewTaskBroadcaster(sse.NewBroadcaster()),
		CheckpointSvc: svc,
	})
	r := chi.NewRouter()
	r.Use(auth.RequireAuth(testJWTSecret))
	h.Mount(r)
	return taskRepo, r
}

func seedWorktreeTask(t *testing.T, taskRepo repo.TaskRepo) string {
	t.Helper()
	task, err := taskRepo.Create(context.Background(), repo.CreateTaskInput{
		Slug:          "cp-task",
		Title:         "cp task",
		Cwd:           "/tmp",
		CurrentStage:  "implementation",
		Priority:      "medium",
		MaxIterations: 3,
	})
	if err != nil {
		t.Fatalf("create task: %v", err)
	}
	wt := "/tmp/wt/cp-task"
	if _, err := taskRepo.Update(context.Background(), task.ID, repo.UpdateTaskInput{WorktreePath: &wt}); err != nil {
		t.Fatalf("set worktree: %v", err)
	}
	return task.ID
}

func TestListCheckpoints(t *testing.T) {
	svc := &fakeCheckpointSvc{listResult: []checkpoint.CheckpointView{{ID: "cp1", Seq: 1}}}
	taskRepo, r := newCheckpointHandler(t, svc)
	taskID := seedWorktreeTask(t, taskRepo)

	w := httptest.NewRecorder()
	req := withAuth(t, httptest.NewRequest("GET", "/api/tasks/"+taskID+"/checkpoints", nil))
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body)
	}
	var got []checkpoint.CheckpointView
	_ = json.NewDecoder(w.Body).Decode(&got)
	if len(got) != 1 || got[0].ID != "cp1" {
		t.Fatalf("unexpected response: %v", got)
	}
}

func TestRevertCheckpoint_Success(t *testing.T) {
	svc := &fakeCheckpointSvc{}
	taskRepo, r := newCheckpointHandler(t, svc)
	taskID := seedWorktreeTask(t, taskRepo)

	w := httptest.NewRecorder()
	req := withAuth(t, httptest.NewRequest("POST", "/api/tasks/"+taskID+"/checkpoints/cp1/revert", strings.NewReader("{}")))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body)
	}
	if svc.revertedID != taskID || svc.revertedCPs != "cp1" {
		t.Fatalf("Revert called with %q/%q", svc.revertedID, svc.revertedCPs)
	}
}

func TestRevertCheckpoint_NoWorktree(t *testing.T) {
	svc := &fakeCheckpointSvc{}
	taskRepo, r := newCheckpointHandler(t, svc)
	task, err := taskRepo.Create(context.Background(), repo.CreateTaskInput{
		Slug:          "cp-no-wt",
		Title:         "no wt",
		Cwd:           "/tmp",
		CurrentStage:  "implementation",
		Priority:      "medium",
		MaxIterations: 3,
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	w := httptest.NewRecorder()
	req := withAuth(t, httptest.NewRequest("POST", "/api/tasks/"+task.ID+"/checkpoints/cp1/revert", strings.NewReader("{}")))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)
	if w.Code != http.StatusConflict {
		t.Fatalf("expected 409 for task without worktree, got %d: %s", w.Code, w.Body)
	}
}
