// Dependency wiring — auto-constructed, not generated.
// Domain-scoped provider functions live in sibling files:
//   di_db.go       — database bundle
//   di_router.go   — router config + HTTP server
//   di_pipeline.go — orchestrator, spawners
//   di_tasks.go    — task HTTP handler
//   di_mcp.go      — MCP HTTP handler
//
// This file is the thin coordinator that assembles all domains into the final Server.

package serverapp

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"time"

	sdk "github.com/lx-wnk/kontor/sdk"
	"github.com/lx-wnk/kontor/server/internal/agentbroadcast"
	"github.com/lx-wnk/kontor/server/internal/api"
	"github.com/lx-wnk/kontor/server/internal/api/adapters"
	"github.com/lx-wnk/kontor/server/internal/api/admin"
	"github.com/lx-wnk/kontor/server/internal/api/agents"
	apianalytics "github.com/lx-wnk/kontor/server/internal/api/analytics"
	apiapplications "github.com/lx-wnk/kontor/server/internal/api/applications"
	coordapi "github.com/lx-wnk/kontor/server/internal/api/coord"
	apicost "github.com/lx-wnk/kontor/server/internal/api/cost"
	apieval "github.com/lx-wnk/kontor/server/internal/api/eval"
	apigithub "github.com/lx-wnk/kontor/server/internal/api/github"
	"github.com/lx-wnk/kontor/server/internal/api/grants"
	apihistory "github.com/lx-wnk/kontor/server/internal/api/history"
	"github.com/lx-wnk/kontor/server/internal/api/hooks"
	kontorsessionapi "github.com/lx-wnk/kontor/server/internal/api/kontorsession"
	apimemory "github.com/lx-wnk/kontor/server/internal/api/memory"
	apiobsidian "github.com/lx-wnk/kontor/server/internal/api/obsidian"
	"github.com/lx-wnk/kontor/server/internal/api/onboarding"
	planapi "github.com/lx-wnk/kontor/server/internal/api/plan"
	apiplugins "github.com/lx-wnk/kontor/server/internal/api/plugins"
	"github.com/lx-wnk/kontor/server/internal/api/presets"
	"github.com/lx-wnk/kontor/server/internal/api/prompttemplates"
	providersapi "github.com/lx-wnk/kontor/server/internal/api/providers"
	refineapi "github.com/lx-wnk/kontor/server/internal/api/refine"
	"github.com/lx-wnk/kontor/server/internal/api/remotes"
	apiresources "github.com/lx-wnk/kontor/server/internal/api/resources"
	"github.com/lx-wnk/kontor/server/internal/api/search"
	settingsapi "github.com/lx-wnk/kontor/server/internal/api/settings"
	apiskills "github.com/lx-wnk/kontor/server/internal/api/skills"
	"github.com/lx-wnk/kontor/server/internal/api/systemprompts"
	"github.com/lx-wnk/kontor/server/internal/api/tasks"
	trackerapi "github.com/lx-wnk/kontor/server/internal/api/tracker"
	apiusage "github.com/lx-wnk/kontor/server/internal/api/usage"
	apivisualizations "github.com/lx-wnk/kontor/server/internal/api/visualizations"
	apiwp "github.com/lx-wnk/kontor/server/internal/api/wphandler"
	"github.com/lx-wnk/kontor/server/internal/apps/github"
	"github.com/lx-wnk/kontor/server/internal/apps/obsidian"
	"github.com/lx-wnk/kontor/server/internal/appsetup"
	"github.com/lx-wnk/kontor/server/internal/askgate"
	authpkg "github.com/lx-wnk/kontor/server/internal/auth"
	"github.com/lx-wnk/kontor/server/internal/capability"
	"github.com/lx-wnk/kontor/server/internal/channelconfig"
	"github.com/lx-wnk/kontor/server/internal/checkpoint"
	"github.com/lx-wnk/kontor/server/internal/claudeconfig"
	"github.com/lx-wnk/kontor/server/internal/claudesettings"
	"github.com/lx-wnk/kontor/server/internal/config"
	"github.com/lx-wnk/kontor/server/internal/db"
	"github.com/lx-wnk/kontor/server/internal/db/ent"
	"github.com/lx-wnk/kontor/server/internal/db/rawrepo"
	"github.com/lx-wnk/kontor/server/internal/db/repo"
	"github.com/lx-wnk/kontor/server/internal/eval"
	histsvc "github.com/lx-wnk/kontor/server/internal/history"
	"github.com/lx-wnk/kontor/server/internal/hookstore"
	"github.com/lx-wnk/kontor/server/internal/kontorsession"
	"github.com/lx-wnk/kontor/server/internal/materializer"
	mcppkg "github.com/lx-wnk/kontor/server/internal/mcp"
	"github.com/lx-wnk/kontor/server/internal/mcpapps"
	"github.com/lx-wnk/kontor/server/internal/memory"
	"github.com/lx-wnk/kontor/server/internal/merger"
	"github.com/lx-wnk/kontor/server/internal/parser"
	"github.com/lx-wnk/kontor/server/internal/permissions"
	"github.com/lx-wnk/kontor/server/internal/pipeline"
	"github.com/lx-wnk/kontor/server/internal/plugin"
	"github.com/lx-wnk/kontor/server/internal/pluginmgmt"
	"github.com/lx-wnk/kontor/server/internal/provider"
	"github.com/lx-wnk/kontor/server/internal/providersettings"
	"github.com/lx-wnk/kontor/server/internal/refine"
	"github.com/lx-wnk/kontor/server/internal/restart"
	"github.com/lx-wnk/kontor/server/internal/scanner"
	"github.com/lx-wnk/kontor/server/internal/scheduler"
	"github.com/lx-wnk/kontor/server/internal/secretbox"
	"github.com/lx-wnk/kontor/server/internal/serverask"
	"github.com/lx-wnk/kontor/server/internal/services"
	"github.com/lx-wnk/kontor/server/internal/settings"
	"github.com/lx-wnk/kontor/server/internal/sse"
	wpservice "github.com/lx-wnk/kontor/server/internal/webpush"
)

// settingsRepoAdapter maps settings.Repo onto the ent AppSettingRepo.
type settingsRepoAdapter struct{ inner repo.AppSettingRepo }

func (a settingsRepoAdapter) Get(ctx context.Context, k string) (string, bool, error) {
	return a.inner.Get(ctx, k)
}

func (a settingsRepoAdapter) Set(ctx context.Context, k, v string) error {
	_, err := a.inner.Upsert(ctx, k, v)
	return err
}

func (a settingsRepoAdapter) ListAll(ctx context.Context) (map[string]string, error) {
	rows, err := a.inner.List(ctx)
	if err != nil {
		return nil, err
	}
	m := make(map[string]string, len(rows))
	for _, r := range rows {
		m[r.Key] = r.Value
	}
	return m, nil
}

func (a settingsRepoAdapter) SetSecret(ctx context.Context, k, ciphertext, nonce string) error {
	_, err := a.inner.UpsertSecret(ctx, k, ciphertext, nonce)
	return err
}

func (a settingsRepoAdapter) GetSecret(ctx context.Context, k string) (string, string, bool, error) {
	return a.inner.GetSecret(ctx, k)
}

// noopSettingsRepo backs the settings service when no database is configured,
// so accessors always resolve to registry defaults and consumers never nil-check.
type noopSettingsRepo struct{}

func (noopSettingsRepo) Get(context.Context, string) (string, bool, error) { return "", false, nil }
func (noopSettingsRepo) Set(context.Context, string, string) error {
	return fmt.Errorf("settings: no database configured")
}
func (noopSettingsRepo) SetSecret(context.Context, string, string, string) error {
	return fmt.Errorf("settings: no database configured")
}
func (noopSettingsRepo) GetSecret(context.Context, string) (string, string, bool, error) {
	return "", "", false, nil
}
func (noopSettingsRepo) ListAll(context.Context) (map[string]string, error) {
	return map[string]string{}, nil
}

// ServerComponents bundles everything initializeServer wires up. Cleanup may be
// set even when an error is also returned (e.g. plugin registry already
// loaded) — callers must invoke it whenever it is non-nil, regardless of err.
type ServerComponents struct {
	API          *api.Server
	Broadcaster  *sse.Broadcaster
	Merger       *merger.Merger
	Orchestrator *pipeline.PipelineOrchestrator
	Scheduler    *scheduler.Scheduler
	HistImporter *histsvc.Importer
	// ApiKeyRepo feeds mcp.SweepExpiredKeys in runComponents; nil when no
	// database is configured, same as HistImporter and Eval above.
	ApiKeyRepo repo.ApiKeyRepo
	// SetupManager feeds its own sweep loop in runComponents; the idle and
	// lifetime limits are what end a setup page nobody closed.
	SetupManager        *appsetup.Manager
	Baseline            agentbroadcast.BaselineProvider
	Enricher            merger.Enricher
	CapabilityDecisions agentbroadcast.CapabilityDecisionProvider
	Eval                *eval.Service
	Settings            *settings.Service
	// Obsidian holds nil while the vault is unconfigured or no database is
	// present; see buildObsidianClient (di_obsidian.go).
	Obsidian *obsidian.ClientHolder
	// GitHub is nil when unconfigured or no database is present; see
	// buildGitHubClient (di_github.go).
	GitHub  *github.Client
	Cleanup func()
}

// ln is the address already bound by Listen, or nil to let the HTTP server
// bind cfg.Addr() itself when it starts.
func initializeServer(ctx context.Context, cfg config.Config, cfgFile string, restartCtl *restart.Controller, ln net.Listener) (*ServerComponents, error) {
	bundle, err := provideDB(cfg)
	if err != nil {
		return nil, err
	}
	var entClient *ent.Client
	if bundle != nil {
		entClient = bundle.Client
	}

	var webPushHandler *apiwp.Handler
	var wpSvc *wpservice.Service
	if bundle != nil {
		notifCfgRepo := rawrepo.NewNotificationConfigRepo(bundle.DB)
		subRepo := rawrepo.NewPushSubscriptionRepo(bundle.DB)
		wpSvc = wpservice.NewService(notifCfgRepo, subRepo)
		webPushHandler = apiwp.NewHandler(wpSvc)
	}

	// broadcaster (agent SSE) and taskBroadcaster (task SSE) are constructed here
	// and injected into handlers. Neither the pipeline nor any sub-package holds a
	// reference to these broadcasters — all notifications flow outward via callbacks
	// registered in OrchestratorOptions (e.g. OnTaskChanged). This keeps the
	// pipeline layer free of any SSE dependency and independently testable.
	// broadcaster and taskBroadcaster are independent — never share them.
	// broadcaster pushes Agent[] snapshots to /api/agents/stream subscribers each scan cycle.
	// taskBase / taskBroadcaster handle typed TaskEvent messages on /api/tasks/stream.
	// Both use sse.Broadcaster under the hood (non-blocking fan-out, drops frames for slow consumers).
	broadcaster := sse.NewBroadcaster()

	// Single shared Merger for the whole process — owns the cross-tick stale
	// tracker, so finished-agent state must persist across every read path
	// (broadcast loop, router accessors, search). Constructing more than one
	// would split that state and lose finished cards between ticks.
	ollama := provider.NewOllamaClassifier("http://localhost:11434")
	providerRegistry, err := provider.NewRegistry(provider.Options{
		UserDir: cfg.ProviderDir,
		Ollama:  ollama,
		Pricing: merger.PricingAdapter(),
	})
	if err != nil {
		return nil, fmt.Errorf("provider registry: %w", err)
	}
	var providerSettingRepo repo.ProviderSettingRepo
	if entClient != nil {
		providerSettingRepo = repo.NewProviderSettingRepo(entClient)
	}
	providerSettingsSvc := providersettings.New(
		providerSettingRepo,
		provider.DefaultEnabled(providerRegistry.Descriptors(), nil),
	)
	if providerSettingRepo != nil {
		if err := providerSettingsSvc.Load(ctx); err != nil {
			return nil, fmt.Errorf("provider settings load: %w", err)
		}
	}
	providerRegistry.SetEnabled(providerSettingsSvc.EnabledFunc())

	var providersHandler *providersapi.Handler
	if providerSettingRepo != nil {
		providersHandler = providersapi.NewHandler(providerRegistry, providerSettingsSvc)
	}

	// Resolve the secretbox master key once, above every secret-aware
	// consumer (settings.Service here, plugin.Service below), so both share
	// one *secretbox.Box built from one key rather than two boxes that could
	// in principle diverge. Only meaningful with a database — without one
	// there is nothing to encrypt into, so box stays nil and each service's
	// own nil-box guard turns a secret read/write into a named error instead
	// of a panic.
	var box *secretbox.Box
	if entClient != nil {
		masterKey, keyErr := secretbox.LoadOrGenerateMasterKey(os.Getenv("DASHBOARD_SECRET_KEY"))
		if keyErr != nil {
			return nil, fmt.Errorf("secret master key: %w", keyErr)
		}
		var boxErr error
		box, boxErr = secretbox.New(masterKey)
		if boxErr != nil {
			return nil, fmt.Errorf("secretbox: %w", boxErr)
		}
	}

	var settingsSvc *settings.Service
	var settingsHandler *settingsapi.Handler
	if entClient != nil {
		appSettingRepo := repo.NewAppSettingRepo(entClient)
		settingsSvc = settings.New(settingsRepoAdapter{inner: appSettingRepo}, box)
		if err := settingsSvc.Load(ctx); err != nil {
			return nil, fmt.Errorf("settings load: %w", err)
		}
		settingsHandler = settingsapi.NewHandler(settingsSvc)
	} else {
		settingsSvc = settings.New(noopSettingsRepo{}, nil)
		if err := settingsSvc.Load(ctx); err != nil {
			return nil, fmt.Errorf("settings load: %w", err)
		}
	}

	// Seed the spawner command allow-list from settings (ApplyRestart).
	services.SetSpawnerAllowedCommands(settingsSvc.StringSlice("spawn.allowedCommands"))

	agentMerger := merger.New(
		merger.WithRegistry(providerRegistry),
		merger.WithScanFn(func(ctx context.Context) ([]scanner.ProcessInfo, error) {
			return scanner.ScanProcessesWithDetector(ctx, providerRegistry)
		}),
		merger.WithScreenProbe(merger.RealScreenProbe),
		merger.WithNotePathFn(func(vaultPath string) (string, bool) {
			return obsidian.RootRelative(settingsSvc.String("obsidian.vaultRoot"), vaultPath)
		}),
	)

	taskBase := sse.NewBroadcaster()
	taskBroadcaster := sse.NewTaskBroadcaster(taskBase)
	spawnerBroadcaster := sse.NewSpawnerBroadcaster(sse.NewBroadcaster())
	projectBroadcaster := sse.NewProjectBroadcaster(sse.NewBroadcaster())

	// memRepo and memRetriever back both the memory_search/memory_write MCP
	// tools (di_mcp.go) and the pipeline's push-at-spawn seam
	// (di_pipeline.go): one Retriever, constructed once, given to both
	// consumers — the same rule Retriever's own doc comment states.
	var memRepo repo.MemoryRepo
	var memRetriever *memory.Retriever
	// grantUsageRepo backs the rate-limit check inside memory.Authorize
	// (mem HTTP handler, MCP memory tools, and the pipeline's memory push
	// closure below all share it) — built once here since it needs
	// bundle.WriteClient, which only this function has in scope.
	var grantUsageRepo repo.GrantUsageRepo

	// The plugin table is the source of truth for enablement. Build the repo
	// early so the boot predicate can read it, and migrate the legacy #230
	// "plugins.enabled" setting into the table once (idempotent).
	var pluginRepo repo.PluginRepo
	// Hoisted out of the block below so the resources HTTP handler, built near
	// the other handlers, can share this one instance.
	var resourceRepo repo.ResourceRepo
	// setupManager is hoisted so runComponents can drive its sweep loop; it is
	// nil when no database is configured, like every other repo-backed piece.
	var setupManager *appsetup.Manager
	// obsidianClients holds nil while the vault is unconfigured (buildObsidianClient's
	// own doc comment covers why that is not an error) and stays nil without
	// a database, since Register and the capability catalogue it depends on
	// need entClient too. obsidianSpaceID is set unconditionally alongside
	// obsidian.Register below, so a vault configured later has its space ready.
	obsidianClients := obsidian.NewClientHolder(nil)
	var obsidianSpaceID string
	// githubClient is nil when GitHub is unconfigured (buildGitHubClient's own
	// doc comment covers why that is not an error) and stays nil without a
	// database, since Register and the capability catalogue it depends on need
	// entClient too.
	var githubClient *github.Client
	if entClient != nil {
		memRepo = repo.NewMemoryRepo(entClient, bundle.WriteClient)
		memRetriever = memory.NewRetriever(bundle.DB, memRepo)
		grantUsageRepo = repo.NewGrantUsageRepo(entClient, bundle.WriteClient)

		pluginRepo = repo.NewPluginRepo(entClient)
		if err := seedPluginsFromEnabledList(ctx, settingsSvc, pluginRepo); err != nil {
			return nil, fmt.Errorf("seed plugins from enabled list: %w", err)
		}

		resourceRepo = repo.NewResourceRepo(entClient)
		if linked, err := repo.ReconcilePluginResources(ctx, resourceRepo, entClient); err != nil {
			slog.Warn("registry: plugin reconcile failed", "err", err)
		} else if linked > 0 {
			slog.Info("registry: linked plugins to registry identities", "count", linked)
		}
		if linked, err := repo.ReconcileScheduleResources(ctx, resourceRepo, entClient); err != nil {
			slog.Warn("registry: schedule reconcile failed", "err", err)
		} else if linked > 0 {
			slog.Info("registry: linked schedules to registry identities", "count", linked)
		}

		mcpAppRepo := repo.NewMCPApplicationRepo(entClient)
		if servers, err := claudeconfig.UserMCPServers(); err != nil {
			// The first-run marker must not be recorded from an unreadable file,
			// or every server that exists today would be demoted to opt-in.
			slog.Warn("mcpapps: ~/.claude.json unreadable — applications not reconciled", "err", err)
		} else if n, err := mcpapps.Reconcile(ctx, servers, resourceRepo, mcpAppRepo, db.MarkerStore{DB: bundle.DB}); err != nil {
			slog.Warn("mcpapps: reconcile failed", "err", err)
		} else {
			slog.Info("mcpapps: applications reconciled", "mirrored", n)
			if n, err := mcpapps.ImportEntries(ctx, servers, mcpAppRepo, db.MarkerStore{DB: bundle.DB}); err != nil {
				slog.Warn("mcpapps: entry import failed", "err", err)
			} else {
				slog.Info("mcpapps: entries imported", "count", n)
			}
		}

		if n, err := channelconfig.SweepOrphanedConfigs(time.Now()); err != nil {
			slog.Warn("channelconfig: orphaned config sweep failed", "err", err)
		} else if n > 0 {
			slog.Info("channelconfig: removed orphaned MCP configs", "count", n)
		}

		// Seed the capability catalogue from the tool allow-list, then load it
		// back into the pipeline package so BuildAllowList's grant-translation
		// path reads real rows instead of a fabricated tool-class view. Without
		// this, the capabilities table stays empty and every lookup resolves to
		// a zero-value CapabilityView, which the gate's fail-closed default
		// sends to deny.
		capabilityRepo := repo.NewCapabilityRepo(entClient)
		if seeded := repo.SeedCapabilities(ctx, capabilityRepo); seeded > 0 {
			slog.Info("capability: seeded catalogue", "count", seeded)
		}

		// Obsidian is a builtin Application: its registry identity and its
		// four capabilities only exist once Register runs — without this
		// call, obsidian.search/obsidian.read/obsidian.write/obsidian.delete
		// all resolve to a catalogue row that was never written (class,
		// enforceable-by, reversible all zero-valued) instead of the one
		// Register declares. Idempotent, so safe on every boot.
		//
		// obsidian.IndexNotes is the function that checks obsidian.search
		// and obsidian.read against this catalogue; POST /api/obsidian/index
		// (obsidianHandler below) is its production caller. Client.Write and
		// Client.Delete are reached by the obsidian_write/obsidian_delete MCP
		// tools (di_mcp.go's provideMCPHandler), gated the same way, with an
		// Asker instead of none.
		if err := obsidian.Register(ctx, resourceRepo, capabilityRepo); err != nil {
			return nil, fmt.Errorf("obsidian: register application: %w", err)
		}

		// GitHub is the second builtin Application, and the reason this slice
		// exists: it goes through the same Register, the same capability
		// catalogue and the same encrypted-settings path Obsidian does. If it
		// ever needs a kernel change Obsidian did not, that is the finding.
		if err := github.Register(ctx, resourceRepo, capabilityRepo); err != nil {
			return nil, fmt.Errorf("github: register application: %w", err)
		}

		// IndexNotes takes a spaceID and memory spaces are never
		// auto-created (repo.MemoryRepo.CreateSpace is always caller-driven)
		// — without this, the trigger below would 500 on a fresh install
		// with nothing to resolve. Idempotent, same as Register just above.
		obsidianSpace, err := ensureObsidianSpace(ctx, resourceRepo)
		if err != nil {
			return nil, fmt.Errorf("obsidian: ensure memory space: %w", err)
		}
		obsidianSpaceID = obsidianSpace.ID

		// Construct the vault client from settings so it exists once the
		// operator configures it, rather than only once something reads
		// obsidian.IndexNotes. A read-only integration does not get to take
		// the server down: a half-configured vault logs a warning and starts
		// with the vault off, the same tolerance githubClient gets below.
		obsidianClients.Set(bootObsidianClient(ctx, settingsSvc))
		watchObsidianSettings(settingsSvc, obsidianClients)

		// A read-only integration does not get to take the server down. The
		// builder still reports precisely why it could not be constructed —
		// most often a gh login that expired — but the dashboard, its agents
		// and its pipeline have nothing to do with GitHub, and an operator
		// whose server refuses to boot cannot even open the page that would
		// tell them what is wrong.
		githubClient, err = buildGitHubClient(ctx, settingsSvc)
		if err != nil {
			slog.Warn("github: integration disabled", "err", err)
			githubClient = nil
		}

		if rows, err := capabilityRepo.List(ctx); err != nil {
			slog.Warn("capability: catalogue load failed", "err", err)
		} else {
			catalogue := make(map[string]capability.CapabilityView, len(rows))
			for _, row := range rows {
				catalogue[row.Name] = capability.CapabilityView{
					Name:          row.Name,
					Class:         row.Class,
					EnforceableBy: row.EnforceableBy,
				}
			}
			pipeline.SetCapabilityCatalogue(catalogue)
		}
	}

	// Load plugins from configured plugin_dir. ctx is the server-lifetime context
	// (cancelled on SIGTERM/SIGINT). Load derives a 30-second startup timeout internally.
	pluginRegistry := plugin.New(cfg.PluginDir)
	activePlugins := map[string]bool{}
	if pluginRepo != nil {
		rows, listErr := pluginRepo.List(ctx)
		if listErr != nil {
			return nil, fmt.Errorf("plugin enablement snapshot: %w", listErr)
		}
		for _, p := range rows {
			activePlugins[p.ID] = p.Active
		}
	}
	pluginRegistry.SetEnabled(func(id string) bool { return activePlugins[id] })

	// Build settings service early so the provider is wired before Load.
	// Nil when running without a database (no entClient). box was resolved
	// above, alongside settings.Service — shared, not re-derived.
	var pluginSettingsSvc *plugin.Service
	if entClient != nil {
		pluginSettingRepo := repo.NewPluginSettingRepo(entClient)
		pluginSettingsSvc = plugin.NewSettingsService(pluginSettingRepoAdapter{inner: pluginSettingRepo}, box)
		pluginRegistry.SetSettingsProvider(func(ctx context.Context, id string) (map[string]string, error) {
			return pluginSettingsSvc.DecryptedAll(ctx, id)
		})
		// A module's callback credential: scoped to what its manifest declares,
		// re-minted on every start, revoked when it stops.
		pluginRegistry.SetCredentialIssuer(moduleCredentials{keys: repo.NewApiKeyRepo(entClient)})
	}
	// Beside the core database rather than inside a module's checkout: a module
	// is updated with git, so data kept in the checkout would be taken along by
	// the update.
	if home, err := os.UserHomeDir(); err == nil {
		pluginRegistry.SetDataRoot(filepath.Join(home, ".claude", "kontor", "modules"))
	} else {
		slog.Warn("modules start without a data directory: home is unknown", "err", err)
	}

	// oauthProvider and pluginLoginURL are set by the SetAuth hook when an auth_provider
	// plugin passes health-check. If no auth_provider plugin is configured both stay at
	// zero values, which activates bypass-auth on loopback.
	var oauthProvider authpkg.OAuthProvider
	var pluginLoginURL string
	if err := pluginRegistry.Load(ctx, plugin.Hooks{
		OnUnhealthy: func(id string) {
			if pluginRepo == nil {
				return
			}
			if err := pluginRepo.SetActive(ctx, id, false); err != nil {
				slog.Error("plugin: failed to persist unhealthy state", "id", id, "err", err)
			}
		},
		SetAuth: func(p authpkg.OAuthProvider, loginURL string) {
			oauthProvider = p
			pluginLoginURL = loginURL
			slog.Info("auth: using plugin provider", "loginURL", loginURL)
		},
	}); err != nil {
		return nil, fmt.Errorf("plugin registry: load failed: %w", err)
	}
	cleanup := func() { pluginRegistry.Shutdown() }

	// Fatal-safety check: if a plugin directory is configured AND at least one
	// plugin.json declared auth_provider capability BUT no healthy auth_provider
	// ended up in the registry, the server must not start — booting without auth
	// would be a silent security regression.
	if pluginRegistry.HasDir() &&
		pluginRegistry.HasAttemptedCapability(plugin.CapAuthProvider) &&
		pluginRegistry.FindByCapability(plugin.CapAuthProvider) == nil {
		return &ServerComponents{Cleanup: cleanup}, fmt.Errorf(
			"auth_provider plugin configured but failed health-check — refusing to start with auth disabled",
		)
	}

	if oauthProvider == nil {
		slog.Info("auth: no auth_provider plugin found — bypass-auth active for loopback")
	}

	// The rename left this installation's own entries in the claude CLI's
	// config under their old names. They are renamed in place, not removed:
	// the entry carries the credential the operator registered, and deleting
	// it would leave them with nothing registered at all. Only entries that
	// still point at this application are touched.
	if moved, err := claudeconfig.MigrateLegacyEntries(); err != nil {
		slog.Warn("pre-rename MCP entries not migrated", "err", err)
	} else if len(moved) > 0 {
		slog.Info("pre-rename MCP entries renamed in place", "entries", moved)
	}

	// A module carries its provider descriptors with it, so they are loaded
	// once the modules are.
	registerModuleProviders(pluginRegistry, providerRegistry)

	// Routines a module ships are created once, owned by it, and disabled
	// until the operator points them at a directory.
	if entClient != nil {
		registerModuleRoutines(ctx, pluginRegistry, repo.NewTaskScheduleRepo(entClient))
	}

	// A module's tools become grantable the moment the module is loaded: the
	// catalogue is what the grants surface lists, so without a row nobody could
	// allow the tool even though the gate is asked about it.
	if entClient != nil {
		registerModuleToolCapabilities(ctx, pluginRegistry, repo.NewCapabilityRepo(entClient))
	}

	// SP1 plugin lifecycle: DB-backed plugin state, per-plugin settings (secret
	// fields encrypted at rest), lifecycle transitions, and on-disk discovery.
	// Constructed only with a database — the handler stays nil otherwise.
	var pluginLifecycleHandler *apiplugins.LifecycleHandler
	if pluginSettingsSvc != nil {
		pluginSettingRepo := repo.NewPluginSettingRepo(entClient)
		lifecycleEngine := plugin.NewLifecycleEngine(
			pluginStateRepoAdapter{inner: pluginRepo},
			plugin.NewHTTPHookCaller(),
			pluginSettingsSvc,
			pluginProcessAdapter{reg: pluginRegistry},
		)
		// The same root the registry hands to a starting module, so an
		// uninstall archives exactly what that module wrote.
		if home, err := os.UserHomeDir(); err == nil {
			lifecycleEngine.SetDataRoot(filepath.Join(home, ".claude", "kontor", "modules"))
		}
		// Deactivating a module takes its routines out of service; they are
		// disabled rather than deleted, so what they already produced stays
		// traceable.
		lifecycleEngine.SetRoutineRetirer(repo.NewTaskScheduleRepo(entClient))
		discoverer := plugin.NewDiscoverer(cfg.PluginDir, pluginDiscoverRepoAdapter{inner: pluginRepo, settings: pluginSettingRepo})
		lifecycleProbe := func(id string) (bool, bool) {
			e, ok := pluginRegistry.Lookup(id)
			return ok, ok && e.Healthy()
		}
		lifecycleController := pluginmgmt.New(pluginRepo, lifecycleEngine, pluginSettingsSvc, cfg.PluginDir, lifecycleProbe)
		pluginLifecycleHandler = apiplugins.NewLifecycle(lifecycleController)

		if res, discErr := discoverer.Discover(ctx); discErr != nil {
			slog.Warn("plugin discovery failed", "error", discErr)
		} else {
			slog.Info("plugin discovery", "found", res.Found, "updatesAvailable", res.UpdatesAvailable)
		}
	}

	routerConfig := provideRouterConfig(cfg, settingsSvc, oauthProvider, pluginLoginURL)

	var systemPromptRepo repo.SystemPromptRepo
	if entClient != nil {
		systemPromptRepo = repo.NewSystemPromptRepo(entClient)
	}

	// Construct repos required by the spawner resolver BEFORE the
	// orchestrator so the resolver can be threaded into stage handlers.
	// Seed claude-default first so the resolver's deployment-wide fallback
	// is guaranteed to exist for every task that lacks an explicit ref.
	var taskRepoForResolver repo.TaskRepo
	var projectRepo repo.ProjectRepo
	var projectFolderRepo repo.ProjectFolderRepo
	var spawnerRepo repo.SpawnerRepo
	var spawnerResolver services.SpawnerResolver
	if entClient != nil {
		taskRepoForResolver = repo.NewTaskRepo(entClient)
		projectRepo = repo.NewProjectRepo(entClient)
		projectFolderRepo = repo.NewProjectFolderRepo(entClient)
		spawnerRepo = repo.NewSpawnerRepo(entClient)
		if bundle != nil {
			if err := repairSpawnerAdapterConfig(ctx, bundle.DB); err != nil {
				return &ServerComponents{Cleanup: cleanup}, fmt.Errorf("repair spawner adapter_config: %w", err)
			}
		}
		if err := seedSpawners(ctx, spawnerRepo); err != nil {
			return &ServerComponents{Cleanup: cleanup}, fmt.Errorf("seed spawners: %w", err)
		}
		if err := migrateAdapterConfigToSpawners(ctx, cfg, spawnerRepo); err != nil {
			return &ServerComponents{Cleanup: cleanup}, fmt.Errorf("migrate adapter config: %w", err)
		}
		pipelineConfigRepo := repo.NewPipelineConfigRepo(entClient)
		spawnerResolver = services.NewSpawnerResolver(taskRepoForResolver, projectRepo, spawnerRepo, pipelineConfigRepo)
	}

	// Per-turn checkpoint/revert: the Service snapshots worktree turns and drives
	// reverts; the Checkpointer runs the fsnotify watcher. orch is forward-declared
	// so the Service's KillFn can reach KillRunningStage once the orchestrator exists.
	var orch *pipeline.PipelineOrchestrator
	var checkpointSvc *checkpoint.Service
	var cpStart func(taskID, worktreePath string)
	var cpStop func(taskID string)
	if entClient != nil {
		cpRepo := repo.NewCheckpointRepo(entClient)
		cpTaskRepo := repo.NewTaskRepo(entClient)
		cpSRRepo := repo.NewStageRunRepo(entClient)
		checkpointSvc = checkpoint.NewService(checkpoint.ServiceOptions{
			Repo:        cpRepo,
			MaxPerTask:  50,
			Broadcaster: taskBroadcaster,
			KillFn: func(ctx context.Context, taskID string) error {
				if orch == nil {
					return nil
				}
				return orch.KillRunningStage(ctx, taskID)
			},
			ParkFn: func(ctx context.Context, taskID, reason string) error {
				task, err := cpTaskRepo.GetByID(ctx, taskID)
				if err != nil {
					return err
				}
				run, err := cpSRRepo.GetLatestByTaskAndStage(ctx, taskID, task.CurrentStage)
				if err != nil || run == nil {
					return err
				}
				awaiting := "awaiting_user"
				if _, err := cpSRRepo.Update(ctx, run.ID, repo.UpdateStageRunInput{
					Status:   &awaiting,
					PIDClear: true,
					Output:   map[string]any{"checkpoint_revert_reason": reason},
				}); err != nil {
					return err
				}
				taskBroadcaster.Broadcast(sse.TaskEvent{Type: "task_updated", TaskID: taskID})
				return nil
			},
		})
		cpManager := checkpoint.NewCheckpointer(checkpoint.CheckpointerOptions{
			OnSnapshot: func(taskID, worktreePath string) {
				_ = checkpointSvc.TakeSnapshot(context.Background(), taskID, worktreePath)
			},
		})
		cpStart = cpManager.Start
		cpStop = func(taskID string) {
			cpManager.Stop(taskID)
			if task, err := cpTaskRepo.GetByID(context.Background(), taskID); err == nil &&
				task != nil && task.WorktreePath != nil && *task.WorktreePath != "" {
				if err := checkpointSvc.PruneRefs(context.Background(), taskID, *task.WorktreePath); err != nil {
					slog.Warn("checkpoint: prune refs on stop", "taskID", taskID, "err", err)
				}
			}
			if err := cpRepo.DeleteByTask(context.Background(), taskID); err != nil {
				slog.Warn("checkpoint: delete rows on stop", "taskID", taskID, "err", err)
			}
		}
	}

	orch, err = provideOrchestrator(cfg, settingsSvc, entClient, taskBroadcaster, systemPromptRepo, spawnerResolver, cpStart, cpStop, memRepo, memRetriever, grantUsageRepo, repo.NewApplicationSecretRepo(entClient, box))
	if err != nil {
		return &ServerComponents{Cleanup: cleanup}, err
	}

	// Construct the detached refinement runner before the task handler so the
	// nil-safe interface value (refineReaderArg) can be threaded in. The runner
	// itself is late-bound to taskHandler.BroadcastTaskUpdate after both are ready.
	var refineRunner *refine.Runner
	if entClient != nil {
		refineRunner = refine.NewRunner(repo.NewRefinementTurnRepo(entClient), nil)
	}

	// Nil-interface guard: a nil *refine.Runner assigned directly to the interface
	// produces a non-nil interface value (typed nil), which defeats the
	// `h.refineReader == nil` guard in applyRefineStatus. Compute a true nil
	// interface when there is no runner.
	var refineReaderArg tasks.RefineStatusReader
	if refineRunner != nil {
		refineReaderArg = refineRunner
	}

	var rawDB *sql.DB
	if bundle != nil {
		rawDB = bundle.DB
	}
	taskHandler := provideTaskHandler(entClient, rawDB, orch, taskBroadcaster, refineReaderArg, settingsSvc.Bool("git.allowPull"), routerConfig.BypassAuth, checkpointSvc, newPushPermissionNotifier(wpSvc))

	// Scheduler: recurring task firing engine + its REST handler. Reuses the task
	// handler's create core, so it must be built after taskHandler. nil when no DB.
	sched, schedulesHandler := provideScheduler(entClient, taskHandler, taskBroadcaster, routerConfig.BypassAuth)

	// The six callers that may block a live request on a human decision: the
	// memory, obsidian and github MCP tools below (an agent waits on the tool
	// response), and the HTTP memory, resources and github handlers further
	// down (a browser request has a human on the other end). Count them by
	// grepping askerArg and memAsker in this file and di_mcp.go — the number
	// has been stale twice, both times because a new caller was added without
	// anyone revisiting a sentence in another file.
	// The pipeline's memory push (di_pipeline.go) and the
	// obsidian trigger's Gate (obsidianHandler, built further down in this
	// function — distinct from the obsidian MCP tools, which share this
	// asker) both construct their own memory.Gate with no Asker instead of
	// sharing this one — nothing is watching either of them run, so an
	// unanswerable ask must deny rather than stall a spawn or an HTTP
	// response.
	var memAsker *serverask.Asker
	// Bypass-auth unmounts the route a human would answer through (router.go),
	// so an asker here would only hold every ask for its full timeout before
	// denying anyway. The router installs its debounced rescan as the asker's
	// onChange once it exists, the same split HookEnforcer already uses.
	if !routerConfig.BypassAuth {
		memAsker = serverask.New(nil)
	}
	askerArg := askerArgFor(memAsker)
	var capabilityDecisions agentbroadcast.CapabilityDecisionProvider
	if memAsker != nil {
		capabilityDecisions = func(context.Context) []sdk.PendingCapabilityDecision {
			return toPendingCapabilityDecisions(memAsker.Pending())
		}
	}

	// A module's stage kinds become resolvable now that the orchestrator exists.
	// A kind that collides with a core stage is refused there and logged.
	registerModuleStageKinds(orch, pluginRegistry)

	mcpHandler := provideMCPHandler(entClient, orch, sched, taskBroadcaster, projectBroadcaster, refineRunner, memRepo, memRetriever, grantUsageRepo, askerArg, obsidianClients, githubClient, newModuleToolSource(pluginRegistry))

	var histImporter *histsvc.Importer
	var historyHandler *apihistory.Handler
	if entClient != nil {
		costRepo := repo.NewAgentCostTrendRepo(entClient)
		costProjectResolver := services.NewCostProjectResolver(projectFolderRepo)
		histImporter = histsvc.NewImporter(costRepo).WithProjectResolver(costProjectResolver)
		historyHandler = apihistory.NewHandler(histImporter)
	}

	// Memory HTTP surface — reuses the single memRepo/memRetriever built above
	// (same rule as the MCP tools and the pipeline's push-at-spawn seam) plus
	// its own capability/grant repos, matching provideMCPHandler's wiring.
	var memoryHandler *apimemory.Handler
	if entClient != nil {
		memoryHandler = apimemory.NewHandler(memRepo, memRetriever, memory.Gate{
			Capabilities: repo.NewCapabilityRepo(entClient),
			Grants:       repo.NewGrantRepo(entClient),
			GrantUsage:   grantUsageRepo,
			Asker:        askerArg,
		})
	}

	// Resource registry read surface — GET /api/resources. Shares the single
	// resourceRepo instance hoisted above (same rule as memRepo/memRetriever
	// just above). Read-only, but not ungated: kind=memory_space answers the
	// same rows GET /api/memory/spaces gates on memory.read, so it is built
	// with the same Gate memoryHandler gets — asker included, because a human
	// is waiting on this request too.
	var resourcesHandler *apiresources.Handler
	if entClient != nil {
		resourcesHandler = apiresources.NewHandler(resourceRepo, repo.NewTaskScheduleRepo(entClient), memory.Gate{
			Capabilities: repo.NewCapabilityRepo(entClient),
			Grants:       repo.NewGrantRepo(entClient),
			GrantUsage:   grantUsageRepo,
			Asker:        askerArg,
		}, routerConfig.BypassAuth)
	}

	var applicationsHandler *apiapplications.Handler
	if entClient != nil {
		setupManager = appsetup.NewManager(appsetup.Options{})
		cleanup = chainCleanup(cleanup, setupManager.StopAll)

		applicationsHandler = apiapplications.NewHandler(
			repo.NewMCPApplicationRepo(entClient),
			repo.NewApplicationSecretRepo(entClient, box),
			mcpapps.Refresher{
				Apps:         repo.NewMCPApplicationRepo(entClient),
				Secrets:      repo.NewApplicationSecretRepo(entClient, box),
				Capabilities: repo.NewCapabilityRepo(entClient),
				Transport:    mcpapps.StdioTransport,
				Now:          time.Now,
			},
			repo.NewGrantRepo(entClient),
			resourceRepo,
			repo.NewTaskScheduleRepo(entClient),
			setupManager,
			apiapplications.StdioToolCaller{},
			repo.NewCapabilityRepo(entClient),
		)

		watchCtx, stopConfigWatch := context.WithCancel(context.Background())
		if err := claudeconfig.Watch(watchCtx, func() {
			taskBroadcaster.Broadcast(sse.TaskEvent{Type: "applications_changed"})
		}); err != nil {
			slog.Warn("claudeconfig: watch failed — outside changes are noticed on refresh only", "err", err)
			stopConfigWatch()
		} else {
			cleanup = chainCleanup(cleanup, stopConfigWatch)
		}
	}

	// Obsidian manual trigger — POST /api/obsidian/index. Unlike memoryHandler
	// just above (built with askerArg because a human is waiting on that
	// request), this Gate carries no Asker: the same reasoning
	// AuthorizeMemory's closure documents for the pipeline's memory push
	// (di_pipeline.go) applies here — nobody is watching this run resolve a
	// capability question, so an ask decision must deny rather than hold the
	// HTTP response open for a human who is not there. Mounted even when
	// the vault is unconfigured; the handler itself answers
	// 503 for that case rather than the route not existing at all, mirroring
	// how auth.Handler stays mounted with a nil OAuthProvider.
	var obsidianHandler *apiobsidian.Handler
	if entClient != nil {
		obsidianHandler = apiobsidian.NewHandler(obsidianClients, memRepo, memory.Gate{
			Capabilities: repo.NewCapabilityRepo(entClient),
			Grants:       repo.NewGrantRepo(entClient),
			GrantUsage:   grantUsageRepo,
		}, obsidianSpaceID)
	}

	// Skill materialization — POST /api/skills/materialize. Shares the single
	// resourceRepo instance hoisted above, for the same reason resourcesHandler
	// does. The two directory sources are the ones the rest of the server
	// already trusts: the parser's four-tier Claude search set (which is what
	// finds a ~/.claude-personal), and the provider registry's enabled config
	// dirs (which already drops the ones that do not exist).
	var skillsHandler *apiskills.Handler
	if entClient != nil {
		skillsHandler = apiskills.NewHandler(materializer.New(
			resourceRepo,
			repo.NewSkillRepo(entClient),
			repo.NewMaterializationRepo(entClient),
			repo.NewCoordLockRepo(entClient),
			materializer.Resolver{
				NodeID:             repo.DefaultNodeID,
				ClaudeConfigDirs:   parser.AllClaudeConfigDirs,
				ProviderConfigDirs: providerRegistry.ConfigDirs,
			},
		))
	}

	// GitHub — /api/github/*. Built WITH askerArg, unlike obsidianHandler
	// just above: obsidianHandler's route only starts an unattended batch
	// run (IndexNotes), while these four routes are the direct request the
	// browser is blocked on, the same shape memoryHandler and
	// resourcesHandler above already gate with askerArg — a human is on the
	// other end of it, so an "ask" decision may legitimately hold for their
	// answer. github.merge never reaches the asker at all: its "spend" class
	// resolves to deny in Decide, and ServerEnforcer returns ErrDenied
	// before the ask branch runs.
	var githubHandler *apigithub.Handler
	if entClient != nil {
		githubHandler = apigithub.NewHandler(githubClient, memory.Gate{
			Capabilities: repo.NewCapabilityRepo(entClient),
			Grants:       repo.NewGrantRepo(entClient),
			GrantUsage:   grantUsageRepo,
			Asker:        askerArg,
		})
	}

	// Eval / drift-detection subsystem. The onDrift callback is the only outward
	// hook — eval/ stays notifications-agnostic; wiring lives here in the root.
	var evalService *eval.Service
	var evalHandler *apieval.Handler
	if entClient != nil {
		evalMetricRepo := repo.NewEvalMetricRepo(entClient)
		driftAlertRepo := repo.NewDriftAlertRepo(entClient)
		collector := eval.NewCollector(repo.NewStageRunRepo(entClient), repo.NewTaskRepo(entClient))
		evalService = eval.NewService(
			collector,
			evalMetricRepo,
			driftAlertRepo,
			eval.Thresholds{RateDropPP: settingsSvc.Float("eval.rateDropPP"), StddevK: settingsSvc.Float("eval.stddevK"), MinSamples: settingsSvc.Int("eval.minSamples")},
			settingsSvc.Int("eval.windowHours"),
		).WithOnDrift(evalOnDrift(taskBroadcaster))
		evalHandler = apieval.NewHandler(evalMetricRepo, driftAlertRepo, evalService)
	}

	var refineHandler *refineapi.Handler
	if entClient != nil {
		refineHandler = refineapi.NewHandler(refineapi.Deps{
			Turns:     repo.NewRefinementTurnRepo(entClient),
			Tasks:     repo.NewTaskRepo(entClient),
			StageRuns: repo.NewStageRunRepo(entClient),
			Runner:    refineRunner,
			Advance: func(ctx context.Context, taskID string) error {
				_, err := orch.ProgressTask(ctx, taskID, nil)
				return err
			},
			Revoke: mcppkg.StageKeyIssuer{Keys: repo.NewApiKeyRepo(entClient)}.Revoke,
			ResolveSpawner: func(ctx context.Context, taskID string) (*ent.Spawner, services.SpawnerSource, error) {
				if spawnerResolver == nil {
					return nil, services.SpawnerSourceDefault, nil
				}
				// Refinement is not a per-stage-configurable stage; resolve with empty
				// stage so it falls through to task -> project default -> claude default.
				return spawnerResolver.Resolve(ctx, taskID, "")
			},
		})
	}

	var planHandler *planapi.Handler
	if entClient != nil {
		planHandler = planapi.NewHandler(planapi.HandlerDeps{
			Turns:     repo.NewRefinementTurnRepo(entClient),
			Tasks:     repo.NewTaskRepo(entClient),
			StageRuns: repo.NewStageRunRepo(entClient),
			Advance: func(ctx context.Context, taskID string) error {
				_, err := orch.ProgressTask(ctx, taskID, nil)
				return err
			},
			Requeue: func(ctx context.Context, taskID, prompt string) error {
				_, err := orch.RequeueForUser(ctx, taskID, prompt)
				return err
			},
			Revoke: mcppkg.StageKeyIssuer{Keys: repo.NewApiKeyRepo(entClient)}.Revoke,
		})
	}

	// Late-bind the runner → task-handler status broadcast. Must happen after both
	// are constructed; safe to call multiple times (last write wins in the runner).
	if refineRunner != nil && taskHandler != nil {
		refineRunner.SetOnRunChange(taskHandler.BroadcastTaskUpdate)
	}

	var analyticsHandler *apianalytics.Handler
	var analyticsRepo rawrepo.AnalyticsRepo
	if bundle != nil {
		cfgRepo := repo.NewPipelineConfigRepo(entClient)
		analyticsRepo = rawrepo.NewAnalyticsRepo(bundle.DB)
		analyticsHandler = apianalytics.NewHandler(analyticsRepo, bundle.DB, cfgRepo)
		// Seed project-configured extra safe Bash commands so grants validate
		// against the stored allow-list from first request onward.
		raw := cfgRepo.GetString(ctx, "extraSafeBashCommands", "")
		permissions.SetExtraSafeBashCommands(permissions.ParseExtraSafeBashCommands(raw))
	}
	// Cost baseline for the agent health score's cost-spike component. Nil repo
	// (no database) yields a provider that returns 0 → no cost penalty.
	baselineProvider := agentbroadcast.NewCostBaselineProvider(analyticsRepo)

	// Pipeline-task enricher: read-only crossing that annotates each scanned agent
	// with its linked pipeline task (ID + title). Nil entClient (no database)
	// leaves it nil → no enrichment. Threaded into BOTH GetAgents call sites
	// (the SSE broadcast loop below and the router's request-scoped accessor) so
	// the crossing is applied consistently.
	var pipelineEnricher merger.Enricher
	if entClient != nil {
		pipelineEnricher = agentbroadcast.NewPipelineTaskEnricher(repo.NewStageRunRepo(entClient), taskRepoForResolver, repo.NewPermissionRepo(entClient), repo.NewGrantRepo(entClient), repo.NewCapabilityRepo(entClient))
	}

	// Hook-event store + enricher: the opt-in receiver records per-event hook
	// granularity here (write side in the hooks API), and this enricher reads it
	// back onto each agent. Always constructed — hooks work without a database.
	hookStore := hookstore.New(settingsSvc.Int("hooks.eventsPerSession"), hookstore.DefaultTTL)

	// Spawner attribution. Runs after the pipeline enricher because it reads the
	// PipelineTaskID that one sets; sessions without a task are placed from the
	// config dir their process carries.
	spawnerEnricher := agentbroadcast.NewSpawnerEnricher(spawnerRepo, taskRepoForResolver)

	// Hook enforcer: holds PreToolUse hook calls open so an approval prompt
	// can be answered here instead of in the session's terminal. Built at this
	// point rather than in the router because the enricher below reads the same
	// instance, and the enricher has to exist before the router is constructed.
	hookEnforcer := hooks.NewHookEnforcer(nil)
	// Every config dir, not just the server's own: a session can run under a
	// custom CLAUDE_CONFIG_DIR and its deny rules live there.
	hookEnforcer.SetDenyReader(claudesettings.NewReader(parser.AllClaudeConfigDirs()...))

	// Combine the read-only crossings into one enricher applied at every GetAgents
	// call site. A nil pipelineEnricher (no DB) composes away.
	agentEnricher := merger.ChainEnrichers(
		pipelineEnricher,
		spawnerEnricher,
		agentbroadcast.NewHookEventEnricher(hookStore),
		agentbroadcast.NewPermissionBridgeEnricher(hookEnforcer),
	)

	// Built here (not earlier) so it captures agentEnricher — admin agent search
	// results carry the same pipeline-task and hook-event annotations as /api/agents.
	var searchHandler *search.Handler
	if bundle != nil {
		searchHandler = search.NewHandler(rawrepo.NewSearchRepo(bundle.DB), agentMerger, agentEnricher, routerConfig.BypassAuth)
	}

	var costHandler *apicost.Handler
	if bundle != nil {
		costHandler = apicost.NewHandler(bundle.DB)
	}

	// Build optional handlers that previously lived inside provideRouterDeps.
	// projectRepo, projectFolderRepo, spawnerRepo were constructed earlier
	// for the spawner resolver — reuse those instances here.
	var userRepo repo.UserRepo
	var apiKeyRepo repo.ApiKeyRepo
	if entClient != nil {
		userRepo = repo.NewUserRepo(entClient)
		apiKeyRepo = repo.NewApiKeyRepo(entClient)
	}
	var remotesHandler *remotes.Handler
	if entClient != nil {
		remotesHandler = remotes.NewHandler(repo.NewRemoteRegistrationRepo(entClient))
	}
	var presetsHandler *presets.Handler
	var permissionPresetRepo repo.PermissionPresetRepo
	if entClient != nil {
		permissionPresetRepo = repo.NewPermissionPresetRepo(entClient)
		presetsHandler = presets.NewHandler(permissionPresetRepo)
	}
	var grantsHandler *grants.Handler
	if entClient != nil {
		grantsHandler = grants.NewHandler(repo.NewGrantRepo(entClient), repo.NewCapabilityRepo(entClient))
	}
	var systemPromptsHandler *systemprompts.Handler
	if entClient != nil {
		systemPromptsHandler = systemprompts.NewHandler(systemPromptRepo)
	}
	var promptTemplatesHandler *prompttemplates.Handler
	if entClient != nil {
		promptTemplatesHandler = prompttemplates.NewHandler(repo.NewPromptTemplateRepo(entClient))
	}
	adapterHandler := adapters.NewHandler()
	replyStore := agents.NewReplyStore()
	var channelStageOutputHandler *agents.ChannelStageOutputHandler
	var auditEventRepo repo.AuditEventRepo
	if entClient != nil {
		// Constructed after the repo it takes: passing auditEventRepo while it was
		// still the zero value handed the handler a nil interface, which its own
		// nil-guard then treated as "auditing disabled" — silently, by design, so
		// nothing logged and the stage_output_submitted event simply never
		// appeared. Keep the assignment first.
		auditEventRepo = repo.NewAuditEventRepo(entClient)
		channelStageOutputHandler = agents.NewChannelStageOutputHandler(repo.NewStageRunRepo(entClient), apiKeyRepo, auditEventRepo)
	}

	usageHandler := apiusage.NewHandler(settingsSvc, nil) // nil agg = uses default scanner

	var onboardingHandler *onboarding.Handler
	if apiKeyRepo != nil {
		onboardingHandler = onboarding.NewHandler(settingsSvc, apiKeyRepo)
	}

	var trackerHandler *trackerapi.Handler
	if pluginSettingsSvc != nil {
		trackerHandler = trackerapi.NewHandler(pluginSettingsSvc, nil, nil)
	}

	var kontorSessionHandler *kontorsessionapi.Handler
	if apiKeyRepo != nil {
		if cacheDir, err := os.UserCacheDir(); err != nil {
			slog.Warn("kontor session disabled: no user cache dir", "err", err)
		} else {
			sessionDir := filepath.Join(cacheDir, "kontor", "session")
			policy := services.NewSpawnPolicy(services.ProjectFolderRootsProvider(projectRepo, projectFolderRepo), sessionDir)
			kontorSpawns := agents.NewSpawnManager(0, 0, 0, 0, spawnerRepo, policy)
			kontorSvc := &kontorsession.Service{
				Keys:       mcppkg.KontorSessionKeyIssuer{Keys: apiKeyRepo},
				Audit:      auditEventRepo,
				TaskAPIURL: fmt.Sprintf("http://127.0.0.1:%d", cfg.Port) + mcppkg.EndpointPath,
				Dir:        sessionDir,
				// Outside the session cwd so claude never auto-loads it as a project .mcp.json.
				ConfigPath: filepath.Join(cacheDir, "kontor", "session-mcp.json"),
				Spawn: func(ctx context.Context, o kontorsession.SpawnOptions) (int, error) {
					return kontorSpawns.SpawnSession(ctx, agents.SessionSpawnOptions(o))
				},
				Terminate: kontorSpawns.TerminateSession,
				Alive:     kontorSpawns.OwnsLiveSession,
				WaitExit:  agents.WaitForExit,
			}
			if err := kontorSvc.Reconcile(ctx); err != nil {
				slog.Warn("kontor session: reconcile failed", "err", err)
			}
			kontorSessionHandler = kontorsessionapi.New(kontorSvc)
		}
	}

	routerDeps := api.RouterDeps{
		Ctx:                    ctx,
		Config:                 routerConfig,
		AgentBroadcaster:       broadcaster,
		Merger:                 agentMerger,
		Enricher:               agentEnricher,
		HookStore:              hookStore,
		HookEnforcer:           hookEnforcer,
		CapabilityDecisions:    capabilityDecisions,
		CapabilityAsker:        capabilityAskerFor(memAsker),
		OAuthProvider:          oauthProvider,
		UserRepo:               userRepo,
		ApiKeyRepo:             apiKeyRepo,
		ProjectRepo:            projectRepo,
		ProjectFolderRepo:      projectFolderRepo,
		SpawnerRepo:            spawnerRepo,
		SpawnerBroadcaster:     spawnerBroadcaster,
		ProjectBroadcaster:     projectBroadcaster,
		TaskProjectOps:         newTaskProjectOps(entClient),
		TaskHandler:            taskHandler,
		CoordHandler:           coordapi.New(repo.NewScratchpadRepo(entClient), repo.NewCoordLockRepo(entClient)),
		WebPushHandler:         webPushHandler,
		RemotesHandler:         remotesHandler,
		PresetsHandler:         presetsHandler,
		PermissionPresetRepo:   permissionPresetRepo,
		GrantsHandler:          grantsHandler,
		SystemPromptsHandler:   systemPromptsHandler,
		PromptTemplatesHandler: promptTemplatesHandler,
		AdapterHandler:         adapterHandler,
		OnboardingHandler:      onboardingHandler,
		ProvidersHandler:       providersHandler,
		SettingsHandler:        settingsHandler,
		SearchHandler:          searchHandler,
		HistoryHandler:         historyHandler,
		MemoryHandler:          memoryHandler,
		ResourcesHandler:       resourcesHandler,
		ApplicationsHandler:    applicationsHandler,
		SkillsHandler:          skillsHandler,
		ObsidianHandler:        obsidianHandler,
		GitHubHandler:          githubHandler,
		RefineHandler:          refineHandler,
		PlanHandler:            planHandler,
		AnalyticsHandler:       analyticsHandler,
		CostHandler:            costHandler,
		EvalHandler:            evalHandler,
		VisualizationsHandler:  apivisualizations.NewHandler(),
		MCPHandler:             mcpHandler,
		SchedulesHandler:       schedulesHandler,
		ChannelReply:           agents.NewChannelReplyHandler(replyStore, apiKeyRepo, repo.NewStageRunRepo(entClient)),
		ChannelStageOutput:     channelStageOutputHandler,
		PluginRegistry:         pluginRegistry,
		PluginLifecycleHandler: pluginLifecycleHandler,
		AuditEventRepo:         auditEventRepo,
		UsageHandler:           usageHandler,
		TrackerHandler:         trackerHandler,
		KontorSessionHandler:   kontorSessionHandler,
		AdminHandler: admin.New(
			restart.NewAuthProviderValidator(pluginRegistry, activePluginIDs(pluginRepo), cfg.PluginDir),
			string(restartCtl.Mode()),
			restart.OSRestarter{},
			restartCtl.Trigger,
		),
	}
	router := api.NewRouter(routerDeps)
	server := provideServer(cfg, settingsSvc, router, ln)
	return &ServerComponents{
		API:                 server,
		Broadcaster:         broadcaster,
		Merger:              agentMerger,
		Orchestrator:        orch,
		Scheduler:           sched,
		HistImporter:        histImporter,
		ApiKeyRepo:          apiKeyRepo,
		Baseline:            baselineProvider,
		Enricher:            agentEnricher,
		CapabilityDecisions: capabilityDecisions,
		Eval:                evalService,
		Settings:            settingsSvc,
		SetupManager:        setupManager,
		Obsidian:            obsidianClients,
		GitHub:              githubClient,
		Cleanup:             cleanup,
	}, nil
}

// activePluginIDs returns a closure listing the IDs of plugins currently marked
// active in the DB. Used by the restart validator to predict the next boot's
// auth_provider set. Nil repo (no DB) -> no active plugins.
func activePluginIDs(pluginRepo repo.PluginRepo) func(context.Context) ([]string, error) {
	return func(ctx context.Context) ([]string, error) {
		if pluginRepo == nil {
			return nil, nil
		}
		rows, err := pluginRepo.List(ctx)
		if err != nil {
			return nil, err
		}
		var out []string
		for _, p := range rows {
			if p.Active {
				out = append(out, p.ID)
			}
		}
		return out, nil
	}
}

// askerArgFor avoids assigning a nil *serverask.Asker straight into the interface, which would defeat memory.Gate.Authorize's `Asker == nil` check with a non-nil interface wrapping a nil pointer.
func askerArgFor(asker *serverask.Asker) capability.Asker {
	if asker == nil {
		return nil
	}
	return asker
}

// capabilityAskerFor is askerArgFor for the router's narrower interface, and
// exists for the same reason: the router tests `deps.CapabilityAsker != nil`.
func capabilityAskerFor(asker *serverask.Asker) interface {
	SetOnChange(func())
	Resolve(id, decision string) (serverask.Pending, error)
} {
	if asker == nil {
		return nil
	}
	return asker
}

func toPendingCapabilityDecisions(entries []askgate.Entry[serverask.Pending]) []sdk.PendingCapabilityDecision {
	out := make([]sdk.PendingCapabilityDecision, len(entries))
	for i, e := range entries {
		out[i] = sdk.PendingCapabilityDecision{
			ID:            e.ID,
			Capability:    e.Meta.Capability,
			Value:         e.Meta.Value,
			ValueElided:   e.Meta.ValueElided,
			Context:       e.Meta.Context,
			ContextElided: e.Meta.ContextElided,
			Reason:        e.Meta.Reason,
			RequestedAt:   e.Meta.RequestedAt.UTC().Format(time.RFC3339),
		}
	}
	return out
}
