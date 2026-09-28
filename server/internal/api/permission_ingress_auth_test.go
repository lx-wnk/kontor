package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"golang.org/x/time/rate"

	"github.com/stretchr/testify/require"

	apianalytics "github.com/lx-wnk/kontor/server/internal/api/analytics"
	apicost "github.com/lx-wnk/kontor/server/internal/api/cost"
	apihistory "github.com/lx-wnk/kontor/server/internal/api/history"
	"github.com/lx-wnk/kontor/server/internal/api/presets"
	refineapi "github.com/lx-wnk/kontor/server/internal/api/refine"
	"github.com/lx-wnk/kontor/server/internal/api/remotes"
	"github.com/lx-wnk/kontor/server/internal/api/search"
	"github.com/lx-wnk/kontor/server/internal/api/systemprompts"
	"github.com/lx-wnk/kontor/server/internal/api/tasks"
	"github.com/lx-wnk/kontor/server/internal/api/visualizations"
	apiwp "github.com/lx-wnk/kontor/server/internal/api/wphandler"
	"github.com/lx-wnk/kontor/server/internal/db"
	"github.com/lx-wnk/kontor/server/internal/db/ent"
	"github.com/lx-wnk/kontor/server/internal/db/rawrepo"
	"github.com/lx-wnk/kontor/server/internal/db/repo"
	histsvc "github.com/lx-wnk/kontor/server/internal/history"
	mcp "github.com/lx-wnk/kontor/server/internal/mcp"
	"github.com/lx-wnk/kontor/server/internal/merger"
	refinesvc "github.com/lx-wnk/kontor/server/internal/refine"
	"github.com/lx-wnk/kontor/server/internal/sse"
	"github.com/lx-wnk/kontor/server/internal/webpush"
)

// buildIngressRouter mirrors buildBypassRouter but also seeds one API key so
// McpAuthMiddleware can authenticate Bearer tokens in the ingress tests.
func buildIngressRouter(t *testing.T, rawToken string) http.Handler {
	t.Helper()
	bundle, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("open in-memory db: %v", err)
	}
	t.Cleanup(func() { _ = bundle.Close() })
	c := bundle.Client
	rawDB := bundle.DB

	keyRepo := repo.NewApiKeyRepo(c)
	hash := mcp.HashToken(rawToken)
	if _, err := keyRepo.Create(context.Background(), repo.CreateApiKeyInput{Name: "test-ingress-key", Hash: hash, Scopes: []string{"pipeline:control"}}); err != nil {
		t.Fatalf("seed api key: %v", err)
	}

	stubSpawner := func(_ context.Context, _ refinesvc.SpawnConfig, _ *ent.Spawner) (<-chan string, error) {
		ch := make(chan string)
		close(ch)
		return ch, nil
	}

	deps := RouterDeps{
		Ctx: context.Background(),
		Config: RouterConfig{
			BypassAuth:            true,
			IsLoopback:            true,
			HooksSecret:           "test-hooks-secret-please-ignore-32b",
			SpawnRateLimit:        5,
			SpawnRateWindowMs:     60000,
			AuthRateLimiterConfig: IPRateLimiterConfig{Rate: rate.Limit(1_000_000), Burst: 1_000_000},
		},
		AgentBroadcaster:  sse.NewBroadcaster(),
		Merger:            merger.New(),
		UserRepo:          repo.NewUserRepo(c),
		ApiKeyRepo:        keyRepo,
		ProjectRepo:       repo.NewProjectRepo(c),
		ProjectFolderRepo: repo.NewProjectFolderRepo(c),
		SpawnerRepo:       repo.NewSpawnerRepo(c),
		TaskHandler: tasks.NewHandler(tasks.Deps{
			Client:            c,
			TaskRepo:          repo.NewTaskRepo(c),
			SRRepo:            repo.NewStageRunRepo(c),
			PermRepo:          repo.NewPermissionRepo(c),
			AuditRepo:         repo.NewAuditEventRepo(c),
			CfgRepo:           repo.NewPipelineConfigRepo(c),
			DepRepo:           repo.NewDependencyRepo(c),
			ProjectRepo:       repo.NewProjectRepo(c),
			ProjectFolderRepo: repo.NewProjectFolderRepo(c),
			SpawnerRepo:       repo.NewSpawnerRepo(c),
			Orchestrator:      noopOrchestrator{},
			Broadcaster:       sse.NewTaskBroadcaster(sse.NewBroadcaster()),
		}),
		WebPushHandler: apiwp.NewHandler(webpush.NewService(
			rawrepo.NewNotificationConfigRepo(rawDB),
			rawrepo.NewPushSubscriptionRepo(rawDB),
		)),
		RemotesHandler:       remotes.NewHandler(repo.NewRemoteRegistrationRepo(c)),
		PresetsHandler:       presets.NewHandler(repo.NewPermissionPresetRepo(c)),
		SystemPromptsHandler: systemprompts.NewHandler(repo.NewSystemPromptRepo(c)),
		SearchHandler:        search.NewHandler(rawrepo.NewSearchRepo(rawDB), merger.New(), nil, false),
		HistoryHandler:       apihistory.NewHandler(histsvc.NewImporter(repo.NewAgentCostTrendRepo(c))),
		RefineHandler: refineapi.NewHandler(refineapi.Deps{
			Turns:     repo.NewRefinementTurnRepo(c),
			Tasks:     repo.NewTaskRepo(c),
			StageRuns: repo.NewStageRunRepo(c),
			Spawner:   stubSpawner,
		}),
		AnalyticsHandler:      apianalytics.NewHandler(rawrepo.NewAnalyticsRepo(rawDB), rawDB, repo.NewPipelineConfigRepo(c)),
		CostHandler:           apicost.NewHandler(rawDB),
		VisualizationsHandler: visualizations.NewHandler(),
	}

	return NewRouter(deps)
}

// driveIngressRequest runs a POST against h with the given Authorization header
// and no Origin (simulating a server-to-server channel bridge call).
func driveIngressRequest(h http.Handler, path, authHeader, body string) int {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body)).WithContext(ctx)
	req.Host = "127.0.0.1"
	req.Header.Set("Content-Type", "application/json")
	// Intentionally NO Origin header — simulates channel bridge server-to-server call.
	if authHeader != "" {
		req.Header.Set("Authorization", authHeader)
	}

	rec := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		defer close(done)
		h.ServeHTTP(rec, req)
	}()

	select {
	case <-done:
	case <-time.After(3 * time.Second):
		cancel()
		<-done
	}
	return rec.Code
}

// TestPermissionIngress_NoOriginValidBearer_Succeeds asserts that the bulk
// permission-request creation endpoint accepts a valid Bearer token even when
// no Origin header is present (server-to-server channel bridge pattern).
// A business-level 4xx (e.g. 404 for unknown stage_run) is acceptable; only
// 401/403 would indicate the auth/CSRF middleware is still blocking the route.
func TestPermissionIngress_NoOriginValidBearer_Succeeds(t *testing.T) {
	const rawToken = "mcp_test_ingress_token_valid_1234"
	router := buildIngressRouter(t, rawToken)

	body := `{"stageRunId":"sr-x","entries":[{"tool":"Bash","pattern":"pnpm test*"}]}`
	code := driveIngressRequest(router, "/api/permission-requests/bulk", "Bearer "+rawToken, body)

	if code == http.StatusForbidden || code == http.StatusUnauthorized {
		t.Errorf("expected auth/CSRF to pass for valid Bearer + no Origin, got %d", code)
	}
	require.NotEqual(t, http.StatusInternalServerError, code, "must not 500 (unexpected handler error)")
}

// TestPermissionIngress_NoBearer_401 asserts that omitting the Authorization
// header returns 401 on the ingress endpoint.
func TestPermissionIngress_NoBearer_401(t *testing.T) {
	const rawToken = "mcp_test_ingress_token_valid_5678"
	router := buildIngressRouter(t, rawToken)

	body := `{"stageRunId":"sr-x","entries":[{"tool":"Bash","pattern":"pnpm test*"}]}`
	code := driveIngressRequest(router, "/api/permission-requests/bulk", "", body)

	if code != http.StatusUnauthorized {
		t.Errorf("expected 401 for missing Bearer, got %d", code)
	}
}

// TestPermissionIngress_ResolutionStaysProtected asserts that the bulk-resolve
// endpoint (browser-driven) is NOT accessible via Bearer + no Origin and returns
// 403, proving resolution routes stayed in the same-origin/JWT group.
func TestPermissionIngress_ResolutionStaysProtected(t *testing.T) {
	const rawToken = "mcp_test_ingress_token_valid_9012"
	router := buildIngressRouter(t, rawToken)

	body := `{"taskId":"t-x","outcome":"denied","all":true}`
	code := driveIngressRequest(router, "/api/permission-requests/bulk-resolve", "Bearer "+rawToken, body)

	if code != http.StatusForbidden {
		t.Errorf("expected 403 for bulk-resolve with no Origin header, got %d", code)
	}
}
