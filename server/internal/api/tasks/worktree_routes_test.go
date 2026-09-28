package tasks_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/lx-wnk/kontor/sdk"
	"github.com/lx-wnk/kontor/server/internal/api/tasks"
	"github.com/lx-wnk/kontor/server/internal/auth"
	"github.com/lx-wnk/kontor/server/internal/db"
	"github.com/lx-wnk/kontor/server/internal/db/repo"
	"github.com/lx-wnk/kontor/server/internal/services"
	"github.com/lx-wnk/kontor/server/internal/sse"
)

// fakeWorktreeMgr implements tasks.WorktreeStatusProvider for testing.
type fakeWorktreeMgr struct {
	createFn func(ctx context.Context, taskID string) (string, error)
	removeFn func(ctx context.Context, taskID string, force bool) error
}

func (f *fakeWorktreeMgr) WorktreeStatus(_ context.Context, _ string) (*sdk.WorktreeStatusDTO, error) {
	return nil, nil
}

func (f *fakeWorktreeMgr) CreateWorktree(ctx context.Context, taskID string) (string, error) {
	return f.createFn(ctx, taskID)
}

func (f *fakeWorktreeMgr) RemoveWorktree(ctx context.Context, taskID string, force bool) error {
	return f.removeFn(ctx, taskID, force)
}

// newWorktreeTestHandler sets up a Handler with the given fake provider and a
// real task row so broadcastEnrichedUpdate doesn't fail.
func newWorktreeTestHandler(t *testing.T, fake tasks.WorktreeStatusProvider) (string, *chi.Mux) {
	t.Helper()
	bundle, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	client := bundle.Client
	t.Cleanup(func() { _ = client.Close() })

	taskRepo := repo.NewTaskRepo(client)
	srRepo := repo.NewStageRunRepo(client)
	permRepo := repo.NewPermissionRepo(client)
	auditRepo := repo.NewAuditEventRepo(client)
	cfgRepo := repo.NewPipelineConfigRepo(client)
	broadcaster := sse.NewTaskBroadcaster(sse.NewBroadcaster())

	task, err := taskRepo.Create(testCtx(t), repo.CreateTaskInput{
		Slug:                "wt-test",
		Title:               "WT Test",
		Cwd:                 "/tmp",
		CurrentStage:        "backlog",
		Priority:            "medium",
		MaxIterations:       5,
		StageTimeoutSeconds: 300,
	})
	if err != nil {
		t.Fatalf("create task: %v", err)
	}

	h := tasks.NewHandler(tasks.Deps{
		TaskRepo:     taskRepo,
		SRRepo:       srRepo,
		PermRepo:     permRepo,
		AuditRepo:    auditRepo,
		CfgRepo:      cfgRepo,
		Orchestrator: &noopOrchestrator{},
		Broadcaster:  broadcaster,
		WorktreeMgr:  fake,
	})

	r := chi.NewRouter()
	r.Use(auth.RequireAuth(testJWTSecret))
	h.Mount(r)
	return task.ID, r
}

func TestCreateWorktreeHandler_Success(t *testing.T) {
	fake := &fakeWorktreeMgr{
		createFn: func(_ context.Context, _ string) (string, error) {
			return "/tmp/wt", nil
		},
	}
	id, r := newWorktreeTestHandler(t, fake)

	req := httptest.NewRequest(http.MethodPost, "/api/tasks/"+id+"/worktree", nil)
	req = withAuth(t, req)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}
	var body map[string]string
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body["worktreePath"] != "/tmp/wt" {
		t.Errorf("expected worktreePath=/tmp/wt, got %q", body["worktreePath"])
	}
}

func TestRemoveWorktreeHandler_Clean(t *testing.T) {
	fake := &fakeWorktreeMgr{
		removeFn: func(_ context.Context, _ string, _ bool) error {
			return nil
		},
	}
	id, r := newWorktreeTestHandler(t, fake)

	req := httptest.NewRequest(http.MethodDelete, "/api/tasks/"+id+"/worktree", nil)
	req = withAuth(t, req)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)

	if rr.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d: %s", rr.Code, rr.Body.String())
	}
}

func TestRemoveWorktreeHandler_Dirty_NoForce(t *testing.T) {
	fake := &fakeWorktreeMgr{
		removeFn: func(_ context.Context, _ string, force bool) error {
			if !force {
				return services.ErrWorktreeDirty
			}
			return nil
		},
	}
	id, r := newWorktreeTestHandler(t, fake)

	req := httptest.NewRequest(http.MethodDelete, "/api/tasks/"+id+"/worktree", nil)
	req = withAuth(t, req)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)

	if rr.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d: %s", rr.Code, rr.Body.String())
	}
}

func TestRemoveWorktreeHandler_NoWorktree(t *testing.T) {
	fake := &fakeWorktreeMgr{
		removeFn: func(_ context.Context, _ string, _ bool) error {
			return services.ErrNoWorktree
		},
	}
	id, r := newWorktreeTestHandler(t, fake)

	req := httptest.NewRequest(http.MethodDelete, "/api/tasks/"+id+"/worktree", nil)
	req = withAuth(t, req)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)

	if rr.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d: %s", rr.Code, rr.Body.String())
	}
}

func TestRemoveWorktreeHandler_Force(t *testing.T) {
	var gotForce bool
	fake := &fakeWorktreeMgr{
		removeFn: func(_ context.Context, _ string, force bool) error {
			gotForce = force
			return nil
		},
	}
	id, r := newWorktreeTestHandler(t, fake)

	req := httptest.NewRequest(http.MethodDelete, "/api/tasks/"+id+"/worktree?force=true", nil)
	req = withAuth(t, req)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)

	if rr.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d: %s", rr.Code, rr.Body.String())
	}
	if !gotForce {
		t.Fatal("expected force=true to be passed to RemoveWorktree")
	}
}
