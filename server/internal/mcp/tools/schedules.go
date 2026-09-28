package tools

import (
	"context"
	"time"

	"github.com/lx-wnk/kontor/server/internal/db/ent"
	"github.com/lx-wnk/kontor/server/internal/db/repo"
	mcp "github.com/lx-wnk/kontor/server/internal/mcp"
	"github.com/lx-wnk/kontor/server/internal/scheduler"
	"github.com/lx-wnk/kontor/server/internal/validation"
)

// ScheduleTranslator converts an NL phrase to a validated cron expression.
type ScheduleTranslator interface {
	Translate(ctx context.Context, phrase string) (string, error)
}

// ScheduleRunner fires a schedule immediately.
type ScheduleRunner interface {
	RunNow(ctx context.Context, scheduleID string) (string, error)
}

// ScheduleDeps holds the dependencies for the schedule MCP tools.
type ScheduleDeps struct {
	Repo       repo.TaskScheduleRepo
	Translator ScheduleTranslator
	Runner     ScheduleRunner
	Broadcast  func(scheduleID string)
}

// RegisterScheduleTools registers manage_schedule (tasks:write) and
// list_schedules (tasks:read).
func RegisterScheduleTools(registry mcp.ToolRegistry, d ScheduleDeps) {
	registerManageSchedule(registry, d)
	registerListSchedules(registry, d)
}

func scheduleNextRunPtr(cronExpr, tz string) *time.Time {
	loc, err := time.LoadLocation(tz)
	if err != nil {
		loc = time.UTC
	}
	runs, err := scheduler.NextRuns(cronExpr, time.Now().In(loc), 1)
	if err != nil || len(runs) == 0 {
		return nil
	}
	return &runs[0]
}

// resolveCron mirrors the REST handler: a raw cronExpr wins; otherwise the
// phrase is translated. Both paths validate.
func resolveScheduleCron(ctx context.Context, t ScheduleTranslator, nlText, cronExpr string) (string, error) {
	if cronExpr != "" {
		if err := scheduler.Validate(cronExpr); err != nil {
			return "", mcp.Fail(err.Error())
		}
		return cronExpr, nil
	}
	if nlText == "" {
		return "", mcp.Fail("nlText or cronExpr is required")
	}
	if t == nil {
		return "", mcp.Fail("translator not available")
	}
	expr, err := t.Translate(ctx, nlText)
	if err != nil {
		return "", mcp.Fail("could not translate phrase to a schedule: " + nlText)
	}
	return expr, nil
}

// ensureModuleOwnsRoutine refuses a module acting on a routine it does not own.
// A caller that is not a module passes through: it is governed by scopes and
// grants like every other caller, and this rule exists only to keep one module
// out of another's — and out of the operator's.
func ensureModuleOwnsRoutine(ctx context.Context, ownerModule string) error {
	auth := mcp.AuthFromContext(ctx)
	if auth == nil || auth.ModuleID == "" {
		return nil
	}
	if auth.ModuleID != ownerModule {
		return mcp.Fail("this routine belongs to " + routineOwnerLabel(ownerModule) + "; a module may only manage its own")
	}
	return nil
}

// callerModuleID names the module behind the request, empty for every other
// caller.
func callerModuleID(ctx context.Context) string {
	if auth := mcp.AuthFromContext(ctx); auth != nil {
		return auth.ModuleID
	}
	return ""
}

// routineOwnerLabel names the owner in a refusal without pretending a routine
// a human created has a module behind it.
func routineOwnerLabel(ownerModule string) string {
	if ownerModule == "" {
		return "no module"
	}
	return "module " + ownerModule
}

func registerManageSchedule(registry mcp.ToolRegistry, d ScheduleDeps) {
	registry.Register(&mcp.ToolDef{
		Name: "manage_schedule",
		Description: "Manage recurring task schedules: create, update, delete, enable, disable, or run_now. " +
			"Natural-language phrases (nlText) are translated to cron at create/update; a raw cronExpr is also accepted.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"action": map[string]any{
					"type": "string",
					"enum": []string{"create", "update", "delete", "enable", "disable", "run_now"},
				},
				"id":          map[string]any{"type": "string", "description": "Schedule ID (required for all actions except create)"},
				"name":        map[string]any{"type": "string"},
				"nlText":      map[string]any{"type": "string", "description": "Natural-language schedule phrase, e.g. 'every weekday at 9am'"},
				"cronExpr":    map[string]any{"type": "string", "description": "Raw 5-field cron expression (overrides nlText)"},
				"timezone":    map[string]any{"type": "string"},
				"catchup":     map[string]any{"type": "string", "enum": []string{"none", "once"}},
				"slugPrefix":  map[string]any{"type": "string"},
				"title":       map[string]any{"type": "string"},
				"description": map[string]any{"type": "string"},
				"cwd":         map[string]any{"type": "string"},
				"runMode": map[string]any{
					"type": "string",
					"enum": []string{"job", "pipeline"},
					"description": "job: one agent run in cwd, no worktree (default). pipeline: a pipeline task " +
						"that starts in ready; cwd must be a git repository.",
				},
				"priority":           map[string]any{"type": "string", "enum": []string{"high", "medium", "low"}},
				"maxIterations":      map[string]any{"type": "integer"},
				"permissionTemplate": map[string]any{"type": "string"},
				"projectId":          map[string]any{"type": "string"},
				"spawnerId":          map[string]any{"type": "string"},
			},
			"required": []string{"action"},
		},
		Handler: func(ctx context.Context, args map[string]any) (*mcp.ToolResult, error) {
			action, err := mcp.StringArg(args, "action")
			if err != nil {
				return nil, err
			}
			switch action {
			case "create":
				return manageScheduleCreate(ctx, d, args)
			case "update":
				return manageScheduleUpdate(ctx, d, args)
			case "delete":
				return manageScheduleSimple(ctx, d, args, "delete")
			case "enable":
				return manageScheduleSimple(ctx, d, args, "enable")
			case "disable":
				return manageScheduleSimple(ctx, d, args, "disable")
			case "run_now":
				return manageScheduleSimple(ctx, d, args, "run_now")
			default:
				return nil, mcp.Fail("unknown action: " + action)
			}
		},
	})
}

func manageScheduleCreate(ctx context.Context, d ScheduleDeps, args map[string]any) (*mcp.ToolResult, error) {
	name := mcp.OptionalString(args, "name")
	title := mcp.OptionalString(args, "title")
	cwd := mcp.OptionalString(args, "cwd")
	slugPrefix := mcp.OptionalString(args, "slugPrefix")
	if name == "" || title == "" || cwd == "" {
		return nil, mcp.Fail("name, title and cwd are required")
	}
	if !validation.IsValidSlug(slugPrefix) {
		return nil, mcp.Fail("slugPrefix: " + validation.SlugPatternMessage)
	}
	runMode := mcp.OptionalString(args, "runMode")
	if runMode == "" {
		runMode = repo.RunModeJob
	}
	if err := scheduler.CheckRunMode(ctx, runMode, cwd); err != nil {
		return nil, mcp.Fail(err.Error())
	}
	cronExpr, err := resolveScheduleCron(ctx, d.Translator, mcp.OptionalString(args, "nlText"), mcp.OptionalString(args, "cronExpr"))
	if err != nil {
		return nil, err
	}
	tz := mcp.OptionalString(args, "timezone")
	if tz == "" {
		tz = "UTC"
	}
	// A module's routine is stamped with the module that created it, taken
	// from the credential rather than from the arguments: ownership a caller
	// could name is ownership a caller could claim.
	in := repo.CreateTaskScheduleInput{
		OwnerModule: callerModuleID(ctx),
		Name:        name,
		CronExpr:    cronExpr,
		Timezone:    tz,
		Catchup:     mcp.OptionalString(args, "catchup"),
		SlugPrefix:  slugPrefix,
		Title:       title,
		Cwd:         cwd,
		RunMode:     runMode,
		Priority:    mcp.OptionalString(args, "priority"),
		NextRunAt:   scheduleNextRunPtr(cronExpr, tz),
	}
	if v := mcp.OptionalString(args, "nlText"); v != "" {
		in.NLText = &v
	}
	if v := mcp.OptionalString(args, "description"); v != "" {
		in.Description = &v
	}
	if v := mcp.OptionalString(args, "permissionTemplate"); v != "" {
		in.PermissionTemplate = &v
	}
	if v := mcp.OptionalString(args, "projectId"); v != "" {
		in.ProjectID = &v
	}
	if v := mcp.OptionalString(args, "spawnerId"); v != "" {
		in.SpawnerID = &v
	}
	if f, ok := mcp.OptionalFloat64(args, "maxIterations"); ok {
		v := int(f)
		in.MaxIterations = v
	}
	if owner := scheduleOwnerID(ctx); owner != "" {
		in.UserID = &owner
	}
	s, err := d.Repo.Create(ctx, in)
	if err != nil {
		return nil, mcp.Fail("manage_schedule create: " + err.Error())
	}
	scheduleBroadcast(d, s.ID)
	return mcp.OK(map[string]any{"action": "create", "schedule": s})
}

func manageScheduleUpdate(ctx context.Context, d ScheduleDeps, args map[string]any) (*mcp.ToolResult, error) {
	id, err := mcp.StringArg(args, "id")
	if err != nil {
		return nil, err
	}
	existing, err := d.Repo.GetByID(ctx, id)
	if err != nil {
		return nil, mcp.Fail("schedule not found: " + id)
	}
	if err := ensureModuleOwnsRoutine(ctx, existing.OwnerModule); err != nil {
		return nil, err
	}
	in := repo.UpdateTaskScheduleInput{}
	runMode := mcp.OptionalString(args, "runMode")
	argCwd := mcp.OptionalString(args, "cwd")
	if runMode != "" || argCwd != "" {
		effectiveMode := runMode
		if effectiveMode == "" {
			effectiveMode = existing.RunMode
		}
		effectiveCwd := argCwd
		if effectiveCwd == "" {
			effectiveCwd = existing.Cwd
		}
		if err := scheduler.CheckRunMode(ctx, effectiveMode, effectiveCwd); err != nil {
			return nil, mcp.Fail(err.Error())
		}
		if runMode != "" {
			in.RunMode = &runMode
		}
	}
	if v := mcp.OptionalString(args, "name"); v != "" {
		in.Name = &v
	}
	if v := mcp.OptionalString(args, "title"); v != "" {
		in.Title = &v
	}
	if v := mcp.OptionalString(args, "cwd"); v != "" {
		in.Cwd = &v
	}
	if v := mcp.OptionalString(args, "catchup"); v != "" {
		in.Catchup = &v
	}
	if v := mcp.OptionalString(args, "priority"); v != "" {
		in.Priority = &v
	}
	nlText := mcp.OptionalString(args, "nlText")
	cronExpr := mcp.OptionalString(args, "cronExpr")
	if nlText != "" || cronExpr != "" {
		resolved, rerr := resolveScheduleCron(ctx, d.Translator, nlText, cronExpr)
		if rerr != nil {
			return nil, rerr
		}
		in.CronExpr = &resolved
		tz := mcp.OptionalString(args, "timezone")
		if tz == "" {
			tz = "UTC"
		}
		in.NextRunAt = scheduleNextRunPtr(resolved, tz)
		if nlText != "" {
			in.NLText = &nlText
		}
	}
	if v := mcp.OptionalString(args, "timezone"); v != "" {
		in.Timezone = &v
	}
	s, err := d.Repo.Update(ctx, id, in)
	if err != nil {
		return nil, mcp.Fail("manage_schedule update: " + err.Error())
	}
	scheduleBroadcast(d, id)
	return mcp.OK(map[string]any{"action": "update", "schedule": s})
}

func manageScheduleSimple(ctx context.Context, d ScheduleDeps, args map[string]any, action string) (*mcp.ToolResult, error) {
	id, err := mcp.StringArg(args, "id")
	if err != nil {
		return nil, err
	}
	// Ownership is checked before the verb, not inside each branch: delete,
	// disable and run_now are equally consequential for a routine somebody
	// else owns.
	existing, err := d.Repo.GetByID(ctx, id)
	if err != nil {
		return nil, mcp.Fail("schedule not found: " + id)
	}
	if err := ensureModuleOwnsRoutine(ctx, existing.OwnerModule); err != nil {
		return nil, err
	}
	switch action {
	case "delete":
		if err := d.Repo.Delete(ctx, id); err != nil {
			return nil, mcp.Fail("manage_schedule delete: " + err.Error())
		}
		scheduleBroadcast(d, id)
		return mcp.OK(map[string]any{"action": "delete", "id": id})
	case "enable", "disable":
		s, err := d.Repo.SetEnabled(ctx, id, action == "enable")
		if err != nil {
			return nil, mcp.Fail("manage_schedule " + action + ": " + err.Error())
		}
		scheduleBroadcast(d, id)
		return mcp.OK(map[string]any{"action": action, "schedule": s})
	case "run_now":
		if d.Runner == nil {
			return nil, mcp.Fail("scheduler not available for run_now")
		}
		taskID, err := d.Runner.RunNow(ctx, id)
		if err != nil {
			return nil, mcp.Fail("manage_schedule run_now: " + err.Error())
		}
		return mcp.OK(map[string]any{"action": "run_now", "taskId": taskID})
	default:
		return nil, mcp.Fail("unknown action: " + action)
	}
}

func registerListSchedules(registry mcp.ToolRegistry, d ScheduleDeps) {
	registry.Register(&mcp.ToolDef{
		Name:        "list_schedules",
		Description: "List recurring task schedules owned by the calling API key.",
		InputSchema: map[string]any{
			"type":       "object",
			"properties": map[string]any{},
		},
		Handler: func(ctx context.Context, _ map[string]any) (*mcp.ToolResult, error) {
			rows, err := d.Repo.ListForUser(ctx, scheduleOwnerID(ctx), false)
			if err != nil {
				return nil, mcp.Fail("list_schedules: " + err.Error())
			}
			if rows == nil {
				rows = []*ent.TaskSchedule{}
			}
			return mcp.OK(rows)
		},
	})
}

// scheduleOwnerID returns the API-key identity that owns MCP-created schedules.
// REST scopes schedules by JWT user; MCP has only the bearer key, so schedules
// created over MCP are owned by — and listed for — the calling key.
func scheduleOwnerID(ctx context.Context) string {
	if info := mcp.AuthFromContext(ctx); info != nil {
		return info.KeyID
	}
	return ""
}

func scheduleBroadcast(d ScheduleDeps, id string) {
	if d.Broadcast != nil {
		d.Broadcast(id)
	}
}
