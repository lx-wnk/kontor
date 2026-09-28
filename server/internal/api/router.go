package api

import (
	"compress/gzip"
	"context"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	chimiddleware "github.com/go-chi/chi/v5/middleware"
	"github.com/lx-wnk/agent-dashboard/sdk"
	"github.com/lx-wnk/agent-dashboard/server/frontend"
	"github.com/lx-wnk/agent-dashboard/server/internal/agentbroadcast"
	"github.com/lx-wnk/agent-dashboard/server/internal/api/adapters"
	"github.com/lx-wnk/agent-dashboard/server/internal/api/admin"
	"github.com/lx-wnk/agent-dashboard/server/internal/api/agents"
	apianalytics "github.com/lx-wnk/agent-dashboard/server/internal/api/analytics"
	apikeyhandler "github.com/lx-wnk/agent-dashboard/server/internal/api/apikeys"
	apiapplications "github.com/lx-wnk/agent-dashboard/server/internal/api/applications"
	apiauth "github.com/lx-wnk/agent-dashboard/server/internal/api/auth"
	"github.com/lx-wnk/agent-dashboard/server/internal/api/capabilities"
	apiconfig "github.com/lx-wnk/agent-dashboard/server/internal/api/config"
	coordapi "github.com/lx-wnk/agent-dashboard/server/internal/api/coord"
	apicost "github.com/lx-wnk/agent-dashboard/server/internal/api/cost"
	apieval "github.com/lx-wnk/agent-dashboard/server/internal/api/eval"
	apigithub "github.com/lx-wnk/agent-dashboard/server/internal/api/github"
	"github.com/lx-wnk/agent-dashboard/server/internal/api/grants"
	apihistory "github.com/lx-wnk/agent-dashboard/server/internal/api/history"
	"github.com/lx-wnk/agent-dashboard/server/internal/api/hooks"
	apimemory "github.com/lx-wnk/agent-dashboard/server/internal/api/memory"
	apiobsidian "github.com/lx-wnk/agent-dashboard/server/internal/api/obsidian"
	"github.com/lx-wnk/agent-dashboard/server/internal/api/onboarding"
	planapi "github.com/lx-wnk/agent-dashboard/server/internal/api/plan"
	apiplugins "github.com/lx-wnk/agent-dashboard/server/internal/api/plugins"
	"github.com/lx-wnk/agent-dashboard/server/internal/api/presets"
	"github.com/lx-wnk/agent-dashboard/server/internal/api/projects"
	"github.com/lx-wnk/agent-dashboard/server/internal/api/prompttemplates"
	providersapi "github.com/lx-wnk/agent-dashboard/server/internal/api/providers"
	refineapi "github.com/lx-wnk/agent-dashboard/server/internal/api/refine"
	"github.com/lx-wnk/agent-dashboard/server/internal/api/remotes"
	"github.com/lx-wnk/agent-dashboard/server/internal/api/resources"
	"github.com/lx-wnk/agent-dashboard/server/internal/api/schedules"
	"github.com/lx-wnk/agent-dashboard/server/internal/api/search"
	"github.com/lx-wnk/agent-dashboard/server/internal/api/sessions"
	settingsapi "github.com/lx-wnk/agent-dashboard/server/internal/api/settings"
	apiskills "github.com/lx-wnk/agent-dashboard/server/internal/api/skills"
	"github.com/lx-wnk/agent-dashboard/server/internal/api/spawners"
	"github.com/lx-wnk/agent-dashboard/server/internal/api/system"
	"github.com/lx-wnk/agent-dashboard/server/internal/api/systemprompts"
	"github.com/lx-wnk/agent-dashboard/server/internal/api/tasks"
	trackerapi "github.com/lx-wnk/agent-dashboard/server/internal/api/tracker"
	"github.com/lx-wnk/agent-dashboard/server/internal/api/visualizations"
	apiwp "github.com/lx-wnk/agent-dashboard/server/internal/api/wphandler"
	authpkg "github.com/lx-wnk/agent-dashboard/server/internal/auth"
	"github.com/lx-wnk/agent-dashboard/server/internal/db/repo"
	"github.com/lx-wnk/agent-dashboard/server/internal/hookstore"
	mcp "github.com/lx-wnk/agent-dashboard/server/internal/mcp"
	"github.com/lx-wnk/agent-dashboard/server/internal/merger"
	"github.com/lx-wnk/agent-dashboard/server/internal/plugin"
	"github.com/lx-wnk/agent-dashboard/server/internal/serverask"
	"github.com/lx-wnk/agent-dashboard/server/internal/services"
	"github.com/lx-wnk/agent-dashboard/server/internal/sse"
)

// newAgentsAccessor builds the request-scoped GetAgents accessor expected by the
// read handlers (agents/sessions/config/search). The return type is an unnamed
// func so it stays assignable to each handler's own named accessor type
// (agents.GetAgentsFn, cmdscope.AgentsFn, …).
//
// These HTTP read paths do not compute the health-score cost baseline — they pass
// zero-value baseline opts, so the cost component is neutral (no penalty); the
// broadcast loop is the single place that injects a real baseline. The
// pipeline-task enricher, by contrast, IS applied here so request-scoped reads
// carry the same PipelineTaskID/Title annotation as the SSE stream. A nil
// enricher disables it.
func newAgentsAccessor(m *merger.Merger, enricher merger.Enricher) func(ctx context.Context) ([]sdk.Agent, error) {
	return func(ctx context.Context) ([]sdk.Agent, error) {
		return m.GetAgents(ctx, merger.GetAgentsOpts{Enricher: enricher})
	}
}

// RouterConfig holds configuration values for the router.
type RouterConfig struct {
	JWTSecret          string
	CallbackURL        string
	IsLoopback         bool            // true when Host is 127.0.0.1 / ::1 / localhost
	BypassAuth         bool            // skip JWT when DASHBOARD_AUTH=none
	Embedded           http.FileSystem // Vue SPA embed (unused until Task 14)
	HooksSecret        string
	HooksDebounceMs    int
	SpawnRateLimit     int
	SpawnRateWindowMs  int
	InjectRateLimit    int
	InjectRateWindowMs int
	// AuthPluginSecret is forwarded to the auth handler to protect POST /api/auth/session.
	AuthPluginSecret string
	// PluginLoginURL, when non-empty, causes GET /api/auth/login to redirect to the
	// auth plugin instead of handling the OAuth dance in core.
	PluginLoginURL string
	// LoopbackHostConfig configures the DNS-rebinding protection middleware.
	// The zero value applies the default loopback whitelist (127.0.0.1, localhost, ::1).
	LoopbackHostConfig RequireLoopbackHostConfig
	// AuthRateLimiterConfig configures the per-IP rate limiter applied to auth,
	// MCP, and bulk-resolve endpoints. The zero value uses safe defaults (10 r/s, burst 20).
	AuthRateLimiterConfig IPRateLimiterConfig
}

// RouterDeps holds all dependencies injected into the router.
type RouterDeps struct {
	// Ctx is the server-lifetime context. When cancelled (e.g. on shutdown) any
	// background goroutines started by the router (e.g. debounced rescan) are
	// also cancelled. If nil, context.Background() is used as a fallback.
	Ctx              context.Context
	Config           RouterConfig
	AgentBroadcaster *sse.Broadcaster
	// Merger is the shared roster builder (single instance per process, owns the
	// cross-tick stale tracker). Used by every request-scoped GetAgents accessor.
	Merger *merger.Merger
	// Enricher, when non-nil, annotates each scanned agent with its linked
	// pipeline task (read-only SQLite crossing). Applied to every request-scoped
	// GetAgents call below via the agentsAccessor closure. May be nil (no DB →
	// no enrichment). The broadcast loop receives the same enricher separately.
	Enricher merger.Enricher
	// HookStore records per-event hook granularity. The same instance is read by
	// the Enricher (via the agentbroadcast hook enricher) so events POSTed to
	// /api/hooks/event surface on the matching agent. May be nil (recording off).
	HookStore *hookstore.Store
	// HookEnforcer holds PreToolUse hook calls open for a dashboard decision.
	// Built in the DI container because the agent enricher reads the same
	// instance and is constructed before this router. May be nil, in which case
	// this router builds an unobserved one so the endpoints still answer — no
	// agent is annotated, because nothing else holds a reference to it.
	HookEnforcer *hooks.HookEnforcer
	// CapabilityDecisions supplies the server-point asks currently waiting for
	// a human. A rescan pushes a whole frame, so omitting it would clear the
	// list in every connected client on the next hook event. Nil when no asker
	// is wired (auth mode none).
	CapabilityDecisions agentbroadcast.CapabilityDecisionProvider
	// CapabilityAsker is notified of this router's rescan so a new ask reaches
	// clients without waiting for the next scan tick, and resolves a human's
	// decision on one. Declared as the two methods used rather than
	// *serverask.Asker: the router needs nothing else from it, and a nil
	// interface here simply means no asker was wired.
	CapabilityAsker interface {
		SetOnChange(func())
		Resolve(id, decision string) (serverask.Pending, error)
	}
	OAuthProvider     authpkg.OAuthProvider
	UserRepo          repo.UserRepo
	ApiKeyRepo        repo.ApiKeyRepo
	ProjectRepo       repo.ProjectRepo
	ProjectFolderRepo repo.ProjectFolderRepo
	SpawnerRepo       repo.SpawnerRepo
	// SpawnerBroadcaster fans out spawner CRUD events to SSE subscribers.
	// May be nil; Stream is only mounted in DI where a broadcaster is always provided.
	SpawnerBroadcaster *sse.SpawnerBroadcaster
	// ProjectBroadcaster fans out project CRUD events to SSE subscribers.
	// May be nil; Stream is only mounted in DI where a broadcaster is always provided.
	ProjectBroadcaster *sse.ProjectBroadcaster
	// TaskProjectOps lets the projects handler check for active tasks and
	// clear project_id on done/cancelled tasks during DELETE /api/projects/{id}.
	// May be nil; when nil the project handler skips the active-task check.
	TaskProjectOps         projects.TaskProjectOps
	CoordHandler           *coordapi.Handler
	TaskHandler            *tasks.Handler
	SchedulesHandler       *schedules.Handler
	WebPushHandler         *apiwp.Handler
	RemotesHandler         *remotes.Handler
	PresetsHandler         *presets.Handler
	SystemPromptsHandler   *systemprompts.Handler
	PromptTemplatesHandler *prompttemplates.Handler
	SearchHandler          *search.Handler
	HistoryHandler         *apihistory.Handler
	MemoryHandler          *apimemory.Handler
	ResourcesHandler       *resources.Handler
	ApplicationsHandler    *apiapplications.Handler
	SkillsHandler          *apiskills.Handler
	ObsidianHandler        *apiobsidian.Handler
	GitHubHandler          *apigithub.Handler
	RefineHandler          *refineapi.Handler
	PlanHandler            *planapi.Handler
	AnalyticsHandler       *apianalytics.Handler
	CostHandler            *apicost.Handler
	EvalHandler            *apieval.Handler
	VisualizationsHandler  *visualizations.Handler
	AdapterHandler         *adapters.Handler
	ProvidersHandler       *providersapi.Handler
	SettingsHandler        *settingsapi.Handler
	OnboardingHandler      *onboarding.Handler
	MCPHandler             http.Handler
	ChannelReply           *agents.ChannelReplyHandler
	ChannelStageOutput     *agents.ChannelStageOutputHandler
	PermissionPresetRepo   repo.PermissionPresetRepo
	GrantsHandler          *grants.Handler
	PluginRegistry         *plugin.Registry
	PluginLifecycleHandler *apiplugins.LifecycleHandler
	AuditEventRepo         repo.AuditEventRepo
	AdminHandler           *admin.Handler
	UsageHandler           http.Handler
	TrackerHandler         *trackerapi.Handler
}

// NewRouter builds the chi router with all middleware and route mounts.
func NewRouter(deps RouterDeps) http.Handler {
	serverCtx := deps.Ctx
	if serverCtx == nil {
		serverCtx = context.Background()
	}

	// Single request-scoped GetAgents accessor, shared by every read path below
	// (SSOT). Captures the injected pipeline-task enricher so HTTP reads and the
	// debounced rescan carry the same task annotation as the SSE broadcast loop.
	getAgents := newAgentsAccessor(deps.Merger, deps.Enricher)

	r := chi.NewRouter()

	// Build the per-IP rate limiter once; it owns its cleanup goroutine.
	// serverCtx cancels the goroutine on shutdown.
	authRateLimiter := NewIPRateLimiter(serverCtx, deps.Config.AuthRateLimiterConfig)

	// Global middleware (applied to every request, including hooks/MCP/channel-reply)
	// StripForwardedHeaders must be FIRST so no downstream middleware ever sees
	// attacker-controlled X-Forwarded-Host / X-Forwarded-Proto / Forwarded values.
	r.Use(StripForwardedHeaders)
	r.Use(chimiddleware.RequestID)
	r.Use(SlogMiddleware)
	r.Use(chimiddleware.Recoverer)
	r.Use(SecurityHeaders)
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			r.Body = http.MaxBytesReader(w, r.Body, 8*1024*1024) // 8 MB
			next.ServeHTTP(w, r)
		})
	})
	r.Use(gzipMiddleware)

	// Public routes (no auth)
	r.Get("/api/system/health", system.HealthHandler)

	// Hook-script ingress: bearer-secret auth, no JWT. These are posted by the
	// local Claude Code hook scripts (which carry DASHBOARD_HOOKS_SECRET), not by
	// the browser, so they are mounted outside the session-auth group.
	debounceMs := deps.Config.HooksDebounceMs
	if debounceMs <= 0 {
		debounceMs = 100
	}
	rescan := newDebouncedRescan(serverCtx, deps.AgentBroadcaster, debounceMs, getAgents, deps.CapabilityDecisions)
	hookEnforcer := deps.HookEnforcer
	if hookEnforcer == nil {
		hookEnforcer = hooks.NewHookEnforcer(nil)
	}
	// The rescan is this router's; installing it on the enforcer is therefore
	// this router's call. A held or resolved permission then reaches connected
	// clients without waiting for the next scan tick.
	hookEnforcer.SetOnChange(rescan)
	if deps.CapabilityAsker != nil {
		deps.CapabilityAsker.SetOnChange(rescan)
	}
	hooksHandler := hooks.New(deps.Config.HooksSecret, deps.HookStore, rescan, hookEnforcer)
	hooksHandler.SetSessionCWD(newSessionCWDLookup(getAgents))
	r.Post("/api/hooks/event", hooksHandler.Event)
	r.Post("/api/hooks/pre-tool", hooksHandler.PreTool)
	// Permission bridge ingress, secret-authenticated like the two above. The
	// request call is held open while a human decides, so it is deliberately a
	// slow endpoint; every failure path answers "no decision" and Claude Code
	// falls back to its own terminal prompt.
	r.Post("/api/hooks/permission", hooksHandler.PermissionRequest)
	r.Post("/api/hooks/notification", hooksHandler.PermissionNotify)
	// NOTE: /api/hooks/respond and /api/hooks/pending are browser-facing (the edit
	// gate UI reads pending edits and posts the user's decision). They carry the
	// session cookie, not the hooks secret, so they are registered inside the
	// protected JWT group below — NOT here.

	// Auth routes (public — OAuth dance must be unauthenticated)
	// F-SEC-010: per-IP rate limit prevents auth-probing and SHA-256 amplification DoS.
	authHandler := apiauth.NewHandler(apiauth.Deps{
		JWTSecret:        deps.Config.JWTSecret,
		CallbackURL:      deps.Config.CallbackURL,
		OAuthProvider:    deps.OAuthProvider,
		UserRepo:         deps.UserRepo,
		IsLoopback:       deps.Config.IsLoopback,
		BypassAuth:       deps.Config.BypassAuth,
		AuthPluginSecret: deps.Config.AuthPluginSecret,
		PluginLoginURL:   deps.Config.PluginLoginURL,
	})
	r.With(authRateLimiter).Get("/api/auth/login", ErrorMiddleware(authHandler.LoginRedirect))
	r.With(authRateLimiter).Get("/api/auth/github", ErrorMiddleware(authHandler.LoginRedirect)) // backwards-compat alias
	r.With(authRateLimiter).Get("/api/auth/callback", ErrorMiddleware(authHandler.Callback))
	r.With(authRateLimiter).Post("/api/auth/logout", ErrorMiddleware(authHandler.Logout))
	// Plugin session endpoint — called by external auth plugins after OAuth completes.
	// Only active when DASHBOARD_AUTH_PLUGIN_SECRET is set.
	r.With(authRateLimiter).Post("/api/auth/session", ErrorMiddleware(authHandler.CreateSession))

	// Protected routes (JWT required, unless auth bypass is active)
	r.Group(func(r chi.Router) {
		// F-SEC-005: reject requests whose Host header is not in the loopback
		// whitelist. Scoped to the browser-facing protected group; hooks, MCP,
		// and channel-reply are excluded because they use bearer-token auth and
		// may be called from non-browser clients on the same machine.
		r.Use(RequireLoopbackHost(deps.Config.LoopbackHostConfig))
		// RequireSameOriginForMutations guards against CSRF in both auth modes:
		// in bypass mode it is the primary CSRF defence; in auth mode it is
		// defence-in-depth on top of JWT validation.
		r.Use(RequireSameOriginForMutations)
		// F-SEC-010: per-IP rate limit on all protected endpoints — catches
		// bulk-resolve, permission-request creation, and any other high-cost
		// pipeline paths. The burst is sized from a measured cold start (see
		// IPRateLimiterConfig): the SPA's own boot peaks at 20 requests in a
		// second, and every local client shares one bucket because the server
		// is loopback-only, so a burst of 20 throttled the app itself.
		r.Use(authRateLimiter)
		if !deps.Config.BypassAuth {
			r.Use(authpkg.RequireAuth(deps.Config.JWTSecret))
		}
		agentHandler := agents.NewHandler(getAgents, deps.AgentBroadcaster)
		r.Get("/api/agents", ErrorMiddleware(agentHandler.List))
		r.Get("/api/agents/stream", agentHandler.Stream)
		r.Get("/api/agents/{sessionId}/output", sessions.Output)

		r.Get("/api/sessions", sessions.List)
		commandsHandler := sessions.NewCommandsHandler(deps.SpawnerRepo, getAgents)
		r.Get("/api/slash-commands", commandsHandler.SlashCommands)

		if deps.UsageHandler != nil {
			r.Get("/api/usage", deps.UsageHandler.ServeHTTP)
		}
		r.Get("/api/config", system.Config)        // frontend expects /api/config
		r.Get("/api/system/config", system.Config) // keep old path for compatibility
		r.Get("/api/system", system.System)        // frontend expects /api/system
		r.Get("/api/system/system", system.System) // keep old path for compatibility

		r.Get("/api/me", ErrorMiddleware(authHandler.Me))
		r.Delete("/api/me", ErrorMiddleware(authHandler.DeleteMe))

		if deps.ApiKeyRepo != nil {
			apiKeyHandler := apikeyhandler.NewHandler(deps.ApiKeyRepo)
			r.Get("/api/settings/api-keys", ErrorMiddleware(apiKeyHandler.List))
			r.Post("/api/settings/api-keys", ErrorMiddleware(apiKeyHandler.Create))
			r.Delete("/api/settings/api-keys/{id}", ErrorMiddleware(apiKeyHandler.Delete))
			r.Post("/api/settings/api-keys/{id}/regenerate", ErrorMiddleware(apiKeyHandler.Regenerate))
		}

		if deps.ProvidersHandler != nil {
			r.Get("/api/providers", ErrorMiddleware(deps.ProvidersHandler.List))
			r.Patch("/api/providers/{id}", ErrorMiddleware(deps.ProvidersHandler.Patch))
		}

		if deps.SettingsHandler != nil {
			deps.SettingsHandler.MountRead(r)
			deps.SettingsHandler.MountWrite(r)
		}

		if deps.OnboardingHandler != nil {
			deps.OnboardingHandler.Mount(r)
		}

		// Projects + ProjectFolders — JWT-protected. No whole-route admin gate
		// (non-admins legitimately edit name/color/folders); the RCE-equivalent
		// setup_command field is admin-gated per-field inside the handler.
		if deps.ProjectRepo != nil && deps.ProjectFolderRepo != nil {
			projectsHandler := projects.NewHandler(deps.ProjectRepo, deps.ProjectFolderRepo, deps.TaskProjectOps, deps.ProjectBroadcaster, deps.Config.BypassAuth)
			projectsHandler.Mount(r)
			r.Get("/api/projects/stream", projectsHandler.Stream)
		}

		// Spawners — JWT only. Spawner CRUD lets the caller define arbitrary
		// processes, so it is RCE-equivalent; the admin gate that used to guard it
		// was never grantable (nothing set is_admin), so it rejected every
		// authenticated user and passed everything through in bypass mode. The
		// protection now rests on authentication and the loopback bind.
		if deps.SpawnerRepo != nil {
			spawnersHandler := spawners.NewHandler(deps.SpawnerRepo, deps.SpawnerBroadcaster)
			spawnersHandler.Mount(r)
			// Read-only live stream — JWT-protected but not admin-gated.
			streamHandler := spawners.NewHandler(deps.SpawnerRepo, deps.SpawnerBroadcaster)
			r.Get("/api/spawners/stream", streamHandler.Stream)
		}

		if deps.WebPushHandler != nil {
			deps.WebPushHandler.Mount(r)
		}

		if deps.TaskHandler != nil {
			deps.TaskHandler.Mount(r)
		}

		if deps.CoordHandler != nil {
			deps.CoordHandler.Mount(r)
		}

		if deps.SchedulesHandler != nil {
			deps.SchedulesHandler.Mount(r)
		}

		if deps.RemotesHandler != nil {
			deps.RemotesHandler.Mount(r)
		}

		if deps.PresetsHandler != nil {
			deps.PresetsHandler.Mount(r)
		}

		// Grants change what agents are permitted to do, so this endpoint stays
		// session-authenticated like every other write path in this group — never
		// mounted in the hook/MCP bearer-token bypass group.
		if deps.GrantsHandler != nil {
			deps.GrantsHandler.Mount(r)
		}

		if deps.SystemPromptsHandler != nil {
			deps.SystemPromptsHandler.Mount(r)
		}

		if deps.PromptTemplatesHandler != nil {
			deps.PromptTemplatesHandler.Mount(r)
		}

		if deps.SearchHandler != nil {
			r.Get("/api/search", ErrorMiddleware(deps.SearchHandler.Search))
		}

		if deps.HistoryHandler != nil {
			deps.HistoryHandler.Mount(r)
		}

		if deps.MemoryHandler != nil {
			deps.MemoryHandler.Mount(r)
		}

		// Read-only registry catalogue; sits with the other session-authenticated
		// read routes (grants, capabilities) and is never mounted in the
		// hook/MCP bearer-token group.
		if deps.ResourcesHandler != nil {
			deps.ResourcesHandler.Mount(r)
		}

		if deps.ApplicationsHandler != nil {
			deps.ApplicationsHandler.Mount(r)
		}

		// Obsidian's manual index trigger stays session-authenticated like
		// every other write path in this group, same as grants above: it
		// runs a real write into the memory store, never mounted in the
		// hook/MCP bearer-token bypass group.
		// Skill materialization writes into the user's own config directories,
		// so it belongs in the session-authenticated group with grants and the
		// Obsidian index trigger — never in the hook/MCP bearer-token group.
		if deps.SkillsHandler != nil {
			deps.SkillsHandler.Mount(r)
		}

		if deps.ObsidianHandler != nil {
			deps.ObsidianHandler.Mount(r)
		}

		// GitHub's four routes stay session-authenticated like every other
		// write path in this group: two of them reach a third party in the
		// user's name, and one of them merges.
		if deps.GitHubHandler != nil {
			deps.GitHubHandler.Mount(r)
		}

		if deps.RefineHandler != nil {
			deps.RefineHandler.Mount(r)
		}

		if deps.PlanHandler != nil {
			deps.PlanHandler.Mount(r)
		}

		if deps.AnalyticsHandler != nil {
			deps.AnalyticsHandler.Mount(r)
		}

		if deps.VisualizationsHandler != nil {
			deps.VisualizationsHandler.Mount(r)
		}

		if deps.AdapterHandler != nil {
			deps.AdapterHandler.Mount(r)
		}

		// Cost analytics — aggregated spend by model, day, and week.
		if deps.CostHandler != nil {
			deps.CostHandler.Mount(r)
		}

		// Eval metrics and drift alerts.
		if deps.EvalHandler != nil {
			deps.EvalHandler.Mount(r)
		}

		// Tracker issue fetch + encrypted token settings.
		if deps.TrackerHandler != nil {
			deps.TrackerHandler.Mount(r)
		}

		// Spawn management — rate-limited user-initiated agent spawning and channel message forwarding.
		// Inside the protected group so only authenticated users can spawn agents.
		//
		// Build the cwd allow-list from registered project folder paths (F-SEC-001).
		// Sensitive home dirs (~/.ssh, ~/.aws, etc.) are always blocked regardless.
		var spawnPolicy services.SpawnPolicy
		if deps.ProjectRepo != nil && deps.ProjectFolderRepo != nil {
			spawnPolicy = services.NewSpawnPolicy(services.ProjectFolderRootsProvider(deps.ProjectRepo, deps.ProjectFolderRepo))
		} else {
			spawnPolicy = services.NewSpawnPolicy(nil)
		}
		spawnMgr := agents.NewSpawnManager(
			deps.Config.SpawnRateLimit, deps.Config.SpawnRateWindowMs,
			deps.Config.InjectRateLimit, deps.Config.InjectRateWindowMs,
			deps.SpawnerRepo, spawnPolicy,
		)
		spawnMgr.SetProjectFolderRepo(deps.ProjectFolderRepo)
		go spawnMgr.StartPruner(serverCtx)
		spawnHandler := agents.NewSpawnHandler(spawnMgr)
		if deps.AuditEventRepo != nil {
			spawnHandler.SetAuditRepo(deps.AuditEventRepo)
		}
		if deps.Merger != nil {
			spawnHandler.SetAgentDismisser(deps.Merger)
		}
		r.Post("/api/agents/spawn", spawnHandler.Spawn)
		r.Get("/api/agents/spawn/{pid}/status", spawnHandler.Status)
		r.Post("/api/agents/{pid}/message", spawnHandler.Message)
		r.Delete("/api/agents/{pid}/channel", spawnHandler.DismissChannel)
		uploadImageHandler := agents.NewUploadImageHandler()
		r.Post("/api/agents/{pid}/upload-image", uploadImageHandler.UploadImage)
		// WebSocket proxy — registered raw specifically because the upgrade
		// hijacks the connection: an ErrorMiddleware wrapper that buffers or
		// writes a response after the handler returns would break the hijacked
		// stream.
		terminalHandler := agents.NewTerminalHandler(getAgents, spawnMgr.TerminalTarget)
		r.Get("/api/agents/{pid}/terminal", terminalHandler.Terminal)
		answerQuestionHandler := agents.NewAnswerQuestionHandler(spawnMgr)
		r.Post("/api/agents/{pid}/answer-question", answerQuestionHandler.AnswerQuestion)
		if deps.PermissionPresetRepo != nil {
			allowToolHandler := agents.NewAllowToolHandler(getAgents, deps.PermissionPresetRepo)
			r.Post("/api/agents/{pid}/allow-tool", ErrorMiddleware(allowToolHandler.AllowTool))
		}

		// Config explorer — enumerate and edit skills, slash commands, and
		// context files (CLAUDE.md / AGENTS.md), scoped per spawner / live
		// session via ?spawnerId / ?sessionId.
		// The only client path accepted is ?cwd; it is sanitized and confined to
		// the spawn policy's project roots so the editable set stays bounded.
		configHandler := apiconfig.NewHandler(deps.SpawnerRepo, getAgents, spawnPolicy)
		r.Get("/api/config/skills", configHandler.Skills)
		r.Get("/api/config/commands", configHandler.Commands)
		r.Get("/api/config/context-files", configHandler.ContextFiles)
		// Deprecated: kept answering identically for one minor version so a
		// client built against the old path keeps working. Logs once per
		// process (see Handler.Memory, which also names what to rename here
		// when this route goes).
		// This is the one caller the deprecation marker is meant to allow:
		// keeping the alias registered is the whole point of the window.
		r.Get("/api/config/memory", configHandler.Memory) //nolint:staticcheck // SA1019: intentional alias
		// Single-file read/write for editable (user/project) config files. Writes
		// are authorized only against the scope's enumerated editable set.
		r.Get("/api/config/file", configHandler.File)
		r.Put("/api/config/file", configHandler.SaveFile)

		// Edit-gate UI endpoints — browser-facing, session-authenticated (or bypass).
		// Unlike /api/hooks/event and /api/hooks/pre-tool (hook-script ingress, secret
		// auth), these are called by EditGateModal.vue with the session cookie.
		r.Get("/api/hooks/pending", hooksHandler.Pending)
		r.Post("/api/hooks/respond", hooksHandler.Respond)
		// Not mounted under DASHBOARD_AUTH=none. The bridge's premise is that a
		// human weighs a tool call before it runs; with JWT off, the remaining
		// gates are loopback and an Origin header any non-browser process sets
		// for itself, so "a human decided" reduces to "any local process
		// decided" — and a hook allow short-circuits Claude Code's own
		// evaluation. Without arming nothing is ever held, so the bridge
		// degrades to the terminal prompt it already falls back to, and the
		// dashboard still reports that a session is waiting there.
		if !deps.Config.BypassAuth {
			r.Post("/api/hooks/permission/respond", hooksHandler.PermissionRespond)
			r.Post("/api/hooks/permission/arm", hooksHandler.PermissionArm)
		}
		// Nil under DASHBOARD_AUTH=none, same as CapabilityDecisions above: with
		// JWT off there is no human on the other end of this endpoint, only any
		// local process, so it would offer a decision nothing can meaningfully make.
		if deps.CapabilityAsker != nil {
			r.Post("/api/capabilities/decisions/respond", capabilities.New(deps.CapabilityAsker, deps.AuditEventRepo).Respond)
		}

		// SP1 lifecycle + settings endpoints under the clean /api/plugins namespace.
		// The read-only list is needed for slot discovery; write actions
		// (install/activate/deactivate/uninstall/settings) are operator actions and
		// are protected by authentication alone since the admin gate was removed.
		if deps.PluginLifecycleHandler != nil {
			deps.PluginLifecycleHandler.MountList(r)
			deps.PluginLifecycleHandler.Mount(r)
		}
		// Live route/ui-extension dispatch. One catch-all resolves the registry per
		// request: chi freezes routes after serve (chi #480), so enable/disable
		// cannot mutate the route tree. Mounted inside the authed group so it
		// inherits JWT + same-origin guards; the proxy strips Cookie/Authorization
		// before forwarding to the plugin.
		if deps.PluginRegistry != nil {
			r.Handle("/api/plugins/{id}/proxy/*", plugin.NewDispatcher(deps.PluginRegistry))
		}
		if deps.AdminHandler != nil {
			deps.AdminHandler.Mount(r)
		}
	})

	// Channel-reply endpoint — bearer token auth via discovery file (no JWT).
	// The channel bridge posts here; auth is validated against the per-PID discovery file.
	if deps.ChannelReply != nil {
		r.Post("/api/channel-reply", deps.ChannelReply.Post)
		r.Get("/api/agents/{sessionId}/replies", deps.ChannelReply.GetReplies)
	}

	// Channel-stage-output endpoint — bearer token auth via api_keys (MCP token),
	// no JWT/Origin/loopback middleware — server-to-server call from the bridge.
	if deps.ChannelStageOutput != nil {
		r.Post("/api/channel-stage-output", deps.ChannelStageOutput.Post)
	}

	// Agent-ingress permission-request creation — bearer token auth via api_keys
	// (MCP token), no JWT/Origin/loopback middleware: server-to-server call from
	// the channel bridge. Resolution endpoints stay in the protected group above.
	if deps.TaskHandler != nil && deps.ApiKeyRepo != nil {
		r.Group(func(r chi.Router) {
			r.Use(authRateLimiter)
			r.Use(mcp.McpAuthMiddleware(deps.ApiKeyRepo))
			deps.TaskHandler.MountAgentIngress(r)
		})
	}

	// MCP endpoint — Bearer token auth (API key), not JWT session auth.
	// F-SEC-010: per-IP rate limit prevents SHA-256 amplification DoS on the
	// API-key lookup path. Applied alongside the existing auth middleware.
	// Mounted outside the JWT group so OAuth-less clients can reach it.
	if deps.MCPHandler != nil {
		r.With(authRateLimiter, mcp.McpAuthMiddleware(deps.ApiKeyRepo)).Post(mcp.EndpointPath, deps.MCPHandler.ServeHTTP)
	}

	// Vue SPA catch-all — must be last (after all API routes)
	sub, err := fs.Sub(frontend.Embedded, "dist")
	if err != nil {
		panic("frontend embed sub: " + err.Error())
	}
	r.Handle("/*", NewSPAHandler(sub))

	return r
}

// gzipPool reuses gzip.Writer instances across requests to avoid the ~32 KiB
// per-response allocation that gzip.NewWriterLevel would otherwise incur.
var gzipPool = sync.Pool{
	New: func() any {
		gz, _ := gzip.NewWriterLevel(io.Discard, gzip.BestSpeed)
		return gz
	},
}

// gzipResponseWriter wraps http.ResponseWriter to write through a gzip.Writer.
type gzipResponseWriter struct {
	http.ResponseWriter
	Writer *gzip.Writer
}

var _ http.Flusher = (*gzipResponseWriter)(nil)

func (g *gzipResponseWriter) Write(b []byte) (int, error) {
	return g.Writer.Write(b)
}

func (g *gzipResponseWriter) Flush() {
	_ = g.Writer.Flush()
	if f, ok := g.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// gzipHijackWriter extends gzipResponseWriter to also implement http.Hijacker.
// Used when the underlying ResponseWriter supports connection hijacking (e.g. WebSocket upgrades).
type gzipHijackWriter struct {
	*gzipResponseWriter
	http.Hijacker
}

var _ http.Hijacker = (*gzipHijackWriter)(nil)
var _ http.Flusher = (*gzipHijackWriter)(nil)

// newGzipWriter wraps w with gzip compression. If w also implements http.Hijacker,
// the returned writer forwards Hijack calls to the underlying writer so chi middleware
// and WebSocket upgrade paths continue to work correctly.
func newGzipWriter(w http.ResponseWriter, gz *gzip.Writer) http.ResponseWriter {
	base := &gzipResponseWriter{ResponseWriter: w, Writer: gz}
	if h, ok := w.(http.Hijacker); ok {
		return &gzipHijackWriter{gzipResponseWriter: base, Hijacker: h}
	}
	return base
}

// gzipMiddleware compresses non-SSE responses when the client accepts gzip encoding.
func gzipMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Set Vary unconditionally so shared caches know this URL varies by
		// Accept-Encoding, even for responses served without compression (RFC 7234).
		// Use Add rather than Set to preserve any Vary values already set by handlers.
		w.Header().Add("Vary", "Accept-Encoding")

		if !strings.Contains(r.Header.Get("Accept-Encoding"), "gzip") {
			next.ServeHTTP(w, r)
			return
		}
		// Skip compression for SSE streams to avoid buffering issues.
		if r.Header.Get("Accept") == "text/event-stream" {
			next.ServeHTTP(w, r)
			return
		}
		gz := gzipPool.Get().(*gzip.Writer)
		gz.Reset(w)
		defer func() {
			_ = gz.Close()
			gzipPool.Put(gz)
		}()
		w.Header().Set("Content-Encoding", "gzip")
		w.Header().Del("Content-Length")
		next.ServeHTTP(newGzipWriter(w, gz), r)
	})
}

// newDebouncedRescan returns an OnEventFn that triggers an agent rescan after debounceMs.
// Multiple calls within the window collapse into one rescan.
// ctx should be the server-lifetime context so the rescan is cancelled on shutdown.
// newSessionCWDLookup answers "is this session live, and where does it run"
// from the same scan the roster is built from. The permission bridge vouches
// for a session at arming time with it: session_id otherwise arrives only in a
// POST body, so any local process holding the hook secret could raise a card
// under a trusted agent's name.
func newSessionCWDLookup(getAgents func(context.Context) ([]sdk.Agent, error)) hooks.SessionCWDFn {
	return func(ctx context.Context, sessionID string) (string, bool) {
		agents, err := getAgents(ctx)
		if err != nil {
			return "", false
		}
		for _, a := range agents {
			if a.SessionID == sessionID {
				return a.CWD, true
			}
		}
		return "", false
	}
}

func newDebouncedRescan(ctx context.Context, broadcaster *sse.Broadcaster, debounceMs int, getAgents func(context.Context) ([]sdk.Agent, error), decisions agentbroadcast.CapabilityDecisionProvider) hooks.OnEventFn {
	var mu sync.Mutex
	var timer *time.Timer
	delay := time.Duration(debounceMs) * time.Millisecond

	return func() {
		mu.Lock()
		defer mu.Unlock()
		if timer != nil {
			timer.Stop()
		}
		timer = time.AfterFunc(delay, func() {
			// Respect server shutdown — skip rescan if context is done.
			if ctx.Err() != nil {
				return
			}
			agents, err := getAgents(ctx)
			if err != nil {
				slog.Warn("hooks: debounced rescan failed", "err", err)
				return
			}
			var pending []sdk.PendingCapabilityDecision
			if decisions != nil {
				pending = decisions(ctx)
			}
			data, err := agentbroadcast.MarshalFrame(agents, pending)
			if err != nil {
				slog.Warn("hooks: marshal failed", "err", err)
				return
			}
			broadcaster.Broadcast(data)
		})
	}
}
