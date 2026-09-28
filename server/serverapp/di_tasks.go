package serverapp

import (
	"database/sql"

	"github.com/lx-wnk/kontor/server/internal/api/tasks"
	"github.com/lx-wnk/kontor/server/internal/checkpoint"
	"github.com/lx-wnk/kontor/server/internal/db/ent"
	"github.com/lx-wnk/kontor/server/internal/db/rawrepo"
	"github.com/lx-wnk/kontor/server/internal/db/repo"
	"github.com/lx-wnk/kontor/server/internal/pipeline"
	"github.com/lx-wnk/kontor/server/internal/services"
	"github.com/lx-wnk/kontor/server/internal/sse"
)

func provideTaskHandler(client *ent.Client, db *sql.DB, orch *pipeline.PipelineOrchestrator, tb *sse.TaskBroadcaster, refineReader tasks.RefineStatusReader, allowGitPull, bypassAuth bool, checkpointSvc *checkpoint.Service, notifier tasks.PermissionNotifier) *tasks.Handler {
	if client == nil || orch == nil {
		return nil
	}
	taskRepo := repo.NewTaskRepo(client)
	// Nil-interface guard: a typed-nil *checkpoint.Service would make the Mount
	// guard see a non-nil interface and mount routes that panic. Keep it true-nil.
	var cpIface tasks.CheckpointServiceIface
	if checkpointSvc != nil {
		cpIface = checkpointSvc
	}
	return tasks.NewHandler(tasks.Deps{
		Client:            client,
		TaskRepo:          taskRepo,
		SRRepo:            repo.NewStageRunRepo(client),
		SRBulkRepo:        rawrepo.NewStageRunBulkRepo(db),
		PermRepo:          repo.NewPermissionRepo(client),
		PresetRepo:        repo.NewPermissionPresetRepo(client),
		AuditRepo:         repo.NewAuditEventRepo(client),
		CfgRepo:           repo.NewPipelineConfigRepo(client),
		DepRepo:           repo.NewDependencyRepo(client),
		ProjectRepo:       repo.NewProjectRepo(client),
		ProjectFolderRepo: repo.NewProjectFolderRepo(client),
		SpawnerRepo:       repo.NewSpawnerRepo(client),
		Orchestrator:      orch,
		Broadcaster:       tb,
		WorktreeMgr:       services.NewWorktreeManager(taskRepo),
		RefineReader:      refineReader,
		CheckpointSvc:     cpIface,
		Notifier:          notifier,
		GrantRepo:         repo.NewGrantRepo(client),
		CapabilityRepo:    repo.NewCapabilityRepo(client),
		AllowGitPull:      allowGitPull,
		BypassAuth:        bypassAuth,
	})
}
