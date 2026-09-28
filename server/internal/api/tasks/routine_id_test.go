package tasks_test

import (
	"context"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/lx-wnk/kontor/server/internal/api/tasks"
	"github.com/lx-wnk/kontor/server/internal/auth"
	"github.com/lx-wnk/kontor/server/internal/db"
	"github.com/lx-wnk/kontor/server/internal/db/repo"
	"github.com/lx-wnk/kontor/server/internal/sse"
)

func newRoutineIDTestEnv(t *testing.T) (*tasks.Handler, repo.TaskRepo, *chi.Mux) {
	t.Helper()
	bundle, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { _ = bundle.Client.Close() })

	taskRepo := repo.NewTaskRepo(bundle.Client)
	h := tasks.NewHandler(tasks.Deps{
		TaskRepo:     taskRepo,
		SRRepo:       repo.NewStageRunRepo(bundle.Client),
		PermRepo:     repo.NewPermissionRepo(bundle.Client),
		AuditRepo:    repo.NewAuditEventRepo(bundle.Client),
		CfgRepo:      repo.NewPipelineConfigRepo(bundle.Client),
		ProjectRepo:  repo.NewProjectRepo(bundle.Client),
		Orchestrator: &noopOrchestrator{},
		Broadcaster:  sse.NewTaskBroadcaster(sse.NewBroadcaster()),
	})
	r := chi.NewRouter()
	r.Use(auth.RequireAuth(testJWTSecret))
	h.Mount(r)
	return h, taskRepo, r
}

// TestCreateTaskFromInput_PersistsRoutineID covers the scheduler's path: it is
// the only caller that sets RoutineID, and the value has to survive to the row
// because that is what memory.RoutineContext later resolves a grant against.
func TestCreateTaskFromInput_PersistsRoutineID(t *testing.T) {
	h, taskRepo, _ := newRoutineIDTestEnv(t)

	created, err := h.CreateTaskFromInput(context.Background(), tasks.CreateTaskParams{
		Slug:      "from-routine",
		Title:     "From routine",
		Cwd:       "/tmp",
		RoutineID: "sched-1",
	})
	if err != nil {
		t.Fatalf("CreateTaskFromInput: %v", err)
	}
	row, err := taskRepo.GetByID(context.Background(), created.ID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if row.RoutineID == nil || *row.RoutineID != "sched-1" {
		t.Fatalf("routine_id = %v, want \"sched-1\"", row.RoutineID)
	}
}

// TestCreateTask_HTTPBodyCannotSetRoutineID is the security half of the same
// field. A routine grant is resolved by routine_id, so a caller able to name
// one in a create body could hand its own task another routine's permissions.
// The create body has no such field; this test fails the moment someone adds
// one.
func TestCreateTask_HTTPBodyCannotSetRoutineID(t *testing.T) {
	_, taskRepo, r := newRoutineIDTestEnv(t)

	body := postCreateTask(t, r, map[string]any{
		"slug":      "hand-made",
		"title":     "Hand made",
		"cwd":       "/tmp",
		"routineId": "sched-1",
		"metadata":  map[string]any{"routineId": "sched-1"},
	})
	id, _ := body["id"].(string)
	if id == "" {
		t.Fatalf("create did not return an id: %v", body)
	}
	row, err := taskRepo.GetByID(context.Background(), id)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if row.RoutineID != nil {
		t.Fatalf("routine_id = %q, want unset — the create body must not be able to claim a routine", *row.RoutineID)
	}
}

// TestCreateTaskFromInput_EmptyRoutineIDStaysNull keeps the column NULL rather
// than empty-string for a human-created task: memory.RoutineContext treats ""
// as "no routine", and an empty-string row would make that distinction depend
// on which of the two the reader looked at.
func TestCreateTaskFromInput_EmptyRoutineIDStaysNull(t *testing.T) {
	h, taskRepo, _ := newRoutineIDTestEnv(t)

	created, err := h.CreateTaskFromInput(context.Background(), tasks.CreateTaskParams{
		Slug:  "no-routine",
		Title: "No routine",
		Cwd:   "/tmp",
	})
	if err != nil {
		t.Fatalf("CreateTaskFromInput: %v", err)
	}
	row, err := taskRepo.GetByID(context.Background(), created.ID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if row.RoutineID != nil {
		t.Fatalf("routine_id = %q, want nil", *row.RoutineID)
	}
}

// TestCreateTaskFromInput_PersistsKind covers the scheduler's job-run path: it
// is the only caller that sets Kind, and the value has to survive to the row
// because that is what stage advancement reads to skip the pipeline.
func TestCreateTaskFromInput_PersistsKind(t *testing.T) {
	h, taskRepo, _ := newRoutineIDTestEnv(t)

	created, err := h.CreateTaskFromInput(context.Background(), tasks.CreateTaskParams{
		Slug:  "job-task",
		Title: "Job task",
		Cwd:   "/tmp",
		Kind:  "job",
		Stage: "job",
	})
	if err != nil {
		t.Fatalf("CreateTaskFromInput: %v", err)
	}
	row, err := taskRepo.GetByID(context.Background(), created.ID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if row.Kind != "job" {
		t.Fatalf("kind = %q, want %q", row.Kind, "job")
	}
}

// TestCreateTask_HTTPBodyCannotSetKind mirrors the routineId guard: kind
// decides whether a task skips the pipeline, so a caller able to name it in a
// create body could bypass every stage gate on its own task. The create body
// has no such field; this test fails the moment someone adds one.
func TestCreateTask_HTTPBodyCannotSetKind(t *testing.T) {
	_, taskRepo, r := newRoutineIDTestEnv(t)

	body := postCreateTask(t, r, map[string]any{
		"slug":  "hand-made-kind",
		"title": "Hand made kind",
		"cwd":   "/tmp",
		"kind":  "job",
	})
	id, _ := body["id"].(string)
	if id == "" {
		t.Fatalf("create did not return an id: %v", body)
	}
	row, err := taskRepo.GetByID(context.Background(), id)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if row.Kind != "pipeline" {
		t.Fatalf("kind = %q, want %q — the create body must not be able to set kind", row.Kind, "pipeline")
	}
}
