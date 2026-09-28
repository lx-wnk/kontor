package tasks_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/lx-wnk/kontor/server/internal/api/tasks"
	"github.com/lx-wnk/kontor/server/internal/db/repo"
)

// TestCreateTask_HTTPStageMustMatchKind verifies POST /api/tasks refuses to
// create a pipeline-kind task directly in the job stage.
func TestCreateTask_HTTPStageMustMatchKind(t *testing.T) {
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
	env := newPlanModeTestEnv(t)

	body := map[string]any{
		"slug":  "stage-kind-guard",
		"title": "Stage kind guard",
		"cwd":   "/tmp",
		"stage": "job",
	}
	b, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/api/tasks", bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	req = withAuth(t, req)
	rr := httptest.NewRecorder()
	env.router.ServeHTTP(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "only job tasks run the job stage") {
		t.Errorf("expected message about job stage, got: %s", rr.Body.String())
	}
}

// TestCreateTaskFromInput_RefusesStageOutsideJob verifies a job-kind task
// cannot be created directly in a pipeline stage.
func TestCreateTaskFromInput_RefusesStageOutsideJob(t *testing.T) {
	h, _, _ := newRoutineIDTestEnv(t)

	_, err := h.CreateTaskFromInput(context.Background(), tasks.CreateTaskParams{
		Slug:  "job-outside-job-stage",
		Title: "Job outside job stage",
		Cwd:   "/tmp",
		Kind:  "job",
		Stage: "implementation",
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "job tasks run only the job stage") {
		t.Errorf("expected job-stage-violation message, got: %v", err)
	}
}

// TestResumeREST_HeldJobResumesIntoJobStage verifies resuming a held job task
// returns it to the job stage, not implementation.
func TestResumeREST_HeldJobResumesIntoJobStage(t *testing.T) {
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
	orch := &lifecycleOrchestrator{}
	client, r := newLifecycleHandler(t, orch)
	taskID := seedTaskWithRun(t, client, "job", "running")
	client.Task.UpdateOneID(taskID).SetKind("job").ExecX(context.Background())
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
	task, err := repo.NewTaskRepo(client).GetByID(context.Background(), taskID)
	if err != nil {
		t.Fatalf("get task: %v", err)
	}
	if task.CurrentStage != "job" {
		t.Errorf("expected held job to resume into job stage, got %q", task.CurrentStage)
	}
}

// TestResumeREST_HeldPipelineTaskResumesIntoImplementation verifies the
// existing behaviour for pipeline-kind tasks is unchanged.
func TestResumeREST_HeldPipelineTaskResumesIntoImplementation(t *testing.T) {
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
	task, err := repo.NewTaskRepo(client).GetByID(context.Background(), taskID)
	if err != nil {
		t.Fatalf("get task: %v", err)
	}
	if task.CurrentStage != "implementation" {
		t.Errorf("expected held pipeline task to resume into implementation, got %q", task.CurrentStage)
	}
}
