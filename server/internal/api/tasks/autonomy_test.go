package tasks_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/lx-wnk/kontor/server/internal/db"
	"github.com/lx-wnk/kontor/server/internal/db/repo"
)

func TestCreatePermissionRequest_AllowAll_AutoApproved(t *testing.T) {
	bundle, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	client := bundle.Client
	t.Cleanup(func() { _ = client.Close() })

	_, r := newTestHandlerWithBroadcaster(t, client)

	// Seed task with spec_gated autonomy.
	taskRepo := repo.NewTaskRepo(client)
	srRepo := repo.NewStageRunRepo(client)

	task, err := taskRepo.Create(testCtx(t), repo.CreateTaskInput{
		Slug:         "auto-approve-task",
		Title:        "Auto Approve",
		Cwd:          "/tmp/aa",
		CurrentStage: "implementation",
		Priority:     "medium",
	})
	if err != nil {
		t.Fatalf("create task: %v", err)
	}
	if _, err = client.Task.UpdateOneID(task.ID).SetAutonomy("spec_gated").Save(testCtx(t)); err != nil {
		t.Fatalf("set autonomy: %v", err)
	}
	sr, err := srRepo.Create(testCtx(t), repo.CreateStageRunInput{
		TaskID:    task.ID,
		Stage:     "implementation",
		Iteration: 1,
	})
	if err != nil {
		t.Fatalf("create stage run: %v", err)
	}

	body := map[string]any{
		"stageRunId": sr.ID,
		"tool":       "Bash",
		"pattern":    "make build",
	}
	b, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/api/permission-requests", bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	req = withAuth(t, req)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)

	if rr.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", rr.Code, rr.Body.String())
	}
	var result map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &result); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	// outcome must be the canonical "granted" immediately for an allow-all task.
	if result["outcome"] != "granted" {
		t.Errorf("expected outcome=granted for allow-all task, got %v", result["outcome"])
	}
}

func TestCreatePermissionRequest_Manual_StillPending(t *testing.T) {
	bundle, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	client := bundle.Client
	t.Cleanup(func() { _ = client.Close() })

	_, r := newTestHandlerWithBroadcaster(t, client)

	taskRepo := repo.NewTaskRepo(client)
	srRepo := repo.NewStageRunRepo(client)

	task, err := taskRepo.Create(testCtx(t), repo.CreateTaskInput{
		Slug:         "gated-task",
		Title:        "Gated",
		Cwd:          "/tmp/gated",
		CurrentStage: "implementation",
		Priority:     "medium",
	})
	if err != nil {
		t.Fatalf("create task: %v", err)
	}
	if _, err = client.Task.UpdateOneID(task.ID).SetAutonomy("manual").Save(testCtx(t)); err != nil {
		t.Fatalf("set autonomy: %v", err)
	}
	sr, err := srRepo.Create(testCtx(t), repo.CreateStageRunInput{
		TaskID:    task.ID,
		Stage:     "implementation",
		Iteration: 1,
	})
	if err != nil {
		t.Fatalf("create stage run: %v", err)
	}

	body := map[string]any{
		"stageRunId": sr.ID,
		"tool":       "Bash",
		"pattern":    "make build",
	}
	b, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/api/permission-requests", bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	req = withAuth(t, req)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)

	if rr.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", rr.Code, rr.Body.String())
	}
	var result map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &result); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	// outcome must be nil (pending) for a gated task.
	if result["outcome"] != nil {
		t.Errorf("expected outcome=nil for gated task, got %v", result["outcome"])
	}
}
