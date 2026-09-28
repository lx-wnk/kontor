package services_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/lx-wnk/kontor/server/internal/db"
	"github.com/lx-wnk/kontor/server/internal/db/ent"
	"github.com/lx-wnk/kontor/server/internal/db/repo"
	"github.com/lx-wnk/kontor/server/internal/services"
)

// openTestDB opens an in-memory database with the schema migrated.
// Local copy because cross-package test helpers do not work in Go.
func openTestDB(t *testing.T) *ent.Client {
	t.Helper()
	bundle, err := db.Open(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { _ = bundle.Client.Close() })
	return bundle.Client
}

type resolverFixture struct {
	client     *ent.Client
	resolver   services.SpawnerResolver
	tasks      repo.TaskRepo
	projects   repo.ProjectRepo
	spawners   repo.SpawnerRepo
	pcfg       repo.PipelineConfigRepo
	defSpawner *ent.Spawner
	altSpawner *ent.Spawner
}

func setupResolver(t *testing.T) *resolverFixture {
	t.Helper()
	client := openTestDB(t)
	taskRepo := repo.NewTaskRepo(client)
	projectRepo := repo.NewProjectRepo(client)
	spawnerRepo := repo.NewSpawnerRepo(client)
	pcfgRepo := repo.NewPipelineConfigRepo(client)

	def, err := spawnerRepo.Create(t.Context(), "Claude Default", "claude-default", "claude", nil, nil, nil, nil, "claude", nil, true)
	require.NoError(t, err)

	alt, err := spawnerRepo.Create(t.Context(), "Custom 1", "custom-1", "claude", nil, nil, nil, nil, "claude", nil, false)
	require.NoError(t, err)

	return &resolverFixture{
		client:     client,
		resolver:   services.NewSpawnerResolver(taskRepo, projectRepo, spawnerRepo, pcfgRepo),
		tasks:      taskRepo,
		projects:   projectRepo,
		spawners:   spawnerRepo,
		pcfg:       pcfgRepo,
		defSpawner: def,
		altSpawner: alt,
	}
}

func createTaskWith(t *testing.T, f *resolverFixture, slug string, projectID, spawnerID *string) string {
	t.Helper()
	task, err := f.tasks.Create(t.Context(), repo.CreateTaskInput{
		Slug:          slug,
		Title:         "T",
		Cwd:           "/tmp",
		CurrentStage:  "backlog",
		Priority:      "medium",
		MaxIterations: 20,
		ProjectID:     projectID,
		SpawnerID:     spawnerID,
	})
	require.NoError(t, err)
	return task.ID
}

func TestSpawnerResolver_FallsBackToClaudeDefault(t *testing.T) {
	f := setupResolver(t)

	taskID := createTaskWith(t, f, "bare", nil, nil)

	sp, src, err := f.resolver.Resolve(t.Context(), taskID, "")
	require.NoError(t, err)
	require.Equal(t, services.SpawnerSourceDefault, src)
	require.Equal(t, f.defSpawner.ID, sp.ID)
}

func TestSpawnerResolver_ProjectDefault(t *testing.T) {
	f := setupResolver(t)

	proj, err := f.projects.Create(t.Context(), "Proj", "proj", nil, nil, &f.altSpawner.ID, nil)
	require.NoError(t, err)

	taskID := createTaskWith(t, f, "with-project", &proj.ID, nil)

	sp, src, err := f.resolver.Resolve(t.Context(), taskID, "")
	require.NoError(t, err)
	require.Equal(t, services.SpawnerSourceProject, src)
	require.Equal(t, f.altSpawner.ID, sp.ID)
}

func TestSpawnerResolver_TaskWinsOverProject(t *testing.T) {
	f := setupResolver(t)

	// Project sets claude-default as its default; task overrides with alt.
	proj, err := f.projects.Create(t.Context(), "Proj", "proj-override", nil, nil, &f.defSpawner.ID, nil)
	require.NoError(t, err)

	taskID := createTaskWith(t, f, "task-override", &proj.ID, &f.altSpawner.ID)

	sp, src, err := f.resolver.Resolve(t.Context(), taskID, "")
	require.NoError(t, err)
	require.Equal(t, services.SpawnerSourceTask, src)
	require.Equal(t, f.altSpawner.ID, sp.ID)
}

func TestSpawnerResolver_TaskSpawnerMissingErrors(t *testing.T) {
	f := setupResolver(t)

	bogus := "nonexistent-spawner-id"
	taskID := createTaskWith(t, f, "bad-spawner", nil, &bogus)

	_, _, err := f.resolver.Resolve(t.Context(), taskID, "")
	require.Error(t, err, "missing task.spawner_id must surface as error, never silent fallback")
}

func TestSpawnerResolver_MissingClaudeDefaultErrors(t *testing.T) {
	f := setupResolver(t)

	// Delete the claude-default spawner to simulate a deployment bug.
	// Delete() refuses BuiltIn so go through ent directly.
	require.NoError(t, f.client.Spawner.DeleteOneID(f.defSpawner.ID).Exec(t.Context()))

	taskID := createTaskWith(t, f, "no-fallback", nil, nil)

	_, _, err := f.resolver.Resolve(t.Context(), taskID, "")
	require.Error(t, err, "missing claude-default with nothing else set must error (deployment bug)")
}

// --- Precedence tests for stage-aware resolution (new in this PR) ---

// TestSpawnerResolver_TaskSpawnerWinsOverAll proves that an explicit task.spawner_id
// wins even when project-stage, project-default, and global-stage configs are all set.
func TestSpawnerResolver_TaskSpawnerWinsOverAll(t *testing.T) {
	f := setupResolver(t)
	ctx := t.Context()

	// Create a third spawner to be used as project-stage / project-default / global-stage.
	third, err := f.spawners.Create(ctx, "Third", "third", "claude", nil, nil, nil, nil, "claude", nil, false)
	require.NoError(t, err)

	proj, err := f.projects.Create(ctx, "Proj", "proj-task-wins", nil, nil, &third.ID, nil)
	require.NoError(t, err)

	// Set project-stage and global-stage config pointing to third.
	require.NoError(t, f.pcfg.SetScoped(ctx, &proj.ID, "stageSpawner.implementation", third.ID))
	require.NoError(t, f.pcfg.SetScoped(ctx, nil, "stageSpawner.implementation", third.ID))

	// Task explicitly points to alt spawner.
	taskID := createTaskWith(t, f, "task-wins-all", &proj.ID, &f.altSpawner.ID)

	sp, src, err := f.resolver.Resolve(ctx, taskID, "implementation")
	require.NoError(t, err)
	require.Equal(t, services.SpawnerSourceTask, src)
	require.Equal(t, f.altSpawner.ID, sp.ID, "task.spawner_id must beat every other tier")
}

// TestSpawnerResolver_ProjectStageWinsOverProjectDefault proves that a project-scoped
// stageSpawner.<stage> config row beats the project.default_spawner_id.
func TestSpawnerResolver_ProjectStageWinsOverProjectDefault(t *testing.T) {
	f := setupResolver(t)
	ctx := t.Context()

	// Project default points to defSpawner; project-stage config points to altSpawner.
	proj, err := f.projects.Create(ctx, "Proj", "proj-stage-beats-default", nil, nil, &f.defSpawner.ID, nil)
	require.NoError(t, err)

	require.NoError(t, f.pcfg.SetScoped(ctx, &proj.ID, "stageSpawner.implementation", f.altSpawner.ID))

	taskID := createTaskWith(t, f, "proj-stage-wins", &proj.ID, nil)

	sp, src, err := f.resolver.Resolve(ctx, taskID, "implementation")
	require.NoError(t, err)
	require.Equal(t, services.SpawnerSourceProjectStage, src)
	require.Equal(t, f.altSpawner.ID, sp.ID, "project stageSpawner.<stage> must beat project.default_spawner_id")
}

// TestSpawnerResolver_ProjectDefaultWinsOverGlobalStage proves that project.default_spawner_id
// beats a global stageSpawner.<stage> config row.
func TestSpawnerResolver_ProjectDefaultWinsOverGlobalStage(t *testing.T) {
	f := setupResolver(t)
	ctx := t.Context()

	// Global stage config points to altSpawner; project default points to defSpawner.
	require.NoError(t, f.pcfg.SetScoped(ctx, nil, "stageSpawner.implementation", f.altSpawner.ID))

	proj, err := f.projects.Create(ctx, "Proj", "proj-default-beats-global", nil, nil, &f.defSpawner.ID, nil)
	require.NoError(t, err)

	taskID := createTaskWith(t, f, "proj-default-wins", &proj.ID, nil)

	sp, src, err := f.resolver.Resolve(ctx, taskID, "implementation")
	require.NoError(t, err)
	require.Equal(t, services.SpawnerSourceProject, src)
	require.Equal(t, f.defSpawner.ID, sp.ID, "project.default_spawner_id must beat global stageSpawner.<stage>")
}

// TestSpawnerResolver_GlobalStageWinsOverClaudeDefault proves that a global
// stageSpawner.<stage> config row beats the claude-default fallback.
func TestSpawnerResolver_GlobalStageWinsOverClaudeDefault(t *testing.T) {
	f := setupResolver(t)
	ctx := t.Context()

	// No task spawner, no project. Only global-stage config.
	require.NoError(t, f.pcfg.SetScoped(ctx, nil, "stageSpawner.implementation", f.altSpawner.ID))

	taskID := createTaskWith(t, f, "global-stage-wins", nil, nil)

	sp, src, err := f.resolver.Resolve(ctx, taskID, "implementation")
	require.NoError(t, err)
	require.Equal(t, services.SpawnerSourceGlobalStage, src)
	require.Equal(t, f.altSpawner.ID, sp.ID, "global stageSpawner.<stage> must beat the claude-default fallback")
}

// TestSpawnerResolver_NoneSetFallsToDefault proves the bottom of the chain: when
// nothing is set the claude-default spawner is returned.
func TestSpawnerResolver_NoneSetFallsToDefault(t *testing.T) {
	f := setupResolver(t)
	ctx := t.Context()

	taskID := createTaskWith(t, f, "none-set", nil, nil)

	sp, src, err := f.resolver.Resolve(ctx, taskID, "implementation")
	require.NoError(t, err)
	require.Equal(t, services.SpawnerSourceDefault, src)
	require.Equal(t, f.defSpawner.ID, sp.ID, "no overrides → must return claude-default")
}

// TestSpawnerResolver_StageArgEmptySkipsStageSteps proves that passing stage=""
// skips steps 2 and 4 (project-stage and global-stage config lookups).
func TestSpawnerResolver_StageArgEmptySkipsStageSteps(t *testing.T) {
	f := setupResolver(t)
	ctx := t.Context()

	proj, err := f.projects.Create(ctx, "Proj", "proj-no-stage", nil, nil, nil, nil)
	require.NoError(t, err)

	// Set a global-stage config that would fire if stage were non-empty.
	require.NoError(t, f.pcfg.SetScoped(ctx, nil, "stageSpawner.implementation", f.altSpawner.ID))

	taskID := createTaskWith(t, f, "empty-stage", &proj.ID, nil)

	// stage="" → steps 2 and 4 are no-ops → falls through to claude-default.
	sp, src, err := f.resolver.Resolve(ctx, taskID, "")
	require.NoError(t, err)
	require.Equal(t, services.SpawnerSourceDefault, src)
	require.Equal(t, f.defSpawner.ID, sp.ID, "stage='' must skip stageSpawner config and use claude-default")
}
