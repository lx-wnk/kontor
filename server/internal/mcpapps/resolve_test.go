package mcpapps_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/lx-wnk/kontor/server/internal/capability"
	"github.com/lx-wnk/kontor/server/internal/db"
	"github.com/lx-wnk/kontor/server/internal/db/ent"
	"github.com/lx-wnk/kontor/server/internal/db/ent/schema"
	"github.com/lx-wnk/kontor/server/internal/db/repo"
	"github.com/lx-wnk/kontor/server/internal/mcpapps"
	"github.com/lx-wnk/kontor/server/internal/secretbox"
)

type resolveFixture struct {
	ctx      context.Context
	apps     repo.MCPApplicationRepo
	secrets  repo.ApplicationSecretRepo
	grants   repo.GrantRepo
	caps     repo.CapabilityRepo
	resolver mcpapps.Resolver
}

func newResolveFixture(t *testing.T) resolveFixture {
	t.Helper()
	bundle, err := db.Open(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { _ = bundle.Client.Close() })
	box, err := secretbox.New(make([]byte, 32))
	require.NoError(t, err)
	f := resolveFixture{
		ctx:     context.Background(),
		apps:    repo.NewMCPApplicationRepo(bundle.Client),
		secrets: repo.NewApplicationSecretRepo(bundle.Client, box),
		grants:  repo.NewGrantRepo(bundle.Client),
		caps:    repo.NewCapabilityRepo(bundle.Client),
	}
	f.resolver = mcpapps.Resolver{Apps: f.apps, Secrets: f.secrets, Grants: f.grants, Capabilities: f.caps}
	return f
}

func (f resolveFixture) addApp(t *testing.T, resourceID, server string, attachAll bool, entry json.RawMessage, tools ...string) {
	t.Helper()
	_, err := f.apps.Upsert(f.ctx, repo.UpsertMCPApplicationInput{
		ResourceID: resourceID, ServerName: server, AttachAll: attachAll, Entry: entry,
	})
	require.NoError(t, err)
	catalogue := make([]schema.CatalogueTool, 0, len(tools))
	for _, tool := range tools {
		catalogue = append(catalogue, schema.CatalogueTool{Name: tool})
		_, err := f.caps.Upsert(f.ctx, repo.UpsertCapabilityInput{
			Name: mcpapps.CapabilityName(server, tool), Class: repo.CapClassTool,
			EnforceableBy: []string{capability.EnforcerSpawn},
		})
		require.NoError(t, err)
	}
	require.NoError(t, f.apps.RecordCatalogue(f.ctx, resourceID, catalogue, "", time.Now()))
}

func (f resolveFixture) grant(t *testing.T, capName, contextKind, contextRef, mode string) {
	t.Helper()
	_, err := f.grants.Create(f.ctx, repo.CreateGrantInput{
		CapabilityName: capName,
		Context:        repo.GrantContext{Kind: contextKind, Ref: contextRef},
		Mode:           mode,
		GrantedBy:      "test",
	})
	require.NoError(t, err)
}

var (
	mailEntry  = json.RawMessage(`{"command":"npx","args":["mail"]}`)
	notesEntry = json.RawMessage(`{"command":"npx","args":["notes"]}`)
)

func TestResolveRun_OnlyAttachedAndAttachAllServersReachTheRun(t *testing.T) {
	f := newResolveFixture(t)
	f.addApp(t, "res-mail", "mail", false, mailEntry, "search")
	f.addApp(t, "res-notes", "notes", true, notesEntry)

	unattached, err := f.resolver.ResolveRun(f.ctx, &ent.Task{ID: "t1", Cwd: "/repo"})
	require.NoError(t, err)
	require.Contains(t, unattached.Servers, "notes")
	require.NotContains(t, unattached.Servers, "mail", "a mailbox nobody attached must not reach a coding task")

	attached, err := f.resolver.ResolveRun(f.ctx, &ent.Task{ID: "t2", Cwd: "/repo", Applications: []string{"res-mail"}})
	require.NoError(t, err)
	require.Contains(t, attached.Servers, "mail")
}

func TestResolveRun_SecretsGoOnlyIntoTheirOwnServerEntry(t *testing.T) {
	f := newResolveFixture(t)
	f.addApp(t, "res-mail", "mail", false, mailEntry)
	f.addApp(t, "res-notes", "notes", true, notesEntry)
	require.NoError(t, f.secrets.Set(f.ctx, "res-mail", "MAIL_PASSWORD", "hunter2"))

	out, err := f.resolver.ResolveRun(f.ctx, &ent.Task{ID: "t1", Cwd: "/repo", Applications: []string{"res-mail"}})
	require.NoError(t, err)
	require.Contains(t, string(out.Servers["mail"]), "hunter2")
	require.NotContains(t, string(out.Servers["notes"]), "hunter2")
}

func TestResolveRun_MissingRequiredSecretFailsTheRun(t *testing.T) {
	f := newResolveFixture(t)
	f.addApp(t, "res-mail", "mail", false, mailEntry)
	_, err := f.apps.SetRequiredEnv(f.ctx, "res-mail", []string{"MAIL_PASSWORD"})
	require.NoError(t, err)

	_, err = f.resolver.ResolveRun(f.ctx, &ent.Task{ID: "t1", Cwd: "/repo", Applications: []string{"res-mail"}})
	var missing *mcpapps.MissingSecretError
	require.True(t, errors.As(err, &missing))
	require.Equal(t, "MAIL_PASSWORD", missing.EnvName)
}

func TestResolveRun_AttachedApplicationWithNoEntryFailsTheRun(t *testing.T) {
	f := newResolveFixture(t)
	f.addApp(t, "res-mail", "mail", false, nil)

	_, err := f.resolver.ResolveRun(f.ctx, &ent.Task{ID: "t1", Cwd: "/repo", Applications: []string{"res-mail"}})
	var missing *mcpapps.MissingServerError
	require.True(t, errors.As(err, &missing))
}

func TestResolveRun_AttachAllApplicationWithNoEntryIsSkippedSilently(t *testing.T) {
	f := newResolveFixture(t)
	f.addApp(t, "res-notes", "notes", true, nil)

	out, err := f.resolver.ResolveRun(f.ctx, &ent.Task{ID: "t1", Cwd: "/repo"})
	require.NoError(t, err)
	require.NotContains(t, out.Servers, "notes")
}

func TestResolveRun_GrantsDecideAllowDenyAndSilenceMeansAsk(t *testing.T) {
	f := newResolveFixture(t)
	f.addApp(t, "res-mail", "mail", false, mailEntry, "search", "send", "draft")
	routineID := "routine-1"
	f.grant(t, "mcp__mail__search", repo.GrantContextRoutine, routineID, "allow")
	f.grant(t, "mcp__mail__send", repo.GrantContextGlobal, "", "deny")

	out, err := f.resolver.ResolveRun(f.ctx, &ent.Task{
		ID: "t1", Cwd: "/repo", RoutineID: &routineID, Applications: []string{"res-mail"},
	})
	require.NoError(t, err)
	require.Equal(t, []string{"mcp__mail__search"}, out.Allow)
	require.Equal(t, []string{"mcp__mail__send"}, out.Deny)
	require.NotContains(t, out.Allow, "mcp__mail__draft", "no grant is not an allow")
	require.True(t, out.CatalogueTools["mcp__mail__draft"])

	other, err := f.resolver.ResolveRun(f.ctx, &ent.Task{ID: "t2", Cwd: "/repo", Applications: []string{"res-mail"}})
	require.NoError(t, err)
	require.NotContains(t, other.Allow, "mcp__mail__search", "a routine's grant does not leak into another task")
}
