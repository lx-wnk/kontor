// Package services contains stateless service-layer helpers that compose
// repos and provide higher-level operations consumed by the pipeline and API
// layers. Services depend only on repos and ent types — never on routes,
// notifications, or the orchestrator itself.
package services

import (
	"context"
	"fmt"

	"github.com/lx-wnk/kontor/server/internal/db/ent"
	"github.com/lx-wnk/kontor/server/internal/db/repo"
)

// SpawnerSource indicates which level of the resolution chain produced the
// effective spawner for a task. Useful for audit logs and debugging.
type SpawnerSource string

const (
	// SpawnerSourceTask means the task itself declared spawner_id.
	SpawnerSourceTask SpawnerSource = "task"
	// SpawnerSourceProjectStage means the project's stageSpawner.<stage> config row won.
	SpawnerSourceProjectStage SpawnerSource = "project_stage"
	// SpawnerSourceProject means the task's project default_spawner_id won.
	SpawnerSourceProject SpawnerSource = "project"
	// SpawnerSourceGlobalStage means the global stageSpawner.<stage> config row won.
	SpawnerSourceGlobalStage SpawnerSource = "global_stage"
	// SpawnerSourceDefault means the chain fell through to claude-default.
	SpawnerSourceDefault SpawnerSource = "default"
)

// stageSpawnerKeyPrefix is the pipeline_config key prefix for per-stage spawner config.
// Must match the constant defined in pipeline/orchestrator.go.
const stageSpawnerKeyPrefix = "stageSpawner."

// claudeDefaultSpawnerSlug is the deployment-required fallback spawner slug.
// Must match the seed in cmd/serve/di_seed.go.
const claudeDefaultSpawnerSlug = "claude-default"

// SpawnerResolver resolves the effective spawner for a task. Resolution order:
//
//  1. task.spawner_id                           → SpawnerSourceTask
//  2. project stageSpawner.<stage> config row   → SpawnerSourceProjectStage
//  3. project.default_spawner_id                → SpawnerSourceProject
//  4. global stageSpawner.<stage> config row    → SpawnerSourceGlobalStage
//  5. is_default spawner                        → SpawnerSourceDefault
//     (falls back to the claude-default slug if no row is flagged default)
//
// Explicit references that fail to load are surfaced as errors — the resolver
// never silently falls back to a lower tier when a higher one named a spawner
// that turned out to be missing or unreadable.
//
// stage may be "" for non-agent stages; steps 2 and 4 no-op in that case.
type SpawnerResolver interface {
	Resolve(ctx context.Context, taskID, stage string) (*ent.Spawner, SpawnerSource, error)
}

type spawnerResolver struct {
	tasks    repo.TaskRepo
	projects repo.ProjectRepo
	spawners repo.SpawnerRepo
	pcfg     repo.PipelineConfigRepo
}

// NewSpawnerResolver constructs a SpawnerResolver from the repos it needs.
func NewSpawnerResolver(tasks repo.TaskRepo, projects repo.ProjectRepo, spawners repo.SpawnerRepo, pcfg repo.PipelineConfigRepo) SpawnerResolver {
	return &spawnerResolver{tasks: tasks, projects: projects, spawners: spawners, pcfg: pcfg}
}

func (r *spawnerResolver) Resolve(ctx context.Context, taskID, stage string) (*ent.Spawner, SpawnerSource, error) {
	if r.tasks == nil || r.spawners == nil {
		return nil, "", fmt.Errorf("spawner_resolver: repos not configured")
	}

	task, err := r.tasks.GetByID(ctx, taskID)
	if err != nil {
		return nil, "", fmt.Errorf("spawner_resolver: load task %s: %w", taskID, err)
	}

	// (1) task-level spawner reference
	if task.SpawnerID != nil && *task.SpawnerID != "" {
		sp, err := r.spawners.GetByID(ctx, *task.SpawnerID)
		if err != nil {
			return nil, "", fmt.Errorf("spawner_resolver: load task spawner %s: %w", *task.SpawnerID, err)
		}
		return sp, SpawnerSourceTask, nil
	}

	hasProject := task.ProjectID != nil && *task.ProjectID != ""

	// (2) project-stage config: stageSpawner.<stage> scoped to this project.
	if stage != "" && hasProject && r.pcfg != nil {
		if id := r.pcfg.GetStringForScope(ctx, task.ProjectID, stageSpawnerKeyPrefix+stage, ""); id != "" {
			sp, err := r.spawners.GetByID(ctx, id)
			if err != nil {
				return nil, "", fmt.Errorf("spawner_resolver: load project-stage spawner %s (stage %s): %w", id, stage, err)
			}
			return sp, SpawnerSourceProjectStage, nil
		}
	}

	// (3) project-level default spawner reference
	if hasProject && r.projects != nil {
		proj, err := r.projects.GetByID(ctx, *task.ProjectID)
		if err != nil {
			return nil, "", fmt.Errorf("spawner_resolver: load project %s: %w", *task.ProjectID, err)
		}
		if proj.DefaultSpawnerID != nil && *proj.DefaultSpawnerID != "" {
			sp, err := r.spawners.GetByID(ctx, *proj.DefaultSpawnerID)
			if err != nil {
				return nil, "", fmt.Errorf("spawner_resolver: load project default spawner %s: %w", *proj.DefaultSpawnerID, err)
			}
			return sp, SpawnerSourceProject, nil
		}
	}

	// (4) global-stage config: stageSpawner.<stage> in global scope.
	if stage != "" && r.pcfg != nil {
		if id := r.pcfg.GetStringForScope(ctx, nil, stageSpawnerKeyPrefix+stage, ""); id != "" {
			sp, err := r.spawners.GetByID(ctx, id)
			if err != nil {
				return nil, "", fmt.Errorf("spawner_resolver: load global-stage spawner %s (stage %s): %w", id, stage, err)
			}
			return sp, SpawnerSourceGlobalStage, nil
		}
	}

	// (5) deployment-wide default: the is_default row, with the seeded
	// claude-default slug as the ultimate backstop if none is flagged.
	if sp, err := r.spawners.GetDefault(ctx); err == nil {
		return sp, SpawnerSourceDefault, nil
	}
	sp, err := r.spawners.GetBySlug(ctx, claudeDefaultSpawnerSlug)
	if err != nil {
		return nil, "", fmt.Errorf("spawner_resolver: load %s fallback: %w", claudeDefaultSpawnerSlug, err)
	}
	return sp, SpawnerSourceDefault, nil
}
