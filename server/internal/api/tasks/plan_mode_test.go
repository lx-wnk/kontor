package tasks_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/lx-wnk/kontor/server/internal/api/tasks"
	"github.com/lx-wnk/kontor/server/internal/auth"
	"github.com/lx-wnk/kontor/server/internal/db"
	"github.com/lx-wnk/kontor/server/internal/db/repo"
	"github.com/lx-wnk/kontor/server/internal/sse"
)

// planModeTestEnv groups the pieces tests need to seed config and make HTTP calls.
type planModeTestEnv struct {
	cfgRepo     repo.PipelineConfigRepo
	projectRepo repo.ProjectRepo
	router      *chi.Mux
}

func newPlanModeTestEnv(t *testing.T) planModeTestEnv {
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
	projectRepo := repo.NewProjectRepo(client)

	h := tasks.NewHandler(tasks.Deps{
		TaskRepo:     taskRepo,
		SRRepo:       srRepo,
		PermRepo:     permRepo,
		AuditRepo:    auditRepo,
		CfgRepo:      cfgRepo,
		ProjectRepo:  projectRepo,
		Orchestrator: &noopOrchestrator{},
		Broadcaster:  sse.NewTaskBroadcaster(sse.NewBroadcaster()),
	})

	r := chi.NewRouter()
	r.Use(auth.RequireAuth(testJWTSecret))
	h.Mount(r)

	return planModeTestEnv{
		cfgRepo:     cfgRepo,
		projectRepo: projectRepo,
		router:      r,
	}
}

// postCreateTask sends a POST /api/tasks and returns the decoded response body.
func postCreateTask(t *testing.T, r *chi.Mux, body map[string]any) map[string]any {
	t.Helper()
	b, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/api/tasks", bytes.NewReader(b))
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
	return result
}

// TestCreateTask_PlanMode_ProjectDefault verifies that when the project has
// planMode=true set and the request omits planMode, the created task gets planMode=true.
func TestCreateTask_PlanMode_ProjectDefault(t *testing.T) {
	env := newPlanModeTestEnv(t)
	ctx := context.Background()

	proj, err := env.projectRepo.Create(ctx, "Plan Project", "plan-proj", nil, nil, nil, nil)
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	if err := env.cfgRepo.SetScoped(ctx, &proj.ID, "planMode", "true"); err != nil {
		t.Fatalf("set project planMode: %v", err)
	}

	result := postCreateTask(t, env.router, map[string]any{
		"slug":      "pm-proj-default",
		"title":     "Project PlanMode Default",
		"cwd":       "/tmp/pm-proj",
		"projectId": proj.ID,
	})

	if result["planMode"] != true {
		t.Errorf("expected planMode=true (project default), got %v", result["planMode"])
	}
}

// TestCreateTask_PlanMode_GlobalDefault verifies that when the global planMode=true
// is set, no project override exists, and the request omits planMode, the created
// task gets planMode=true.
func TestCreateTask_PlanMode_GlobalDefault(t *testing.T) {
	env := newPlanModeTestEnv(t)
	ctx := context.Background()

	if err := env.cfgRepo.Set(ctx, "planMode", "true"); err != nil {
		t.Fatalf("set global planMode: %v", err)
	}

	result := postCreateTask(t, env.router, map[string]any{
		"slug":  "pm-global-default",
		"title": "Global PlanMode Default",
		"cwd":   "/tmp/pm-global",
	})

	if result["planMode"] != true {
		t.Errorf("expected planMode=true (global default), got %v", result["planMode"])
	}
}

// TestCreateTask_PlanMode_ExplicitOverridesProjectDefault verifies that an
// explicit planMode=false in the request body wins over a project default of true.
func TestCreateTask_PlanMode_ExplicitOverridesProjectDefault(t *testing.T) {
	env := newPlanModeTestEnv(t)
	ctx := context.Background()

	proj, err := env.projectRepo.Create(ctx, "Override Project", "override-proj", nil, nil, nil, nil)
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	if err := env.cfgRepo.SetScoped(ctx, &proj.ID, "planMode", "true"); err != nil {
		t.Fatalf("set project planMode: %v", err)
	}

	falseVal := false
	body := map[string]any{
		"slug":      "pm-explicit-override",
		"title":     "Explicit Override",
		"cwd":       "/tmp/pm-override",
		"projectId": proj.ID,
		"planMode":  falseVal,
	}
	result := postCreateTask(t, env.router, body)

	if result["planMode"] != false {
		t.Errorf("expected planMode=false (explicit request wins), got %v", result["planMode"])
	}
}

// TestCreateTask_PlanMode_NoConfigNoRequest verifies that when no config exists
// anywhere and the request omits planMode, the created task gets planMode=false.
func TestCreateTask_PlanMode_NoConfigNoRequest(t *testing.T) {
	env := newPlanModeTestEnv(t)

	result := postCreateTask(t, env.router, map[string]any{
		"slug":  "pm-no-config",
		"title": "No Config PlanMode",
		"cwd":   "/tmp/pm-none",
	})

	if result["planMode"] != false {
		t.Errorf("expected planMode=false (no config, no request), got %v", result["planMode"])
	}
}
