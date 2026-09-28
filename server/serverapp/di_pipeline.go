package serverapp

import (
	"context"
	"fmt"
	"log/slog"
	"os/exec"
	"time"

	"github.com/lx-wnk/kontor/server/internal/api/tasks"
	"github.com/lx-wnk/kontor/server/internal/config"
	"github.com/lx-wnk/kontor/server/internal/db/ent"
	"github.com/lx-wnk/kontor/server/internal/db/repo"
	"github.com/lx-wnk/kontor/server/internal/mcp"
	"github.com/lx-wnk/kontor/server/internal/mcpapps"
	"github.com/lx-wnk/kontor/server/internal/memory"
	"github.com/lx-wnk/kontor/server/internal/pipeline"
	"github.com/lx-wnk/kontor/server/internal/services"
	"github.com/lx-wnk/kontor/server/internal/settings"
	"github.com/lx-wnk/kontor/server/internal/sse"
)

// resolveAdditionalDirs returns the extra project-folder paths that should be
// forwarded to the spawned stage agent as --add-dir flags. It excludes the
// task's own cwd (already the working directory) so only genuinely additional
// folders are passed through.
func resolveAdditionalDirs(folderRepo repo.ProjectFolderRepo) func(ctx context.Context, task *ent.Task) []string {
	return func(ctx context.Context, task *ent.Task) []string {
		if task.ProjectID == nil || *task.ProjectID == "" {
			return nil
		}
		folders, err := folderRepo.ListByProject(ctx, *task.ProjectID)
		if err != nil {
			slog.Warn("resolveAdditionalDirs: ListByProject failed", "projectID", *task.ProjectID, "err", err)
			return nil
		}
		return services.AdditionalDirsForProject(folders, task.Cwd)
	}
}

// makeSetupWorktreeFn returns a SetupWorktreeFn that runs the project's
// setup_command once in a freshly created worktree. It is a no-op when the task
// has no project or the project defines no setup_command.
func makeSetupWorktreeFn(projectRepo repo.ProjectRepo) func(ctx context.Context, projectID *string, worktreePath string) error {
	return func(ctx context.Context, projectID *string, worktreePath string) error {
		if projectID == nil {
			return nil
		}
		proj, err := projectRepo.GetByID(ctx, *projectID)
		if err != nil {
			slog.Warn("setup worktree: project lookup failed", "projectID", *projectID, "err", err)
			return nil
		}
		if proj.SetupCommand == nil || *proj.SetupCommand == "" {
			return nil
		}
		return runSetupCommand(ctx, worktreePath, *proj.SetupCommand)
	}
}

// runSetupCommand executes cmd in dir with a 5-minute timeout, logging combined output.
func runSetupCommand(ctx context.Context, dir, cmd string) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	c := exec.CommandContext(ctx, "sh", "-c", cmd)
	c.Dir = dir
	out, err := c.CombinedOutput()
	if len(out) > 0 {
		slog.Info("worktree setup_command output", "dir", dir, "output", string(out))
	}
	if err != nil {
		return fmt.Errorf("setup_command %q: %w", cmd, err)
	}
	return nil
}

// memoryPushBudgetChars bounds the memory block injected into a stage's user
// prompt. Chosen well under systemPromptMaxChars (10,000, spawner.go) even
// though the push lands in the user prompt rather than the system prompt: a
// few thousand characters of prior context is enough to be useful without
// crowding out the stage's own instructions or the user's additional prompt,
// both of which share that same prompt.
const memoryPushBudgetChars = 2000

func provideOrchestrator(
	cfg config.Config,
	settingsSvc *settings.Service,
	client *ent.Client,
	tb *sse.TaskBroadcaster,
	systemPromptRepo repo.SystemPromptRepo,
	spawnerResolver services.SpawnerResolver,
	checkpointerStart func(taskID, worktreePath string),
	checkpointerStop func(taskID string),
	memRepo repo.MemoryRepo,
	memRetriever *memory.Retriever,
	grantUsageRepo repo.GrantUsageRepo,
	appSecrets repo.ApplicationSecretRepo,
) (*pipeline.PipelineOrchestrator, error) {
	if client == nil {
		return nil, nil
	}
	taskRepo := repo.NewTaskRepo(client)
	srRepo := repo.NewStageRunRepo(client)
	permRepo := repo.NewPermissionRepo(client)
	auditRepo := repo.NewAuditEventRepo(client)
	cfgRepo := repo.NewPipelineConfigRepo(client)
	folderRepo := repo.NewProjectFolderRepo(client)
	projectRepo := repo.NewProjectRepo(client)
	worktreeManager := services.NewWorktreeManager(taskRepo)
	capabilityRepo := repo.NewCapabilityRepo(client)
	grantRepo := repo.NewGrantRepo(client)
	apiKeyRepo := repo.NewApiKeyRepo(client)
	stageKeyIssuer := mcp.StageKeyIssuer{Keys: apiKeyRepo}

	var resolveFn pipeline.SpawnerResolverFunc
	if spawnerResolver != nil {
		resolveFn = func(ctx context.Context, taskID, stage string) (*ent.Spawner, error) {
			sp, _, err := spawnerResolver.Resolve(ctx, taskID, stage)
			return sp, err
		}
	}

	orch, err := pipeline.NewOrchestrator(pipeline.OrchestratorOptions{
		Client:           client,
		TaskRepo:         taskRepo,
		StageRunRepo:     srRepo,
		PermissionRepo:   permRepo,
		AuditRepo:        auditRepo,
		ConfigRepo:       cfgRepo,
		SystemPromptRepo: systemPromptRepo,
		DepRepo:          repo.NewDependencyRepo(client),
		MCPToken:         cfg.MCPToken,
		MCPUrl:           fmt.Sprintf("http://127.0.0.1:%d", cfg.Port),
		WorktreeRoot:     cfg.WorktreeRoot,
		ForceWorktrees:   settingsSvc.Bool("worktree.force"),
		AllowGitPush:     settingsSvc.Bool("git.allowPush"),
		SpawnFn:          pipeline.SpawnStageAgent,
		EnsureWorktreeFn: pipeline.EnsureTaskWorktree,
		RemoveWorktreeFn: func(ctx context.Context, task *ent.Task, force bool) error {
			return worktreeManager.RemoveWorktree(ctx, task.ID, force)
		},
		SetupWorktreeFn:     makeSetupWorktreeFn(projectRepo),
		CheckpointerStartFn: checkpointerStart,
		CheckpointerStopFn:  checkpointerStop,
		HasUnpushedWorkFn: func(ctx context.Context, task *ent.Task) bool {
			return worktreeManager.HasUnpushedWork(ctx, task)
		},
		PushFn:                pipeline.ProductionPushFn,
		CreateDraftPRFn:       pipeline.ProductionCreateDraftPRFn,
		ResolveSpawner:        resolveFn,
		ResolveAdditionalDirs: resolveAdditionalDirs(folderRepo),
		// InjectMemory is a bound method value, not a function reference: it
		// closes over memRetriever the same way ResolveAdditionalDirs above
		// closes over folderRepo. memRepo.RecordInjection matches
		// RecordMemoryInjection's signature directly, no adapter needed.
		InjectMemory:          memRetriever.Retrieve,
		MemoryBudget:          memoryPushBudgetChars,
		RecordMemoryInjection: memRepo.RecordInjection,
		// AuthorizeMemory gates the push behind the same memory.Gate check
		// every human-facing memory route already uses (mcp/tools/memory.go,
		// api/memory/handler.go), but with no Asker: nobody is watching a
		// stage spawn, so an ask decision here must deny rather than stall
		// the pipeline. The push reads across the whole scope rather than
		// one space, so it authorizes with the empty wildcard value, same as
		// memory_search.
		AuthorizeMemory: func(ctx context.Context, scope repo.Scope, routineID string) error {
			gate := memory.Gate{Capabilities: capabilityRepo, Grants: grantRepo, GrantUsage: grantUsageRepo}
			return gate.Authorize(ctx, repo.CapabilityMemoryRead, "", scope, memory.RoutineContext(routineID)...)
		},
		IssueTaskAPIKey: func(ctx context.Context, stageRunID string, stageTimeout time.Duration) (string, error) {
			return stageKeyIssuer.Issue(ctx, stageRunID, stageTimeout)
		},
		RevokeTaskAPIKeys: func(ctx context.Context, stageRunID string) error {
			return stageKeyIssuer.Revoke(ctx, stageRunID)
		},
		ResolveApplications: mcpapps.Resolver{
			Apps:         repo.NewMCPApplicationRepo(client),
			Secrets:      appSecrets,
			Grants:       grantRepo,
			Capabilities: capabilityRepo,
		}.ResolveRun,
		// BuildTaskPayload is called inside applyTransitionWrites, bound to the
		// active transaction, so the returned snapshot reflects the just-applied
		// writes before tx.Commit(). The result is forwarded to OnTaskChanged
		// after the commit succeeds.
		BuildTaskPayload: func(ctx context.Context, taskID string, txSRRepo repo.StageRunRepo, txPermRepo repo.PermissionRepo) any {
			t, err := taskRepo.GetByID(ctx, taskID)
			if err != nil {
				slog.Warn("BuildTaskPayload: task lookup failed", "taskID", taskID, "err", err)
				return nil
			}
			enriched, err := tasks.EnrichTaskWithDeps(ctx, t, txSRRepo, txPermRepo, nil, repo.NewDependencyRepo(client), taskRepo)
			if err != nil {
				slog.Warn("BuildTaskPayload: enrich failed", "taskID", taskID, "err", err)
				return nil
			}
			return enriched
		},
		OnTaskChanged: func(taskID string, _ string, payload any) {
			// Broadcast stage_run_updated with the enriched task so the client can
			// apply the update directly without a round-trip refetch (F-PERF-013).
			//
			// payload is a pre-built *tasks.EnrichedTask read inside the transaction
			// (consistent, post-write state). When nil — e.g. for out-of-tx callers
			// like tryAttachSessionID — fall back to a live read.
			var enriched *tasks.EnrichedTask
			if payload != nil {
				if e, ok := payload.(*tasks.EnrichedTask); ok {
					enriched = e
				}
			}
			if enriched == nil {
				ctx := context.Background()
				t, err := taskRepo.GetByID(ctx, taskID)
				if err != nil {
					slog.Warn("OnTaskChanged: task lookup failed", "taskID", taskID, "err", err)
					return
				}
				var ferr error
				enriched, ferr = tasks.EnrichTask(ctx, t, srRepo, permRepo, nil)
				if ferr != nil {
					slog.Warn("OnTaskChanged: enrich failed", "taskID", taskID, "err", ferr)
					return
				}
			}
			tb.Broadcast(sse.TaskEvent{
				Type:    "stage_run_updated",
				TaskID:  taskID,
				Payload: enriched,
			})
		},
	})
	return orch, err
}
