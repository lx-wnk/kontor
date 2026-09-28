package projects

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/lx-wnk/kontor/server/internal/apierr"
	"github.com/lx-wnk/kontor/server/internal/db/ent"
	"github.com/lx-wnk/kontor/server/internal/db/repo"
	"github.com/lx-wnk/kontor/server/internal/sse"
	"github.com/lx-wnk/kontor/server/internal/validation"
)

// TaskProjectOps abstracts the task-side operations the project handler needs
// when deleting a project. Implemented by repo.TaskRepo callers via the
// adapter in router wiring. Kept narrow so the project package has no runtime
// dependency on the full TaskRepo interface.
type TaskProjectOps interface {
	// CountActiveByProject returns the number of tasks linked to projectID
	// whose current_stage is not "done" or "cancelled".
	CountActiveByProject(ctx context.Context, projectID string) (int, error)
	// ClearProjectForTerminalTasks sets project_id = NULL on all tasks linked
	// to projectID whose current_stage is "done" or "cancelled".
	ClearProjectForTerminalTasks(ctx context.Context, projectID string) error
}

// Handler exposes CRUD endpoints for projects and their folders.
type Handler struct {
	projects    projectRepo
	folders     folderRepo
	tasks       TaskProjectOps
	broadcaster *sse.ProjectBroadcaster
	bypassAuth  bool
}

// projectRepo is the subset of repo.ProjectRepo this handler needs.
type projectRepo interface {
	Create(ctx context.Context, name, slug string, description, color, defaultSpawnerID, setupCommand *string) (*ent.Project, error)
	GetByID(ctx context.Context, id string) (*ent.Project, error)
	GetWithFolders(ctx context.Context, id string) (*ent.Project, error)
	ListWithFolderCount(ctx context.Context) ([]repo.ProjectWithCount, error)
	Update(ctx context.Context, id string, name, slug *string, description, color, defaultSpawnerID, setupCommand *string, clearDescription, clearColor, clearDefaultSpawnerID, clearSetupCommand bool) (*ent.Project, error)
	Delete(ctx context.Context, id string) error
}

// folderRepo is the subset of repo.ProjectFolderRepo this handler needs.
type folderRepo interface {
	Create(ctx context.Context, projectID, path string, label *string, isDefault bool) (*ent.ProjectFolder, error)
	GetByID(ctx context.Context, id string) (*ent.ProjectFolder, error)
	ListByProject(ctx context.Context, projectID string) ([]*ent.ProjectFolder, error)
	Update(ctx context.Context, id string, path, label *string, clearLabel bool, isDefault *bool) (*ent.ProjectFolder, error)
	Delete(ctx context.Context, id string) error
	Suggest(ctx context.Context, projectID string) ([]*ent.ProjectFolder, error)
}

// NewHandler returns a Handler wired with the given repos and task ops.
// broadcaster may be nil (e.g. in tests); emit becomes a no-op then.
// bypassAuth mirrors the loopback single-user mode in which every request is
// already treated as a local admin (see auth.RequireAdminOrBypass).
func NewHandler(p repo.ProjectRepo, f repo.ProjectFolderRepo, tasks TaskProjectOps, broadcaster *sse.ProjectBroadcaster, bypassAuth bool) *Handler {
	return &Handler{projects: p, folders: f, tasks: tasks, broadcaster: broadcaster, bypassAuth: bypassAuth}
}

// canSetSetupCommand reports whether the request may write the per-project
// setup_command. That command runs an arbitrary server-side `sh -c` after
// worktree creation (RCE-equivalent), so only loopback single-user mode may set
// it: there the implicit user already controls the machine the command runs on.
// Under JWT auth no caller may set it — the role that used to permit this was
// never grantable, so gating on it admitted nobody.
func (h *Handler) canSetSetupCommand(_ *http.Request) bool {
	return h.bypassAuth
}

// emit broadcasts a typed project event. No-op when broadcaster is nil.
func (h *Handler) emit(eventType, id string, payload any) {
	if h.broadcaster == nil {
		return
	}
	h.broadcaster.Broadcast(sse.ProjectEvent{Type: eventType, ProjectID: id, Payload: payload})
}

// emitProjectUpdated reloads the project and its folders (same path as Get)
// and broadcasts a project_updated event. Errors are silently ignored —
// the HTTP response has already been written by the caller.
func (h *Handler) emitProjectUpdated(r *http.Request, projectID string) {
	if h.broadcaster == nil {
		return
	}
	p, err := h.projects.GetWithFolders(r.Context(), projectID)
	if err != nil {
		return
	}
	folders, _ := p.Edges.FoldersOrErr()
	count := len(folders)
	v := toProjectView(p, &count, folders, p.ID)
	h.emit("project_updated", p.ID, v)
}

// Stream serves GET /api/projects/stream — live project CRUD events.
func (h *Handler) Stream(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return
	}
	sse.WriteHeaders(w)
	flusher.Flush()
	sub := h.broadcaster.Subscribe()
	defer h.broadcaster.Unsubscribe(sub)
	for {
		select {
		case data, ok := <-sub:
			if !ok {
				return
			}
			w.Write(data) //nolint:errcheck
			flusher.Flush()
		case <-r.Context().Done():
			return
		}
	}
}

// Mount registers all project + folder routes on r. Caller is responsible
// for putting r inside a JWT-protected group.
func (h *Handler) Mount(r chi.Router) {
	r.Get("/api/projects", apierr.ErrorMiddleware(h.List))
	r.Post("/api/projects", apierr.ErrorMiddleware(h.Create))
	r.Get("/api/projects/{id}", apierr.ErrorMiddleware(h.Get))
	r.Patch("/api/projects/{id}", apierr.ErrorMiddleware(h.Update))
	r.Delete("/api/projects/{id}", apierr.ErrorMiddleware(h.Delete))

	r.Get("/api/projects/{id}/folders", apierr.ErrorMiddleware(h.ListFolders))
	r.Post("/api/projects/{id}/folders", apierr.ErrorMiddleware(h.CreateFolder))
	r.Get("/api/projects/{id}/folders/suggest", apierr.ErrorMiddleware(h.SuggestFolders))
	r.Patch("/api/projects/{id}/folders/{folderId}", apierr.ErrorMiddleware(h.UpdateFolder))
	r.Delete("/api/projects/{id}/folders/{folderId}", apierr.ErrorMiddleware(h.DeleteFolder))
}

// projectView is the JSON shape returned by all project endpoints.
type projectView struct {
	ID               string       `json:"id"`
	Slug             string       `json:"slug"`
	Name             string       `json:"name"`
	Description      *string      `json:"description,omitempty"`
	Color            *string      `json:"color,omitempty"`
	DefaultSpawnerID *string      `json:"defaultSpawnerId,omitempty"`
	SetupCommand     *string      `json:"setupCommand,omitempty"`
	FolderCount      *int         `json:"folderCount,omitempty"`
	Folders          []folderView `json:"folders,omitempty"`
	CreatedAt        string       `json:"createdAt"`
	UpdatedAt        string       `json:"updatedAt"`
}

type folderView struct {
	ID        string  `json:"id"`
	ProjectID string  `json:"projectId"`
	Path      string  `json:"path"`
	Label     *string `json:"label,omitempty"`
	IsDefault bool    `json:"isDefault"`
	CreatedAt string  `json:"createdAt"`
}

const isoFormat = "2006-01-02T15:04:05Z"

func tsFmt(t time.Time) string { return t.UTC().Format(isoFormat) }

func toProjectView(p *ent.Project, folderCount *int, folders []*ent.ProjectFolder, projectIDForFolders string) projectView {
	pv := projectView{
		ID:               p.ID,
		Slug:             p.Slug,
		Name:             p.Name,
		Description:      p.Description,
		Color:            p.Color,
		DefaultSpawnerID: p.DefaultSpawnerID,
		SetupCommand:     p.SetupCommand,
		FolderCount:      folderCount,
		CreatedAt:        tsFmt(p.CreatedAt),
		UpdatedAt:        tsFmt(p.UpdatedAt),
	}
	if folders != nil {
		fs := make([]folderView, len(folders))
		for i, f := range folders {
			fs[i] = toFolderView(f, projectIDForFolders)
		}
		pv.Folders = fs
	}
	return pv
}

func toFolderView(f *ent.ProjectFolder, projectID string) folderView {
	return folderView{
		ID:        f.ID,
		ProjectID: projectID,
		Path:      f.Path,
		Label:     f.Label,
		IsDefault: f.IsDefault,
		CreatedAt: tsFmt(f.CreatedAt),
	}
}

// List returns all projects with their folder counts.
// GET /api/projects
func (h *Handler) List(w http.ResponseWriter, r *http.Request) error {
	rows, err := h.projects.ListWithFolderCount(r.Context())
	if err != nil {
		return err
	}
	out := make([]projectView, len(rows))
	for i, row := range rows {
		count := row.FolderCount
		out[i] = toProjectView(row.Project, &count, nil, "")
	}
	apierr.WriteJSON(w, http.StatusOK, out)
	return nil
}

type createProjectBody struct {
	Name             string  `json:"name"`
	Slug             string  `json:"slug"`
	Description      *string `json:"description"`
	Color            *string `json:"color"`
	DefaultSpawnerID *string `json:"defaultSpawnerId"`
	SetupCommand     *string `json:"setupCommand"`
}

// Create creates a new project.
// POST /api/projects
func (h *Handler) Create(w http.ResponseWriter, r *http.Request) error {
	var body createProjectBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		return apierr.NewAppError(http.StatusBadRequest, "invalid JSON body")
	}
	if body.Name == "" {
		return apierr.NewAppError(http.StatusBadRequest, "name is required")
	}
	if !ValidateName(body.Name) {
		return apierr.NewAppError(http.StatusBadRequest, validation.ProjectNameLengthMessage)
	}
	if body.Description != nil && !ValidateDescription(*body.Description) {
		return apierr.NewAppError(http.StatusBadRequest, validation.ProjectDescriptionLengthMessage)
	}
	if !ValidateSlug(body.Slug) {
		return apierr.NewAppError(http.StatusBadRequest, validation.SlugPatternMessage)
	}
	if body.Color != nil && *body.Color != "" && !ValidateColor(*body.Color) {
		return apierr.NewAppError(http.StatusBadRequest, "color must be #rgb or #rrggbb hex")
	}
	if body.SetupCommand != nil && !h.canSetSetupCommand(r) {
		return apierr.NewAppError(http.StatusForbidden, "setupCommand requires admin privileges")
	}
	p, err := h.projects.Create(r.Context(), body.Name, body.Slug, body.Description, body.Color, body.DefaultSpawnerID, body.SetupCommand)
	if err != nil {
		if ent.IsConstraintError(err) {
			return apierr.NewAppError(http.StatusConflict, "slug already exists")
		}
		return err
	}
	v := toProjectView(p, nil, nil, "")
	h.emit("project_created", p.ID, v)
	apierr.WriteJSON(w, http.StatusCreated, v)
	return nil
}

// Get returns a single project with its folders embedded.
// GET /api/projects/{id}
func (h *Handler) Get(w http.ResponseWriter, r *http.Request) error {
	id := chi.URLParam(r, "id")
	p, err := h.projects.GetWithFolders(r.Context(), id)
	if err != nil {
		if ent.IsNotFound(err) {
			return apierr.NewAppError(http.StatusNotFound, "project not found")
		}
		return err
	}
	folders, _ := p.Edges.FoldersOrErr()
	count := len(folders)
	apierr.WriteJSON(w, http.StatusOK, toProjectView(p, &count, folders, p.ID))
	return nil
}

type updateProjectBody struct {
	Name             *string         `json:"name"`
	Slug             *string         `json:"slug"`
	Description      json.RawMessage `json:"description"`
	Color            json.RawMessage `json:"color"`
	DefaultSpawnerID json.RawMessage `json:"defaultSpawnerId"`
	SetupCommand     json.RawMessage `json:"setupCommand"`
}

// Update partially updates a project. JSON `null` clears the field; absent
// keeps the existing value.
// PATCH /api/projects/{id}
func (h *Handler) Update(w http.ResponseWriter, r *http.Request) error {
	id := chi.URLParam(r, "id")
	var body updateProjectBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		return apierr.NewAppError(http.StatusBadRequest, "invalid JSON body")
	}
	if body.Name != nil && *body.Name == "" {
		return apierr.NewAppError(http.StatusBadRequest, "name is required")
	}
	if body.Name != nil && !ValidateName(*body.Name) {
		return apierr.NewAppError(http.StatusBadRequest, validation.ProjectNameLengthMessage)
	}
	if body.Slug != nil && !ValidateSlug(*body.Slug) {
		return apierr.NewAppError(http.StatusBadRequest, validation.SlugPatternMessage)
	}
	if len(body.SetupCommand) != 0 && !h.canSetSetupCommand(r) {
		return apierr.NewAppError(http.StatusForbidden, "setupCommand requires admin privileges")
	}

	description, clearDescription, err := parseNullableString(body.Description)
	if err != nil {
		return apierr.NewAppError(http.StatusBadRequest, "description must be a string or null")
	}
	if description != nil && !ValidateDescription(*description) {
		return apierr.NewAppError(http.StatusBadRequest, validation.ProjectDescriptionLengthMessage)
	}
	color, clearColor, err := parseNullableString(body.Color)
	if err != nil {
		return apierr.NewAppError(http.StatusBadRequest, "color must be a string or null")
	}
	if color != nil && *color != "" && !ValidateColor(*color) {
		return apierr.NewAppError(http.StatusBadRequest, "color must be #rgb or #rrggbb hex")
	}
	defaultSpawnerID, clearDefaultSpawner, err := parseNullableString(body.DefaultSpawnerID)
	if err != nil {
		return apierr.NewAppError(http.StatusBadRequest, "defaultSpawnerId must be a string or null")
	}
	setupCommand, clearSetupCommand, err := parseNullableString(body.SetupCommand)
	if err != nil {
		return apierr.NewAppError(http.StatusBadRequest, "setupCommand must be a string or null")
	}

	p, err := h.projects.Update(r.Context(), id,
		body.Name, body.Slug,
		description, color, defaultSpawnerID, setupCommand,
		clearDescription, clearColor, clearDefaultSpawner, clearSetupCommand,
	)
	if err != nil {
		if ent.IsNotFound(err) {
			return apierr.NewAppError(http.StatusNotFound, "project not found")
		}
		if ent.IsConstraintError(err) {
			return apierr.NewAppError(http.StatusConflict, "slug already exists")
		}
		return err
	}
	v := toProjectView(p, nil, nil, "")
	h.emit("project_updated", p.ID, v)
	apierr.WriteJSON(w, http.StatusOK, v)
	return nil
}

// Delete removes a project. Refuses (409) if any task is in a non-terminal
// stage. Tasks in done/cancelled have their project_id cleared first.
// DELETE /api/projects/{id}
func (h *Handler) Delete(w http.ResponseWriter, r *http.Request) error {
	id := chi.URLParam(r, "id")
	if _, err := h.projects.GetByID(r.Context(), id); err != nil {
		if ent.IsNotFound(err) {
			return apierr.NewAppError(http.StatusNotFound, "project not found")
		}
		return err
	}
	if h.tasks != nil {
		active, err := h.tasks.CountActiveByProject(r.Context(), id)
		if err != nil {
			return err
		}
		if active > 0 {
			return apierr.NewAppError(http.StatusConflict, "project has active tasks")
		}
		if err := h.tasks.ClearProjectForTerminalTasks(r.Context(), id); err != nil {
			return err
		}
	}
	if err := h.projects.Delete(r.Context(), id); err != nil {
		if ent.IsNotFound(err) {
			return apierr.NewAppError(http.StatusNotFound, "project not found")
		}
		return err
	}
	h.emit("project_deleted", id, nil)
	w.WriteHeader(http.StatusNoContent)
	return nil
}

// parseNullableString decodes a PATCH field that may be absent, JSON null, or
// a string. Returns (value, clear, err): clear=true when JSON null was sent.
func parseNullableString(raw json.RawMessage) (*string, bool, error) {
	if len(raw) == 0 {
		return nil, false, nil
	}
	s := string(raw)
	if s == "null" {
		return nil, true, nil
	}
	var v string
	if err := json.Unmarshal(raw, &v); err != nil {
		return nil, false, err
	}
	return &v, false, nil
}
