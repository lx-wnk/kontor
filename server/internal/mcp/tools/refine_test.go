package tools

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/lx-wnk/kontor/server/internal/db"
	"github.com/lx-wnk/kontor/server/internal/db/ent"
	"github.com/lx-wnk/kontor/server/internal/db/repo"
	mcp "github.com/lx-wnk/kontor/server/internal/mcp"
	"github.com/lx-wnk/kontor/server/internal/refine"
	"github.com/stretchr/testify/require"
)

// seedBacklogTask creates a task in the backlog stage with a backlog stage run.
func seedBacklogTask(t *testing.T, ctx context.Context, taskRepo repo.TaskRepo, srRepo repo.StageRunRepo) *ent.Task {
	t.Helper()
	task, err := taskRepo.Create(ctx, repo.CreateTaskInput{
		Slug:          "refine-mcp-test-" + t.Name(),
		Title:         "Refine MCP Test",
		Cwd:           t.TempDir(),
		MaxIterations: 3,
		Priority:      "normal",
		CurrentStage:  "backlog",
	})
	require.NoError(t, err)

	run, err := srRepo.Create(ctx, repo.CreateStageRunInput{
		TaskID:    task.ID,
		Stage:     "backlog",
		Iteration: 0,
	})
	require.NoError(t, err)
	running := "running"
	_, err = srRepo.Update(ctx, run.ID, repo.UpdateStageRunInput{Status: &running})
	require.NoError(t, err)

	return task
}

func TestRegisterRefineTools_AllToolsPresent(t *testing.T) {
	registry := mcp.ToolRegistry{}
	RegisterRefineTools(registry, RefineDeps{})
	for _, name := range []string{"get_refine_status", "approve_spec", "refine_task"} {
		require.Contains(t, registry, name, "expected tool %q to be registered", name)
	}
}

func TestGetRefineStatus_IdleWhenNoRun(t *testing.T) {
	bundle, err := db.Open(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { _ = bundle.Client.Close() })

	turnsRepo := repo.NewRefinementTurnRepo(bundle.Client)
	runner := refine.NewRunner(turnsRepo, nil)

	registry := mcp.ToolRegistry{}
	RegisterRefineTools(registry, RefineDeps{Runner: runner})

	tool := registry["get_refine_status"]
	require.NotNil(t, tool)

	result, err := tool.Handler(context.Background(), map[string]any{"task_id": "no-such-task"})
	require.NoError(t, err)

	var payload map[string]any
	require.NoError(t, json.Unmarshal([]byte(result.Content[0].Text), &payload))
	require.Equal(t, "none", payload["status"])
}

func TestApproveSpec_AdvancesTaskPastBacklog(t *testing.T) {
	bundle, err := db.Open(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { _ = bundle.Client.Close() })

	ctx := context.Background()
	taskRepo := repo.NewTaskRepo(bundle.Client)
	srRepo := repo.NewStageRunRepo(bundle.Client)
	turnsRepo := repo.NewRefinementTurnRepo(bundle.Client)

	task := seedBacklogTask(t, ctx, taskRepo, srRepo)

	// Record that Advance was called.
	advanced := false
	registry := mcp.ToolRegistry{}
	RegisterRefineTools(registry, RefineDeps{
		Turns:     turnsRepo,
		Tasks:     taskRepo,
		StageRuns: srRepo,
		Advance: func(_ context.Context, taskID string) error {
			if taskID == task.ID {
				advanced = true
			}
			return nil
		},
	})

	tool := registry["approve_spec"]
	require.NotNil(t, tool)

	result, err := tool.Handler(ctx, map[string]any{"task_id": task.ID})
	require.NoError(t, err)
	require.NotEmpty(t, result.Content)

	var payload map[string]any
	require.NoError(t, json.Unmarshal([]byte(result.Content[0].Text), &payload))
	require.NotNil(t, payload["task"], "expected task in response")

	// Confirm sentinel turn was persisted.
	turns, err := turnsRepo.ListForTask(ctx, task.ID, 0)
	require.NoError(t, err)
	var found bool
	for _, tr := range turns {
		if tr.Phase != nil && *tr.Phase == "confirmed" {
			found = true
		}
	}
	require.True(t, found, "expected a confirmed sentinel turn")
	require.True(t, advanced, "expected Advance to be called")

	// Backlog stage run should be marked done.
	sr, err := srRepo.GetLatestByTaskAndStage(ctx, task.ID, "backlog")
	require.NoError(t, err)
	require.NotNil(t, sr)
	require.Equal(t, "done", sr.Status)
}

func TestRefineTask_StoresUserTurnAndRunsRefinement(t *testing.T) {
	bundle, err := db.Open(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { _ = bundle.Client.Close() })

	ctx := context.Background()
	taskRepo := repo.NewTaskRepo(bundle.Client)
	srRepo := repo.NewStageRunRepo(bundle.Client)
	turnsRepo := repo.NewRefinementTurnRepo(bundle.Client)

	task := seedBacklogTask(t, ctx, taskRepo, srRepo)

	// Stub spawner that returns a canned assistant line.
	stubSpawn := func(_ context.Context, _ refine.SpawnConfig, _ *ent.Spawner) (<-chan string, error) {
		ch := make(chan string, 1)
		ch <- "assistant reply"
		close(ch)
		return ch, nil
	}
	runner := refine.NewRunner(turnsRepo, stubSpawn)

	registry := mcp.ToolRegistry{}
	RegisterRefineTools(registry, RefineDeps{
		Turns:  turnsRepo,
		Tasks:  taskRepo,
		Runner: runner,
	})

	tool := registry["refine_task"]
	require.NotNil(t, tool)

	result, err := tool.Handler(ctx, map[string]any{
		"task_id": task.ID,
		"message": "please clarify the scope",
	})
	require.NoError(t, err)
	require.NotEmpty(t, result.Content)

	var payload map[string]any
	require.NoError(t, json.Unmarshal([]byte(result.Content[0].Text), &payload))
	require.Equal(t, "draft_ready", payload["status"])
	require.Contains(t, payload["response"], "assistant reply")

	// Both user and assistant turns should be persisted.
	turns, err := turnsRepo.ListForTask(ctx, task.ID, 0)
	require.NoError(t, err)
	var userCount, assistantCount int
	for _, tr := range turns {
		switch string(tr.Role) {
		case "user":
			userCount++
		case "assistant":
			assistantCount++
		}
	}
	require.GreaterOrEqual(t, userCount, 1, "expected at least one user turn")
	require.GreaterOrEqual(t, assistantCount, 1, "expected at least one assistant turn")
}

func TestInjectConcept_DraftReadyThenApproveSpecPersistsMetadata(t *testing.T) {
	bundle, err := db.Open(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { _ = bundle.Client.Close() })

	ctx := context.Background()
	taskRepo := repo.NewTaskRepo(bundle.Client)
	srRepo := repo.NewStageRunRepo(bundle.Client)
	turnsRepo := repo.NewRefinementTurnRepo(bundle.Client)
	runner := refine.NewRunner(turnsRepo, nil)

	task := seedBacklogTask(t, ctx, taskRepo, srRepo)

	registry := mcp.ToolRegistry{}
	RegisterRefineTools(registry, RefineDeps{
		Turns:     turnsRepo,
		Tasks:     taskRepo,
		StageRuns: srRepo,
		Runner:    runner,
		Advance:   func(_ context.Context, _ string) error { return nil },
	})

	inject := registry["inject_concept"]
	require.NotNil(t, inject)
	res, err := inject.Handler(ctx, map[string]any{
		"task_id": task.ID,
		"concept": map[string]any{
			"spec":         "Add a foo endpoint",
			"plan":         "1. handler 2. test",
			"refinedTitle": "Foo endpoint",
		},
	})
	require.NoError(t, err)
	var injectResp map[string]any
	require.NoError(t, json.Unmarshal([]byte(res.Content[0].Text), &injectResp))
	require.Equal(t, "draft_ready", injectResp["status"])
	status, _ := runner.State(task.ID)
	require.Equal(t, "draft_ready", status)

	approve := registry["approve_spec"]
	_, err = approve.Handler(ctx, map[string]any{"task_id": task.ID})
	require.NoError(t, err)

	updated, err := taskRepo.GetByID(ctx, task.ID)
	require.NoError(t, err)
	require.Equal(t, "Add a foo endpoint", updated.Metadata["spec"], "injected spec must reach task metadata, not empty {}")
}
