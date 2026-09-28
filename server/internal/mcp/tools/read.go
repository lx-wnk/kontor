package tools

import (
	"context"

	"github.com/lx-wnk/kontor/server/internal/db/ent"
	"github.com/lx-wnk/kontor/server/internal/db/repo"
	mcp "github.com/lx-wnk/kontor/server/internal/mcp"
)

// ReadDeps holds the repositories required by the read tools.
type ReadDeps struct {
	TaskRepo    repo.TaskRepo
	SRRepo      repo.StageRunRepo
	PermRepo    repo.PermissionRepo
	AuditRepo   repo.AuditEventRepo
	ProjectRepo repo.ProjectRepo
	SpawnerRepo repo.SpawnerRepo
	// Caller resolves the stage run on the request's credential into the task
	// that credential is confined to. The zero value resolves a user key to no
	// confinement at all, which is how the dashboard's own API keys keep
	// seeing every task.
	Caller mcp.CallerResolver
}

// RegisterReadTools registers all read tools into the given registry.
func RegisterReadTools(registry mcp.ToolRegistry, d ReadDeps) {
	registerListTasks(registry, d)
	registerGetTask(registry, d)
	registerListStageRuns(registry, d)
	registerListAudit(registry, d)
	registerListPermissionRequests(registry, d)
	registerListProjects(registry, d)
	registerListSpawners(registry, d)
}

// spawnerView is the JSON shape returned by the list_spawners MCP tool.
// Mirrors the camelCase wire shape used by the spawners HTTP handler.
type spawnerView struct {
	ID            string            `json:"id"`
	Name          string            `json:"name"`
	Slug          string            `json:"slug"`
	Command       string            `json:"command"`
	Args          []string          `json:"args"`
	Env           map[string]string `json:"env"`
	ModelOverride *string           `json:"modelOverride,omitempty"`
	Description   *string           `json:"description,omitempty"`
	BuiltIn       bool              `json:"builtIn"`
	CreatedAt     string            `json:"createdAt"`
	UpdatedAt     string            `json:"updatedAt"`
}

// registerListSpawners registers the list_spawners tool.
// Returns an array of spawner views including built-ins.
// Scope: tasks:read.
func registerListSpawners(registry mcp.ToolRegistry, d ReadDeps) {
	registry.Register(&mcp.ToolDef{
		Name:        "list_spawners",
		Description: "List all spawners (built-in and custom).",
		InputSchema: map[string]any{
			"type":       "object",
			"properties": map[string]any{},
		},
		Handler: func(ctx context.Context, _ map[string]any) (*mcp.ToolResult, error) {
			if d.SpawnerRepo == nil {
				return nil, mcp.Fail("list_spawners: spawner repository not configured")
			}
			spawners, err := d.SpawnerRepo.List(ctx)
			if err != nil {
				return nil, mcp.Fail("list_spawners: " + err.Error())
			}
			result := make([]spawnerView, len(spawners))
			for i, s := range spawners {
				args := s.Args
				if args == nil {
					args = []string{}
				}
				env := s.Env
				if env == nil {
					env = map[string]string{}
				}
				result[i] = spawnerView{
					ID:            s.ID,
					Name:          s.Name,
					Slug:          s.Slug,
					Command:       s.Command,
					Args:          args,
					Env:           env,
					ModelOverride: s.ModelOverride,
					Description:   s.Description,
					BuiltIn:       s.BuiltIn,
					CreatedAt:     tsFmt(s.CreatedAt),
					UpdatedAt:     tsFmt(s.UpdatedAt),
				}
			}
			return mcp.OK(result)
		},
	})
}

// checkTaskAccess refuses a task that the caller's credential is not bound to.
//
// A user key resolves to no task and may read every task, exactly as before.
// A stage-run key is confined to the task its run belongs to — without this a
// stage agent working on task A could read every other task's spec, audit
// trail and pending permission requests, in every project.
//
// A credential that names a stage run which cannot be resolved is refused
// rather than treated as unconfined: an unresolvable caller is not an
// unrestricted one.
func checkTaskAccess(ctx context.Context, caller mcp.CallerResolver, taskID string) error {
	own, err := caller.TaskID(ctx)
	if err != nil {
		return mcp.Fail("access denied: " + err.Error())
	}
	if own != "" && own != taskID {
		return mcp.Fail("access denied: this credential may only read task " + own)
	}
	return nil
}

// registerListTasks registers the list_tasks tool.
// Optional "stage" argument filters by pipeline stage; without it all tasks are returned.
func registerListTasks(registry mcp.ToolRegistry, d ReadDeps) {
	registry.Register(&mcp.ToolDef{
		Name:        "list_tasks",
		Description: "List pipeline tasks, optionally filtered by stage.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"stage": map[string]any{
					"type":        "string",
					"description": "Filter by pipeline stage (e.g. concept, implementation, done).",
				},
			},
		},
		Handler: func(ctx context.Context, args map[string]any) (*mcp.ToolResult, error) {
			stage := mcp.OptionalString(args, "stage")
			own, err := d.Caller.TaskID(ctx)
			if err != nil {
				return nil, mcp.Fail("list_tasks: access denied: " + err.Error())
			}
			if own != "" {
				task, err := d.TaskRepo.GetByID(ctx, own)
				if err != nil {
					return nil, mcp.Fail("list_tasks: " + err.Error())
				}
				if stage != "" && task.CurrentStage != stage {
					return mcp.OK([]*ent.Task{})
				}
				return mcp.OK([]*ent.Task{task})
			}
			if stage != "" {
				tasks, err := d.TaskRepo.ListByStage(ctx, stage)
				if err != nil {
					return nil, mcp.Fail("list_tasks: " + err.Error())
				}
				if tasks == nil {
					tasks = []*ent.Task{}
				}
				return mcp.OK(tasks)
			}
			// MCPAuthInfo has no UserID — treat as admin (isAdmin=true, userID="").
			tasks, err := d.TaskRepo.ListForUser(ctx, "", true)
			if err != nil {
				return nil, mcp.Fail("list_tasks: " + err.Error())
			}
			if tasks == nil {
				tasks = []*ent.Task{}
			}
			return mcp.OK(tasks)
		},
	})
}

// registerGetTask registers the get_task tool.
// Accepts an ID or slug via the "id_or_slug" argument.
func registerGetTask(registry mcp.ToolRegistry, d ReadDeps) {
	registry.Register(&mcp.ToolDef{
		Name:        "get_task",
		Description: "Fetch a single task by ID or slug.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"id_or_slug": map[string]any{
					"type":        "string",
					"description": "Task UUID or slug.",
				},
			},
			"required": []string{"id_or_slug"},
		},
		Handler: func(ctx context.Context, args map[string]any) (*mcp.ToolResult, error) {
			idOrSlug, err := mcp.StringArg(args, "id_or_slug")
			if err != nil {
				return nil, err
			}
			task, err := d.TaskRepo.GetByID(ctx, idOrSlug)
			if err != nil {
				if !ent.IsNotFound(err) {
					return nil, mcp.Fail("get_task: " + err.Error())
				}
				// Not found by ID — try slug
				task, err = d.TaskRepo.GetBySlug(ctx, idOrSlug)
				if err != nil {
					return nil, mcp.Fail("Task not found: " + idOrSlug)
				}
			}
			if accessErr := checkTaskAccess(ctx, d.Caller, task.ID); accessErr != nil {
				return nil, accessErr
			}
			return mcp.OK(task)
		},
	})
}

// registerListStageRuns registers the list_stage_runs tool.
func registerListStageRuns(registry mcp.ToolRegistry, d ReadDeps) {
	registry.Register(&mcp.ToolDef{
		Name:        "list_stage_runs",
		Description: "List all stage runs for a task.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"task_id": map[string]any{
					"type":        "string",
					"description": "Task UUID.",
				},
			},
			"required": []string{"task_id"},
		},
		Handler: func(ctx context.Context, args map[string]any) (*mcp.ToolResult, error) {
			taskID, err := mcp.StringArg(args, "task_id")
			if err != nil {
				return nil, err
			}
			task, err := d.TaskRepo.GetByID(ctx, taskID)
			if err != nil {
				if ent.IsNotFound(err) {
					return nil, mcp.Fail("Task not found: " + taskID)
				}
				return nil, mcp.Fail("list_stage_runs: " + err.Error())
			}
			if accessErr := checkTaskAccess(ctx, d.Caller, task.ID); accessErr != nil {
				return nil, accessErr
			}
			runs, err := d.SRRepo.ListForTask(ctx, taskID)
			if err != nil {
				return nil, mcp.Fail("list_stage_runs: " + err.Error())
			}
			if runs == nil {
				runs = []*ent.StageRun{}
			}
			return mcp.OK(runs)
		},
	})
}

// registerListAudit registers the list_audit tool.
func registerListAudit(registry mcp.ToolRegistry, d ReadDeps) {
	registry.Register(&mcp.ToolDef{
		Name:        "list_audit",
		Description: "List audit log entries for a task.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"task_id": map[string]any{
					"type":        "string",
					"description": "Task UUID.",
				},
			},
			"required": []string{"task_id"},
		},
		Handler: func(ctx context.Context, args map[string]any) (*mcp.ToolResult, error) {
			taskID, err := mcp.StringArg(args, "task_id")
			if err != nil {
				return nil, err
			}
			task, err := d.TaskRepo.GetByID(ctx, taskID)
			if err != nil {
				if ent.IsNotFound(err) {
					return nil, mcp.Fail("Task not found: " + taskID)
				}
				return nil, mcp.Fail("list_audit: " + err.Error())
			}
			if accessErr := checkTaskAccess(ctx, d.Caller, task.ID); accessErr != nil {
				return nil, accessErr
			}
			entries, err := d.AuditRepo.ListForTask(ctx, taskID)
			if err != nil {
				return nil, mcp.Fail("list_audit: " + err.Error())
			}
			if entries == nil {
				entries = []*ent.AuditEvent{}
			}
			return mcp.OK(entries)
		},
	})
}

// registerListPermissionRequests registers the list_permission_requests tool.
func registerListPermissionRequests(registry mcp.ToolRegistry, d ReadDeps) {
	registry.Register(&mcp.ToolDef{
		Name:        "list_permission_requests",
		Description: "List all pending permission requests across every stage run of a task.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"task_id": map[string]any{
					"type":        "string",
					"description": "Task UUID.",
				},
			},
			"required": []string{"task_id"},
		},
		Handler: func(ctx context.Context, args map[string]any) (*mcp.ToolResult, error) {
			taskID, err := mcp.StringArg(args, "task_id")
			if err != nil {
				return nil, err
			}
			task, err := d.TaskRepo.GetByID(ctx, taskID)
			if err != nil {
				if ent.IsNotFound(err) {
					return nil, mcp.Fail("Task not found: " + taskID)
				}
				return nil, mcp.Fail("list_permission_requests: " + err.Error())
			}
			if accessErr := checkTaskAccess(ctx, d.Caller, task.ID); accessErr != nil {
				return nil, accessErr
			}
			runs, err := d.SRRepo.ListForTask(ctx, taskID)
			if err != nil {
				return nil, mcp.Fail("list_permission_requests: " + err.Error())
			}
			requests := make([]*ent.PermissionRequest, 0)
			for _, run := range runs {
				pending, err := d.PermRepo.ListPendingForStageRun(ctx, run.ID)
				if err != nil {
					return nil, mcp.Fail("list_permission_requests: " + err.Error())
				}
				requests = append(requests, pending...)
			}
			return mcp.OK(requests)
		},
	})
}
