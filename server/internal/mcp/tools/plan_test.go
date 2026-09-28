package tools

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/lx-wnk/kontor/server/internal/db"
	"github.com/lx-wnk/kontor/server/internal/db/repo"
	mcp "github.com/lx-wnk/kontor/server/internal/mcp"
	"github.com/stretchr/testify/require"
)

// seedPlanReviewTaskMCP creates a task in plan_review with an awaiting_user stage run.
func seedPlanReviewTaskMCP(t *testing.T, ctx context.Context, taskRepo repo.TaskRepo, srRepo repo.StageRunRepo) string {
	t.Helper()
	task, err := taskRepo.Create(ctx, repo.CreateTaskInput{
		Slug:          "plan-mcp-test-" + t.Name(),
		Title:         "Plan MCP Test",
		Cwd:           t.TempDir(),
		MaxIterations: 3,
		Priority:      "normal",
		CurrentStage:  "plan_review",
	})
	require.NoError(t, err)

	run, err := srRepo.Create(ctx, repo.CreateStageRunInput{
		TaskID:    task.ID,
		Stage:     "plan_review",
		Iteration: 0,
	})
	require.NoError(t, err)
	status := "awaiting_user"
	_, err = srRepo.Update(ctx, run.ID, repo.UpdateStageRunInput{Status: &status})
	require.NoError(t, err)

	return task.ID
}

func TestRegisterPlanTools_AllToolsPresent(t *testing.T) {
	registry := mcp.ToolRegistry{}
	RegisterPlanTools(registry, PlanDeps{})
	for _, name := range []string{"approve_plan", "reject_plan", "get_plan_status"} {
		require.Contains(t, registry, name, "expected tool %q to be registered", name)
	}
}

func TestToolScopeMap_PlanToolsHavePipelineControlScope(t *testing.T) {
	for _, tool := range []string{"approve_plan", "reject_plan", "get_plan_status"} {
		scope, ok := mcp.ToolScopeMap[tool]
		require.True(t, ok, "ToolScopeMap must contain %q", tool)
		require.Equal(t, "pipeline:control", scope, "tool %q must map to pipeline:control", tool)
	}
}

func TestApprovePlan_MCPTool_AdvancesTask(t *testing.T) {
	bundle, err := db.Open(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { _ = bundle.Client.Close() })

	ctx := context.Background()
	taskRepo := repo.NewTaskRepo(bundle.Client)
	srRepo := repo.NewStageRunRepo(bundle.Client)
	turnsRepo := repo.NewRefinementTurnRepo(bundle.Client)

	taskID := seedPlanReviewTaskMCP(t, ctx, taskRepo, srRepo)

	advanced := false
	registry := mcp.ToolRegistry{}
	RegisterPlanTools(registry, PlanDeps{
		Turns:     turnsRepo,
		Tasks:     taskRepo,
		StageRuns: srRepo,
		Advance: func(_ context.Context, id string) error {
			if id == taskID {
				advanced = true
			}
			return nil
		},
	})

	tool := registry["approve_plan"]
	require.NotNil(t, tool)

	result, err := tool.Handler(ctx, map[string]any{"task_id": taskID})
	require.NoError(t, err)
	require.NotEmpty(t, result.Content)

	var payload map[string]any
	require.NoError(t, json.Unmarshal([]byte(result.Content[0].Text), &payload))
	require.NotNil(t, payload["task"], "expected task in response")
	require.True(t, advanced, "Advance must be called")

	sr, err := srRepo.GetLatestByTaskAndStage(ctx, taskID, "plan_review")
	require.NoError(t, err)
	require.Equal(t, "done", sr.Status)
}

func TestRejectPlan_MCPTool_StoresFeedback(t *testing.T) {
	bundle, err := db.Open(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { _ = bundle.Client.Close() })

	ctx := context.Background()
	taskRepo := repo.NewTaskRepo(bundle.Client)
	srRepo := repo.NewStageRunRepo(bundle.Client)
	turnsRepo := repo.NewRefinementTurnRepo(bundle.Client)

	taskID := seedPlanReviewTaskMCP(t, ctx, taskRepo, srRepo)

	requeued := false
	registry := mcp.ToolRegistry{}
	RegisterPlanTools(registry, PlanDeps{
		Turns:     turnsRepo,
		Tasks:     taskRepo,
		StageRuns: srRepo,
		Requeue: func(_ context.Context, id, _ string) error {
			if id == taskID {
				requeued = true
			}
			return nil
		},
	})

	tool := registry["reject_plan"]
	require.NotNil(t, tool)

	result, err := tool.Handler(ctx, map[string]any{
		"task_id":  taskID,
		"feedback": "needs more detail on step 3",
	})
	require.NoError(t, err)
	require.NotEmpty(t, result.Content)

	var payload map[string]any
	require.NoError(t, json.Unmarshal([]byte(result.Content[0].Text), &payload))
	require.Equal(t, "requeued", payload["status"])
	require.True(t, requeued, "Requeue must be called")
}

func TestGetPlanStatus_MCPTool_ReturnsGateState(t *testing.T) {
	bundle, err := db.Open(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { _ = bundle.Client.Close() })

	ctx := context.Background()
	taskRepo := repo.NewTaskRepo(bundle.Client)
	srRepo := repo.NewStageRunRepo(bundle.Client)
	turnsRepo := repo.NewRefinementTurnRepo(bundle.Client)

	taskID := seedPlanReviewTaskMCP(t, ctx, taskRepo, srRepo)

	registry := mcp.ToolRegistry{}
	RegisterPlanTools(registry, PlanDeps{
		Turns:     turnsRepo,
		Tasks:     taskRepo,
		StageRuns: srRepo,
	})

	tool := registry["get_plan_status"]
	require.NotNil(t, tool)

	result, err := tool.Handler(ctx, map[string]any{"task_id": taskID})
	require.NoError(t, err)
	require.NotEmpty(t, result.Content)

	var payload map[string]any
	require.NoError(t, json.Unmarshal([]byte(result.Content[0].Text), &payload))
	require.Equal(t, "awaiting_user", payload["gate_state"])
}
