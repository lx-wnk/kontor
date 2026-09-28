package mcpapps_test

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/lx-wnk/kontor/server/internal/channelconfig"
	"github.com/lx-wnk/kontor/server/internal/db/ent"
	"github.com/lx-wnk/kontor/server/internal/mcp"
	"github.com/lx-wnk/kontor/server/internal/mcpapps"
)

func TestDetectDrift_ServerWithNoRowIsFound(t *testing.T) {
	servers := map[string]json.RawMessage{"notes": []byte(`{"command":"x"}`)}
	drift := mcpapps.DetectDrift(servers, nil)
	require.Equal(t, []string{"notes"}, drift.Found)
	require.Empty(t, drift.Changed)
}

func TestDetectDrift_NonExportedRowIsNeverChanged(t *testing.T) {
	servers := map[string]json.RawMessage{"mail": []byte(`{"command":"other"}`)}
	apps := []*ent.MCPApplication{{
		ServerName:     "mail",
		Entry:          []byte(`{"command":"x"}`),
		ExportToClaude: false,
		ExportedHash:   mcpapps.EntryHash([]byte(`{"command":"x"}`)),
	}}
	drift := mcpapps.DetectDrift(servers, apps)
	require.Empty(t, drift.Found)
	require.Empty(t, drift.Changed)
}

func TestDetectDrift_ExportedRowWithDifferentFileEntryIsChanged(t *testing.T) {
	stored := []byte(`{"command":"x"}`)
	servers := map[string]json.RawMessage{"mail": []byte(`{"command":"other"}`)}
	apps := []*ent.MCPApplication{{
		ServerName:     "mail",
		Entry:          stored,
		ExportToClaude: true,
		ExportedHash:   mcpapps.EntryHash(stored),
	}}
	drift := mcpapps.DetectDrift(servers, apps)
	require.Equal(t, []string{"mail"}, drift.Changed)
}

func TestDetectDrift_ExportedRowMissingFromFileIsChanged(t *testing.T) {
	stored := []byte(`{"command":"x"}`)
	apps := []*ent.MCPApplication{{
		ServerName:     "mail",
		Entry:          stored,
		ExportToClaude: true,
		ExportedHash:   mcpapps.EntryHash(stored),
	}}
	drift := mcpapps.DetectDrift(nil, apps)
	require.Equal(t, []string{"mail"}, drift.Changed)
}

func TestDetectDrift_ReservedServersNeverAppear(t *testing.T) {
	servers := map[string]json.RawMessage{
		channelconfig.ChannelServerName: []byte(`{"command":"x"}`),
		mcp.ServerName:                  []byte(`{"command":"y"}`),
	}
	drift := mcpapps.DetectDrift(servers, nil)
	require.Empty(t, drift.Found)
	require.Empty(t, drift.Changed)
}

func TestDetectDrift_BothListsAreSorted(t *testing.T) {
	stored := []byte(`{"command":"x"}`)
	servers := map[string]json.RawMessage{
		"zeta":  []byte(`{"command":"z"}`),
		"alpha": []byte(`{"command":"a"}`),
		"mail":  []byte(`{"command":"other"}`),
		"notes": []byte(`{"command":"other"}`),
	}
	apps := []*ent.MCPApplication{
		{ServerName: "notes", Entry: stored, ExportToClaude: true, ExportedHash: mcpapps.EntryHash(stored)},
		{ServerName: "mail", Entry: stored, ExportToClaude: true, ExportedHash: mcpapps.EntryHash(stored)},
	}
	drift := mcpapps.DetectDrift(servers, apps)
	require.Equal(t, []string{"alpha", "zeta"}, drift.Found)
	require.Equal(t, []string{"mail", "notes"}, drift.Changed)
}
