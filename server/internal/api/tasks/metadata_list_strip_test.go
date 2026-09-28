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

// metadataStripRouter seeds one task carrying a non-trivial metadata payload
// (spec + plan, as the task detail modal reads them) and mounts the handler
// with auth bypassed.
func metadataStripRouter(t *testing.T) (*chi.Mux, string) {
	t.Helper()
	bundle, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { _ = bundle.Client.Close() })

	taskRepo := repo.NewTaskRepo(bundle.Client)
	task, err := taskRepo.Create(t.Context(), repo.CreateTaskInput{
		Slug: "metadata-strip", Title: "Metadata Strip", Cwd: "/tmp",
		CurrentStage: "backlog", Priority: "medium",
		MaxIterations: 20,
		Kind:          "pipeline",
		Metadata: map[string]any{
			"spec": "a spec long enough to matter",
			"plan": "a plan long enough to matter",
		},
	})
	if err != nil {
		t.Fatalf("seed task: %v", err)
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
	return r, task.ID
}

// TestListTasks_OmitsMetadata asserts the list route drops metadata content —
// the field the client only reads from the single-task detail fetch — while
// GET /api/tasks/{id} keeps returning it in full.
func TestListTasks_OmitsMetadata(t *testing.T) {
	r, taskID := metadataStripRouter(t)

	listRec := httptest.NewRecorder()
	r.ServeHTTP(listRec, httptest.NewRequest(http.MethodGet, "/api/tasks", nil))
	if listRec.Code != http.StatusOK {
		t.Fatalf("list: expected 200, got %d: %s", listRec.Code, listRec.Body.String())
	}
	var rows []map[string]any
	if err := json.Unmarshal(listRec.Body.Bytes(), &rows); err != nil {
		t.Fatalf("decode list: %v — body=%s", err, listRec.Body.String())
	}
	if len(rows) != 1 {
		t.Fatalf("expected 1 task, got %d", len(rows))
	}
	if meta := rows[0]["metadata"]; meta != nil {
		t.Errorf("list metadata = %v, want nil/absent", meta)
	}

	oneRec := httptest.NewRecorder()
	r.ServeHTTP(oneRec, httptest.NewRequest(http.MethodGet, "/api/tasks/"+taskID, nil))
	if oneRec.Code != http.StatusOK {
		t.Fatalf("getOne: expected 200, got %d: %s", oneRec.Code, oneRec.Body.String())
	}
	var one map[string]any
	if err := json.Unmarshal(oneRec.Body.Bytes(), &one); err != nil {
		t.Fatalf("decode getOne: %v — body=%s", err, oneRec.Body.String())
	}
	meta, ok := one["metadata"].(map[string]any)
	if !ok {
		t.Fatalf("getOne metadata = %v, want an object", one["metadata"])
	}
	if meta["spec"] != "a spec long enough to matter" {
		t.Errorf("getOne metadata.spec = %v, want the seeded spec", meta["spec"])
	}
	if meta["plan"] != "a plan long enough to matter" {
		t.Errorf("getOne metadata.plan = %v, want the seeded plan", meta["plan"])
	}
}
