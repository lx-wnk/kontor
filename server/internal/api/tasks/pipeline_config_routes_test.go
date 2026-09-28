package tasks_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/lx-wnk/kontor/server/internal/api/tasks"
	"github.com/lx-wnk/kontor/server/internal/auth"
	"github.com/lx-wnk/kontor/server/internal/claudemodel"
	"github.com/lx-wnk/kontor/server/internal/db"
	"github.com/lx-wnk/kontor/server/internal/db/ent"
	"github.com/lx-wnk/kontor/server/internal/db/repo"
	"github.com/lx-wnk/kontor/server/internal/pipeline"
	"github.com/lx-wnk/kontor/server/internal/sse"
)

// newTestHandlerWithRealOrchestrator returns a handler backed by an in-memory DB
// and a real PipelineOrchestrator, so EffectiveStageModel resolves from the DB.
func newTestHandlerWithRealOrchestrator(t *testing.T) *chi.Mux {
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

	orch, err := pipeline.NewOrchestrator(pipeline.OrchestratorOptions{
		TaskRepo:       taskRepo,
		StageRunRepo:   srRepo,
		PermissionRepo: permRepo,
		AuditRepo:      auditRepo,
		ConfigRepo:     cfgRepo,
	})
	if err != nil {
		t.Fatalf("NewOrchestrator: %v", err)
	}

	h := tasks.NewHandler(tasks.Deps{
		TaskRepo:     taskRepo,
		SRRepo:       srRepo,
		PermRepo:     permRepo,
		AuditRepo:    auditRepo,
		CfgRepo:      cfgRepo,
		Orchestrator: orch,
		Broadcaster:  sse.NewTaskBroadcaster(sse.NewBroadcaster()),
	})
	r := chi.NewRouter()
	r.Use(auth.RequireAuth(testJWTSecret))
	h.Mount(r)
	return r
}

func TestGetPipelineConfig_DefaultStageModels(t *testing.T) {
	_, r := newTestHandler(t)

	req := withAuth(t, httptest.NewRequest(http.MethodGet, "/api/pipeline/config", nil))
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}

	var resp struct {
		MaxParallelOrchestrators int               `json:"maxParallelOrchestrators"`
		StageTimeoutSeconds      int               `json:"stageTimeoutSeconds"`
		StageModels              map[string]string `json:"stageModels"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}

	if resp.MaxParallelOrchestrators != 3 {
		t.Errorf("expected maxParallelOrchestrators=3, got %d", resp.MaxParallelOrchestrators)
	}
	if resp.StageTimeoutSeconds != 1800 {
		t.Errorf("expected stageTimeoutSeconds=1800, got %d", resp.StageTimeoutSeconds)
	}

	// noopOrchestrator returns coded defaults.
	wantModels := map[string]string{
		"implementation": "claude-opus-4-6",
		"self_review":    "claude-sonnet-4-6",
		"finalization":   "claude-haiku-4-5",
		"plan_review":    "claude-sonnet-4-6",
	}
	for stage, want := range wantModels {
		got, ok := resp.StageModels[stage]
		if !ok {
			t.Errorf("stageModels missing key %q", stage)
			continue
		}
		if got != want {
			t.Errorf("stageModels[%q]: expected %q, got %q", stage, want, got)
		}
	}
	if len(resp.StageModels) != len(wantModels) {
		t.Errorf("stageModels has %d keys, expected %d", len(resp.StageModels), len(wantModels))
	}
}

func TestPutPipelineConfig_StageModelOverrideRoundTrip(t *testing.T) {
	// Real orchestrator so EffectiveStageModel resolves from the DB row we write.
	r := newTestHandlerWithRealOrchestrator(t)

	// PUT — override only self_review.
	body := map[string]any{
		"stageModels": map[string]string{
			"self_review": "claude-haiku-4-5",
		},
	}
	b, _ := json.Marshal(body)
	req := withAuth(t, httptest.NewRequest(http.MethodPut, "/api/pipeline/config", bytes.NewReader(b)))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("PUT: expected 200, got %d: %s", rr.Code, rr.Body.String())
	}

	var resp struct {
		StageModels map[string]string `json:"stageModels"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode PUT response: %v", err)
	}
	if resp.StageModels["self_review"] != "claude-haiku-4-5" {
		t.Errorf("after PUT: self_review=%q, want claude-haiku-4-5", resp.StageModels["self_review"])
	}
	// Other stages should still return coded defaults.
	if want := claudemodel.Latest(claudemodel.Opus); resp.StageModels["implementation"] != want {
		t.Errorf("after PUT: implementation=%q, want coded default %s", resp.StageModels["implementation"], want)
	}
	if want := claudemodel.Latest(claudemodel.Haiku); resp.StageModels["finalization"] != want {
		t.Errorf("after PUT: finalization=%q, want coded default %s", resp.StageModels["finalization"], want)
	}

	// GET should round-trip the persisted override.
	req2 := withAuth(t, httptest.NewRequest(http.MethodGet, "/api/pipeline/config", nil))
	rr2 := httptest.NewRecorder()
	r.ServeHTTP(rr2, req2)
	if rr2.Code != http.StatusOK {
		t.Fatalf("GET: expected 200, got %d: %s", rr2.Code, rr2.Body.String())
	}
	var resp2 struct {
		StageModels map[string]string `json:"stageModels"`
	}
	if err := json.Unmarshal(rr2.Body.Bytes(), &resp2); err != nil {
		t.Fatalf("decode GET response: %v", err)
	}
	if resp2.StageModels["self_review"] != "claude-haiku-4-5" {
		t.Errorf("GET round-trip: self_review=%q, want claude-haiku-4-5", resp2.StageModels["self_review"])
	}
}

func TestPutPipelineConfig_EmptyModelClearsRow(t *testing.T) {
	// Use a real orchestrator so EffectiveStageModel resolves from the DB.
	r := newTestHandlerWithRealOrchestrator(t)

	// First, override self_review with a non-default model.
	set := map[string]any{"stageModels": map[string]string{"self_review": "claude-haiku-4-5"}}
	b, _ := json.Marshal(set)
	req := withAuth(t, httptest.NewRequest(http.MethodPut, "/api/pipeline/config", bytes.NewReader(b)))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("PUT override: expected 200, got %d: %s", rr.Code, rr.Body.String())
	}

	// Now clear it by sending empty string — should revert to the coded default.
	clear := map[string]any{"stageModels": map[string]string{"self_review": ""}}
	b2, _ := json.Marshal(clear)
	req2 := withAuth(t, httptest.NewRequest(http.MethodPut, "/api/pipeline/config", bytes.NewReader(b2)))
	req2.Header.Set("Content-Type", "application/json")
	rr2 := httptest.NewRecorder()
	r.ServeHTTP(rr2, req2)
	if rr2.Code != http.StatusOK {
		t.Fatalf("PUT clear: expected 200, got %d: %s", rr2.Code, rr2.Body.String())
	}
	var resp struct {
		StageModels map[string]string `json:"stageModels"`
	}
	if err := json.Unmarshal(rr2.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if want := claudemodel.Latest(claudemodel.Sonnet); resp.StageModels["self_review"] != want {
		t.Errorf("after clear: self_review=%q, want coded default %s", resp.StageModels["self_review"], want)
	}
}

func TestPutPipelineConfig_UnknownModelRejected(t *testing.T) {
	_, r := newTestHandler(t)

	body := map[string]any{
		"stageModels": map[string]string{
			"implementation": "claude-unknown-99",
		},
	}
	b, _ := json.Marshal(body)
	req := withAuth(t, httptest.NewRequest(http.MethodPut, "/api/pipeline/config", bytes.NewReader(b)))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for unknown model, got %d: %s", rr.Code, rr.Body.String())
	}
}

func TestPutPipelineConfig_UnknownStageIgnored(t *testing.T) {
	_, r := newTestHandler(t)

	body := map[string]any{
		"stageModels": map[string]string{
			"nonexistent_stage": "some-model",
		},
	}
	b, _ := json.Marshal(body)
	req := withAuth(t, httptest.NewRequest(http.MethodPut, "/api/pipeline/config", bytes.NewReader(b)))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}
	// Just verify the response still has exactly the 3 valid stage keys.
	var resp struct {
		StageModels map[string]string `json:"stageModels"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if _, bad := resp.StageModels["nonexistent_stage"]; bad {
		t.Errorf("unknown stage key leaked into GET response")
	}
	if len(resp.StageModels) != 4 {
		t.Errorf("expected 4 stageModels keys, got %d", len(resp.StageModels))
	}
}

// newTestHandlerWithSpawner builds a handler wired with projectRepo + spawnerRepo and
// seeds a spawner. Returns the router, the ent.Client (for direct seeding), and the
// spawner ID.
func newTestHandlerWithSpawner(t *testing.T) (*ent.Client, *chi.Mux, string) {
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
	spawnerRepo := repo.NewSpawnerRepo(client)

	spawner, err := spawnerRepo.Create(testCtx(t), "test", "test-spawner", "claude", nil, nil, nil, nil, "claude", nil, false)
	if err != nil {
		t.Fatalf("seed spawner: %v", err)
	}

	h := tasks.NewHandler(tasks.Deps{
		Client:       client,
		TaskRepo:     taskRepo,
		SRRepo:       srRepo,
		PermRepo:     permRepo,
		AuditRepo:    auditRepo,
		CfgRepo:      cfgRepo,
		ProjectRepo:  projectRepo,
		SpawnerRepo:  spawnerRepo,
		Orchestrator: &noopOrchestrator{},
		Broadcaster:  sse.NewTaskBroadcaster(sse.NewBroadcaster()),
	})

	r := chi.NewRouter()
	r.Use(auth.RequireAuth(testJWTSecret))
	h.Mount(r)
	return client, r, spawner.ID
}

// TestGetPipelineConfig_StageSpawnersInResponse verifies the global GET includes stageSpawners.
func TestGetPipelineConfig_StageSpawnersInResponse(t *testing.T) {
	_, r := newTestHandler(t)

	req := withAuth(t, httptest.NewRequest(http.MethodGet, "/api/pipeline/config", nil))
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}
	var resp struct {
		StageSpawners map[string]string `json:"stageSpawners"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.StageSpawners == nil {
		t.Fatal("stageSpawners must be present in global config response")
	}
	for _, stage := range []string{"implementation", "self_review", "finalization"} {
		if _, ok := resp.StageSpawners[stage]; !ok {
			t.Errorf("stageSpawners missing key %q", stage)
		}
	}
}

// TestPutPipelineConfig_StageSpawnersRoundTrip verifies global stageSpawner set/get.
func TestPutPipelineConfig_StageSpawnersRoundTrip(t *testing.T) {
	_, r, spawnerID := newTestHandlerWithSpawner(t)

	body := map[string]any{
		"stageSpawners": map[string]string{
			"implementation": spawnerID,
		},
	}
	b, _ := json.Marshal(body)
	req := withAuth(t, httptest.NewRequest(http.MethodPut, "/api/pipeline/config", bytes.NewReader(b)))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("PUT: expected 200, got %d: %s", rr.Code, rr.Body.String())
	}

	var resp struct {
		StageSpawners map[string]string `json:"stageSpawners"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.StageSpawners["implementation"] != spawnerID {
		t.Errorf("expected implementation spawner=%q, got %q", spawnerID, resp.StageSpawners["implementation"])
	}
	// Other stages should be empty (no override).
	if resp.StageSpawners["self_review"] != "" {
		t.Errorf("self_review spawner should be empty, got %q", resp.StageSpawners["self_review"])
	}

	// Clear by sending empty string.
	clear := map[string]any{"stageSpawners": map[string]string{"implementation": ""}}
	b2, _ := json.Marshal(clear)
	req2 := withAuth(t, httptest.NewRequest(http.MethodPut, "/api/pipeline/config", bytes.NewReader(b2)))
	req2.Header.Set("Content-Type", "application/json")
	rr2 := httptest.NewRecorder()
	r.ServeHTTP(rr2, req2)
	if rr2.Code != http.StatusOK {
		t.Fatalf("PUT clear: expected 200, got %d: %s", rr2.Code, rr2.Body.String())
	}
	var resp2 struct {
		StageSpawners map[string]string `json:"stageSpawners"`
	}
	if err := json.Unmarshal(rr2.Body.Bytes(), &resp2); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp2.StageSpawners["implementation"] != "" {
		t.Errorf("after clear: implementation spawner should be empty, got %q", resp2.StageSpawners["implementation"])
	}
}

// TestPutPipelineConfig_UnknownSpawnerRejected verifies 400 on unknown spawner ID.
func TestPutPipelineConfig_UnknownSpawnerRejected(t *testing.T) {
	_, r, _ := newTestHandlerWithSpawner(t)

	body := map[string]any{
		"stageSpawners": map[string]string{
			"implementation": "nonexistent-spawner-id",
		},
	}
	b, _ := json.Marshal(body)
	req := withAuth(t, httptest.NewRequest(http.MethodPut, "/api/pipeline/config", bytes.NewReader(b)))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for unknown spawner, got %d: %s", rr.Code, rr.Body.String())
	}
}

// TestProjectPipelineConfig_RoundTrip verifies per-project set/get/clear for stageSpawners and stageModels.
func TestProjectPipelineConfig_RoundTrip(t *testing.T) {
	client, r, spawnerID := newTestHandlerWithSpawner(t)
	projectRepo := repo.NewProjectRepo(client)
	proj, err := projectRepo.Create(testCtx(t), "Test Project", "test-project", nil, nil, nil, nil)
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	projectID := proj.ID

	// PUT — set stageSpawners and stageModels.
	body := map[string]any{
		"stageSpawners": map[string]string{"implementation": spawnerID},
		"stageModels":   map[string]string{"self_review": "claude-haiku-4-5"},
	}
	b, _ := json.Marshal(body)
	req := withAuth(t, httptest.NewRequest(http.MethodPut, "/api/projects/"+projectID+"/pipeline-config", bytes.NewReader(b)))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("PUT: expected 200, got %d: %s", rr.Code, rr.Body.String())
	}

	var putResp struct {
		StageSpawners map[string]string `json:"stageSpawners"`
		StageModels   map[string]string `json:"stageModels"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &putResp); err != nil {
		t.Fatalf("decode PUT: %v", err)
	}
	if putResp.StageSpawners["implementation"] != spawnerID {
		t.Errorf("PUT: implementation spawner=%q, want %q", putResp.StageSpawners["implementation"], spawnerID)
	}
	if putResp.StageModels["self_review"] != "claude-haiku-4-5" {
		t.Errorf("PUT: self_review model=%q, want claude-haiku-4-5", putResp.StageModels["self_review"])
	}
	// Unset stages should be empty string (inherit).
	if putResp.StageSpawners["self_review"] != "" {
		t.Errorf("PUT: self_review spawner should be empty, got %q", putResp.StageSpawners["self_review"])
	}

	// GET — verify round-trip.
	req2 := withAuth(t, httptest.NewRequest(http.MethodGet, "/api/projects/"+projectID+"/pipeline-config", nil))
	rr2 := httptest.NewRecorder()
	r.ServeHTTP(rr2, req2)
	if rr2.Code != http.StatusOK {
		t.Fatalf("GET: expected 200, got %d: %s", rr2.Code, rr2.Body.String())
	}
	var getResp struct {
		StageSpawners map[string]string `json:"stageSpawners"`
		StageModels   map[string]string `json:"stageModels"`
	}
	if err := json.Unmarshal(rr2.Body.Bytes(), &getResp); err != nil {
		t.Fatalf("decode GET: %v", err)
	}
	if getResp.StageSpawners["implementation"] != spawnerID {
		t.Errorf("GET: implementation spawner=%q, want %q", getResp.StageSpawners["implementation"], spawnerID)
	}

	// Clear the spawner override.
	clear := map[string]any{"stageSpawners": map[string]string{"implementation": ""}}
	b2, _ := json.Marshal(clear)
	req3 := withAuth(t, httptest.NewRequest(http.MethodPut, "/api/projects/"+projectID+"/pipeline-config", bytes.NewReader(b2)))
	req3.Header.Set("Content-Type", "application/json")
	rr3 := httptest.NewRecorder()
	r.ServeHTTP(rr3, req3)
	if rr3.Code != http.StatusOK {
		t.Fatalf("PUT clear: expected 200, got %d: %s", rr3.Code, rr3.Body.String())
	}
	var clearResp struct {
		StageSpawners map[string]string `json:"stageSpawners"`
	}
	if err := json.Unmarshal(rr3.Body.Bytes(), &clearResp); err != nil {
		t.Fatalf("decode clear: %v", err)
	}
	if clearResp.StageSpawners["implementation"] != "" {
		t.Errorf("after clear: implementation spawner should be empty, got %q", clearResp.StageSpawners["implementation"])
	}
}

// TestProjectPipelineConfig_UnknownSpawnerRejected verifies 400 on unknown spawner for project scope.
func TestProjectPipelineConfig_UnknownSpawnerRejected(t *testing.T) {
	client, r, _ := newTestHandlerWithSpawner(t)
	projectRepo := repo.NewProjectRepo(client)
	proj, err := projectRepo.Create(testCtx(t), "P2", "p2", nil, nil, nil, nil)
	if err != nil {
		t.Fatalf("create project: %v", err)
	}

	body := map[string]any{
		"stageSpawners": map[string]string{"implementation": "nonexistent-id"},
	}
	b, _ := json.Marshal(body)
	req := withAuth(t, httptest.NewRequest(http.MethodPut, "/api/projects/"+proj.ID+"/pipeline-config", bytes.NewReader(b)))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for unknown spawner, got %d: %s", rr.Code, rr.Body.String())
	}
}

// TestProjectPipelineConfig_UnknownProjectReturns404 verifies 404 when project does not exist.
func TestProjectPipelineConfig_UnknownProjectReturns404(t *testing.T) {
	_, r, _ := newTestHandlerWithSpawner(t)

	req := withAuth(t, httptest.NewRequest(http.MethodGet, "/api/projects/nonexistent-id/pipeline-config", nil))
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d: %s", rr.Code, rr.Body.String())
	}

	body := map[string]any{"stageModels": map[string]string{"implementation": "claude-haiku-4-5"}}
	b, _ := json.Marshal(body)
	req2 := withAuth(t, httptest.NewRequest(http.MethodPut, "/api/projects/nonexistent-id/pipeline-config", bytes.NewReader(b)))
	req2.Header.Set("Content-Type", "application/json")
	rr2 := httptest.NewRecorder()
	r.ServeHTTP(rr2, req2)
	if rr2.Code != http.StatusNotFound {
		t.Errorf("PUT: expected 404, got %d: %s", rr2.Code, rr2.Body.String())
	}
}

// TestPutPipelineConfig_PlanReviewStageModelRoundTrip verifies that plan_review
// is accepted as a valid stage key for stageModels and stageSpawners.
func TestPutPipelineConfig_PlanReviewStageModelRoundTrip(t *testing.T) {
	r := newTestHandlerWithRealOrchestrator(t)

	body := map[string]any{
		"stageModels": map[string]string{
			"plan_review": "claude-haiku-4-5",
		},
	}
	b, _ := json.Marshal(body)
	req := withAuth(t, httptest.NewRequest(http.MethodPut, "/api/pipeline/config", bytes.NewReader(b)))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("PUT: expected 200, got %d: %s", rr.Code, rr.Body.String())
	}

	var putResp struct {
		StageModels map[string]string `json:"stageModels"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &putResp); err != nil {
		t.Fatalf("decode PUT: %v", err)
	}
	if putResp.StageModels["plan_review"] != "claude-haiku-4-5" {
		t.Errorf("PUT: plan_review model=%q, want claude-haiku-4-5", putResp.StageModels["plan_review"])
	}

	// GET should round-trip the persisted override.
	req2 := withAuth(t, httptest.NewRequest(http.MethodGet, "/api/pipeline/config", nil))
	rr2 := httptest.NewRecorder()
	r.ServeHTTP(rr2, req2)
	if rr2.Code != http.StatusOK {
		t.Fatalf("GET: expected 200, got %d: %s", rr2.Code, rr2.Body.String())
	}
	var getResp struct {
		StageModels map[string]string `json:"stageModels"`
	}
	if err := json.Unmarshal(rr2.Body.Bytes(), &getResp); err != nil {
		t.Fatalf("decode GET: %v", err)
	}
	if getResp.StageModels["plan_review"] != "claude-haiku-4-5" {
		t.Errorf("GET round-trip: plan_review model=%q, want claude-haiku-4-5", getResp.StageModels["plan_review"])
	}
}

// TestPutPipelineConfig_PlanReviewStageSpawnerRoundTrip verifies plan_review
// is accepted as a valid stage key for stageSpawners.
func TestPutPipelineConfig_PlanReviewStageSpawnerRoundTrip(t *testing.T) {
	_, r, spawnerID := newTestHandlerWithSpawner(t)

	body := map[string]any{
		"stageSpawners": map[string]string{
			"plan_review": spawnerID,
		},
	}
	b, _ := json.Marshal(body)
	req := withAuth(t, httptest.NewRequest(http.MethodPut, "/api/pipeline/config", bytes.NewReader(b)))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("PUT: expected 200, got %d: %s", rr.Code, rr.Body.String())
	}

	var resp struct {
		StageSpawners map[string]string `json:"stageSpawners"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.StageSpawners["plan_review"] != spawnerID {
		t.Errorf("plan_review spawner=%q, want %q", resp.StageSpawners["plan_review"], spawnerID)
	}
}

// TestPipelineConfig_PlanModeGlobalRoundTrip verifies the global planMode bool
// round-trips through PUT/GET.
func TestPipelineConfig_PlanModeGlobalRoundTrip(t *testing.T) {
	_, r := newTestHandler(t)

	// Default should be false.
	req := withAuth(t, httptest.NewRequest(http.MethodGet, "/api/pipeline/config", nil))
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("GET default: expected 200, got %d: %s", rr.Code, rr.Body.String())
	}
	var defaultResp struct {
		PlanMode bool `json:"planMode"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &defaultResp); err != nil {
		t.Fatalf("decode default GET: %v", err)
	}
	if defaultResp.PlanMode {
		t.Errorf("default planMode should be false, got true")
	}

	// PUT planMode=true.
	planModeTrue := true
	putBody := map[string]any{"planMode": planModeTrue}
	b, _ := json.Marshal(putBody)
	req2 := withAuth(t, httptest.NewRequest(http.MethodPut, "/api/pipeline/config", bytes.NewReader(b)))
	req2.Header.Set("Content-Type", "application/json")
	rr2 := httptest.NewRecorder()
	r.ServeHTTP(rr2, req2)
	if rr2.Code != http.StatusOK {
		t.Fatalf("PUT planMode=true: expected 200, got %d: %s", rr2.Code, rr2.Body.String())
	}
	var putResp struct {
		PlanMode bool `json:"planMode"`
	}
	if err := json.Unmarshal(rr2.Body.Bytes(), &putResp); err != nil {
		t.Fatalf("decode PUT response: %v", err)
	}
	if !putResp.PlanMode {
		t.Errorf("after PUT planMode=true: response planMode=%v, want true", putResp.PlanMode)
	}

	// GET should round-trip.
	req3 := withAuth(t, httptest.NewRequest(http.MethodGet, "/api/pipeline/config", nil))
	rr3 := httptest.NewRecorder()
	r.ServeHTTP(rr3, req3)
	if rr3.Code != http.StatusOK {
		t.Fatalf("GET after PUT: expected 200, got %d: %s", rr3.Code, rr3.Body.String())
	}
	var getResp struct {
		PlanMode bool `json:"planMode"`
	}
	if err := json.Unmarshal(rr3.Body.Bytes(), &getResp); err != nil {
		t.Fatalf("decode GET: %v", err)
	}
	if !getResp.PlanMode {
		t.Errorf("GET round-trip: planMode=%v, want true", getResp.PlanMode)
	}
}

// TestPipelineConfig_PlanModeProjectRoundTrip verifies the per-project planMode bool
// round-trips through PUT/GET on the project pipeline-config endpoints.
func TestPipelineConfig_PlanModeProjectRoundTrip(t *testing.T) {
	client, r, _ := newTestHandlerWithSpawner(t)
	projectRepo := repo.NewProjectRepo(client)
	proj, err := projectRepo.Create(testCtx(t), "PM Project", "pm-project", nil, nil, nil, nil)
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	projectID := proj.ID

	// Default: planMode absent (false).
	req := withAuth(t, httptest.NewRequest(http.MethodGet, "/api/projects/"+projectID+"/pipeline-config", nil))
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("GET default: expected 200, got %d: %s", rr.Code, rr.Body.String())
	}
	var defaultResp struct {
		PlanMode *bool `json:"planMode"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &defaultResp); err != nil {
		t.Fatalf("decode default GET: %v", err)
	}
	// No project-level override yet — should be nil or false.
	if defaultResp.PlanMode != nil && *defaultResp.PlanMode {
		t.Errorf("default project planMode should be nil/false, got true")
	}

	// PUT project planMode=true.
	planModeTrue := true
	putBody := map[string]any{"planMode": planModeTrue}
	b, _ := json.Marshal(putBody)
	req2 := withAuth(t, httptest.NewRequest(http.MethodPut, "/api/projects/"+projectID+"/pipeline-config", bytes.NewReader(b)))
	req2.Header.Set("Content-Type", "application/json")
	rr2 := httptest.NewRecorder()
	r.ServeHTTP(rr2, req2)
	if rr2.Code != http.StatusOK {
		t.Fatalf("PUT planMode=true: expected 200, got %d: %s", rr2.Code, rr2.Body.String())
	}
	var putResp struct {
		PlanMode *bool `json:"planMode"`
	}
	if err := json.Unmarshal(rr2.Body.Bytes(), &putResp); err != nil {
		t.Fatalf("decode PUT response: %v", err)
	}
	if putResp.PlanMode == nil || !*putResp.PlanMode {
		t.Errorf("after PUT planMode=true: response planMode=%v, want true", putResp.PlanMode)
	}

	// GET should round-trip.
	req3 := withAuth(t, httptest.NewRequest(http.MethodGet, "/api/projects/"+projectID+"/pipeline-config", nil))
	rr3 := httptest.NewRecorder()
	r.ServeHTTP(rr3, req3)
	if rr3.Code != http.StatusOK {
		t.Fatalf("GET after PUT: expected 200, got %d: %s", rr3.Code, rr3.Body.String())
	}
	var getResp struct {
		PlanMode *bool `json:"planMode"`
	}
	if err := json.Unmarshal(rr3.Body.Bytes(), &getResp); err != nil {
		t.Fatalf("decode GET: %v", err)
	}
	if getResp.PlanMode == nil || !*getResp.PlanMode {
		t.Errorf("GET round-trip: project planMode=%v, want true", getResp.PlanMode)
	}
}
