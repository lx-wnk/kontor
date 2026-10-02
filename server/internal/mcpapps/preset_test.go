package mcpapps_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
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

func TestPresetDenyGlobal_AllNamesExistInLiveFixture(t *testing.T) {
	raw, err := os.ReadFile("testdata/imap-mcp-server-2.0.0-tools.txt")
	require.NoError(t, err)

	var liveTools []string
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			liveTools = append(liveTools, line)
		}
	}
	require.NotEmpty(t, liveTools)

	preset, err := mcpapps.LoadPreset("imap-mcp-server")
	require.NoError(t, err)

	live := make(map[string]bool, len(liveTools))
	for _, name := range liveTools {
		live[name] = true
	}

	for _, denied := range preset.DenyGlobal {
		require.True(t, live[denied], "denyGlobal entry %q is not in the live tool fixture — tool renamed or removed?", denied)
	}
}

// TestApplyPresetDenies_UnconfirmedPresetIsRefused proves that a preset with
// Confirmed:false is rejected before any grant is written. This is the gate
// that prevents an unreviewed deny list from being applied to a server.
func TestApplyPresetDenies_UnconfirmedPresetIsRefused(t *testing.T) {
	bundle, err := db.Open(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { _ = bundle.Client.Close() })
	ctx := context.Background()
	apps := repo.NewMCPApplicationRepo(bundle.Client)
	grants := repo.NewGrantRepo(bundle.Client)

	_, err = apps.Upsert(ctx, repo.UpsertMCPApplicationInput{
		ResourceID: "res-mail2", ServerName: "mail2",
		Entry: json.RawMessage(`{"command":"npx","args":["-y","imap-mcp-server"]}`),
	})
	require.NoError(t, err)
	app, err := apps.GetByResourceID(ctx, "res-mail2")
	require.NoError(t, err)

	unconfirmed := mcpapps.Preset{
		Server:     "imap-mcp-server",
		Confirmed:  false,
		DenyGlobal: []string{"imap_send_email"},
	}

	_, err = mcpapps.ApplyPresetDenies(ctx, grants, unconfirmed, app, "test")
	require.Error(t, err)
	require.True(t, errors.Is(err, mcpapps.ErrPresetUnconfirmed), "expected ErrPresetUnconfirmed, got: %v", err)

	// No grants written.
	rows, listErr := grants.ListForCapability(ctx, mcpapps.CapabilityName("mail2", "imap_send_email"))
	require.NoError(t, listErr)
	require.Empty(t, rows, "unconfirmed preset must not write any grants")
}

// TestPresetDenyGlobal_AllDangerousToolsAreDenied is the inverse of
// TestPresetDenyGlobal_AllNamesExistInLiveFixture: it asserts that every
// send, delete, and account-management tool known to be dangerous is present
// in denyGlobal, catching accidental removals that would slip through the
// name-staleness check.
func TestPresetDenyGlobal_AllDangerousToolsAreDenied(t *testing.T) {
	// This list is the security invariant. Add a tool here if the live
	// catalogue adds a new dangerous capability and the deny list must cover it.
	dangerousTools := []string{
		"imap_send_email",
		"imap_reply_to_email",
		"imap_forward_email",
		"imap_delete_email",
		"imap_bulk_delete",
		"imap_bulk_delete_by_search",
		"imap_delete_spam",
		"imap_delete_by_domain",
		"imap_add_account",
		"imap_update_account",
		"imap_remove_account",
		"imap_download_attachment",
		"imap_upload_file",
		"imap_add_spam_domain",
		"imap_remove_spam_domain",
		"imap_add_whitelist_domain",
	}

	preset, err := mcpapps.LoadPreset("imap-mcp-server")
	require.NoError(t, err)

	denied := make(map[string]bool, len(preset.DenyGlobal))
	for _, tool := range preset.DenyGlobal {
		denied[tool] = true
	}

	for _, tool := range dangerousTools {
		require.True(t, denied[tool], "dangerous tool %q is not in denyGlobal — security regression", tool)
	}
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
