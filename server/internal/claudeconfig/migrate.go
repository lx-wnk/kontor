package claudeconfig

import (
	"encoding/json"
	"path/filepath"
	"sort"
	"strings"
)

// legacyEntries maps the mcpServers keys this application registered before it
// was renamed to the names it uses now. The entry is moved, not deleted: it
// carries the credential the operator registered, and removing it would leave
// them with no server registered at all until they ran `claude mcp add` again.
//
// Only an entry that still points at this application is touched: the file
// belongs to the claude CLI, and everything else in it belongs to somebody
// else.
var legacyEntries = map[string]string{
	"dashboard-channel": "kontor-channel",
	"dashboard-tasks":   "kontor-tasks",
}

// ourBinaries are the names this application has been installed under. An entry
// whose command is one of them was written by this application; one pointing
// anywhere else was not, whatever it is called.
var ourBinaries = map[string]bool{
	"kontor":                  true,
	"kontor-desktop":          true,
	"agent-dashboard":         true,
	"agent-dashboard-desktop": true,
}

// MigrateLegacyEntries renames this installation's pre-rename entries in place
// and reports the old names it moved. An entry under a legacy name that points
// somewhere else is left alone: the name alone is not proof of ownership, and
// touching a stranger's server would be a far worse failure than leaving a
// stale name behind.
//
// When an entry already exists under the new name, the old one is dropped
// rather than overwriting it — the newer registration is the one the operator
// made most recently.
//
// The file is only rewritten when something actually moves.
func MigrateLegacyEntries() ([]string, error) {
	servers, err := UserMCPServers()
	if err != nil {
		return nil, err
	}
	moves := map[string]string{}
	for old, current := range legacyEntries {
		raw, found := servers[old]
		if !found || !entryIsOurs(raw) {
			continue
		}
		moves[old] = current
	}
	if len(moves) == 0 {
		return nil, nil
	}
	if err := modifyServerEntries(func(entries map[string]json.RawMessage) {
		for old, current := range moves {
			if _, exists := entries[current]; !exists {
				entries[current] = entries[old]
			}
			delete(entries, old)
		}
	}); err != nil {
		return nil, err
	}
	moved := make([]string, 0, len(moves))
	for old := range moves {
		moved = append(moved, old)
	}
	sort.Strings(moved)
	return moved, nil
}

// entryIsOurs reports whether an mcpServers entry was written by this
// application: a stdio entry running one of its binaries, or an HTTP entry
// pointing at its own MCP endpoint on loopback.
func entryIsOurs(raw json.RawMessage) bool {
	var entry struct {
		Command string `json:"command"`
		URL     string `json:"url"`
	}
	if err := json.Unmarshal(raw, &entry); err != nil {
		return false
	}
	if entry.Command != "" && ourBinaries[filepath.Base(entry.Command)] {
		return true
	}
	if entry.URL == "" {
		return false
	}
	isLoopback := strings.Contains(entry.URL, "127.0.0.1") || strings.Contains(entry.URL, "localhost")
	return isLoopback && strings.HasSuffix(entry.URL, "/api/mcp")
}
