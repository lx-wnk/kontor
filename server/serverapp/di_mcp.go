package serverapp

import (
	"context"
	"net/http"

	"github.com/lx-wnk/kontor/server/internal/apps/github"
	"github.com/lx-wnk/kontor/server/internal/apps/obsidian"
	"github.com/lx-wnk/kontor/server/internal/capability"
	"github.com/lx-wnk/kontor/server/internal/db/ent"
	"github.com/lx-wnk/kontor/server/internal/db/repo"
	mcp "github.com/lx-wnk/kontor/server/internal/mcp"
	mcptools "github.com/lx-wnk/kontor/server/internal/mcp/tools"
	"github.com/lx-wnk/kontor/server/internal/memory"
	"github.com/lx-wnk/kontor/server/internal/pipeline"
	"github.com/lx-wnk/kontor/server/internal/refine"
	"github.com/lx-wnk/kontor/server/internal/scheduler"
	"github.com/lx-wnk/kontor/server/internal/sse"
)

func provideMCPHandler(
	client *ent.Client,
	orch *pipeline.PipelineOrchestrator,
	sched *scheduler.Scheduler,
	tb *sse.TaskBroadcaster,
	pb *sse.ProjectBroadcaster,
	refineRunner *refine.Runner,
	memRepo repo.MemoryRepo,
	memRetriever *memory.Retriever,
	grantUsageRepo repo.GrantUsageRepo,
	memAsker capability.Asker,
	obsidianClient *obsidian.Client,
	githubClient *github.Client,
) http.Handler {
	if client == nil || orch == nil {
		return nil
	}

	taskRepo := repo.NewTaskRepo(client)
	srRepo := repo.NewStageRunRepo(client)
	permRepo := repo.NewPermissionRepo(client)
	auditRepo := repo.NewAuditEventRepo(client)
	depRepo := repo.NewDependencyRepo(client)
	apiKeyRepo := repo.NewApiKeyRepo(client)
	projectRepo := repo.NewProjectRepo(client)
	spawnerRepo := repo.NewSpawnerRepo(client)
	scratchRepo := repo.NewScratchpadRepo(client)
	lockRepo := repo.NewCoordLockRepo(client)
	turnsRepo := repo.NewRefinementTurnRepo(client)

	caller := mcp.CallerResolver{StageRuns: srRepo, Tasks: taskRepo}

	broadcast := func(taskID string) {
		tb.Broadcast(sse.TaskEvent{Type: "task_changed", TaskID: taskID, Payload: map[string]string{}})
	}
	broadcastDeleted := func(taskID string) {
		tb.Broadcast(sse.TaskEvent{Type: "task_deleted", TaskID: taskID, Payload: map[string]string{}})
	}

	registry := mcp.ToolRegistry{}
	mcptools.RegisterReadTools(registry, mcptools.ReadDeps{
		TaskRepo:    taskRepo,
		SRRepo:      srRepo,
		PermRepo:    permRepo,
		AuditRepo:   auditRepo,
		ProjectRepo: projectRepo,
		SpawnerRepo: spawnerRepo,
		Caller:      caller,
	})
	mcptools.RegisterWriteTools(registry, mcptools.WriteDeps{
		TaskRepo:         taskRepo,
		PermRepo:         permRepo,
		AuditRepo:        auditRepo,
		DepRepo:          depRepo,
		ProjectRepo:      projectRepo,
		SpawnerRepo:      spawnerRepo,
		Broadcast:        broadcast,
		BroadcastDeleted: broadcastDeleted,
		// Same broadcaster the projects HTTP handler writes to, so an
		// agent-created project reaches the SPA without a reload.
		ProjectBroadcaster: pb,
	})
	mcptools.RegisterControlTools(registry, mcptools.ControlDeps{
		TaskRepo:     taskRepo,
		SRRepo:       srRepo,
		PermRepo:     permRepo,
		AuditRepo:    auditRepo,
		Orchestrator: orch,
		RefineReader: refineRunner,
		Broadcast:    broadcast,
	})
	mcptools.RegisterKeyTools(registry, mcptools.KeyDeps{
		ApiKeyRepo: apiKeyRepo,
	})
	mcptools.RegisterRefineTools(registry, mcptools.RefineDeps{
		Turns:     turnsRepo,
		Tasks:     taskRepo,
		StageRuns: srRepo,
		Runner:    refineRunner,
		Advance: func(ctx context.Context, taskID string) error {
			_, err := orch.ProgressTask(ctx, taskID, nil)
			return err
		},
		Revoke: mcp.StageKeyIssuer{Keys: apiKeyRepo}.Revoke,
	})
	mcptools.RegisterPlanTools(registry, mcptools.PlanDeps{
		Turns:     turnsRepo,
		Tasks:     taskRepo,
		StageRuns: srRepo,
		Advance: func(ctx context.Context, taskID string) error {
			_, err := orch.ProgressTask(ctx, taskID, nil)
			return err
		},
		Requeue: func(ctx context.Context, taskID, prompt string) error {
			_, err := orch.RequeueForUser(ctx, taskID, prompt)
			return err
		},
		Revoke: mcp.StageKeyIssuer{Keys: apiKeyRepo}.Revoke,
	})
	mcptools.RegisterScheduleTools(registry, mcptools.ScheduleDeps{
		Repo:       repo.NewTaskScheduleRepo(client),
		Translator: scheduler.NewNLCron(nil),
		Runner:     sched,
		Broadcast:  broadcast,
	})
	mcptools.RegisterCoordTools(registry, mcptools.CoordDeps{Scratch: scratchRepo, Locks: lockRepo})
	mcptools.RegisterMemoryTools(registry, mcptools.MemoryDeps{
		Repo:      memRepo,
		Retriever: memRetriever,
		Caller:    caller,
		Gate: memory.Gate{
			Capabilities: repo.NewCapabilityRepo(client),
			Grants:       repo.NewGrantRepo(client),
			GrantUsage:   grantUsageRepo,
			Asker:        memAsker,
		},
	})
	// Shares memAsker with the memory tools above, not the obsidian trigger
	// handler's Gate (built with no Asker in di.go): an MCP tool call has an
	// agent genuinely waiting on the response, the same reasoning that
	// applies to memory_write/memory_search, so an ask decision here may
	// legitimately hold rather than having to fail closed. RegisterObsidianTools
	// itself skips registering any tool when obsidianClient is nil (vault
	// unconfigured).
	mcptools.RegisterObsidianTools(registry, mcptools.ObsidianDeps{
		Client: obsidianClient,
		Caller: caller,
		Gate: memory.Gate{
			Capabilities: repo.NewCapabilityRepo(client),
			Grants:       repo.NewGrantRepo(client),
			GrantUsage:   grantUsageRepo,
			Asker:        memAsker,
		},
	})
	// Same Asker as the memory and Obsidian tools: an agent is waiting on the
	// tool response either way. RegisterGitHubTools itself skips registering
	// anything when githubClient is nil.
	mcptools.RegisterGitHubTools(registry, mcptools.GitHubDeps{
		Client: githubClient,
		Gate: memory.Gate{
			Capabilities: repo.NewCapabilityRepo(client),
			Grants:       repo.NewGrantRepo(client),
			GrantUsage:   grantUsageRepo,
			Asker:        memAsker,
		},
	})
	return mcp.MCPHandler(registry)
}
