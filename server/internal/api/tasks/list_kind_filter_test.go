package tasks_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/lx-wnk/kontor/server/internal/api/tasks"
	"github.com/lx-wnk/kontor/server/internal/db"
	rawrepo "github.com/lx-wnk/kontor/server/internal/db/rawrepo"
	"github.com/lx-wnk/kontor/server/internal/db/repo"
	"github.com/lx-wnk/kontor/server/internal/sse"
)

// kindFilterRouter seeds one pipeline task and two jobs (routines "r1", "r2")
// and mounts the handler with auth bypassed so the request drives the filter
// directly.
func kindFilterRouter(t *testing.T) *chi.Mux {
	t.Helper()
	bundle, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { _ = bundle.Client.Close() })

	taskRepo := repo.NewTaskRepo(bundle.Client)
	if _, err := taskRepo.Create(t.Context(), repo.CreateTaskInput{
		Slug: "pipe", Title: "pipe", Cwd: "/tmp",
		CurrentStage: "backlog", Priority: "medium",
		MaxIterations: 20,
		Kind:          "pipeline",
	}); err != nil {
		t.Fatalf("seed pipe: %v", err)
	}
	r1 := "r1"
	if _, err := taskRepo.Create(t.Context(), repo.CreateTaskInput{
		Slug: "job-r1", Title: "job-r1", Cwd: "/tmp",
		CurrentStage: "job", Priority: "medium",
		MaxIterations: 20,
		Kind:          "job", RoutineID: &r1,
	}); err != nil {
		t.Fatalf("seed job-r1: %v", err)
	}
	r2 := "r2"
	if _, err := taskRepo.Create(t.Context(), repo.CreateTaskInput{
		Slug: "job-r2", Title: "job-r2", Cwd: "/tmp",
		CurrentStage: "job", Priority: "medium",
		MaxIterations: 20,
		Kind:          "job", RoutineID: &r2,
	}); err != nil {
		t.Fatalf("seed job-r2: %v", err)
	}

	h := tasks.NewHandler(tasks.Deps{
		Client:       bundle.Client,
		TaskRepo:     taskRepo,
		SRBulkRepo:   rawrepo.NewStageRunBulkRepo(bundle.DB),
		SRRepo:       repo.NewStageRunRepo(bundle.Client),
		PermRepo:     repo.NewPermissionRepo(bundle.Client),
		AuditRepo:    repo.NewAuditEventRepo(bundle.Client),
		CfgRepo:      repo.NewPipelineConfigRepo(bundle.Client),
		Orchestrator: &noopOrchestrator{},
		Broadcaster:  sse.NewTaskBroadcaster(sse.NewBroadcaster()),
		BypassAuth:   true,
	})
	r := chi.NewRouter()
	h.Mount(r)
	return r
}

type kindFilterRow struct {
	Slug      string  `json:"slug"`
	Kind      string  `json:"kind"`
	RoutineID *string `json:"routineId"`
}

func listWithQuery(t *testing.T, r *chi.Mux, query string) (int, []kindFilterRow) {
	t.Helper()
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/tasks"+query, nil))
	if rec.Code != http.StatusOK {
		return rec.Code, nil
	}
	var rows []kindFilterRow
	if err := json.Unmarshal(rec.Body.Bytes(), &rows); err != nil {
		t.Fatalf("decode: %v — body=%s", err, rec.Body.String())
	}
	return rec.Code, rows
}

func TestListTasks_KindDefaultsToPipeline(t *testing.T) {
	_, rows := listWithQuery(t, kindFilterRouter(t), "")
	if len(rows) != 1 || rows[0].Slug != "pipe" {
		t.Fatalf("expected only the pipeline task by default, got %+v", rows)
	}
	if rows[0].Kind != "pipeline" || rows[0].RoutineID != nil {
		t.Fatalf("expected kind=pipeline, routineId=null, got kind=%s routineId=%v", rows[0].Kind, rows[0].RoutineID)
	}
}

func TestListTasks_KindJob(t *testing.T) {
	_, rows := listWithQuery(t, kindFilterRouter(t), "?kind=job")
	if len(rows) != 2 {
		t.Fatalf("expected two jobs, got %+v", rows)
	}
}

func TestListTasks_KindJobAndRoutine(t *testing.T) {
	_, rows := listWithQuery(t, kindFilterRouter(t), "?kind=job&routineId=r1")
	if len(rows) != 1 || rows[0].Slug != "job-r1" {
		t.Fatalf("expected only job-r1, got %+v", rows)
	}
	if rows[0].RoutineID == nil || *rows[0].RoutineID != "r1" {
		t.Fatalf("expected routineId=r1, got %v", rows[0].RoutineID)
	}
}

func TestListTasks_KindAll(t *testing.T) {
	_, rows := listWithQuery(t, kindFilterRouter(t), "?kind=all")
	if len(rows) != 3 {
		t.Fatalf("expected all three tasks, got %+v", rows)
	}
}

func TestListTasks_KindInvalidIs400(t *testing.T) {
	code, _ := listWithQuery(t, kindFilterRouter(t), "?kind=weird")
	if code != http.StatusBadRequest {
		t.Fatalf("expected 400 for invalid kind, got %d", code)
	}
}
