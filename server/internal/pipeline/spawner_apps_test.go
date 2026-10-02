package pipeline

import (
	"context"
	"encoding/json"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/lx-wnk/kontor/server/internal/db"
	"github.com/lx-wnk/kontor/server/internal/db/ent"
	"github.com/lx-wnk/kontor/server/internal/db/repo"
	"github.com/lx-wnk/kontor/server/internal/mcpapps"
	"github.com/lx-wnk/kontor/server/internal/secretbox"
)

func TestSpawnToolLists_RendersApplicationDecisions(t *testing.T) {
	opts := SpawnAgentOptions{
		Task: &ent.Task{Autonomy: "spec_gated"},
		Applications: mcpapps.RunApplications{
			Allow:          []string{"mcp__mail__search"},
			Deny:           []string{"mcp__mail__send"},
			CatalogueTools: map[string]bool{"mcp__mail__search": true, "mcp__mail__send": true, "mcp__mail__draft": true},
		},
	}
	allow, deny := spawnToolLists(opts, false)
	require.Contains(t, allow, "mcp__mail__search")
	require.Contains(t, deny, "mcp__mail__send", "a deny grant must reach --disallowedTools")
	require.NotContains(t, allow, "mcp__mail__draft", "allow-all autonomy does not allow application tools")
	require.NotContains(t, allow, "mcp__mail__send")
}

func TestBuildAllowList_TaskPermissionForACatalogueToolSurvives(t *testing.T) {
	granted := &ent.TaskPermission{ID: "p1", Tool: "mcp__mail__search", Granted: true}
	unknown := &ent.TaskPermission{ID: "p2", Tool: "mcp__other__thing", Granted: true}
	allow := BuildAllowList("manual", []*ent.TaskPermission{granted, unknown}, false, false,
		map[string]bool{"mcp__mail__search": true})
	require.Contains(t, allow, "mcp__mail__search")
	require.NotContains(t, allow, "mcp__other__thing", "a tool outside any catalogue stays ungrantable")
}

// resolveMailApp runs the real resolver over one attach-all application named
// "mail" whose server entry is entry.
func resolveMailApp(t *testing.T, entry string) mcpapps.RunApplications {
	t.Helper()
	bundle, err := db.Open(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { _ = bundle.Client.Close() })
	box, err := secretbox.New(make([]byte, 32))
	require.NoError(t, err)
	appRepo := repo.NewMCPApplicationRepo(bundle.Client)
	_, err = appRepo.Upsert(context.Background(), repo.UpsertMCPApplicationInput{
		ResourceID: "res-mail", ServerName: "mail", AttachAll: true, Entry: json.RawMessage(entry),
	})
	require.NoError(t, err)
	resolver := mcpapps.Resolver{
		Apps:         appRepo,
		Secrets:      repo.NewApplicationSecretRepo(bundle.Client, box),
		Grants:       repo.NewGrantRepo(bundle.Client),
		Capabilities: repo.NewCapabilityRepo(bundle.Client),
	}
	apps, err := resolver.ResolveRun(context.Background(), &ent.Task{ID: "t1", Cwd: "/repo"})
	require.NoError(t, err)
	return apps
}

// spawnDenyCarriers renders what one spawn hands Claude Code as deny rules: the
// --disallowedTools values, and the permissions.deny of the settings file.
func spawnDenyCarriers(t *testing.T, apps mcpapps.RunApplications) (flagDeny, settingsDeny []string) {
	t.Helper()
	opts := SpawnAgentOptions{Task: &ent.Task{Autonomy: "spec_gated"}, Applications: apps}
	allow, deny := spawnToolLists(opts, false)

	args := buildSpawnArgsWithChannelConfig(opts, "", allow, deny)
	idx := slices.Index(args, "--disallowedTools")
	require.GreaterOrEqual(t, idx, 0, "the deny list must reach the spawn as --disallowedTools")
	flagDeny = args[idx+1:]

	path, wrote, _, err := writeSettingsFile(t.TempDir(), allow, deny)
	require.NoError(t, err)
	require.True(t, wrote)
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	var parsed struct {
		Permissions struct {
			Deny []string `json:"deny"`
		} `json:"permissions"`
	}
	require.NoError(t, json.Unmarshal(data, &parsed))
	return flagDeny, parsed.Permissions.Deny
}

func TestSpawn_PresetArgDenyRulesReachTheFlagButNotTheSettingsFile(t *testing.T) {
	preset, err := mcpapps.LoadPreset("imap-mcp-server")
	require.NoError(t, err)
	want := preset.ArgDenyRules("mail")
	require.Len(t, want, 9)
	require.Contains(t, want, "mcp__mail__imap_move_email(targetFolder:*Trash*)")

	apps := resolveMailApp(t, `{"command":"npx","args":["-y","imap-mcp-server@2.0.0"]}`)
	flagDeny, settingsDeny := spawnDenyCarriers(t, apps)

	require.Equal(t, append(BuildDenyList("spec_gated", false), want...), flagDeny,
		"--disallowedTools carries the base deny list, then the preset's argument rules in declaration order")
	require.Equal(t, BuildDenyList("spec_gated", false), settingsDeny,
		"Claude Code skips parenthesised mcp__ rules in settings.json and `claude doctor` flags them as invalid")
	for _, entry := range settingsDeny {
		require.False(t, strings.HasPrefix(entry, "mcp__"), "no argument rule in settings.json: %s", entry)
	}
}

func TestSpawn_ApplicationWithoutPresetArgDenyGetsNoArgRules(t *testing.T) {
	apps := resolveMailApp(t, `{"command":"npx","args":["notes"]}`)
	flagDeny, settingsDeny := spawnDenyCarriers(t, apps)

	require.Equal(t, BuildDenyList("spec_gated", false), flagDeny)
	require.Equal(t, BuildDenyList("spec_gated", false), settingsDeny)
}
