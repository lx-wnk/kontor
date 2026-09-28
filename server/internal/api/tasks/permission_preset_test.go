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
	"github.com/lx-wnk/kontor/server/internal/sse"
)

// newPresetHandler builds a handler with a real presetRepo wired in.
func newPresetHandler(t *testing.T, orch tasks.OrchestratorIface) (*ent.Client, *chi.Mux) {
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
		PresetRepo:   repo.NewPermissionPresetRepo(client),
		AuditRepo:    repo.NewAuditEventRepo(client),
		CfgRepo:      repo.NewPipelineConfigRepo(client),
		Orchestrator: orch,
		Broadcaster:  sse.NewTaskBroadcaster(sse.NewBroadcaster()),
	})
	r := chi.NewRouter()
	r.Use(auth.RequireAuth(testJWTSecret))
	h.Mount(r)
	h.MountAgentIngress(r)
	return client, r
}

// TestBulkResolve_Remember_UpsertPresets verifies that resolving with remember=true
// stores a preset entry for the task's cwd so a subsequent bulk-create auto-grants it.
func TestBulkResolve_Remember_UpsertPresets(t *testing.T) {
	client, r := newPresetHandler(t, &captureOrchestrator{})
	taskID, _, reqID := seedPendingPermissionWithPattern(t, client, "remember-preset", "Bash", "git *")

	body := `{"taskId":"` + taskID + `","outcome":"granted","permissionIds":["` + reqID + `"],"remember":true}`
	req := withAuth(t, httptest.NewRequest(http.MethodPost, "/api/permission-requests/bulk-resolve", strings.NewReader(body)))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var resp struct {
		Resolved int `json:"resolved"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.Resolved != 1 {
		t.Fatalf("expected resolved=1, got %d", resp.Resolved)
	}

	// The preset must now exist for the task's cwd.
	presetRepo := repo.NewPermissionPresetRepo(client)
	// userID from JWT sub in withAuth is "user-1" (see handler_test.go withAuth).
	userID := "user-1"
	task, err := repo.NewTaskRepo(client).GetByID(context.Background(), taskID)
	if err != nil {
		t.Fatalf("get task: %v", err)
	}
	rows, err := presetRepo.ListForCwd(context.Background(), &userID, task.Cwd)
	if err != nil {
		t.Fatalf("ListForCwd: %v", err)
	}
	if len(rows) == 0 {
		t.Fatal("expected at least one preset after remember=true resolve, got none")
	}
	found := false
	for _, p := range rows {
		if p.Tool == "Bash" && p.Pattern != nil && *p.Pattern == "git *" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("preset for Bash/\"git *\" not found; got %+v", rows)
	}
}

// TestBulkCreate_PresetCoversEntry verifies that a stored preset causes a
// matching bulk-create entry to be auto-granted without creating a pending request.
func TestBulkCreate_PresetCoversEntry(t *testing.T) {
	client, r := newPresetHandler(t, &captureOrchestrator{})

	// Seed a task and stage_run directly, then seed a preset for the same cwd.
	ctx := context.Background()
	cwd := t.TempDir()
	task, err := repo.NewTaskRepo(client).Create(ctx, repo.CreateTaskInput{
		Slug: "preset-covers", Title: "Preset Covers", Cwd: cwd,
		MaxIterations: 5, Priority: "normal", CurrentStage: "implementation",
	})
	if err != nil {
		t.Fatalf("create task: %v", err)
	}
	run, err := repo.NewStageRunRepo(client).Create(ctx, repo.CreateStageRunInput{TaskID: task.ID, Stage: "implementation", Iteration: 0})
	if err != nil {
		t.Fatalf("create stage run: %v", err)
	}

	// Insert a global preset (no user_id) for the cwd.
	presetRepo := repo.NewPermissionPresetRepo(client)
	pat := "grep *"
	if err := presetRepo.Upsert(ctx, repo.UpsertPresetInput{ProjectCwd: cwd, Tool: "Bash", Pattern: &pat}); err != nil {
		t.Fatalf("upsert preset: %v", err)
	}

	// Bulk-create a request covered by that preset (auth bypass context → nil userID, global preset applies).
	body := `{"stageRunId":"` + run.ID + `","entries":[{"tool":"Bash","pattern":"grep *"}]}`
	// Use MountAgentIngress path (no JWT required for this route in the test router).
	req := httptest.NewRequest(http.MethodPost, "/api/permission-requests/bulk", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	// Add auth so the JWT middleware passes (the preset lookup uses auth context for userID).
	req = withAuth(t, req)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var results []struct {
		Tool        string  `json:"tool"`
		AutoGranted bool    `json:"autoGranted"`
		RequestID   *string `json:"requestId"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &results); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if !results[0].AutoGranted {
		t.Errorf("expected autoGranted=true for preset-covered entry, got false (requestId=%v)", results[0].RequestID)
	}
}

// TestBulkResolve_NoRemember_NoPreset verifies that remember=false (the default)
// does not store any preset even when the outcome is granted.
func TestBulkResolve_NoRemember_NoPreset(t *testing.T) {
	client, r := newPresetHandler(t, &captureOrchestrator{})
	taskID, _, reqID := seedPendingPermissionWithPattern(t, client, "no-remember", "Read", "")

	body := `{"taskId":"` + taskID + `","outcome":"granted","permissionIds":["` + reqID + `"]}`
	req := withAuth(t, httptest.NewRequest(http.MethodPost, "/api/permission-requests/bulk-resolve", strings.NewReader(body)))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	task, err := repo.NewTaskRepo(client).GetByID(context.Background(), taskID)
	if err != nil {
		t.Fatalf("get task: %v", err)
	}
	presetRepo := repo.NewPermissionPresetRepo(client)
	rows, err := presetRepo.ListForCwd(context.Background(), nil, task.Cwd)
	if err != nil {
		t.Fatalf("ListForCwd: %v", err)
	}
	if len(rows) != 0 {
		t.Errorf("expected no presets without remember=true, got %d", len(rows))
	}
}
