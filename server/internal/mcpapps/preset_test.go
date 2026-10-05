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

func fixtureSet(t *testing.T, file string) map[string]bool {
	t.Helper()
	raw, err := os.ReadFile("testdata/" + file)
	require.NoError(t, err)

	set := map[string]bool{}
	for _, line := range strings.Split(string(raw), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			set[line] = true
		}
	}
	require.NotEmpty(t, set)
	return set
}

func liveToolFixture(t *testing.T) map[string]bool {
	t.Helper()
	return fixtureSet(t, "imap-mcp-server-2.0.0-tools.txt")
}

func TestPresetDenyGlobal_AllNamesExistInLiveFixture(t *testing.T) {
	live := liveToolFixture(t)

	preset, err := mcpapps.LoadPreset("imap-mcp-server")
	require.NoError(t, err)

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

	// Deliberate exceptions: destructive in some argument shapes, kept at the
	// ask tier because denying the whole tool would disable the core triage
	// action. A grant matches the tool name only, so the destructive shape is
	// denied through argument-scoped denyArgs instead.
	intentionallyAsked := map[string]string{
		"imap_move_email": "a move to a trash folder is a soft delete; every other folder move is triage",
	}
	live := liveToolFixture(t)
	rules := preset.ArgDenyRules("mail")
	for tool, why := range intentionallyAsked {
		require.True(t, live[tool], "intentionally-asked tool %q is not in the live tool fixture — renamed or removed?", tool)
		require.False(t, denied[tool], "%q is an intentional ask-tier tool (%s); moving it into denyGlobal disables triage — decide that deliberately", tool, why)
		require.Contains(t, rules, mcpapps.CapabilityName("mail", tool)+"(targetFolder:*Trash*)", "%q is ask-tier only with argument-scoped trash denies", tool)
	}
}

func TestPresetDenyArgs_ToolsExistInLiveFixture(t *testing.T) {
	live := liveToolFixture(t)
	preset, err := mcpapps.LoadPreset("imap-mcp-server")
	require.NoError(t, err)
	require.NotEmpty(t, preset.DenyArgs)
	for _, d := range preset.DenyArgs {
		require.True(t, live[d.Tool], "denyArgs tool %q is not in the live tool fixture — tool renamed or removed?", d.Tool)
	}
}

func TestPresetDenyArgs_ParamsExistInLiveFixture(t *testing.T) {
	params := fixtureSet(t, "imap-mcp-server-2.0.0-params.txt")
	preset, err := mcpapps.LoadPreset("imap-mcp-server")
	require.NoError(t, err)
	require.NotEmpty(t, preset.DenyArgs)
	for _, d := range preset.DenyArgs {
		require.True(t, params[d.Tool+" "+d.Param], "denyArgs %s(%s) is not in the live param fixture — parameter renamed? the rule would silently stop matching", d.Tool, d.Param)
	}
}

func TestPreset_ArgDenyRules_RendersImapTrashDenies(t *testing.T) {
	preset, err := mcpapps.LoadPreset("imap-mcp-server")
	require.NoError(t, err)

	require.Equal(t, []string{
		"mcp__mail__imap_move_email(targetFolder:*Trash*)",
		"mcp__mail__imap_move_email(targetFolder:*trash*)",
		"mcp__mail__imap_move_email(targetFolder:*TRASH*)",
		"mcp__mail__imap_move_email(targetFolder:[Gmail]/Bin)",
		"mcp__mail__imap_move_email(targetFolder:[Google Mail]/Bin)",
		"mcp__mail__imap_move_email(targetFolder:*Deleted*)",
		"mcp__mail__imap_move_email(targetFolder:*deleted*)",
		"mcp__mail__imap_move_email(targetFolder:*Papierkorb*)",
		"mcp__mail__imap_move_email(targetFolder:*Gelöschte*)",
	}, preset.ArgDenyRules("mail"))
}

func TestPreset_ArgDenyRules_NoneDeclared(t *testing.T) {
	require.Empty(t, mcpapps.Preset{Server: "x"}.ArgDenyRules("x"))
}

func TestPreset_ValidateDenyArgs(t *testing.T) {
	valid := mcpapps.ArgDeny{Tool: "t", Param: "p", Values: []string{"*v*"}}
	require.NoError(t, mcpapps.Preset{DenyArgs: []mcpapps.ArgDeny{valid}}.ValidateDenyArgs())

	cases := map[string]mcpapps.ArgDeny{
		"empty tool":      {Tool: "", Param: "p", Values: []string{"v"}},
		"empty param":     {Tool: "t", Param: "", Values: []string{"v"}},
		"no values":       {Tool: "t", Param: "p"},
		"empty value":     {Tool: "t", Param: "p", Values: []string{""}},
		"open paren":      {Tool: "t", Param: "p", Values: []string{"a(b"}},
		"close paren":     {Tool: "t", Param: "p", Values: []string{"a)b"}},
		"colon":           {Tool: "t", Param: "p", Values: []string{"a:b"}},
		"colon in param":  {Tool: "t", Param: "p:q", Values: []string{"v"}},
		"later value bad": {Tool: "t", Param: "p", Values: []string{"ok", "bad)"}},
	}
	for name, d := range cases {
		t.Run(name, func(t *testing.T) {
			require.Error(t, mcpapps.Preset{DenyArgs: []mcpapps.ArgDeny{d}}.ValidateDenyArgs())
		})
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
