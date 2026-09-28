package applications_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/require"

	"github.com/lx-wnk/kontor/server/internal/api/applications"
	"github.com/lx-wnk/kontor/server/internal/appsetup"
	"github.com/lx-wnk/kontor/server/internal/claudeconfig"
	"github.com/lx-wnk/kontor/server/internal/db"
	"github.com/lx-wnk/kontor/server/internal/db/ent"
	"github.com/lx-wnk/kontor/server/internal/db/ent/schema"
	"github.com/lx-wnk/kontor/server/internal/db/repo"
	"github.com/lx-wnk/kontor/server/internal/mcpapps"
	"github.com/lx-wnk/kontor/server/internal/secretbox"
)

func newMux(t *testing.T) (*chi.Mux, repo.MCPApplicationRepo, repo.GrantRepo, repo.ApplicationSecretRepo, repo.ResourceRepo, repo.TaskScheduleRepo) {
	t.Helper()
	bundle, err := db.Open(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { _ = bundle.Client.Close() })
	box, err := secretbox.New(make([]byte, 32))
	require.NoError(t, err)
	apps := repo.NewMCPApplicationRepo(bundle.Client)
	secrets := repo.NewApplicationSecretRepo(bundle.Client, box)
	grants := repo.NewGrantRepo(bundle.Client)
	resources := repo.NewResourceRepo(bundle.Client)
	schedules := repo.NewTaskScheduleRepo(bundle.Client)
	_, err = apps.Upsert(context.Background(), repo.UpsertMCPApplicationInput{ResourceID: "res-mail", ServerName: "mail"})
	require.NoError(t, err)
	mux := chi.NewRouter()
	applications.NewHandler(apps, secrets, mcpapps.Refresher{Now: time.Now}, grants, resources, schedules, appsetup.NewManager(appsetup.Options{}), nil, repo.NewCapabilityRepo(bundle.Client)).Mount(mux)
	return mux, apps, grants, secrets, resources, schedules
}

func do(t *testing.T, mux http.Handler, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var buf bytes.Buffer
	if body != nil {
		require.NoError(t, json.NewEncoder(&buf).Encode(body))
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(method, path, &buf))
	return rec
}

func TestSecrets_AreWriteOnly(t *testing.T) {
	mux, _, _, _, _, _ := newMux(t)
	rec := do(t, mux, http.MethodPut, "/api/applications/res-mail/secrets/MAIL_PASSWORD", map[string]string{"value": "hunter2"})
	require.Equal(t, http.StatusNoContent, rec.Code, rec.Body.String())

	rec = do(t, mux, http.MethodGet, "/api/applications", nil)
	require.Equal(t, http.StatusOK, rec.Code)
	require.NotContains(t, rec.Body.String(), "hunter2", "a secret value must never leave the server")
	require.Contains(t, rec.Body.String(), `"envName":"MAIL_PASSWORD"`)
}

func TestSecrets_RejectInvalidVariableNames(t *testing.T) {
	mux, _, _, _, _, _ := newMux(t)
	rec := do(t, mux, http.MethodPut, "/api/applications/res-mail/secrets/not-an-env-name", map[string]string{"value": "x"})
	require.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestPatch_SetsAttachAllAndRequiredEnv(t *testing.T) {
	mux, apps, _, _, _, _ := newMux(t)
	rec := do(t, mux, http.MethodPatch, "/api/applications/res-mail", map[string]any{
		"attachAll": true, "requiredEnv": []string{"MAIL_PASSWORD"},
	})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	app, err := apps.GetByResourceID(context.Background(), "res-mail")
	require.NoError(t, err)
	require.True(t, app.AttachAll)
	require.Equal(t, []string{"MAIL_PASSWORD"}, app.RequiredEnv)
}

func TestUnknownApplicationIs404(t *testing.T) {
	mux, _, _, _, _, _ := newMux(t)
	rec := do(t, mux, http.MethodPatch, "/api/applications/nope", map[string]any{"attachAll": true})
	require.Equal(t, http.StatusNotFound, rec.Code)
	require.False(t, strings.Contains(rec.Body.String(), "panic"))
}

func TestDenies_AppliesPresetAndIsIdempotent(t *testing.T) {
	mux, apps, grants, _, _, _ := newMux(t)
	ctx := context.Background()
	_, err := apps.SetEntry(ctx, "res-mail", json.RawMessage(`{"command":"npx","args":["-y","imap-mcp-server"]}`))
	require.NoError(t, err)

	rec := do(t, mux, http.MethodPost, "/api/applications/res-mail/denies", nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var first mcpapps.DenyResult
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &first))
	require.Equal(t, "imap-mcp-server", first.Preset)
	require.NotEmpty(t, first.Created)
	require.Empty(t, first.Existing)

	rec = do(t, mux, http.MethodPost, "/api/applications/res-mail/denies", nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var second mcpapps.DenyResult
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &second))
	require.Empty(t, second.Created, "applying twice must not duplicate grants")
	require.ElementsMatch(t, first.Created, second.Existing)

	rows, err := grants.List(ctx)
	require.NoError(t, err)
	require.NotEmpty(t, rows)
}

func TestDenies_NoMatchingPresetIsEmptyResult(t *testing.T) {
	mux, _, _, _, _, _ := newMux(t)
	rec := do(t, mux, http.MethodPost, "/api/applications/res-mail/denies", nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var res mcpapps.DenyResult
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &res))
	require.Equal(t, mcpapps.DenyResult{Created: []string{}, Existing: []string{}}, res)
}

func TestDenies_UnknownApplicationIs404(t *testing.T) {
	mux, _, _, _, _, _ := newMux(t)
	rec := do(t, mux, http.MethodPost, "/api/applications/nope/denies", nil)
	require.Equal(t, http.StatusNotFound, rec.Code)
}

func TestCreateApplication_Succeeds(t *testing.T) {
	mux, apps, _, _, _, _ := newMux(t)
	rec := do(t, mux, http.MethodPost, "/api/applications", map[string]any{
		"name":    "notes",
		"command": "uvx",
		"args":    []string{"notes-mcp"},
		"env":     map[string]string{"NOTES_DIR": "/tmp"},
	})
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())

	var view struct {
		ResourceID     string              `json:"resourceId"`
		ServerName     string              `json:"serverName"`
		ExportToClaude bool                `json:"exportToClaude"`
		Entry          mcpapps.ServerEntry `json:"entry"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &view))
	require.Equal(t, "notes", view.ServerName)
	require.False(t, view.ExportToClaude)
	wantEntry := mcpapps.ServerEntry{Command: "uvx", Args: []string{"notes-mcp"}, Env: map[string]string{"NOTES_DIR": "/tmp"}}
	require.Equal(t, wantEntry, view.Entry)

	rec = do(t, mux, http.MethodGet, "/api/applications", nil)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Contains(t, rec.Body.String(), `"serverName":"notes"`)

	app, err := apps.GetByResourceID(context.Background(), view.ResourceID)
	require.NoError(t, err)
	storedEntry, err := mcpapps.ParseEntry(app.Entry)
	require.NoError(t, err)
	require.Equal(t, wantEntry, storedEntry)
}

func TestCreateApplication_InvalidSlugIs400(t *testing.T) {
	mux, _, _, _, _, _ := newMux(t)
	rec := do(t, mux, http.MethodPost, "/api/applications", map[string]any{
		"name": "Notes Server", "command": "uvx",
	})
	require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
	require.Contains(t, rec.Body.String(), "slug must match")
}

func TestCreateApplication_ReservedNameIs400(t *testing.T) {
	for _, name := range []string{"dashboard-channel", "dashboard-tasks"} {
		t.Run(name, func(t *testing.T) {
			mux, _, _, _, _, _ := newMux(t)
			rec := do(t, mux, http.MethodPost, "/api/applications", map[string]any{
				"name": name, "command": "uvx",
			})
			require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
		})
	}
}

func TestCreateApplication_DuplicateNameIs409(t *testing.T) {
	mux, _, _, _, _, _ := newMux(t)
	rec := do(t, mux, http.MethodPost, "/api/applications", map[string]any{
		"name": "mail", "command": "uvx",
	})
	require.Equal(t, http.StatusConflict, rec.Code, rec.Body.String())
}

func TestCreateApplication_MissingCommandIs400(t *testing.T) {
	mux, _, _, _, _, _ := newMux(t)
	rec := do(t, mux, http.MethodPost, "/api/applications", map[string]any{
		"name": "notes",
	})
	require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
	require.Contains(t, rec.Body.String(), "command is required")
}

func TestCreateApplication_InvalidEnvKeyIs400(t *testing.T) {
	mux, _, _, _, _, _ := newMux(t)
	rec := do(t, mux, http.MethodPost, "/api/applications", map[string]any{
		"name": "notes", "command": "uvx", "env": map[string]string{"lower": "x"},
	})
	require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
}

func TestPatch_ReplacesEntry(t *testing.T) {
	mux, apps, _, _, _, _ := newMux(t)
	rec := do(t, mux, http.MethodPatch, "/api/applications/res-mail", map[string]any{
		"attachAll": true,
	})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	rec = do(t, mux, http.MethodPatch, "/api/applications/res-mail", map[string]any{
		"entry": map[string]any{"command": "uvx", "args": []string{"x"}},
	})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	app, err := apps.GetByResourceID(context.Background(), "res-mail")
	require.NoError(t, err)
	entry, err := mcpapps.ParseEntry(app.Entry)
	require.NoError(t, err)
	require.Equal(t, mcpapps.ServerEntry{Command: "uvx", Args: []string{"x"}}, entry)
	require.True(t, app.AttachAll)
}

func TestView_NoEntryIsEmptyObject(t *testing.T) {
	mux, _, _, _, _, _ := newMux(t)
	rec := do(t, mux, http.MethodGet, "/api/applications", nil)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Contains(t, rec.Body.String(), `"entry":{}`)
}

func TestPatch_EntryKeepsFieldsTheAppDoesNotEdit(t *testing.T) {
	mux, apps, _, _, _, _ := newMux(t)
	ctx := context.Background()
	_, err := apps.SetEntry(ctx, "res-mail", json.RawMessage(`{"type":"http","url":"https://x","headers":{"A":"b"},"args":["old"]}`))
	require.NoError(t, err)

	rec := do(t, mux, http.MethodPatch, "/api/applications/res-mail", map[string]any{
		"entry": map[string]any{"type": "http", "url": "https://y"},
	})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	app, err := apps.GetByResourceID(ctx, "res-mail")
	require.NoError(t, err)
	var obj map[string]any
	require.NoError(t, json.Unmarshal(app.Entry, &obj))
	require.Equal(t, map[string]any{"A": "b"}, obj["headers"], "a key the app does not edit must survive an edit")
	require.Equal(t, "https://y", obj["url"])
	require.NotContains(t, obj, "args", "a field cleared in the form must be removed")
}

// deletableApp gives res-mail everything a delete has to clean up: a registry
// row to orphan, a catalogue so there is a capability to revoke, a live grant
// on it and one secret.
func deletableApp(t *testing.T, apps repo.MCPApplicationRepo, grants repo.GrantRepo, secrets repo.ApplicationSecretRepo, resources repo.ResourceRepo) string {
	t.Helper()
	ctx := context.Background()
	res, err := resources.Upsert(ctx, repo.UpsertResourceInput{
		Kind: repo.ResourceKindApplication, Slug: "mcp-mail", Name: "mail",
		Scope: repo.GlobalScope(), State: repo.ResourceStateDiscovered,
		Origin: repo.ResourceOriginLocal, OriginRef: "mail",
	})
	require.NoError(t, err)
	_, err = apps.Upsert(ctx, repo.UpsertMCPApplicationInput{ResourceID: res.ID, ServerName: "notes"})
	require.NoError(t, err)
	require.NoError(t, apps.RecordCatalogue(ctx, res.ID, []schema.CatalogueTool{{Name: "read"}}, "", time.Now()))
	require.NoError(t, secrets.Set(ctx, res.ID, "NOTES_TOKEN", "hunter2"))
	_, err = grants.Create(ctx, repo.CreateGrantInput{
		CapabilityName: mcpapps.CapabilityName("notes", "read"),
		Context:        repo.GrantContextFor("application", res.ID),
		Mode:           repo.GrantModeAllow,
		GrantedBy:      "tester",
	})
	require.NoError(t, err)
	return res.ID
}

func attachRoutine(t *testing.T, schedules repo.TaskScheduleRepo, name, resourceID string) {
	t.Helper()
	_, err := schedules.Create(context.Background(), repo.CreateTaskScheduleInput{
		Name: name, CronExpr: "*/5 * * * *", SlugPrefix: name,
		Title: name, Cwd: "/tmp", Priority: "medium",
		MaxIterations: 20, StageTimeoutSeconds: 1800,
		Applications: []string{resourceID},
	})
	require.NoError(t, err)
}

func liveGrants(t *testing.T, grants repo.GrantRepo, capName string) int {
	t.Helper()
	rows, err := grants.ListForCapability(context.Background(), capName)
	require.NoError(t, err)
	live := 0
	for _, g := range rows {
		if g.RevokedAt == nil {
			live++
		}
	}
	return live
}

func TestDeleteApplication_RemovesRowSecretsAndGrantsAndOrphansTheResource(t *testing.T) {
	mux, apps, grants, secrets, resources, _ := newMux(t)
	ctx := context.Background()
	resID := deletableApp(t, apps, grants, secrets, resources)

	rec := do(t, mux, http.MethodDelete, "/api/applications/"+resID, nil)
	require.Equal(t, http.StatusNoContent, rec.Code, rec.Body.String())

	_, err := apps.GetByResourceID(ctx, resID)
	require.True(t, ent.IsNotFound(err))

	meta, err := secrets.List(ctx, resID)
	require.NoError(t, err)
	require.Empty(t, meta)

	require.Equal(t, 0, liveGrants(t, grants, mcpapps.CapabilityName("notes", "read")))

	res, err := resources.Get(ctx, repo.ResourceKindApplication, repo.GlobalScope(), "mcp-mail")
	require.NoError(t, err, "the resource row must survive so grants anchored to it still resolve")
	require.Equal(t, repo.ResourceStateOrphaned, res.State)
}

func TestDeleteApplication_AttachedToRoutinesIs409AndRemovesNothing(t *testing.T) {
	mux, apps, grants, secrets, resources, schedules := newMux(t)
	ctx := context.Background()
	resID := deletableApp(t, apps, grants, secrets, resources)
	attachRoutine(t, schedules, "inbox", resID)
	attachRoutine(t, schedules, "nightly", resID)

	rec := do(t, mux, http.MethodDelete, "/api/applications/"+resID, nil)
	require.Equal(t, http.StatusConflict, rec.Code)
	require.Contains(t, rec.Body.String(), "inbox")
	require.Contains(t, rec.Body.String(), "nightly")

	_, err := apps.GetByResourceID(ctx, resID)
	require.NoError(t, err, "nothing may be removed while a routine still attaches it")
	meta, err := secrets.List(ctx, resID)
	require.NoError(t, err)
	require.Len(t, meta, 1)
	require.Equal(t, 1, liveGrants(t, grants, mcpapps.CapabilityName("notes", "read")))
}

func TestDeleteApplication_UnknownIdIs404(t *testing.T) {
	mux, _, _, _, _, _ := newMux(t)
	rec := do(t, mux, http.MethodDelete, "/api/applications/res-nope", nil)
	require.Equal(t, http.StatusNotFound, rec.Code)
}

func TestPatch_ExportToClaudeWritesFileAndStoresHash(t *testing.T) {
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
	mux, apps, _, _, _, _ := newMux(t)
	ctx := context.Background()
	_, err := apps.SetEntry(ctx, "res-mail", json.RawMessage(`{"command":"uvx"}`))
	require.NoError(t, err)

	rec := do(t, mux, http.MethodPatch, "/api/applications/res-mail", map[string]any{"exportToClaude": true})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	servers, err := claudeconfig.UserMCPServers()
	require.NoError(t, err)
	require.Contains(t, servers, "mail")

	app, err := apps.GetByResourceID(ctx, "res-mail")
	require.NoError(t, err)
	require.True(t, app.ExportToClaude)
	require.NotEmpty(t, app.ExportedHash)
	require.Equal(t, mcpapps.EntryHash(app.Entry), app.ExportedHash)
}

func TestPatch_UnexportRemovesFileEntryAndClearsHash(t *testing.T) {
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
	mux, apps, _, _, _, _ := newMux(t)
	ctx := context.Background()
	_, err := apps.SetEntry(ctx, "res-mail", json.RawMessage(`{"command":"uvx"}`))
	require.NoError(t, err)
	rec := do(t, mux, http.MethodPatch, "/api/applications/res-mail", map[string]any{"exportToClaude": true})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	rec = do(t, mux, http.MethodPatch, "/api/applications/res-mail", map[string]any{"exportToClaude": false})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	servers, err := claudeconfig.UserMCPServers()
	require.NoError(t, err)
	require.NotContains(t, servers, "mail")

	app, err := apps.GetByResourceID(ctx, "res-mail")
	require.NoError(t, err)
	require.False(t, app.ExportToClaude)
	require.Equal(t, "", app.ExportedHash)
}

func TestPatch_EntryChangeWhileExportedRewritesFileAndHash(t *testing.T) {
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
	mux, apps, _, _, _, _ := newMux(t)
	ctx := context.Background()
	_, err := apps.SetEntry(ctx, "res-mail", json.RawMessage(`{"command":"uvx"}`))
	require.NoError(t, err)
	rec := do(t, mux, http.MethodPatch, "/api/applications/res-mail", map[string]any{"exportToClaude": true})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	rec = do(t, mux, http.MethodPatch, "/api/applications/res-mail", map[string]any{
		"entry": map[string]any{"command": "npx"},
	})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	servers, err := claudeconfig.UserMCPServers()
	require.NoError(t, err)
	var entry mcpapps.ServerEntry
	require.NoError(t, json.Unmarshal(servers["mail"], &entry))
	require.Equal(t, "npx", entry.Command)

	app, err := apps.GetByResourceID(ctx, "res-mail")
	require.NoError(t, err)
	require.Equal(t, mcpapps.EntryHash(app.Entry), app.ExportedHash)
}

func TestPatch_ExportWriteFailureIs502AndLeavesRowUntouched(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", dir)
	target := dir + "/real.json"
	require.NoError(t, os.WriteFile(target, []byte(`{"mcpServers":{}}`), 0o600))
	require.NoError(t, os.Symlink(target, dir+"/.claude.json"))

	mux, apps, _, _, _, _ := newMux(t)
	ctx := context.Background()
	_, err := apps.SetEntry(ctx, "res-mail", json.RawMessage(`{"command":"uvx"}`))
	require.NoError(t, err)

	rec := do(t, mux, http.MethodPatch, "/api/applications/res-mail", map[string]any{"exportToClaude": true})
	require.Equal(t, http.StatusBadGateway, rec.Code, rec.Body.String())

	app, err := apps.GetByResourceID(ctx, "res-mail")
	require.NoError(t, err)
	require.False(t, app.ExportToClaude)
	require.Equal(t, "", app.ExportedHash)
}

func TestDrift_ReportsServerWithNoApplicationRow(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", dir)
	require.NoError(t, os.WriteFile(dir+"/.claude.json", []byte(`{"mcpServers":{"notes":{"command":"x"}}}`), 0o600))
	mux, _, _, _, _, _ := newMux(t)

	rec := do(t, mux, http.MethodGet, "/api/applications/drift", nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.JSONEq(t, `{"found":["notes"],"changed":[]}`, rec.Body.String())
}

func TestImportApplication_CopiesEntryFromClaudeConfig(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", dir)
	require.NoError(t, os.WriteFile(dir+"/.claude.json", []byte(`{"mcpServers":{"notes":{"command":"uvx","args":["notes-mcp"]}}}`), 0o600))
	mux, apps, _, _, _, _ := newMux(t)

	rec := do(t, mux, http.MethodPost, "/api/applications/import", map[string]any{"name": "notes"})
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())

	var view struct {
		ResourceID     string              `json:"resourceId"`
		ServerName     string              `json:"serverName"`
		ExportToClaude bool                `json:"exportToClaude"`
		Entry          mcpapps.ServerEntry `json:"entry"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &view))
	require.Equal(t, "notes", view.ServerName)
	require.False(t, view.ExportToClaude)
	require.Equal(t, mcpapps.ServerEntry{Command: "uvx", Args: []string{"notes-mcp"}}, view.Entry)

	app, err := apps.GetByResourceID(context.Background(), view.ResourceID)
	require.NoError(t, err)
	storedEntry, err := mcpapps.ParseEntry(app.Entry)
	require.NoError(t, err)
	require.Equal(t, mcpapps.ServerEntry{Command: "uvx", Args: []string{"notes-mcp"}}, storedEntry)
}

func TestImportApplication_UnknownNameIs404(t *testing.T) {
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
	mux, _, _, _, _, _ := newMux(t)

	rec := do(t, mux, http.MethodPost, "/api/applications/import", map[string]any{"name": "mail"})
	require.Equal(t, http.StatusNotFound, rec.Code, rec.Body.String())
	var errBody struct {
		Error string `json:"error"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &errBody))
	require.Equal(t, `no server named "mail" in Claude's config`, errBody.Error)
}

func TestImportApplication_ExistingRowIs409(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", dir)
	require.NoError(t, os.WriteFile(dir+"/.claude.json", []byte(`{"mcpServers":{"mail":{"command":"x"}}}`), 0o600))
	mux, _, _, _, _, _ := newMux(t)

	rec := do(t, mux, http.MethodPost, "/api/applications/import", map[string]any{"name": "mail"})
	require.Equal(t, http.StatusConflict, rec.Code, rec.Body.String())
}

func TestImportApplication_ReservedNameIs400(t *testing.T) {
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
	mux, _, _, _, _, _ := newMux(t)

	rec := do(t, mux, http.MethodPost, "/api/applications/import", map[string]any{"name": "dashboard-channel"})
	require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
}

func TestDrift_UnreadableConfigReportsNothingRatherThanEverything(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", dir)
	require.NoError(t, os.WriteFile(dir+"/.claude.json", []byte(`{ not json`), 0o600))
	mux, apps, _, _, _, _ := newMux(t)
	ctx := context.Background()
	_, err := apps.SetEntry(ctx, "res-mail", json.RawMessage(`{"command":"x"}`))
	require.NoError(t, err)
	_, err = apps.SetExport(ctx, "res-mail", true, mcpapps.EntryHash([]byte(`{"command":"x"}`)))
	require.NoError(t, err)

	rec := do(t, mux, http.MethodGet, "/api/applications/drift", nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.JSONEq(t, `{"found":[],"changed":[]}`, rec.Body.String(),
		"a config that cannot be read is no information, not evidence of an edit")
}

func TestDeleteApplication_TakesItsMirrorOutOfClaudeConfig(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", dir)
	require.NoError(t, os.WriteFile(dir+"/.claude.json",
		[]byte(`{"mcpServers":{"notes":{"command":"x"},"mine":{"command":"y"}}}`), 0o600))
	mux, apps, grants, secrets, resources, _ := newMux(t)
	ctx := context.Background()
	resID := deletableApp(t, apps, grants, secrets, resources)
	_, err := apps.SetEntry(ctx, resID, json.RawMessage(`{"command":"y"}`))
	require.NoError(t, err)
	rec := do(t, mux, http.MethodPatch, "/api/applications/"+resID, map[string]any{"exportToClaude": true})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	rec = do(t, mux, http.MethodDelete, "/api/applications/"+resID, nil)
	require.Equal(t, http.StatusNoContent, rec.Code, rec.Body.String())

	servers, err := claudeconfig.UserMCPServers()
	require.NoError(t, err)
	require.NotContains(t, servers, "notes", "a mirror the app wrote is removed with the application")
	require.Contains(t, servers, "mine", "a server the app never mirrored stays")
}

func liveDenies(t *testing.T, grants repo.GrantRepo, capName string) int {
	t.Helper()
	rows, err := grants.ListForCapability(context.Background(), capName)
	require.NoError(t, err)
	n := 0
	for _, g := range rows {
		if g.RevokedAt == nil && g.Mode == repo.GrantModeDeny && g.ContextKind == repo.GrantContextGlobal {
			n++
		}
	}
	return n
}

func TestCreateApplication_DeniesTheDangerousToolsAtOnce(t *testing.T) {
	mux, _, grants, _, _, _ := newMux(t)

	rec := do(t, mux, http.MethodPost, "/api/applications", map[string]any{
		"name": "inbox", "command": "npx", "args": []string{"-y", "imap-mcp-server"},
	})
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())

	require.Equal(t, 1, liveDenies(t, grants, mcpapps.CapabilityName("inbox", "imap_send_email")),
		"a new server's dangerous tools are denied before anyone refreshes its catalogue")
	require.Equal(t, 1, liveDenies(t, grants, mcpapps.CapabilityName("inbox", "imap_bulk_delete")))
}

func TestCreateApplication_UnknownServerGetsNoGrants(t *testing.T) {
	mux, _, grants, _, _, _ := newMux(t)

	rec := do(t, mux, http.MethodPost, "/api/applications", map[string]any{
		"name": "notes", "command": "npx", "args": []string{"-y", "notes-mcp"},
	})
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())

	rows, err := grants.List(context.Background())
	require.NoError(t, err)
	require.Empty(t, rows, "a server with no preset gets no grants at all")
}

func TestImportApplication_DeniesTheDangerousTools(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", dir)
	require.NoError(t, os.WriteFile(dir+"/.claude.json",
		[]byte(`{"mcpServers":{"inbox":{"command":"npx","args":["-y","imap-mcp-server"]}}}`), 0o600))
	mux, _, grants, _, _, _ := newMux(t)

	rec := do(t, mux, http.MethodPost, "/api/applications/import", map[string]any{"name": "inbox"})
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())

	require.Equal(t, 1, liveDenies(t, grants, mcpapps.CapabilityName("inbox", "imap_send_email")))
}

func TestDenies_AppliedTwiceLeavesOneGrant(t *testing.T) {
	mux, _, grants, _, _, _ := newMux(t)
	rec := do(t, mux, http.MethodPost, "/api/applications", map[string]any{
		"name": "inbox", "command": "npx", "args": []string{"-y", "imap-mcp-server"},
	})
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	var created struct {
		ResourceID string `json:"resourceId"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &created))

	rec = do(t, mux, http.MethodPost, "/api/applications/"+created.ResourceID+"/denies", nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	require.Equal(t, 1, liveDenies(t, grants, mcpapps.CapabilityName("inbox", "imap_send_email")),
		"re-applying the denies must not stack a second grant")
}

// fakeSetup records what the routes asked for and answers without starting a
// process. The manager's own behaviour is covered in its package.
type fakeSetup struct {
	started   int
	stopped   int
	lastEnv   map[string]string
	lastSetup mcpapps.PresetSetup
	session   *appsetup.Session
	startErr  error
}

func (f *fakeSetup) Start(_ context.Context, resourceID string, setup mcpapps.PresetSetup, env map[string]string) (appsetup.Session, error) {
	f.started++
	f.lastEnv = env
	f.lastSetup = setup
	if f.startErr != nil {
		return appsetup.Session{}, f.startErr
	}
	sess := appsetup.Session{ResourceID: resourceID, Port: 4711, URL: "http://127.0.0.1:4711", Warning: "Setup is reachable on your local network until you click Done."}
	f.session = &sess
	return sess, nil
}

func (f *fakeSetup) Stop(string) error {
	f.stopped++
	f.session = nil
	return nil
}

func (f *fakeSetup) Get(string) (appsetup.Session, bool) {
	if f.session == nil {
		return appsetup.Session{}, false
	}
	return *f.session, true
}

func newMuxWithSetup(t *testing.T, setup applications.SetupRunner) (*chi.Mux, repo.MCPApplicationRepo, repo.ApplicationSecretRepo) {
	t.Helper()
	bundle, err := db.Open(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { _ = bundle.Client.Close() })
	box, err := secretbox.New(make([]byte, 32))
	require.NoError(t, err)
	apps := repo.NewMCPApplicationRepo(bundle.Client)
	secrets := repo.NewApplicationSecretRepo(bundle.Client, box)
	_, err = apps.Upsert(context.Background(), repo.UpsertMCPApplicationInput{ResourceID: "res-mail", ServerName: "mail"})
	require.NoError(t, err)
	mux := chi.NewRouter()
	applications.NewHandler(apps, secrets, mcpapps.Refresher{Now: time.Now},
		repo.NewGrantRepo(bundle.Client), repo.NewResourceRepo(bundle.Client),
		repo.NewTaskScheduleRepo(bundle.Client), setup, nil, repo.NewCapabilityRepo(bundle.Client)).Mount(mux)
	return mux, apps, secrets
}

func TestStartSetup_ServerWithoutASetupPageIs409(t *testing.T) {
	setup := &fakeSetup{}
	mux, apps, _ := newMuxWithSetup(t, setup)
	_, err := apps.SetEntry(context.Background(), "res-mail", json.RawMessage(`{"command":"npx","args":["-y","notes-mcp"]}`))
	require.NoError(t, err)

	rec := do(t, mux, http.MethodPost, "/api/applications/res-mail/setup", nil)
	require.Equal(t, http.StatusConflict, rec.Code, rec.Body.String())
	require.Contains(t, rec.Body.String(), "no setup page")
	require.Equal(t, 0, setup.started, "nothing may be started for a server that declares no setup")
}

func TestStartSetup_PassesTheEntryEnvAndTheSecrets(t *testing.T) {
	setup := &fakeSetup{}
	mux, apps, secrets := newMuxWithSetup(t, setup)
	ctx := context.Background()
	_, err := apps.SetEntry(ctx, "res-mail", json.RawMessage(`{"command":"npx","args":["-y","imap-mcp-server"],"env":{"IMAP_HOST":"imap.example.com"}}`))
	require.NoError(t, err)
	require.NoError(t, secrets.Set(ctx, "res-mail", "IMAP_MCP_ACCOUNT_WORK_IMAP_PASSWORD", "hunter2"))

	rec := do(t, mux, http.MethodPost, "/api/applications/res-mail/setup", nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Equal(t, 1, setup.started)
	require.Equal(t, "imap.example.com", setup.lastEnv["IMAP_HOST"], "the entry's own environment reaches the setup process")
	require.Equal(t, "hunter2", setup.lastEnv["IMAP_MCP_ACCOUNT_WORK_IMAP_PASSWORD"], "the application's secrets reach it too — the setup page configures the account")
	require.Contains(t, setup.lastSetup.Args, "{port}", "the preset's args are passed through unsubstituted; the manager fills the port")
	require.NotContains(t, rec.Body.String(), "hunter2", "no secret value may appear in the response")
	require.Contains(t, rec.Body.String(), "local network", "the response carries the warning the panel shows")
}

func TestStartSetup_FailureIs502(t *testing.T) {
	setup := &fakeSetup{startErr: errors.New("appsetup: never became ready: setup failed: no config")}
	mux, apps, _ := newMuxWithSetup(t, setup)
	_, err := apps.SetEntry(context.Background(), "res-mail", json.RawMessage(`{"command":"npx","args":["-y","imap-mcp-server"]}`))
	require.NoError(t, err)

	rec := do(t, mux, http.MethodPost, "/api/applications/res-mail/setup", nil)
	require.Equal(t, http.StatusBadGateway, rec.Code)
	require.Contains(t, rec.Body.String(), "setup failed: no config", "the child's own words reach the operator")
}

func TestSetupRoutes_UnknownApplicationIs404(t *testing.T) {
	mux, _, _ := newMuxWithSetup(t, &fakeSetup{})
	for _, method := range []string{http.MethodPost, http.MethodGet, http.MethodDelete} {
		rec := do(t, mux, method, "/api/applications/res-nope/setup", nil)
		require.Equal(t, http.StatusNotFound, rec.Code, method)
	}
}

func TestGetSetup_NothingRunningIs404AndStopIsIdempotent(t *testing.T) {
	setup := &fakeSetup{}
	mux, _, _ := newMuxWithSetup(t, setup)

	rec := do(t, mux, http.MethodGet, "/api/applications/res-mail/setup", nil)
	require.Equal(t, http.StatusNotFound, rec.Code)

	for range 2 {
		rec = do(t, mux, http.MethodDelete, "/api/applications/res-mail/setup", nil)
		require.Equal(t, http.StatusNoContent, rec.Code, rec.Body.String())
	}
	require.Equal(t, 2, setup.stopped, "stopping twice is not an error")
}

type fakeTools struct {
	raw     json.RawMessage
	err     error
	lastEnv map[string]string
	calls   int
}

func (f *fakeTools) Call(_ context.Context, _ mcpapps.ServerEntry, env map[string]string, _ string, _ map[string]any) (json.RawMessage, error) {
	f.calls++
	f.lastEnv = env
	return f.raw, f.err
}

func newMuxWithTools(t *testing.T, tools applications.ToolCaller) (*chi.Mux, repo.MCPApplicationRepo, repo.ApplicationSecretRepo) {
	t.Helper()
	bundle, err := db.Open(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { _ = bundle.Client.Close() })
	box, err := secretbox.New(make([]byte, 32))
	require.NoError(t, err)
	apps := repo.NewMCPApplicationRepo(bundle.Client)
	secrets := repo.NewApplicationSecretRepo(bundle.Client, box)
	_, err = apps.Upsert(context.Background(), repo.UpsertMCPApplicationInput{ResourceID: "res-mail", ServerName: "mail"})
	require.NoError(t, err)
	mux := chi.NewRouter()
	applications.NewHandler(apps, secrets, mcpapps.Refresher{Now: time.Now},
		repo.NewGrantRepo(bundle.Client), repo.NewResourceRepo(bundle.Client),
		repo.NewTaskScheduleRepo(bundle.Client), &fakeSetup{}, tools, repo.NewCapabilityRepo(bundle.Client)).Mount(mux)
	return mux, apps, secrets
}

func imapApp(t *testing.T, apps repo.MCPApplicationRepo) {
	t.Helper()
	_, err := apps.SetEntry(context.Background(), "res-mail",
		json.RawMessage(`{"command":"npx","args":["-y","imap-mcp-server"]}`))
	require.NoError(t, err)
}

func TestAccounts_DerivesOneSecretNamePerTemplatePerAccount(t *testing.T) {
	tools := &fakeTools{raw: json.RawMessage(`{"accounts":[{"id":"a1","name":"work@example.com"},{"id":"a2","name":"Work Gmail"}]}`)}
	mux, apps, secrets := newMuxWithTools(t, tools)
	imapApp(t, apps)
	require.NoError(t, secrets.Set(context.Background(), "res-mail", "IMAP_HOST", "imap.example.com"))

	rec := do(t, mux, http.MethodPost, "/api/applications/res-mail/accounts", nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.JSONEq(t, `{"names":[
		"IMAP_MCP_ACCOUNT_WORK_EXAMPLE_COM_IMAP_PASSWORD",
		"IMAP_MCP_ACCOUNT_WORK_GMAIL_IMAP_PASSWORD",
		"IMAP_MCP_ACCOUNT_WORK_EXAMPLE_COM_SMTP_PASSWORD",
		"IMAP_MCP_ACCOUNT_WORK_GMAIL_SMTP_PASSWORD"]}`, rec.Body.String())
	require.Equal(t, "imap.example.com", tools.lastEnv["IMAP_HOST"], "the server is started with the application's own secrets")
	require.NotContains(t, rec.Body.String(), "work@example.com", "the accounts themselves are not part of the answer")
}

func TestAccounts_EmptyListIsAnEmptyAnswerNotAnError(t *testing.T) {
	tools := &fakeTools{raw: json.RawMessage(`{"accounts":[]}`)}
	mux, apps, _ := newMuxWithTools(t, tools)
	imapApp(t, apps)

	rec := do(t, mux, http.MethodPost, "/api/applications/res-mail/accounts", nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.JSONEq(t, `{"names":[]}`, rec.Body.String())
}

func TestAccounts_ToolFailureIs502AndStoresNothing(t *testing.T) {
	tools := &fakeTools{err: errors.New("mcpapps.CallTool imap_list_accounts: mailbox unreachable")}
	mux, apps, secrets := newMuxWithTools(t, tools)
	imapApp(t, apps)

	rec := do(t, mux, http.MethodPost, "/api/applications/res-mail/accounts", nil)
	require.Equal(t, http.StatusBadGateway, rec.Code)
	require.Contains(t, rec.Body.String(), "mailbox unreachable")
	meta, err := secrets.List(context.Background(), "res-mail")
	require.NoError(t, err)
	require.Empty(t, meta, "a failed lookup must not leave a secret behind")
}

func TestAccounts_ServerWithoutTemplatesIs409(t *testing.T) {
	tools := &fakeTools{raw: json.RawMessage(`{"accounts":[]}`)}
	mux, apps, _ := newMuxWithTools(t, tools)
	_, err := apps.SetEntry(context.Background(), "res-mail", json.RawMessage(`{"command":"npx","args":["-y","notes-mcp"]}`))
	require.NoError(t, err)

	rec := do(t, mux, http.MethodPost, "/api/applications/res-mail/accounts", nil)
	require.Equal(t, http.StatusConflict, rec.Code, rec.Body.String())
	require.Equal(t, 0, tools.calls, "a server with no per-account secrets is never started")
}

func newMuxWithCaps(t *testing.T) (*chi.Mux, repo.MCPApplicationRepo, repo.GrantRepo, repo.CapabilityRepo) {
	t.Helper()
	bundle, err := db.Open(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { _ = bundle.Client.Close() })
	box, err := secretbox.New(make([]byte, 32))
	require.NoError(t, err)
	apps := repo.NewMCPApplicationRepo(bundle.Client)
	grants := repo.NewGrantRepo(bundle.Client)
	caps := repo.NewCapabilityRepo(bundle.Client)
	_, err = apps.Upsert(context.Background(), repo.UpsertMCPApplicationInput{ResourceID: "res-mail", ServerName: "mail"})
	require.NoError(t, err)
	mux := chi.NewRouter()
	applications.NewHandler(apps, repo.NewApplicationSecretRepo(bundle.Client, box),
		mcpapps.Refresher{Now: time.Now}, grants, repo.NewResourceRepo(bundle.Client),
		repo.NewTaskScheduleRepo(bundle.Client), &fakeSetup{}, &fakeTools{}, caps).Mount(mux)
	return mux, apps, grants, caps
}

func TestToolState_DeniedAsksAndAllowedForARoutine(t *testing.T) {
	mux, apps, grants, caps := newMuxWithCaps(t)
	ctx := context.Background()
	require.NoError(t, apps.RecordCatalogue(ctx, "res-mail", []schema.CatalogueTool{
		{Name: "imap_send_email"}, {Name: "imap_search_emails"}, {Name: "imap_save_draft"},
	}, "", time.Now()))
	for _, tool := range []string{"imap_send_email", "imap_search_emails", "imap_save_draft"} {
		_, err := caps.Upsert(ctx, repo.UpsertCapabilityInput{Name: mcpapps.CapabilityName("mail", tool), Class: repo.CapClassTool})
		require.NoError(t, err)
	}
	_, err := grants.Create(ctx, repo.CreateGrantInput{
		CapabilityName: mcpapps.CapabilityName("mail", "imap_send_email"),
		Context:        repo.GrantContextFor(repo.GrantContextGlobal, ""),
		Mode:           repo.GrantModeDeny, GrantedBy: "tester",
	})
	require.NoError(t, err)
	_, err = grants.Create(ctx, repo.CreateGrantInput{
		CapabilityName: mcpapps.CapabilityName("mail", "imap_save_draft"),
		Context:        repo.GrantContextFor(repo.GrantContextRoutine, "routine-7"),
		Mode:           repo.GrantModeAllow, GrantedBy: "tester",
	})
	require.NoError(t, err)

	rec := do(t, mux, http.MethodGet, "/api/applications", nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var views []struct {
		Tools []struct {
			Name      string   `json:"name"`
			State     string   `json:"state"`
			AllowedIn []string `json:"allowedIn"`
		} `json:"tools"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &views))
	require.Len(t, views, 1)
	byName := map[string]struct {
		state     string
		allowedIn []string
	}{}
	for _, tv := range views[0].Tools {
		byName[tv.Name] = struct {
			state     string
			allowedIn []string
		}{tv.State, tv.AllowedIn}
	}
	require.Equal(t, "denied", byName["imap_send_email"].state, "a global deny reads as denied")
	require.Equal(t, "asks", byName["imap_search_emails"].state, "a tool with no grant asks")
	require.Equal(t, "asks", byName["imap_save_draft"].state, "a routine allow does not make it allowed everywhere")
	require.Equal(t, []string{"routine:routine-7"}, byName["imap_save_draft"].allowedIn,
		"but the panel names the routine it is allowed in")
	require.Empty(t, byName["imap_search_emails"].allowedIn)

	// Revoking that allow must take the routine off the list: a revoked grant
	// decides nothing, so showing it would promise access that no longer exists.
	rows, err := grants.ListForCapability(ctx, mcpapps.CapabilityName("mail", "imap_save_draft"))
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.NoError(t, grants.Revoke(ctx, rows[0].ID, "tester"))

	rec = do(t, mux, http.MethodGet, "/api/applications", nil)
	require.Equal(t, http.StatusOK, rec.Code)
	require.NotContains(t, rec.Body.String(), "routine:routine-7",
		"a revoked allow must not be listed: %s", rec.Body.String())
	_ = caps
}

func TestToolState_RevokingTheDenyMakesItAskAgain(t *testing.T) {
	mux, apps, grants, caps := newMuxWithCaps(t)
	ctx := context.Background()
	require.NoError(t, apps.RecordCatalogue(ctx, "res-mail", []schema.CatalogueTool{{Name: "imap_send_email"}}, "", time.Now()))
	_, err := caps.Upsert(ctx, repo.UpsertCapabilityInput{Name: mcpapps.CapabilityName("mail", "imap_send_email"), Class: repo.CapClassTool})
	require.NoError(t, err)
	g, err := grants.Create(ctx, repo.CreateGrantInput{
		CapabilityName: mcpapps.CapabilityName("mail", "imap_send_email"),
		Context:        repo.GrantContextFor(repo.GrantContextGlobal, ""),
		Mode:           repo.GrantModeDeny, GrantedBy: "tester",
	})
	require.NoError(t, err)

	require.NoError(t, grants.Revoke(ctx, g.ID, "tester"))

	rec := do(t, mux, http.MethodGet, "/api/applications", nil)
	require.Contains(t, rec.Body.String(), `"state":"asks"`, "a revoked deny stops denying: %s", rec.Body.String())
}
