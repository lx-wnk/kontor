package mcpapps_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/lx-wnk/kontor/server/internal/db"
	"github.com/lx-wnk/kontor/server/internal/db/repo"
	"github.com/lx-wnk/kontor/server/internal/mcpapps"
)

func TestFindPreset_MatchesByCommandLine(t *testing.T) {
	p, ok := mcpapps.FindPreset(mcpapps.ServerEntry{Command: "npx", Args: []string{"-y", "imap-mcp-server"}})
	require.True(t, ok)
	require.Equal(t, "imap-mcp-server", p.Server)
}

func TestFindPreset_NoMatchIsFalse(t *testing.T) {
	_, ok := mcpapps.FindPreset(mcpapps.ServerEntry{Command: "npx", Args: []string{"-y", "notes-mcp"}})
	require.False(t, ok)
}

func TestLoadPreset_ProbeOutputIsEmbedded(t *testing.T) {
	p, err := mcpapps.LoadPreset("imap-mcp-server")
	require.NoError(t, err)
	require.NotEmpty(t, p.DenyGlobal, "the preset must deny at least the send tools")
	require.NotEmpty(t, p.Match)
	require.NotNil(t, p.Setup)
	require.Contains(t, p.Setup.Args, "{port}")
	require.Equal(t, "/api/health", p.Setup.Readiness)
	require.Len(t, p.SecretTemplates, 2)
}

func TestApplyDefaultDenies_DeniesEvenWithoutCatalogue(t *testing.T) {
	bundle, err := db.Open(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { _ = bundle.Client.Close() })
	ctx := context.Background()
	apps := repo.NewMCPApplicationRepo(bundle.Client)
	grants := repo.NewGrantRepo(bundle.Client)

	_, err = apps.Upsert(ctx, repo.UpsertMCPApplicationInput{
		ResourceID: "res-mail", ServerName: "mail",
		Entry: json.RawMessage(`{"command":"npx","args":["-y","imap-mcp-server"]}`),
	})
	require.NoError(t, err)
	app, err := apps.GetByResourceID(ctx, "res-mail")
	require.NoError(t, err)
	require.Empty(t, app.Catalogue, "the point of this test is an unrefreshed catalogue")

	preset, err := mcpapps.LoadPreset("imap-mcp-server")
	require.NoError(t, err)
	expected := make([]string, len(preset.DenyGlobal))
	for i, tool := range preset.DenyGlobal {
		expected[i] = mcpapps.CapabilityName("mail", tool)
	}

	first, err := mcpapps.ApplyDefaultDenies(ctx, grants, app, "test")
	require.NoError(t, err)
	require.Equal(t, "imap-mcp-server", first.Preset)
	require.ElementsMatch(t, expected, first.Created)
	require.Empty(t, first.Existing)

	second, err := mcpapps.ApplyDefaultDenies(ctx, grants, app, "test")
	require.NoError(t, err)
	require.Empty(t, second.Created, "applying twice must not duplicate grants")
	require.ElementsMatch(t, expected, second.Existing)

	rows, err := grants.ListForCapability(ctx, expected[0])
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.Equal(t, repo.GrantModeDeny, rows[0].Mode)
	require.Equal(t, repo.GrantContextGlobal, rows[0].ContextKind)
}

func TestApplyDefaultDenies_NoMatchIsNoOp(t *testing.T) {
	bundle, err := db.Open(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { _ = bundle.Client.Close() })
	ctx := context.Background()
	apps := repo.NewMCPApplicationRepo(bundle.Client)
	grants := repo.NewGrantRepo(bundle.Client)

	_, err = apps.Upsert(ctx, repo.UpsertMCPApplicationInput{
		ResourceID: "res-notes", ServerName: "notes",
		Entry: json.RawMessage(`{"command":"npx","args":["-y","notes-mcp"]}`),
	})
	require.NoError(t, err)
	app, err := apps.GetByResourceID(ctx, "res-notes")
	require.NoError(t, err)

	res, err := mcpapps.ApplyDefaultDenies(ctx, grants, app, "test")
	require.NoError(t, err)
	require.Equal(t, mcpapps.DenyResult{Preset: "", Created: []string{}, Existing: []string{}}, res)
}
