package tools

import (
	"context"
	"os"
	"strings"

	refineapi "github.com/lx-wnk/kontor/server/internal/api/refine"
	"github.com/lx-wnk/kontor/server/internal/db/repo"
	mcp "github.com/lx-wnk/kontor/server/internal/mcp"
	"github.com/lx-wnk/kontor/server/internal/refine"
)

// RefineDeps holds dependencies required by the refinement MCP tools.
type RefineDeps struct {
	Turns     repo.RefinementTurnRepo
	Tasks     repo.TaskRepo
	StageRuns repo.StageRunRepo
	Runner    *refine.Runner
	Advance   func(ctx context.Context, taskID string) error
	Revoke    func(ctx context.Context, stageRunID string) error
}

// RegisterRefineTools registers the refinement MCP tools into the registry.
func RegisterRefineTools(registry mcp.ToolRegistry, d RefineDeps) {
	registerGetRefineStatus(registry, d)
	registerApproveSpec(registry, d)
	registerRefineTask(registry, d)
	registerInjectConcept(registry, d)
}

// registerInjectConcept exposes the direct concept-inject path: an agent hands a
// finished concept to a task in one call (no refine-agent round-trip), landing it
// draft_ready so approve_spec can freeze it onto the task.
func registerInjectConcept(registry mcp.ToolRegistry, d RefineDeps) {
	registry.Register(&mcp.ToolDef{
		Name:        "inject_concept",
		Description: "Inject a finished concept (spec, plan, toolRequests, optional refinedTitle/sourceBranch/targetBranch) onto a task without spawning a refine agent. Marks the task draft_ready; follow with approve_spec to advance it.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"task_id": map[string]any{"type": "string", "description": "Task ID"},
				"concept": map[string]any{"type": "object", "description": "The finished concept as a JSON object (spec, plan, toolRequests, and optional refinedTitle/sourceBranch/targetBranch routing keys)"},
			},
			"required": []string{"task_id", "concept"},
		},
		Handler: func(ctx context.Context, args map[string]any) (*mcp.ToolResult, error) {
			taskID, err := mcp.StringArg(args, "task_id")
			if err != nil {
				return nil, err
			}
			raw, ok := args["concept"].(map[string]any)
			if !ok || len(raw) == 0 {
				return nil, mcp.Fail("concept must be a non-empty object")
			}
			concept := refine.Concept{Raw: raw}
			if s, ok := raw["refinedTitle"].(string); ok {
				concept.RefinedTitle = s
			}
			if s, ok := raw["sourceBranch"].(string); ok {
				concept.SourceBranch = s
			}
			if s, ok := raw["targetBranch"].(string); ok {
				concept.TargetBranch = s
			}
			if d.Runner == nil {
				return nil, mcp.Fail("refinement runner not available")
			}
			if err := refineapi.InjectConcept(ctx, refineapi.InjectDeps{Turns: d.Turns, Runner: d.Runner}, taskID, concept); err != nil {
				return nil, mcp.Fail("inject_concept: " + err.Error())
			}
			status, _ := d.Runner.State(taskID)
			return mcp.OK(map[string]any{"status": status})
		},
	})
}

func registerGetRefineStatus(registry mcp.ToolRegistry, d RefineDeps) {
	registry.Register(&mcp.ToolDef{
		Name:        "get_refine_status",
		Description: "Get the current refinement run status for a task.",
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
			if d.Runner == nil {
				return nil, mcp.Fail("refinement runner not available")
			}
			status, errMsg := d.Runner.State(taskID)
			resp := map[string]any{"status": status}
			if errMsg != "" {
				resp["error"] = errMsg
			}
			return mcp.OK(resp)
		},
	})
}

func registerApproveSpec(registry mcp.ToolRegistry, d RefineDeps) {
	registry.Register(&mcp.ToolDef{
		Name:        "approve_spec",
		Description: "Confirm the refined spec, freezing it onto the task and advancing it past the backlog stage.",
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
			task, err := refineapi.Confirm(ctx, refineapi.ConfirmDeps{
				Turns:     d.Turns,
				Tasks:     d.Tasks,
				StageRuns: d.StageRuns,
				Advance:   d.Advance,
				Revoke:    d.Revoke,
			}, taskID)
			if err != nil {
				return nil, mcp.Fail("approve_spec: " + err.Error())
			}
			return mcp.OK(map[string]any{"task": task})
		},
	})
}

func registerRefineTask(registry mcp.ToolRegistry, d RefineDeps) {
	registry.Register(&mcp.ToolDef{
		Name:        "refine_task",
		Description: "Submit a refinement message for a task and wait for the assistant response.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"task_id": map[string]any{"type": "string", "description": "Task ID"},
				"message": map[string]any{"type": "string", "description": "Refinement message to submit"},
			},
			"required": []string{"task_id", "message"},
		},
		Handler: func(ctx context.Context, args map[string]any) (*mcp.ToolResult, error) {
			taskID, err := mcp.StringArg(args, "task_id")
			if err != nil {
				return nil, err
			}
			message, err := mcp.StringArg(args, "message")
			if err != nil {
				return nil, err
			}
			if strings.TrimSpace(message) == "" {
				return nil, mcp.Fail("message must not be blank")
			}
			if d.Runner == nil {
				return nil, mcp.Fail("refinement runner not available")
			}

			task, err := d.Tasks.GetByID(ctx, taskID)
			if err != nil {
				return nil, mcp.Fail("task not found: " + taskID)
			}

			history, err := d.Turns.ListForTaskNewest(ctx, taskID, 20)
			if err != nil {
				return nil, mcp.Fail("refine_task: failed to fetch history: " + err.Error())
			}

			turns := make([]refine.Turn, 0, len(history))
			for _, t := range history {
				if t.Phase != nil && *t.Phase == "confirmed" {
					continue
				}
				turns = append(turns, refine.Turn{
					Role:    string(t.Role),
					Content: t.Content,
				})
			}

			workDir := task.Cwd
			if workDir == "" {
				workDir, _ = os.Getwd()
			}

			var desc string
			if task.Description != nil {
				desc = *task.Description
			}

			cfg := refine.SpawnConfig{
				TaskTitle:       task.Title,
				TaskDescription: desc,
				History:         turns,
				UserMessage:     message,
				WorkDir:         workDir,
			}

			if d.Runner.IsRunning(taskID) {
				return nil, mcp.Fail("a refinement run is already in progress for this task")
			}

			if _, err := d.Turns.Create(ctx, repo.CreateTurnInput{
				TaskID:  taskID,
				Role:    "user",
				Content: message,
			}); err != nil {
				return nil, mcp.Fail("refine_task: failed to store user turn: " + err.Error())
			}

			// MCP tools cannot stream SSE; drain the channel synchronously so the
			// caller gets the complete assistant response in a single tool result.
			out, err := d.Runner.Start(taskID, cfg, nil)
			if err != nil {
				return nil, mcp.Fail("refine_task: " + err.Error())
			}

			var sb strings.Builder
			for line := range out {
				if sb.Len() > 0 {
					sb.WriteString("\n")
				}
				sb.WriteString(line)
			}

			status, errMsg := d.Runner.State(taskID)
			resp := map[string]any{
				"status":   status,
				"response": sb.String(),
			}
			if errMsg != "" {
				resp["error"] = errMsg
			}
			return mcp.OK(resp)
		},
	})
}
