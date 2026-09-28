package applications

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/lx-wnk/kontor/server/internal/apierr"
	"github.com/lx-wnk/kontor/server/internal/appsetup"
	"github.com/lx-wnk/kontor/server/internal/auth"
	"github.com/lx-wnk/kontor/server/internal/capability"
	"github.com/lx-wnk/kontor/server/internal/channelconfig"
	"github.com/lx-wnk/kontor/server/internal/claudeconfig"
	"github.com/lx-wnk/kontor/server/internal/db/ent"
	"github.com/lx-wnk/kontor/server/internal/db/repo"
	"github.com/lx-wnk/kontor/server/internal/mcpapps"
	"github.com/lx-wnk/kontor/server/internal/validation"
)

var envNameRE = regexp.MustCompile(`^[A-Z_][A-Z0-9_]*$`)

type Handler struct {
	apps         repo.MCPApplicationRepo
	secrets      repo.ApplicationSecretRepo
	refresher    mcpapps.Refresher
	grants       repo.GrantRepo
	resources    repo.ResourceRepo
	schedules    repo.TaskScheduleRepo
	setup        SetupRunner
	tools        ToolCaller
	capabilities repo.CapabilityRepo
}

// ToolCaller runs one tool on an application's own MCP server. It is an
// interface so a route can be tested without launching a server, and because
// the handler has no business knowing how a transport is built.
type ToolCaller interface {
	Call(ctx context.Context, entry mcpapps.ServerEntry, env map[string]string, tool string, args map[string]any) (json.RawMessage, error)
}

// StdioToolCaller is the production ToolCaller: it starts the application's
// own server over stdio and calls the tool there.
type StdioToolCaller struct{}

func (StdioToolCaller) Call(ctx context.Context, entry mcpapps.ServerEntry, env map[string]string, tool string, args map[string]any) (json.RawMessage, error) {
	transport, err := mcpapps.StdioTransport(entry, env)
	if err != nil {
		return nil, err
	}
	return mcpapps.CallTool(ctx, transport, tool, args)
}

// SetupRunner is the part of appsetup.Manager these routes use. It is an
// interface so a test can drive the routes without starting a real process —
// the manager's own behaviour is covered by its package's tests.
type SetupRunner interface {
	Start(ctx context.Context, resourceID string, setup mcpapps.PresetSetup, env map[string]string) (appsetup.Session, error)
	Stop(resourceID string) error
	Get(resourceID string) (appsetup.Session, bool)
}

func NewHandler(apps repo.MCPApplicationRepo, secrets repo.ApplicationSecretRepo, refresher mcpapps.Refresher, grants repo.GrantRepo, resources repo.ResourceRepo, schedules repo.TaskScheduleRepo, setup SetupRunner, tools ToolCaller, capabilities repo.CapabilityRepo) *Handler {
	if tools == nil {
		tools = StdioToolCaller{}
	}
	return &Handler{apps: apps, secrets: secrets, refresher: refresher, grants: grants, resources: resources, schedules: schedules, setup: setup, tools: tools, capabilities: capabilities}
}

func (h *Handler) Mount(r chi.Router) {
	r.Get("/api/applications", apierr.ErrorMiddleware(h.list))
	r.Post("/api/applications", apierr.ErrorMiddleware(h.create))
	r.Get("/api/applications/drift", apierr.ErrorMiddleware(h.drift))
	r.Post("/api/applications/import", apierr.ErrorMiddleware(h.importApplication))
	r.Patch("/api/applications/{resourceId}", apierr.ErrorMiddleware(h.patch))
	r.Put("/api/applications/{resourceId}/secrets/{envName}", apierr.ErrorMiddleware(h.putSecret))
	r.Delete("/api/applications/{resourceId}/secrets/{envName}", apierr.ErrorMiddleware(h.deleteSecret))
	r.Post("/api/applications/{resourceId}/refresh", apierr.ErrorMiddleware(h.refresh))
	r.Post("/api/applications/{resourceId}/denies", apierr.ErrorMiddleware(h.denies))
	r.Post("/api/applications/{resourceId}/setup", apierr.ErrorMiddleware(h.startSetup))
	r.Delete("/api/applications/{resourceId}/setup", apierr.ErrorMiddleware(h.stopSetup))
	r.Get("/api/applications/{resourceId}/setup", apierr.ErrorMiddleware(h.getSetup))
	r.Post("/api/applications/{resourceId}/accounts", apierr.ErrorMiddleware(h.accounts))
	r.Delete("/api/applications/{resourceId}", apierr.ErrorMiddleware(h.delete))
}

type toolView struct {
	Capability      string `json:"capability"`
	Name            string `json:"name"`
	Description     string `json:"description,omitempty"`
	ReadOnlyHint    bool   `json:"readOnlyHint"`
	DestructiveHint *bool  `json:"destructiveHint,omitempty"`
	// State is what happens when an agent reaches for this tool with nothing
	// but the global context to go on: "denied", "asks" or "allowed". It is
	// resolved with the same capability.Decide a run's allow list uses.
	State string `json:"state"`
	// AllowedIn names the contexts that carry a live allow grant, so a tool
	// that is denied by default but allowed for one routine says so instead of
	// looking simply forbidden.
	AllowedIn []string `json:"allowedIn"`
}

type secretView struct {
	EnvName   string `json:"envName"`
	UpdatedAt string `json:"updatedAt"`
}

type applicationView struct {
	ResourceID           string              `json:"resourceId"`
	ServerName           string              `json:"serverName"`
	AttachAll            bool                `json:"attachAll"`
	RequiredEnv          []string            `json:"requiredEnv"`
	Entry                mcpapps.ServerEntry `json:"entry"`
	ExportToClaude       bool                `json:"exportToClaude"`
	Secrets              []secretView        `json:"secrets"`
	Tools                []toolView          `json:"tools"`
	CatalogueError       string              `json:"catalogueError,omitempty"`
	CatalogueRefreshedAt *string             `json:"catalogueRefreshedAt,omitempty"`
}

func (h *Handler) view(r *http.Request, app *ent.MCPApplication) (applicationView, error) {
	meta, err := h.secrets.List(r.Context(), app.ResourceID)
	if err != nil {
		return applicationView{}, err
	}
	v := applicationView{
		ResourceID:     app.ResourceID,
		ServerName:     app.ServerName,
		AttachAll:      app.AttachAll,
		RequiredEnv:    app.RequiredEnv,
		ExportToClaude: app.ExportToClaude,
		Secrets:        make([]secretView, 0, len(meta)),
		Tools:          make([]toolView, 0, len(app.Catalogue)),
		CatalogueError: app.CatalogueError,
	}
	if v.RequiredEnv == nil {
		v.RequiredEnv = []string{}
	}
	if !mcpapps.IsEmptyEntry(app.Entry) {
		entry, err := mcpapps.ParseEntry(app.Entry)
		if err != nil {
			return applicationView{}, err
		}
		v.Entry = entry
	}
	for _, m := range meta {
		v.Secrets = append(v.Secrets, secretView{EnvName: m.EnvName, UpdatedAt: m.UpdatedAt.UTC().Format(time.RFC3339)})
	}
	for _, t := range app.Catalogue {
		capName := mcpapps.CapabilityName(app.ServerName, t.Name)
		state, allowedIn := h.toolState(r.Context(), capName)
		v.Tools = append(v.Tools, toolView{
			State:           state,
			AllowedIn:       allowedIn,
			Capability:      capName,
			Name:            t.Name,
			Description:     t.Description,
			ReadOnlyHint:    t.ReadOnlyHint,
			DestructiveHint: t.DestructiveHint,
		})
	}
	if app.CatalogueRefreshedAt != nil {
		s := app.CatalogueRefreshedAt.UTC().Format(time.RFC3339)
		v.CatalogueRefreshedAt = &s
	}
	return v, nil
}

func (h *Handler) load(r *http.Request) (*ent.MCPApplication, error) {
	app, err := h.apps.GetByResourceID(r.Context(), chi.URLParam(r, "resourceId"))
	if ent.IsNotFound(err) {
		return nil, apierr.ErrNotFound
	}
	return app, err
}

func writeJSON(w http.ResponseWriter, status int, v any) error {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	return json.NewEncoder(w).Encode(v)
}

func (h *Handler) list(w http.ResponseWriter, r *http.Request) error {
	apps, err := h.apps.List(r.Context())
	if err != nil {
		return err
	}
	out := make([]applicationView, 0, len(apps))
	for _, app := range apps {
		v, err := h.view(r, app)
		if err != nil {
			return err
		}
		out = append(out, v)
	}
	return writeJSON(w, http.StatusOK, out)
}

func (h *Handler) patch(w http.ResponseWriter, r *http.Request) error {
	app, err := h.load(r)
	if err != nil {
		return err
	}
	var body struct {
		AttachAll      *bool                `json:"attachAll"`
		RequiredEnv    *[]string            `json:"requiredEnv"`
		Entry          *mcpapps.ServerEntry `json:"entry"`
		ExportToClaude *bool                `json:"exportToClaude"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		return apierr.NewAppError(http.StatusBadRequest, "invalid JSON body")
	}
	if body.RequiredEnv != nil {
		for _, name := range *body.RequiredEnv {
			if !envNameRE.MatchString(name) {
				return apierr.NewAppError(http.StatusBadRequest, "requiredEnv: "+name+" is not an environment variable name")
			}
		}
		if app, err = h.apps.SetRequiredEnv(r.Context(), app.ResourceID, *body.RequiredEnv); err != nil {
			return err
		}
	}
	if body.AttachAll != nil {
		if app, err = h.apps.SetAttachAll(r.Context(), app.ResourceID, *body.AttachAll); err != nil {
			return err
		}
	}
	if body.Entry != nil {
		raw, err := mcpapps.MergeEntry(app.Entry, *body.Entry)
		if err != nil {
			return err
		}
		if app, err = h.apps.SetEntry(r.Context(), app.ResourceID, raw); err != nil {
			return err
		}
	}
	// Export runs after the entry write above so a PATCH carrying both entry
	// and exportToClaude mirrors the new entry into Claude's config, not the
	// stale one.
	exportToClaude := app.ExportToClaude
	if body.ExportToClaude != nil {
		exportToClaude = *body.ExportToClaude
	}
	if body.ExportToClaude != nil || (exportToClaude && body.Entry != nil) {
		if app, err = h.applyExport(r, app, exportToClaude); err != nil {
			return err
		}
	}
	v, err := h.view(r, app)
	if err != nil {
		return err
	}
	return writeJSON(w, http.StatusOK, v)
}

// applyExport mirrors app's entry into Claude's own config, or removes it, and
// records the outcome on the row. A write failure answers 502 and leaves the
// database untouched — the flag and hash must not move ahead of the file.
func (h *Handler) applyExport(r *http.Request, app *ent.MCPApplication, export bool) (*ent.MCPApplication, error) {
	if export {
		if err := claudeconfig.WriteServerEntry(app.ServerName, app.Entry); err != nil {
			return nil, apierr.NewAppError(http.StatusBadGateway, "write Claude config: "+err.Error())
		}
		return h.apps.SetExport(r.Context(), app.ResourceID, true, mcpapps.EntryHash(app.Entry))
	}
	if err := claudeconfig.RemoveServerEntry(app.ServerName); err != nil {
		return nil, apierr.NewAppError(http.StatusBadGateway, "write Claude config: "+err.Error())
	}
	return h.apps.SetExport(r.Context(), app.ResourceID, false, "")
}

func (h *Handler) drift(w http.ResponseWriter, r *http.Request) error {
	servers, readErr := claudeconfig.UserMCPServers()
	if readErr != nil {
		// An unreadable config is not "every exported server changed" — it is
		// no information at all, and the panel must not accuse the operator of
		// edits they did not make.
		slog.Warn("applications: Claude config unreadable — drift not reported", "err", readErr)
		return writeJSON(w, http.StatusOK, mcpapps.Drift{Found: []string{}, Changed: []string{}})
	}
	apps, err := h.apps.List(r.Context())
	if err != nil {
		return err
	}
	return writeJSON(w, http.StatusOK, mcpapps.DetectDrift(servers, apps))
}

func (h *Handler) importApplication(w http.ResponseWriter, r *http.Request) error {
	var body struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		return apierr.NewAppError(http.StatusBadRequest, "invalid JSON body")
	}
	if channelconfig.IsReservedServerName(body.Name) {
		return apierr.NewAppError(http.StatusBadRequest, body.Name+" is a reserved server name")
	}
	servers, err := claudeconfig.UserMCPServers()
	if err != nil {
		return err
	}
	entry, ok := servers[body.Name]
	if !ok {
		return apierr.NewAppError(http.StatusNotFound, "no server named \""+body.Name+"\" in Claude's config")
	}

	existing, err := h.apps.List(r.Context())
	if err != nil {
		return err
	}
	for _, app := range existing {
		if app.ServerName == body.Name {
			return apierr.NewAppError(http.StatusConflict, body.Name+" already exists")
		}
	}

	res, err := mcpapps.EnsureResource(r.Context(), h.resources, body.Name)
	if err != nil {
		return err
	}
	app, err := h.apps.Upsert(r.Context(), repo.UpsertMCPApplicationInput{
		ResourceID: res.ID,
		ServerName: body.Name,
		Entry:      entry,
	})
	if err != nil {
		return err
	}
	// A server whose dangerous tools are not denied yet is the state this
	// exists to prevent, so a failure here fails the request.
	if err := h.applyDenies(r, app); err != nil {
		return err
	}
	v, err := h.view(r, app)
	if err != nil {
		return err
	}
	return writeJSON(w, http.StatusCreated, v)
}

func (h *Handler) create(w http.ResponseWriter, r *http.Request) error {
	var body struct {
		Name    string            `json:"name"`
		Command string            `json:"command"`
		Args    []string          `json:"args"`
		Env     map[string]string `json:"env"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		return apierr.NewAppError(http.StatusBadRequest, "invalid JSON body")
	}
	if !validation.IsValidSlug(body.Name) {
		return apierr.NewAppError(http.StatusBadRequest, validation.SlugPatternMessage)
	}
	if channelconfig.IsReservedServerName(body.Name) {
		return apierr.NewAppError(http.StatusBadRequest, body.Name+" is a reserved server name")
	}
	if body.Command == "" {
		return apierr.NewAppError(http.StatusBadRequest, "command is required")
	}
	for name := range body.Env {
		if !envNameRE.MatchString(name) {
			return apierr.NewAppError(http.StatusBadRequest, "env: "+name+" is not an environment variable name")
		}
	}

	existing, err := h.apps.List(r.Context())
	if err != nil {
		return err
	}
	for _, app := range existing {
		if app.ServerName == body.Name {
			return apierr.NewAppError(http.StatusConflict, body.Name+" already exists")
		}
	}

	res, err := mcpapps.EnsureResource(r.Context(), h.resources, body.Name)
	if err != nil {
		return err
	}
	entry, err := json.Marshal(mcpapps.ServerEntry{Command: body.Command, Args: body.Args, Env: body.Env})
	if err != nil {
		return err
	}
	app, err := h.apps.Upsert(r.Context(), repo.UpsertMCPApplicationInput{
		ResourceID: res.ID,
		ServerName: body.Name,
		Entry:      entry,
	})
	if err != nil {
		return err
	}
	// A server whose dangerous tools are not denied yet is the state this
	// exists to prevent, so a failure here fails the request.
	if err := h.applyDenies(r, app); err != nil {
		return err
	}
	v, err := h.view(r, app)
	if err != nil {
		return err
	}
	return writeJSON(w, http.StatusCreated, v)
}

func (h *Handler) putSecret(w http.ResponseWriter, r *http.Request) error {
	app, err := h.load(r)
	if err != nil {
		return err
	}
	name := chi.URLParam(r, "envName")
	if !envNameRE.MatchString(name) {
		return apierr.NewAppError(http.StatusBadRequest, name+" is not an environment variable name")
	}
	var body struct {
		Value string `json:"value"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Value == "" {
		return apierr.NewAppError(http.StatusBadRequest, "value is required")
	}
	if err := h.secrets.Set(r.Context(), app.ResourceID, name, body.Value); err != nil {
		if errors.Is(err, repo.ErrSecretsUnavailable) {
			return apierr.NewAppError(http.StatusServiceUnavailable, err.Error())
		}
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

func (h *Handler) deleteSecret(w http.ResponseWriter, r *http.Request) error {
	app, err := h.load(r)
	if err != nil {
		return err
	}
	if err := h.secrets.Delete(r.Context(), app.ResourceID, chi.URLParam(r, "envName")); err != nil {
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

func (h *Handler) refresh(w http.ResponseWriter, r *http.Request) error {
	app, err := h.load(r)
	if err != nil {
		return err
	}
	if _, err := h.refresher.Refresh(r.Context(), app.ResourceID); err != nil {
		return apierr.NewAppError(http.StatusBadGateway, "tool catalogue: "+err.Error())
	}
	app, err = h.apps.GetByResourceID(r.Context(), app.ResourceID)
	if err != nil {
		return err
	}
	if err := h.applyDenies(r, app); err != nil {
		return err
	}
	v, err := h.view(r, app)
	if err != nil {
		return err
	}
	return writeJSON(w, http.StatusOK, v)
}

// applyDenies writes the preset's default denies for app. It runs wherever an
// application's definition appears or changes, and is idempotent, so the
// window between adding a server and reading its tool list is never open.
func (h *Handler) applyDenies(r *http.Request, app *ent.MCPApplication) error {
	payload, ok := auth.PayloadFromContext(r.Context())
	if !ok {
		// Missing payload ⟹ bypass mode (DASHBOARD_AUTH=none); act as local admin.
		payload = auth.BypassPayload()
	}
	_, err := mcpapps.ApplyDefaultDenies(r.Context(), h.grants, app, payload.Sub)
	return err
}

func (h *Handler) denies(w http.ResponseWriter, r *http.Request) error {
	app, err := h.load(r)
	if err != nil {
		return err
	}
	payload, ok := auth.PayloadFromContext(r.Context())
	if !ok {
		// Missing payload ⟹ bypass mode (DASHBOARD_AUTH=none); act as local admin.
		payload = auth.BypassPayload()
	}
	res, err := mcpapps.ApplyDefaultDenies(r.Context(), h.grants, app, payload.Sub)
	if err != nil {
		return err
	}
	return writeJSON(w, http.StatusOK, res)
}

func (h *Handler) delete(w http.ResponseWriter, r *http.Request) error {
	app, err := h.load(r)
	if err != nil {
		return err
	}

	schedules, err := h.schedules.ListForUser(r.Context(), "", true)
	if err != nil {
		return err
	}
	var attachedTo []string
	for _, s := range schedules {
		if slices.Contains(s.Applications, app.ResourceID) {
			attachedTo = append(attachedTo, s.Name)
		}
	}
	if len(attachedTo) > 0 {
		return apierr.NewAppError(http.StatusConflict, "still attached to: "+strings.Join(attachedTo, ", "))
	}

	payload, ok := auth.PayloadFromContext(r.Context())
	if !ok {
		// Missing payload ⟹ bypass mode (DASHBOARD_AUTH=none); act as local admin.
		payload = auth.BypassPayload()
	}
	for _, t := range app.Catalogue {
		capName := mcpapps.CapabilityName(app.ServerName, t.Name)
		grants, err := h.grants.ListForCapability(r.Context(), capName)
		if err != nil {
			return err
		}
		for _, g := range grants {
			if g.RevokedAt != nil {
				continue
			}
			if err := h.grants.Revoke(r.Context(), g.ID, payload.Sub); err != nil {
				return err
			}
		}
	}

	secretMeta, err := h.secrets.List(r.Context(), app.ResourceID)
	if err != nil {
		return err
	}
	for _, m := range secretMeta {
		if err := h.secrets.Delete(r.Context(), app.ResourceID, m.EnvName); err != nil {
			return err
		}
	}

	// Only a mirror the app itself wrote is taken back out of Claude's config;
	// a server the operator registered there stays, and the panel then offers
	// it as found rather than silently losing it.
	if app.ExportToClaude {
		if err := claudeconfig.RemoveServerEntry(app.ServerName); err != nil {
			return apierr.NewAppError(http.StatusBadGateway, "write Claude config: "+err.Error())
		}
	}
	if err := h.apps.Delete(r.Context(), app.ResourceID); err != nil {
		return err
	}
	if _, err := h.resources.SetState(r.Context(), app.ResourceID, repo.ResourceStateOrphaned); err != nil {
		return err
	}

	w.WriteHeader(http.StatusNoContent)
	return nil
}

// startSetup runs the application's preset setup wizard as a child process so
// an operator can complete first-time configuration, replacing any setup
// already running for this application.
func (h *Handler) startSetup(w http.ResponseWriter, r *http.Request) error {
	app, err := h.load(r)
	if err != nil {
		return err
	}
	var entry mcpapps.ServerEntry
	if !mcpapps.IsEmptyEntry(app.Entry) {
		entry, err = mcpapps.ParseEntry(app.Entry)
		if err != nil {
			return err
		}
	}
	preset, ok := mcpapps.FindPreset(entry)
	if !ok || preset.Setup == nil {
		return apierr.NewAppError(http.StatusConflict, "this server has no setup page")
	}
	secretValues, err := h.secrets.Values(r.Context(), app.ResourceID)
	if err != nil {
		return err
	}
	env := make(map[string]string, len(entry.Env)+len(secretValues))
	for k, v := range entry.Env {
		env[k] = v
	}
	for k, v := range secretValues {
		env[k] = v
	}
	sess, err := h.setup.Start(r.Context(), app.ResourceID, *preset.Setup, env)
	if err != nil {
		return apierr.NewAppError(http.StatusBadGateway, err.Error())
	}
	return writeJSON(w, http.StatusOK, sess)
}

func (h *Handler) stopSetup(w http.ResponseWriter, r *http.Request) error {
	app, err := h.load(r)
	if err != nil {
		return err
	}
	if err := h.setup.Stop(app.ResourceID); err != nil {
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

func (h *Handler) getSetup(w http.ResponseWriter, r *http.Request) error {
	app, err := h.load(r)
	if err != nil {
		return err
	}
	sess, ok := h.setup.Get(app.ResourceID)
	if !ok {
		return apierr.ErrNotFound
	}
	return writeJSON(w, http.StatusOK, sess)
}

// accounts asks the application's own server which accounts it knows and
// answers with the secret names those accounts need — never the accounts
// themselves, and nothing is stored. The operator fills the names through the
// secrets route.
//
// The tool call is an operator action, so it consults no grant: a human
// clicked it, and no agent is involved.
func (h *Handler) accounts(w http.ResponseWriter, r *http.Request) error {
	app, err := h.load(r)
	if err != nil {
		return err
	}
	var entry mcpapps.ServerEntry
	if !mcpapps.IsEmptyEntry(app.Entry) {
		if entry, err = mcpapps.ParseEntry(app.Entry); err != nil {
			return err
		}
	}
	preset, ok := mcpapps.FindPreset(entry)
	if !ok || len(preset.SecretTemplates) == 0 {
		return apierr.NewAppError(http.StatusConflict, "this server declares no per-account secrets")
	}
	values, err := h.secrets.Values(r.Context(), app.ResourceID)
	if err != nil {
		return err
	}
	raw, err := h.tools.Call(r.Context(), entry, values, "imap_list_accounts", nil)
	if err != nil {
		return apierr.NewAppError(http.StatusBadGateway, err.Error())
	}
	names, err := mcpapps.AccountNames(raw)
	if err != nil {
		return apierr.NewAppError(http.StatusBadGateway, err.Error())
	}
	return writeJSON(w, http.StatusOK, map[string]any{"names": mcpapps.SecretNamesForAccounts(preset.SecretTemplates, names)})
}

// toolState answers what a tool does today: its effect in the global context,
// and the contexts where a live allow grant exists. One capability.Decide per
// tool, the same resolution a run's allow list is built from — the panel must
// never describe a rule the spawner does not follow.
func (h *Handler) toolState(ctx context.Context, capName string) (state string, allowedIn []string) {
	allowedIn = []string{}
	if h.grants == nil || h.capabilities == nil {
		return "asks", allowedIn
	}
	decision, err := mcpapps.Decide(ctx, h.grants, h.capabilities, capName,
		[]capability.Context{{Kind: repo.GrantContextGlobal}})
	switch {
	case err != nil:
		state = "asks"
	case decision.Effect == capability.EffectDeny:
		state = "denied"
	case decision.Effect == capability.EffectAllow:
		state = "allowed"
	default:
		state = "asks"
	}

	rows, err := h.grants.ListForCapability(ctx, capName)
	if err != nil {
		return state, allowedIn
	}
	seen := map[string]bool{}
	for _, g := range rows {
		if g.RevokedAt != nil || g.Mode != repo.GrantModeAllow {
			continue
		}
		label := g.ContextKind
		if g.ContextRef != "" {
			label += ":" + g.ContextRef
		}
		if !seen[label] {
			seen[label] = true
			allowedIn = append(allowedIn, label)
		}
	}
	return state, allowedIn
}
