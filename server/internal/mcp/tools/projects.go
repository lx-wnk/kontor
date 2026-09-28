package tools

import (
	"context"
	"log/slog"
	"sort"
	"strconv"
	"strings"

	"github.com/lx-wnk/kontor/server/internal/db/ent"
	"github.com/lx-wnk/kontor/server/internal/db/repo"
	mcp "github.com/lx-wnk/kontor/server/internal/mcp"
	"github.com/lx-wnk/kontor/server/internal/validation"
)

// projectView is the JSON shape returned by the list_projects and
// create_project MCP tools. Mirrors the camelCase wire shape used by the
// projects HTTP handler.
type projectView struct {
	ID               string  `json:"id"`
	Slug             string  `json:"slug"`
	Name             string  `json:"name"`
	Description      *string `json:"description,omitempty"`
	Color            *string `json:"color,omitempty"`
	DefaultSpawnerID *string `json:"defaultSpawnerId,omitempty"`
	// HasSetupCommand reports presence, never the text: a setup command
	// routinely embeds registry tokens and list_projects only costs tasks:read.
	HasSetupCommand bool   `json:"hasSetupCommand"`
	FolderCount     int    `json:"folderCount"`
	CreatedAt       string `json:"createdAt"`
	UpdatedAt       string `json:"updatedAt"`
}

func toProjectView(p *ent.Project, folderCount int) projectView {
	return projectView{
		ID:               p.ID,
		Slug:             p.Slug,
		Name:             p.Name,
		Description:      p.Description,
		Color:            p.Color,
		DefaultSpawnerID: p.DefaultSpawnerID,
		HasSetupCommand:  p.SetupCommand != nil && *p.SetupCommand != "",
		FolderCount:      folderCount,
		CreatedAt:        tsFmt(p.CreatedAt),
		UpdatedAt:        tsFmt(p.UpdatedAt),
	}
}

// registerListProjects registers the list_projects tool.
// Returns an array of project views, each with a `folderCount` field.
// Scope: tasks:read.
func registerListProjects(registry mcp.ToolRegistry, d ReadDeps) {
	registry.Register(&mcp.ToolDef{
		Name:        "list_projects",
		Description: "List all projects with folder counts.",
		InputSchema: map[string]any{
			"type":       "object",
			"properties": map[string]any{},
		},
		Handler: func(ctx context.Context, _ map[string]any) (*mcp.ToolResult, error) {
			if d.ProjectRepo == nil {
				return nil, mcp.Fail("list_projects: project repository not configured")
			}
			rows, err := d.ProjectRepo.ListWithFolderCount(ctx)
			if err != nil {
				return nil, mcp.Fail("list_projects: " + err.Error())
			}
			result := make([]projectView, len(rows))
			for i, r := range rows {
				result[i] = toProjectView(r.Project, r.FolderCount)
			}
			return mcp.OK(result)
		},
	})
}

// createProjectResult is the create_project response: the project view plus the
// one thing the view cannot express — that the project is not usable from the
// New-Task form yet. Not on projectView itself: list_projects reuses that shape
// and so does the SSE payload the SPA reads as a Project.
type createProjectResult struct {
	projectView
	NextStep string `json:"nextStep"`
}

// noFolderYetAdvice names the dead end a freshly created project sits in:
// BacklogForm sources `cwd` exclusively from the project's folder suggestions,
// so with none the New-Task form can never be submitted for it.
const noFolderYetAdvice = "This project has no folder yet. The dashboard's New-Task form takes its working directory " +
	"only from a project's folders, so it cannot be submitted for this project until someone adds one under " +
	"Settings → Projects. Tell whoever asked for the project. Tasks created with create_task are unaffected — they carry their own cwd."

// createProjectProperties is both the advertised JSON Schema `properties` block
// and the accepted-key set the handler enforces — one definition, so the
// "additionalProperties": false the schema promises cannot drift from what the
// handler actually refuses.
var createProjectProperties = map[string]any{
	"slug":             map[string]any{"type": "string", "description": "Unique " + validation.SlugPatternMessage},
	"name":             map[string]any{"type": "string", "description": "Human-readable project name"},
	"description":      map[string]any{"type": "string", "description": "Optional free-text summary shown in the dashboard project list"},
	"color":            map[string]any{"type": "string", "description": "Hex colour for the dashboard, e.g. #3b82f6"},
	"defaultSpawnerId": map[string]any{"type": "string", "description": "Optional spawner ID used by this project's tasks by default"},
}

// rejectUnknownArgs makes a schema's "additionalProperties": false binding. The
// JSON-RPC layer hands tool arguments through unvalidated, so without this the
// flag would only constrain clients that validate for themselves and a smuggled
// key would still be dropped in silence.
// create_project only, deliberately: the other 40 tools have live callers whose
// harmless extra keys must not start failing.
func rejectUnknownArgs(tool string, args map[string]any, properties map[string]any) error {
	var unknown []string
	for key := range args {
		if _, known := properties[key]; !known {
			unknown = append(unknown, key)
		}
	}
	if len(unknown) == 0 {
		return nil
	}
	sort.Strings(unknown)
	accepted := mapKeys(properties)
	sort.Strings(accepted)
	return mcp.Fail(tool + ": unknown argument(s): " + strings.Join(unknown, ", ") +
		" — this tool accepts only: " + strings.Join(accepted, ", "))
}

// registerCreateProject registers the create_project tool. Scope: tasks:write.
// Stricter than POST /api/projects on purpose: name is trimmed, blank optionals
// are stored as NULL rather than "", and defaultSpawnerId must resolve.
func registerCreateProject(registry mcp.ToolRegistry, d WriteDeps) {
	registry.Register(&mcp.ToolDef{
		Name: "create_project",
		Description: "Create a project. Fails if the slug is already taken — call list_projects first to reuse an existing project. " +
			"The project starts with no folder, and the dashboard's New-Task form takes its working directory only from a project's folders, " +
			"so nobody can create a task for it in the UI until a folder is added under Settings → Projects. Pass that on to whoever asked for the project. " +
			"create_task over MCP is unaffected — it takes an explicit cwd.",
		InputSchema: map[string]any{
			"type":                 "object",
			"properties":           createProjectProperties,
			"required":             []string{"slug", "name"},
			"additionalProperties": false,
		},
		Handler: func(ctx context.Context, args map[string]any) (*mcp.ToolResult, error) {
			if d.ProjectRepo == nil {
				return nil, mcp.Fail("create_project: project repository not configured")
			}
			if err := rejectUnknownArgs("create_project", args, createProjectProperties); err != nil {
				return nil, err
			}
			slug, err := mcp.StringArg(args, "slug")
			if err != nil {
				return nil, err
			}
			if !validation.IsValidSlug(slug) {
				return nil, mcp.Fail("create_project: invalid slug " + strconv.Quote(slug) + ": " + validation.SlugPatternMessage)
			}
			name, err := mcp.StringArg(args, "name")
			if err != nil {
				return nil, err
			}
			name = strings.TrimSpace(name)
			if name == "" {
				return nil, mcp.Fail("create_project: name is required")
			}
			if !validation.IsValidProjectName(name) {
				return nil, mcp.Fail("create_project: " + validation.ProjectNameLengthMessage)
			}
			color, err := nonBlankPtr(args, "color")
			if err != nil {
				return nil, err
			}
			if color != nil && !validation.IsValidColor(*color) {
				return nil, mcp.Fail("create_project: " + validation.ColorPatternMessage)
			}
			if existing, err := d.ProjectRepo.GetBySlug(ctx, slug); err == nil {
				return nil, mcp.Fail("create_project: project already exists: " + slug + " (id " + existing.ID + ") — use it instead of creating a new one")
			} else if !ent.IsNotFound(err) {
				return nil, mcp.Fail("create_project: " + err.Error())
			}

			spawnerID, err := nonBlankPtr(args, "defaultSpawnerId")
			if err != nil {
				return nil, err
			}
			if spawnerID != nil {
				if d.SpawnerRepo == nil {
					return nil, mcp.Fail("create_project: spawner repository not configured")
				}
				if _, err := d.SpawnerRepo.GetByID(ctx, *spawnerID); err != nil {
					return nil, mcp.Fail("create_project: spawner not found")
				}
			}

			description, err := nonBlankPtr(args, "description")
			if err != nil {
				return nil, err
			}
			if description != nil && !validation.IsValidProjectDescription(*description) {
				return nil, mcp.Fail("create_project: " + validation.ProjectDescriptionLengthMessage)
			}
			// setupCommand is deliberately absent from this tool: it is an
			// RCE-equivalent sink (`sh -c` in the worktree, see
			// serverapp/di_pipeline.go) that the HTTP writer gates behind an
			// admin check. tasks:write is not admin, so the MCP path always
			// creates a project without one — do not add it back for parity.
			// Sending the key anyway is refused by rejectUnknownArgs above.
			p, err := d.ProjectRepo.Create(ctx, name, slug, description, color, spawnerID, nil)
			if err != nil {
				// Lost race against a concurrent create: the unique index, not the
				// pre-check above, is the authoritative duplicate guard.
				if ent.IsConstraintError(err) {
					return nil, mcp.Fail("create_project: project already exists: " + slug)
				}
				return nil, mcp.Fail("create_project: " + err.Error())
			}
			recordProjectCreated(ctx, d.AuditRepo, p)
			// A project has no folders until one is added through the UI or the
			// folders API, so the count is zero by construction rather than queried.
			view := toProjectView(p, 0)
			// The broadcast keeps the plain project shape the SPA casts it to; the
			// advisory is for the caller of this tool only.
			safeBroadcastProject(d.ProjectBroadcaster, "project_created", p.ID, view)
			return mcp.OK(createProjectResult{projectView: view, NextStep: noFolderYetAdvice})
		},
	})
}

// recordProjectCreated attributes an MCP-created project: a durable audit row
// plus a log line carrying the API key that made the call, which the audit row
// has no column for. Both carry the slug and id only — name and description are
// agent-supplied free text and stay out of both sinks. Best-effort, like every
// other audit write in this package.
func recordProjectCreated(ctx context.Context, auditRepo repo.AuditEventRepo, p *ent.Project) {
	keyID := ""
	if auth := mcp.AuthFromContext(ctx); auth != nil {
		keyID = auth.KeyID
	}
	slog.Info("mcp: project created", "slug", p.Slug, "projectId", p.ID, "keyId", keyID)
	if auditRepo == nil {
		return
	}
	_ = auditRepo.RecordAudit(ctx, nil, "project_created", "project:"+p.ID, map[string]any{
		"slug":   p.Slug,
		"source": "mcp_create_project",
	})
}

// nonBlankPtr returns a pointer to the named string argument, or nil when it is
// absent or blank — the repo layer distinguishes "leave unset" (nil) from "set
// to empty string", so an omitted optional must not become a pointer to "".
// Create-only: an update path needs the absent/null/value distinction instead
// (cf. api/projects.parseNullableString).
func nonBlankPtr(args map[string]any, key string) (*string, error) {
	raw, err := mcp.OptionalStringArg(args, key)
	if err != nil {
		return nil, err
	}
	if v := strings.TrimSpace(raw); v != "" {
		return &v, nil
	}
	return nil, nil
}
