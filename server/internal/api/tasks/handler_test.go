package tasks_test

import (
	"bytes"
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
	"github.com/lx-wnk/kontor/server/internal/db/ent/task"
	rawrepo "github.com/lx-wnk/kontor/server/internal/db/rawrepo"
	"github.com/lx-wnk/kontor/server/internal/db/repo"
	"github.com/lx-wnk/kontor/server/internal/pipeline"
	"github.com/lx-wnk/kontor/server/internal/sse"
)

// testCtx returns a background context for use in tests.
func testCtx(_ *testing.T) context.Context {
	return context.Background()
}

const testJWTSecret = "test-secret-for-tasks"

// withAuth adds a valid JWT cookie to the request so RequireAuth passes.
func withAuth(t *testing.T, r *http.Request) *http.Request {
	t.Helper()
	token, err := auth.SignJWT(auth.JWTPayload{Sub: "user-1", Login: "testuser"}, testJWTSecret, 3600)
	if err != nil {
		t.Fatalf("sign jwt: %v", err)
	}
	r.AddCookie(&http.Cookie{Name: "auth_token", Value: token})
	return r
}

// newTestHandler creates a Handler backed by an in-memory SQLite DB.
func newTestHandler(t *testing.T) (*tasks.Handler, *chi.Mux) {
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

	h := tasks.NewHandler(tasks.Deps{
		TaskRepo:     taskRepo,
		SRRepo:       srRepo,
		PermRepo:     permRepo,
		AuditRepo:    auditRepo,
		CfgRepo:      cfgRepo,
		Orchestrator: &noopOrchestrator{},
		Broadcaster:  broadcaster,
		// The grant and capability repos are what let a listed request say it
		// is already denied; production wires both.
		GrantRepo:      repo.NewGrantRepo(client),
		CapabilityRepo: repo.NewCapabilityRepo(client),
	})

	r := chi.NewRouter()
	r.Use(auth.RequireAuth(testJWTSecret))
	h.Mount(r)
	return h, r
}

// newTestHandlerWithBroadcaster creates a Handler against the provided client
// and returns its TaskBroadcaster so tests can subscribe to emitted events.
func newTestHandlerWithBroadcaster(t *testing.T, client *ent.Client) (*sse.TaskBroadcaster, *chi.Mux) {
	t.Helper()
	taskRepo := repo.NewTaskRepo(client)
	srRepo := repo.NewStageRunRepo(client)
	permRepo := repo.NewPermissionRepo(client)
	auditRepo := repo.NewAuditEventRepo(client)
	cfgRepo := repo.NewPipelineConfigRepo(client)

	broadcaster := sse.NewTaskBroadcaster(sse.NewBroadcaster())

	h := tasks.NewHandler(tasks.Deps{
		TaskRepo:       taskRepo,
		SRRepo:         srRepo,
		PermRepo:       permRepo,
		AuditRepo:      auditRepo,
		CfgRepo:        cfgRepo,
		Orchestrator:   &noopOrchestrator{},
		Broadcaster:    broadcaster,
		GrantRepo:      repo.NewGrantRepo(client),
		CapabilityRepo: repo.NewCapabilityRepo(client),
	})

	r := chi.NewRouter()
	r.Use(auth.RequireAuth(testJWTSecret))
	h.Mount(r)
	// Mount ingress routes without bearer middleware — unit tests drive them
	// directly and don't need McpAuthMiddleware.
	h.MountAgentIngress(r)
	return broadcaster, r
}

// noopOrchestrator satisfies tasks.OrchestratorIface without touching the DB.
type noopOrchestrator struct{}

func (n *noopOrchestrator) ProgressTask(_ context.Context, _ string, _ *pipeline.ProgressOpts) (*ent.StageRun, error) {
	return nil, nil
}
func (n *noopOrchestrator) ResumeFromUser(_ context.Context, _ string, _ string) (*ent.StageRun, error) {
	return nil, nil
}
func (n *noopOrchestrator) NotifyTaskTerminated(_ context.Context, _, _ string)      {}
func (n *noopOrchestrator) InvalidateConfigCache()                                   {}
func (n *noopOrchestrator) ClearStalePendingPermissions(_ context.Context, _ string) {}
func (n *noopOrchestrator) RequeueForUser(_ context.Context, _ string, _ string) (*ent.StageRun, error) {
	return nil, nil
}
func (n *noopOrchestrator) EffectiveStageModel(_ context.Context, stage string) string {
	defaults := map[string]string{
		"implementation": "claude-opus-4-6",
		"self_review":    "claude-sonnet-4-6",
		"finalization":   "claude-haiku-4-5",
		"plan_review":    "claude-sonnet-4-6",
	}
	return defaults[stage]
}

func TestListTasks_Empty(t *testing.T) {
	_, r := newTestHandler(t)

	req := httptest.NewRequest(http.MethodGet, "/api/tasks", nil)
	req = withAuth(t, req)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}
	var result []any
	if err := json.Unmarshal(rr.Body.Bytes(), &result); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(result) != 0 {
		t.Fatalf("expected empty list, got %d items", len(result))
	}
}

func TestCreateTask_Success(t *testing.T) {
	_, r := newTestHandler(t)

	body := map[string]any{
		"slug":  "my-task",
		"title": "My Task",
		"cwd":   "/tmp/test",
	}
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
	if result["slug"] != "my-task" {
		t.Errorf("expected slug=my-task, got %v", result["slug"])
	}
}

func TestGetPipelineConfig_ReturnsRetryKeys(t *testing.T) {
	_, r := newTestHandler(t)

	req := httptest.NewRequest(http.MethodGet, "/api/pipeline/config", nil)
	req = withAuth(t, req)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}
	var result map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &result); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if _, ok := result["maxAutoRetries"]; !ok {
		t.Error("expected maxAutoRetries key in pipeline config response")
	}
	if _, ok := result["retryBackoffSeconds"]; !ok {
		t.Error("expected retryBackoffSeconds key in pipeline config response")
	}
	if result["maxAutoRetries"] != float64(3) {
		t.Errorf("expected default maxAutoRetries=3, got %v", result["maxAutoRetries"])
	}
	if result["retryBackoffSeconds"] != float64(60) {
		t.Errorf("expected default retryBackoffSeconds=60, got %v", result["retryBackoffSeconds"])
	}
}

func TestPutPipelineConfig_RetryKeys(t *testing.T) {
	_, r := newTestHandler(t)

	body := map[string]any{
		"maxAutoRetries":      5,
		"retryBackoffSeconds": 120,
	}
	b, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPut, "/api/pipeline/config", bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	req = withAuth(t, req)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}
	var result map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &result); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if result["maxAutoRetries"] != float64(5) {
		t.Errorf("expected maxAutoRetries=5, got %v", result["maxAutoRetries"])
	}
	if result["retryBackoffSeconds"] != float64(120) {
		t.Errorf("expected retryBackoffSeconds=120, got %v", result["retryBackoffSeconds"])
	}
}

func TestListPermissionRequests_OutsideSafeList(t *testing.T) {
	bundle, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { _ = bundle.Client.Close() })
	_, r := newTestHandlerWithBroadcaster(t, bundle.Client)

	// Create a task via HTTP.
	taskBody, _ := json.Marshal(map[string]any{"slug": "pr-test", "title": "PR Test", "cwd": "/tmp/pr"})
	tReq := httptest.NewRequest(http.MethodPost, "/api/tasks", bytes.NewReader(taskBody))
	tReq.Header.Set("Content-Type", "application/json")
	tReq = withAuth(t, tReq)
	tRR := httptest.NewRecorder()
	r.ServeHTTP(tRR, tReq)
	if tRR.Code != http.StatusCreated {
		t.Fatalf("create task: %d %s", tRR.Code, tRR.Body.String())
	}
	var task map[string]any
	_ = json.Unmarshal(tRR.Body.Bytes(), &task)
	taskID := task["id"].(string)

	// Create a stage_run and permission requests directly in the DB.
	srRepo := repo.NewStageRunRepo(bundle.Client)
	permRepo := repo.NewPermissionRepo(bundle.Client)
	sr, err := srRepo.Create(testCtx(t), repo.CreateStageRunInput{
		TaskID:    taskID,
		Stage:     "implementation",
		Iteration: 1,
	})
	if err != nil {
		t.Fatalf("create stage run: %v", err)
	}

	pattern1 := "chmod 777 /tmp"
	pattern2 := "ls /tmp"
	pattern3 := "/tmp/file.txt"
	for _, in := range []repo.CreatePermissionRequestInput{
		{StageRunID: sr.ID, Tool: "Bash", Pattern: &pattern1},
		{StageRunID: sr.ID, Tool: "Bash", Pattern: &pattern2},
		{StageRunID: sr.ID, Tool: "Read", Pattern: &pattern3},
	} {
		if _, err := permRepo.CreatePermissionRequest(testCtx(t), in); err != nil {
			t.Fatalf("create permission request: %v", err)
		}
	}

	// List via HTTP and verify outsideSafeList.
	listReq := httptest.NewRequest(http.MethodGet, "/api/tasks/"+taskID+"/permission-requests", nil)
	listReq = withAuth(t, listReq)
	listRR := httptest.NewRecorder()
	r.ServeHTTP(listRR, listReq)
	if listRR.Code != http.StatusOK {
		t.Fatalf("list permission requests: %d %s", listRR.Code, listRR.Body.String())
	}
	var listed []map[string]any
	if err := json.Unmarshal(listRR.Body.Bytes(), &listed); err != nil {
		t.Fatalf("decode list response: %v", err)
	}
	if len(listed) != 3 {
		t.Fatalf("expected 3 permission requests, got %d: %s", len(listed), listRR.Body.String())
	}

	byPattern := make(map[string]map[string]any)
	for _, item := range listed {
		if p, ok := item["pattern"].(string); ok {
			byPattern[p] = item
		}
	}

	if v, _ := byPattern["chmod 777 /tmp"]["outsideSafeList"].(bool); !v {
		t.Errorf("expected outsideSafeList=true for chmod 777 /tmp, got %v", byPattern["chmod 777 /tmp"]["outsideSafeList"])
	}
	if v, _ := byPattern["ls /tmp"]["outsideSafeList"].(bool); v {
		t.Errorf("expected outsideSafeList=false for ls /tmp")
	}
	if v, _ := byPattern["/tmp/file.txt"]["outsideSafeList"].(bool); v {
		t.Errorf("expected outsideSafeList=false for non-Bash Read tool")
	}
}

// TestGrantPermission_RecordsDecidedBy verifies the direct-grant REST endpoint
// (POST /api/tasks/{id}/permissions) records the authenticated caller's real
// identity, not a role literal.
func TestGrantPermission_RecordsDecidedBy(t *testing.T) {
	client, r := newTestHandlerWithRepos(t)
	task, err := repo.NewTaskRepo(client).Create(testCtx(t), repo.CreateTaskInput{
		Slug:          "grant-decided-by",
		Title:         "Grant Decided By",
		Cwd:           t.TempDir(),
		MaxIterations: 5,
		Priority:      "normal",
		CurrentStage:  "implementation",
	})
	if err != nil {
		t.Fatalf("create task: %v", err)
	}

	body := `{"tool":"Bash","pattern":"echo hi"}`
	req := withAuth(t, httptest.NewRequest(http.MethodPost, "/api/tasks/"+task.ID+"/permissions", strings.NewReader(body)))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", w.Code, w.Body.String())
	}

	perms, err := repo.NewPermissionRepo(client).ListTaskPermissions(testCtx(t), task.ID)
	if err != nil {
		t.Fatalf("list permissions: %v", err)
	}
	if len(perms) != 1 {
		t.Fatalf("expected 1 permission, got %d", len(perms))
	}
	// "user-1" is the Sub withAuth signs into the request's JWT.
	if perms[0].DecidedBy == nil || *perms[0].DecidedBy != "user-1" {
		t.Errorf("expected decided_by=%q, got %v", "user-1", perms[0].DecidedBy)
	}
	if perms[0].DecidedAt == nil {
		t.Error("expected decided_at to be recorded, got nil")
	}
}

// TestCreateTask_BodyCannotAttachApplications pins that applications reach a
// task only through the scheduler: a caller that could name them on the task
// it creates could hand itself any mailbox.
func TestCreateTask_BodyCannotAttachApplications(t *testing.T) {
	client, r := newTestHandlerWithRepos(t)
	b, _ := json.Marshal(map[string]any{
		"slug": "no-attach", "title": "T", "cwd": "/tmp/no-attach",
		"applications": []string{"res-mail"},
	})
	req := httptest.NewRequest(http.MethodPost, "/api/tasks", bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	req = withAuth(t, req)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)
	if rr.Code != http.StatusCreated && rr.Code != http.StatusOK {
		t.Fatalf("create: %d %s", rr.Code, rr.Body.String())
	}
	created, err := client.Task.Query().Where(task.Slug("no-attach")).Only(context.Background())
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if len(created.Applications) != 0 {
		t.Fatalf("applications = %v — the create body must not attach applications", created.Applications)
	}
}

func TestCreateTask_DuplicateSlug(t *testing.T) {
	_, r := newTestHandler(t)

	body := map[string]any{
		"slug":  "dup-slug",
		"title": "First",
		"cwd":   "/tmp/test",
	}
	b, _ := json.Marshal(body)

	// First creation — should succeed.
	req1 := httptest.NewRequest(http.MethodPost, "/api/tasks", bytes.NewReader(b))
	req1.Header.Set("Content-Type", "application/json")
	req1 = withAuth(t, req1)
	rr1 := httptest.NewRecorder()
	r.ServeHTTP(rr1, req1)
	if rr1.Code != http.StatusCreated {
		t.Fatalf("first create: expected 201, got %d: %s", rr1.Code, rr1.Body.String())
	}

	// Second creation with same slug — should return 409.
	b2, _ := json.Marshal(body)
	req2 := httptest.NewRequest(http.MethodPost, "/api/tasks", bytes.NewReader(b2))
	req2.Header.Set("Content-Type", "application/json")
	req2 = withAuth(t, req2)
	rr2 := httptest.NewRecorder()
	r.ServeHTTP(rr2, req2)
	if rr2.Code != http.StatusConflict {
		t.Fatalf("duplicate create: expected 409, got %d: %s", rr2.Code, rr2.Body.String())
	}
}

// newTestHandlerWithRepos wires ProjectRepo, SpawnerRepo, and SRBulkRepo in
// addition to the repos from newTestHandler. Returns the ent.Client so tests
// can seed data, and the configured router.
func newTestHandlerWithRepos(t *testing.T) (*ent.Client, *chi.Mux) {
	t.Helper()
	bundle, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	client := bundle.Client
	t.Cleanup(func() { _ = client.Close() })

	taskRepo := repo.NewTaskRepo(client)
	srRepo := repo.NewStageRunRepo(client)
	srBulkRepo := rawrepo.NewStageRunBulkRepo(bundle.DB)
	permRepo := repo.NewPermissionRepo(client)
	auditRepo := repo.NewAuditEventRepo(client)
	cfgRepo := repo.NewPipelineConfigRepo(client)
	projectRepo := repo.NewProjectRepo(client)
	spawnerRepo := repo.NewSpawnerRepo(client)

	broadcaster := sse.NewTaskBroadcaster(sse.NewBroadcaster())

	h := tasks.NewHandler(tasks.Deps{
		Client:       client,
		TaskRepo:     taskRepo,
		SRRepo:       srRepo,
		SRBulkRepo:   srBulkRepo,
		PermRepo:     permRepo,
		AuditRepo:    auditRepo,
		CfgRepo:      cfgRepo,
		DepRepo:      repo.NewDependencyRepo(client),
		ProjectRepo:  projectRepo,
		SpawnerRepo:  spawnerRepo,
		Orchestrator: &noopOrchestrator{},
		Broadcaster:  broadcaster,
	})

	r := chi.NewRouter()
	r.Use(auth.RequireAuth(testJWTSecret))
	h.Mount(r)
	// Mount ingress routes without bearer middleware — unit tests drive them
	// directly and don't need McpAuthMiddleware.
	h.MountAgentIngress(r)
	return client, r
}

// TestCreateTask_ValidationRejections covers all 400-returning branches in create().
func TestCreateTask_ValidationRejections(t *testing.T) {
	longDesc := strings.Repeat("a", 10001)

	cases := []struct {
		name     string
		body     map[string]any
		wantBody string // substring the 400 response must contain (proves which branch fired)
	}{
		{
			name: "bad_slug",
			body: map[string]any{"slug": "Bad Slug!", "title": "T", "cwd": "/tmp"},
		},
		{
			name: "empty_title",
			body: map[string]any{"slug": "valid-slug", "title": "", "cwd": "/tmp"},
		},
		{
			name:     "title_too_long",
			body:     map[string]any{"slug": "valid-slug", "title": strings.Repeat("x", 201), "cwd": "/tmp"},
			wantBody: "title is required",
		},
		{
			name:     "description_too_long",
			body:     map[string]any{"slug": "valid-slug", "title": "T", "description": longDesc, "cwd": "/tmp"},
			wantBody: "description must be",
		},
		{
			name: "missing_cwd",
			body: map[string]any{"slug": "valid-slug", "title": "T", "cwd": ""},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, r := newTestHandler(t)
			b, _ := json.Marshal(tc.body)
			req := httptest.NewRequest(http.MethodPost, "/api/tasks", bytes.NewReader(b))
			req.Header.Set("Content-Type", "application/json")
			req = withAuth(t, req)
			rr := httptest.NewRecorder()
			r.ServeHTTP(rr, req)
			if rr.Code != http.StatusBadRequest {
				t.Fatalf("expected 400, got %d: %s", rr.Code, rr.Body.String())
			}
			if tc.wantBody != "" && !strings.Contains(rr.Body.String(), tc.wantBody) {
				t.Fatalf("expected body to contain %q, got: %s", tc.wantBody, rr.Body.String())
			}
		})
	}
}

// TestCreateTask_DescriptionAtLimit_Accepted guards the boundary: a description
// of exactly maxDescriptionChars (10000) must NOT be rejected, so the check
// stays a strict `>` and never regresses to `>=`.
func TestCreateTask_DescriptionAtLimit_Accepted(t *testing.T) {
	_, r := newTestHandler(t)
	body := map[string]any{
		"slug":        "desc-at-limit",
		"title":       "T",
		"cwd":         "/tmp/dal",
		"description": strings.Repeat("a", 10000),
	}
	b, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/api/tasks", bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	req = withAuth(t, req)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)
	if rr.Code != http.StatusCreated {
		t.Fatalf("expected 201 for description at limit, got %d: %s", rr.Code, rr.Body.String())
	}
}

// TestUpdateTask_ValidationRejections covers 400-returning branches in update().
func TestUpdateTask_ValidationRejections(t *testing.T) {
	_, r := newTestHandler(t)

	// Seed a valid task to PATCH.
	createBody := map[string]any{"slug": "patch-target", "title": "Patch Target", "cwd": "/tmp/pt"}
	b, _ := json.Marshal(createBody)
	req := httptest.NewRequest(http.MethodPost, "/api/tasks", bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	req = withAuth(t, req)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)
	if rr.Code != http.StatusCreated {
		t.Fatalf("seed task: expected 201, got %d: %s", rr.Code, rr.Body.String())
	}
	var created map[string]any
	_ = json.Unmarshal(rr.Body.Bytes(), &created)
	taskID := created["id"].(string)

	longDesc := strings.Repeat("a", 10001)
	stage := "implementation"

	cases := []struct {
		name     string
		body     map[string]any
		wantBody string // substring the 400 response must contain (proves which branch fired)
	}{
		{
			name:     "currentStage_rejected",
			body:     map[string]any{"currentStage": &stage},
			wantBody: "currentStage cannot be set",
		},
		{
			name:     "description_too_long",
			body:     map[string]any{"description": longDesc},
			wantBody: "description must be",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			b, _ := json.Marshal(tc.body)
			req := httptest.NewRequest(http.MethodPatch, "/api/tasks/"+taskID, bytes.NewReader(b))
			req.Header.Set("Content-Type", "application/json")
			req = withAuth(t, req)
			rr := httptest.NewRecorder()
			r.ServeHTTP(rr, req)
			if rr.Code != http.StatusBadRequest {
				t.Fatalf("expected 400, got %d: %s", rr.Code, rr.Body.String())
			}
			if tc.wantBody != "" && !strings.Contains(rr.Body.String(), tc.wantBody) {
				t.Fatalf("expected body to contain %q, got: %s", tc.wantBody, rr.Body.String())
			}
		})
	}

	// Boundary: a description of exactly maxDescriptionChars must be accepted.
	atLimit, _ := json.Marshal(map[string]any{"description": strings.Repeat("a", 10000)})
	req = httptest.NewRequest(http.MethodPatch, "/api/tasks/"+taskID, bytes.NewReader(atLimit))
	req.Header.Set("Content-Type", "application/json")
	req = withAuth(t, req)
	rr = httptest.NewRecorder()
	r.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200 for description at limit, got %d: %s", rr.Code, rr.Body.String())
	}
}

// TestCreateTask_ProjectNotFound expects 404 when projectId does not exist.
func TestCreateTask_ProjectNotFound(t *testing.T) {
	_, r := newTestHandlerWithRepos(t)

	body := map[string]any{
		"slug":      "proj-miss",
		"title":     "T",
		"cwd":       "/tmp/proj",
		"projectId": "nonexistent-id",
	}
	b, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/api/tasks", bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	req = withAuth(t, req)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d: %s", rr.Code, rr.Body.String())
	}
}

// TestCreateTask_SpawnerNotFound expects 404 when spawnerId does not exist.
func TestCreateTask_SpawnerNotFound(t *testing.T) {
	_, r := newTestHandlerWithRepos(t)

	body := map[string]any{
		"slug":      "spawner-miss",
		"title":     "T",
		"cwd":       "/tmp/spawner",
		"spawnerId": "nonexistent-id",
	}
	b, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/api/tasks", bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	req = withAuth(t, req)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d: %s", rr.Code, rr.Body.String())
	}
}
