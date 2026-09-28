package pipeline_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/lx-wnk/kontor/server/internal/db/repo"
	"github.com/lx-wnk/kontor/server/internal/pipeline"
)

func TestSpawn_StageKeyTTLUsesGlobalConfig(t *testing.T) {
	bundle := openBundle(t)
	c := bundle.Client
	cfgRepo := repo.NewPipelineConfigRepo(c)
	ctx := context.Background()
	require.NoError(t, cfgRepo.Set(ctx, "stageTimeoutSeconds", "3600"))

	var gotTimeout time.Duration
	orch, err := pipeline.NewOrchestrator(pipeline.OrchestratorOptions{
		TaskRepo:       repo.NewTaskRepo(c),
		StageRunRepo:   repo.NewStageRunRepo(c),
		PermissionRepo: repo.NewPermissionRepo(c),
		AuditRepo:      repo.NewAuditEventRepo(c),
		ConfigRepo:     cfgRepo,
		IssueTaskAPIKey: func(_ context.Context, _ string, timeout time.Duration) (string, error) {
			gotTimeout = timeout
			return "tok", nil
		},
	})
	require.NoError(t, err)

	task, err := repo.NewTaskRepo(c).Create(ctx, repo.CreateTaskInput{
		Slug:          "mcp-created-task",
		Title:         "MCP-created task",
		Cwd:           "/tmp",
		CurrentStage:  "implementation",
		Priority:      "medium",
		MaxIterations: 3,
	})
	require.NoError(t, err)

	var capturedOpts pipeline.SpawnAgentOptions
	spawnFn := modelCaptureSpawnFn(&capturedOpts)
	handler := pipeline.NewAgentStageHandlerForTest("implementation", spawnFn)
	orch.SetHandlerOverride("implementation", handler)

	_, err = orch.ProgressTask(ctx, task.ID, nil)
	require.NoError(t, err)

	require.Equal(t, "tok", capturedOpts.TaskAPIToken)
	require.Equal(t, 3600*time.Second, gotTimeout,
		"the stage-run key TTL must use the global stageTimeoutSeconds config")
}
