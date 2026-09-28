package api

import (
	"context"
	obsidianapp "github.com/lx-wnk/kontor/server/internal/apps/obsidian"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"golang.org/x/time/rate"

	apianalytics "github.com/lx-wnk/kontor/server/internal/api/analytics"
	apicost "github.com/lx-wnk/kontor/server/internal/api/cost"
	apigithub "github.com/lx-wnk/kontor/server/internal/api/github"
	"github.com/lx-wnk/kontor/server/internal/api/grants"
	apihistory "github.com/lx-wnk/kontor/server/internal/api/history"
	apikontorsession "github.com/lx-wnk/kontor/server/internal/api/kontorsession"
	apimemory "github.com/lx-wnk/kontor/server/internal/api/memory"
	apiobsidian "github.com/lx-wnk/kontor/server/internal/api/obsidian"
	apiplugins "github.com/lx-wnk/kontor/server/internal/api/plugins"
	"github.com/lx-wnk/kontor/server/internal/api/presets"
	refineapi "github.com/lx-wnk/kontor/server/internal/api/refine"
	"github.com/lx-wnk/kontor/server/internal/api/remotes"
	"github.com/lx-wnk/kontor/server/internal/api/resources"
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
	"github.com/lx-wnk/kontor/server/internal/memory"
	"github.com/lx-wnk/kontor/server/internal/merger"
	"github.com/lx-wnk/kontor/server/internal/pipeline"
	"github.com/lx-wnk/kontor/server/internal/plugin"
	refinesvc "github.com/lx-wnk/kontor/server/internal/refine"
	"github.com/lx-wnk/kontor/server/internal/sse"
	"github.com/lx-wnk/kontor/server/internal/webpush"
)

// noopOrchestrator satisfies tasks.OrchestratorIface without driving the state machine.
type noopOrchestrator struct{}

func (noopOrchestrator) ProgressTask(_ context.Context, _ string, _ *pipeline.ProgressOpts) (*ent.StageRun, error) {
	return nil, nil
}
func (noopOrchestrator) ResumeFromUser(_ context.Context, _ string, _ string) (*ent.StageRun, error) {
	return nil, nil
}
func (noopOrchestrator) NotifyTaskTerminated(_ context.Context, _, _ string)      {}
func (noopOrchestrator) InvalidateConfigCache()                                   {}
func (noopOrchestrator) ClearStalePendingPermissions(_ context.Context, _ string) {}
func (noopOrchestrator) RequeueForUser(_ context.Context, _ string, _ string) (*ent.StageRun, error) {
	return nil, nil
}
func (noopOrchestrator) EffectiveStageModel(_ context.Context, _ string) string {
	return ""
}

// buildBypassRouter wires the full production router (every handler mounted) in
// bypass-auth mode against an in-memory SQLite database. It mirrors the DI in
// cmd/serve/di.go so the route set matches production exactly — the point of the
// smoke test below is that NO protected route hard-fails with 401/403 when the
// JWT middleware is skipped (DASHBOARD_AUTH=none).
func buildBypassRouter(t *testing.T) http.Handler {
	t.Helper()
	bundle, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("open in-memory db: %v", err)
	}
	t.Cleanup(func() { _ = bundle.Close() })
	c := bundle.Client
	rawDB := bundle.DB

	// Refine spawner is stubbed so hitting POST /api/refine/{id}/turn never execs a
	// real `claude` process — it returns an immediately-closed stream.
	stubSpawner := func(_ context.Context, _ refinesvc.SpawnConfig, _ *ent.Spawner) (<-chan string, error) {
		ch := make(chan string)
		close(ch)
		return ch, nil
	}

	deps := RouterDeps{
		Ctx: context.Background(),
		Config: RouterConfig{
			BypassAuth:        true,
			IsLoopback:        true,
			HooksSecret:       "test-hooks-secret-please-ignore-32b",
			SpawnRateLimit:    5,
			SpawnRateWindowMs: 60000,
			// Defang the per-IP limiter: the walk fires 100+ requests from one IP
			// in a tight loop, and a 429 would otherwise mask the 401/403 we test for.
			AuthRateLimiterConfig: IPRateLimiterConfig{Rate: rate.Limit(1_000_000), Burst: 1_000_000},
		},
		AgentBroadcaster:  sse.NewBroadcaster(),
		Merger:            merger.New(),
		UserRepo:          repo.NewUserRepo(c),
		ApiKeyRepo:        repo.NewApiKeyRepo(c),
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
		GrantsHandler:        grants.NewHandler(repo.NewGrantRepo(c), repo.NewCapabilityRepo(c)),
		ResourcesHandler: resources.NewHandler(repo.NewResourceRepo(c), repo.NewTaskScheduleRepo(c), memory.Gate{
			Capabilities: repo.NewCapabilityRepo(c),
			Grants:       repo.NewGrantRepo(c),
			GrantUsage:   repo.NewGrantUsageRepo(c, bundle.WriteClient),
		}, true),
		SearchHandler:  search.NewHandler(rawrepo.NewSearchRepo(rawDB), merger.New(), nil, true),
		HistoryHandler: apihistory.NewHandler(histsvc.NewImporter(repo.NewAgentCostTrendRepo(c))),
		MemoryHandler: apimemory.NewHandler(
			repo.NewMemoryRepo(c, bundle.WriteClient),
			memory.NewRetriever(rawDB, repo.NewMemoryRepo(c, bundle.WriteClient)),
			memory.Gate{
				Capabilities: repo.NewCapabilityRepo(c),
				Grants:       repo.NewGrantRepo(c),
				GrantUsage:   repo.NewGrantUsageRepo(c, bundle.WriteClient),
			},
		),
		// client is nil here — this router mirrors an unconfigured vault, the
		// common case in production too — so /api/obsidian/index answers 503
		// before ever reaching the gate below.
		ObsidianHandler: apiobsidian.NewHandler(
			obsidianapp.NewClientHolder(nil),
			repo.NewMemoryRepo(c, bundle.WriteClient),
			memory.Gate{
				Capabilities: repo.NewCapabilityRepo(c),
				Grants:       repo.NewGrantRepo(c),
				GrantUsage:   repo.NewGrantUsageRepo(c, bundle.WriteClient),
			},
			"obsidian",
		),
		// client is nil here — this router mirrors an unconfigured GitHub
		// application, the common case in production too — so every
		// /api/github/* route answers 503 before ever reaching the gate.
		GitHubHandler: apigithub.NewHandler(nil, memory.Gate{
			Capabilities: repo.NewCapabilityRepo(c),
			Grants:       repo.NewGrantRepo(c),
			GrantUsage:   repo.NewGrantUsageRepo(c, bundle.WriteClient),
		}),
		RefineHandler: refineapi.NewHandler(refineapi.Deps{
			Turns:     repo.NewRefinementTurnRepo(c),
			Tasks:     repo.NewTaskRepo(c),
			StageRuns: repo.NewStageRunRepo(c),
			Spawner:   stubSpawner,
		}),
		AnalyticsHandler:      apianalytics.NewHandler(rawrepo.NewAnalyticsRepo(rawDB), rawDB, repo.NewPipelineConfigRepo(c)),
		CostHandler:           apicost.NewHandler(rawDB),
		VisualizationsHandler: visualizations.NewHandler(),
		// Wire the plugin route surface so the route-golden and the bypass-auth
		// smoke both cover it. The registry is empty and the controller is a stub —
		// route registration is static, independent of runtime plugin state.
		PluginRegistry:         plugin.New(""),
		PluginLifecycleHandler: apiplugins.NewLifecycle(stubPluginController{}),
	}
	// Stub, never the real service: the route walk must not be able to spawn a
	// process.
	deps.KontorSessionHandler = apikontorsession.New(stubKontorSessions{})

	return NewRouter(deps)
}

type stubKontorSessions struct{}

func (stubKontorSessions) Current(context.Context) (int, bool, error)       { return 0, false, nil }
func (stubKontorSessions) Start(context.Context, string) (int, bool, error) { return 1, true, nil }
func (stubKontorSessions) Renew(context.Context, string) (int, error)       { return 1, nil }
func (stubKontorSessions) End(context.Context, string) error                { return nil }

// bypassSkip reports routes that legitimately do NOT pass through the session-auth
// group in bypass mode and therefore may return 401/403 by design.
func bypassSkip(method, pattern string) bool {
	switch {
	case strings.HasPrefix(pattern, "/api/auth/"): // public OAuth dance (may 302/500)
		return true
	case pattern == "/*": // Vue SPA catch-all, not an API route
		return true
	case pattern == "/api/hooks/event", pattern == "/api/hooks/pre-tool",
		pattern == "/api/hooks/permission", pattern == "/api/hooks/notification": // hook-script ingress, bearer-secret auth
		return true
	case pattern == "/api/mcp", pattern == "/api/channel-reply",
		pattern == "/api/channel-stage-output",
		pattern == "/api/permission-requests", pattern == "/api/permission-requests/bulk": // bearer-token auth, outside JWT group
		return true
	case strings.HasSuffix(pattern, "/replies"): // channel-reply read, bearer auth
		return true
	case pattern == "/api/agents/stream", pattern == "/api/tasks/stream",
		pattern == "/api/spawners/stream", pattern == "/api/projects/stream": // long-lived SSE
		return true
	case method == http.MethodDelete && pattern == "/api/me": // account deletion disabled in bypass mode (by design)
		return true
	}
	return false
}

// bypassMemoryRequestBody is the JSON body driveRequest sends for every
// /api/memory/* write route in this test, a superset of the fields any one
// of them requires (slug, spaceSlug, summary, content, kind, sourceKind,
// supersededBy). An empty "{}" body — every other route's default — would
// hit a route's own required-field validation (400) before the request ever
// reaches h.authorize, masking whichever capability check this test exists
// to guard.
const bypassMemoryRequestBody = `{"slug":"smoke-test","spaceSlug":"smoke-test","summary":"s","content":"c","kind":"fact","sourceKind":"user","supersededBy":"smoke-test"}`

// bypassMemoryExpectedStatus reports the status a /api/memory/* route must
// answer with here, with capabilities seeded but no grant at all: these
// routes are capability-gated (memory.read/memory.write), not JWT-gated, so
// 403 — not 401/403-as-bug — is the fail-closed default the gate is built to
// produce; skipping the whole prefix left a future route that forgets to
// call authorize invisible to this test, so every one of them is asserted
// against a concrete expected code instead.
//
// The two id-addressed routes (supersede, expire) still resolve a different,
// but equally deterministic, code: spaceOfEntry looks the entry up before
// either one reaches authorize, and "smoke-test" is never a real id, so both
// 404 regardless of grants — a real fixture entry would be needed to drive
// them as far as the capability check, which this smoke test does not carry.
func bypassMemoryExpectedStatus(pattern string) int {
	switch {
	case strings.HasSuffix(pattern, "/entries/{id}"):
		return http.StatusNotFound
	default:
		return http.StatusForbidden
	}
}

// concretePath substitutes chi path params ({id}, {taskId:...}) and wildcards with
// a dummy segment so the route is actually reachable.
func concretePath(pattern string) string {
	parts := strings.Split(pattern, "/")
	for i, p := range parts {
		if strings.HasPrefix(p, "{") || p == "*" {
			parts[i] = "smoke-test"
		}
	}
	return strings.Join(parts, "/")
}

// driveRequest runs one request against h with a hard deadline so streaming /
// blocking handlers can't hang the test. The status code is read only after the
// handler goroutine has returned, so there is no race on the recorder.
// jsonBody is ignored for GET/HEAD/OPTIONS; "{}" is the default body every
// route but the memory ones need — an empty object satisfies handlers that
// don't check for required fields and 400s harmlessly on the ones that do.
func driveRequest(h http.Handler, method, path, jsonBody string) int {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var body *strings.Reader
	if method == http.MethodGet || method == http.MethodHead || method == http.MethodOptions {
		body = strings.NewReader("")
	} else {
		body = strings.NewReader(jsonBody)
	}
	req := httptest.NewRequest(method, path, body).WithContext(ctx)
	// Loopback host + matching Origin so RequireLoopbackHost and
	// RequireSameOriginForMutations both pass — we are testing auth, not CSRF.
	req.Host = "127.0.0.1"
	if method != http.MethodGet && method != http.MethodHead && method != http.MethodOptions {
		req.Header.Set("Origin", "http://127.0.0.1")
		req.Header.Set("Content-Type", "application/json")
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

// TestBypassAuth_NoProtectedRouteReturnsAuthError is the regression guard for the
// DASHBOARD_AUTH=none bug class: in bypass mode the RequireAuth middleware is
// skipped, so no JWT payload is placed in the request context. Any handler that
// hard-gates on auth.PayloadFromContext (returning 401/403 when it is absent) is
// unreachable in the default config. This test walks EVERY route registered by
// NewRouter and asserts none of them answer an unauthenticated request with 401
// or 403 — except the explicit bypassSkip allow-list (bearer-auth ingress, the
// public OAuth dance, and account deletion which is disabled in bypass by design).
//
// Because it enumerates routes dynamically, any NEW handler added to NewRouter is
// covered automatically — that is the "never again" guarantee.
func TestBypassAuth_NoProtectedRouteReturnsAuthError(t *testing.T) {
	router := buildBypassRouter(t)
	routes, ok := router.(chi.Routes)
	if !ok {
		t.Fatalf("NewRouter did not return a chi.Routes; got %T", router)
	}

	var mu sync.Mutex
	checked := 0

	walkErr := chi.Walk(routes, func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		if bypassSkip(method, route) {
			return nil
		}
		path := concretePath(route)
		body := "{}"
		if strings.HasPrefix(route, "/api/memory/") {
			body = bypassMemoryRequestBody
			if route == "/api/memory/injections" { // stageRun is a required query param, not a body field
				path += "?stageRun=smoke-test"
			}
		}
		code := driveRequest(router, method, path, body)

		mu.Lock()
		checked++
		mu.Unlock()

		if strings.HasPrefix(route, "/api/memory/") {
			if want := bypassMemoryExpectedStatus(route); code != want {
				t.Errorf("bypass mode: %s %s returned %d, want %d — a capability-gated memory route must fail closed the same way every time; a 200-level response here means a handler forgot to call authorize", method, route, code, want)
			}
			return nil
		}

		if code == http.StatusUnauthorized || code == http.StatusForbidden {
			t.Errorf("bypass mode: %s %s returned %d — handler must not hard-gate on a JWT payload that bypass never sets (use auth.BypassPayload() on !ok, or drop the guard)", method, route, code)
		}
		return nil
	})
	if walkErr != nil {
		t.Fatalf("chi.Walk: %v", walkErr)
	}

	if checked == 0 {
		t.Fatal("walked zero routes — router wiring is wrong, the guard would be a false pass")
	}
	t.Logf("bypass-auth smoke: %d protected routes checked, none returned 401/403", checked)
}

// The permission bridge is not mounted under DASHBOARD_AUTH=none. Its premise is
// that a human weighs a tool call before it runs; with JWT off, the remaining
// gates are loopback and an Origin header any non-browser process sets for
// itself — and a hook allow short-circuits Claude Code's own evaluation,
// including the user's deny rules. The golden route file records the absence,
// but only as a list; this says why it is there.
func TestBypassAuth_DoesNotMountThePermissionBridgeDecisions(t *testing.T) {
	// Asserted against the route table, not a response code: an unregistered
	// POST falls through to the SPA catch-all, which answers 301, so driving a
	// request cannot tell "absent" from "mounted and redirecting".
	routes, ok := buildBypassRouter(t).(chi.Routes)
	if !ok {
		t.Fatal("NewRouter did not return a chi.Routes")
	}
	mounted := map[string]bool{}
	if err := chi.Walk(routes, func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		mounted[method+" "+route] = true
		return nil
	}); err != nil {
		t.Fatalf("chi.Walk: %v", err)
	}

	for _, route := range []string{"POST /api/hooks/permission/respond", "POST /api/hooks/permission/arm"} {
		if mounted[route] {
			t.Errorf("%s is mounted under bypass — any local process could answer a prompt", route)
		}
	}
	// The ingress stays: without arming nothing is held, so it answers "no
	// decision" and the session falls back to its terminal prompt, which the
	// dashboard still reports.
	if !mounted["POST /api/hooks/permission"] {
		t.Error("the PreToolUse ingress was removed too; the bridge must degrade, not disappear")
	}
}
