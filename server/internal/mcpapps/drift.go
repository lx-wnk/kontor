package mcpapps

import (
	"encoding/json"
	"sort"

	"github.com/lx-wnk/kontor/server/internal/channelconfig"
	"github.com/lx-wnk/kontor/server/internal/db/ent"
)

// Drift reports where Claude's own config and the application rows disagree.
type Drift struct {
	Found   []string `json:"found"`   // in Claude's config, no application row
	Changed []string `json:"changed"` // exported, but the file no longer matches exported_hash
}

// DetectDrift compares Claude's own ~/.claude.json against the application
// rows. A server is Found when the config names it but no row owns that
// server name. A row is Changed only when it is currently exported and the
// config's entry — or its absence — no longer hashes to ExportedHash; a row
// that was never exported has nothing in the file to drift from. Both lists
// are sorted and never name a reserved server.
func DetectDrift(servers map[string]json.RawMessage, apps []*ent.MCPApplication) Drift {
	byName := make(map[string]*ent.MCPApplication, len(apps))
	for _, app := range apps {
		byName[app.ServerName] = app
	}

	found := []string{}
	changed := []string{}
	for name := range servers {
		if channelconfig.IsReservedServerName(name) {
			continue
		}
		if _, ok := byName[name]; !ok {
			found = append(found, name)
		}
	}
	for _, app := range apps {
		if !app.ExportToClaude || channelconfig.IsReservedServerName(app.ServerName) {
			continue
		}
		if EntryHash(servers[app.ServerName]) != app.ExportedHash {
			changed = append(changed, app.ServerName)
		}
	}

	sort.Strings(found)
	sort.Strings(changed)
	return Drift{Found: found, Changed: changed}
}
