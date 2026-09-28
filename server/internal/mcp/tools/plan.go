package tools

import (
	"context"

	planapi "github.com/lx-wnk/kontor/server/internal/api/plan"
	"github.com/lx-wnk/kontor/server/internal/db/repo"
	mcp "github.com/lx-wnk/kontor/server/internal/mcp"
)

// PlanDeps holds dependencies required by the plan MCP tools.
type PlanDeps struct {
	Turns     repo.RefinementTurnRepo
	Tasks     repo.TaskRepo
	StageRuns repo.StageRunRepo
	Advance   func(ctx context.Context, taskID string) error
	Requeue   func(ctx context.Context, taskID, prompt string) error
	Revoke    func(ctx context.Context, stageRunID string) error
	Broadcast func(ctx context.Context, eventType, taskID string)
}

// RegisterPlanTools registers the plan gate MCP tools into the registry.
func RegisterPlanTools(registry mcp.ToolRegistry, d PlanDeps) {
	registerApprovePlan(registry, d)
	registerRejectPlan(registry, d)
	registerGetPlanStatus(registry, d)
}

func registerApprovePlan(registry mcp.ToolRegistry, d PlanDeps) {
	registry.Register(&mcp.ToolDef{
		Name:        "approve_plan",
		Description: "Approve the plan produced by the plan_review stage, freezing it onto the task and advancing it to implementation.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"task_id": map[string]any{"type": "string", "description": "Task ID"},
			},
			"required": []string{"task_id"},
		},
		Handler: func(ctx context.Context, args map[string]any) (*mcp.ToolResult, error) {
			taskID, err := mcp.StringArg(args, "task_id")
			if err != nil {
				return nil, err
			}
			task, err := planapi.ApprovePlan(ctx, planapi.ApproveDeps{
				Turns:     d.Turns,
				Tasks:     d.Tasks,
				StageRuns: d.StageRuns,
				Advance:   d.Advance,
				Revoke:    d.Revoke,
			}, taskID)
			if err != nil {
				return nil, mcp.Fail("approve_plan: " + err.Error())
			}
			safeBroadcast(d.Broadcast, ctx, "task_updated", taskID)
			return mcp.OK(map[string]any{"task": task})
		},
	})
}

func registerRejectPlan(registry mcp.ToolRegistry, d PlanDeps) {
	registry.Register(&mcp.ToolDef{
		Name:        "reject_plan",
		Description: "Reject the plan from the plan_review stage with feedback, triggering a new plan_review run (up to the iteration cap).",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"task_id":  map[string]any{"type": "string", "description": "Task ID"},
				"feedback": map[string]any{"type": "string", "description": "Feedback for the agent to improve the plan"},
			},
			"required": []string{"task_id", "feedback"},
		},
		Handler: func(ctx context.Context, args map[string]any) (*mcp.ToolResult, error) {
			taskID, err := mcp.StringArg(args, "task_id")
			if err != nil {
				return nil, err
			}
			feedback, err := mcp.StringArg(args, "feedback")
			if err != nil {
				return nil, err
			}
			if err := planapi.RejectPlan(ctx, planapi.RejectDeps{
				Turns:     d.Turns,
				Tasks:     d.Tasks,
				StageRuns: d.StageRuns,
				Requeue:   d.Requeue,
			}, taskID, feedback); err != nil {
				return nil, mcp.Fail("reject_plan: " + err.Error())
			}
			safeBroadcast(d.Broadcast, ctx, "task_updated", taskID)
			return mcp.OK(map[string]any{"status": "requeued"})
		},
	})
}

func registerGetPlanStatus(registry mcp.ToolRegistry, d PlanDeps) {
	registry.Register(&mcp.ToolDef{
		Name:        "get_plan_status",
		Description: "Get the current plan gate state for a task (awaiting_user, done, etc.) and the approved plan if it exists.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"task_id": map[string]any{"type": "string", "description": "Task ID"},
			},
			"required": []string{"task_id"},
		},
		Handler: func(ctx context.Context, args map[string]any) (*mcp.ToolResult, error) {
			taskID, err := mcp.StringArg(args, "task_id")
			if err != nil {
				return nil, err
			}
			status, err := planapi.PlanStatus(ctx, planapi.StatusDeps{
				Turns:     d.Turns,
				Tasks:     d.Tasks,
				StageRuns: d.StageRuns,
			}, taskID)
			if err != nil {
				return nil, mcp.Fail("get_plan_status: " + err.Error())
			}
			return mcp.OK(map[string]any{
				"gate_state":    status.GateState,
				"approved_plan": status.ApprovedPlan,
			})
		},
	})
}
