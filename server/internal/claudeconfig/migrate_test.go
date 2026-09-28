package claudeconfig

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func writeConfig(t *testing.T, dir string, servers map[string]any) {
	t.Helper()
	raw, err := json.Marshal(map[string]any{"mcpServers": servers})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".claude.json"), raw, 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
}

func readServers(t *testing.T, dir string) map[string]json.RawMessage {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(dir, ".claude.json"))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	var cfg configFile
	if err := json.Unmarshal(raw, &cfg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	return cfg.MCPServers
}

// The rename orphans the entries an installation already registered. They are
// moved rather than deleted: the entry carries the credential the operator
// registered, and deleting it would leave them with nothing registered until
// they ran `claude mcp add` again. Only the entries this installation wrote are
// touched — anything else in that file belongs to somebody else.
func TestMigrateLegacyEntries(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", dir)

	writeConfig(t, dir, map[string]any{
		"dashboard-channel": map[string]any{"command": "/opt/kontor/bin/kontor", "args": []string{"channel"}},
		"dashboard-tasks":   map[string]any{"type": "http", "url": "http://127.0.0.1:13120/api/mcp"},
		"someone-elses":     map[string]any{"command": "/usr/local/bin/other", "args": []string{"serve"}},
		"dashboard-lookalike": map[string]any{
			"command": "/usr/local/bin/not-ours", "args": []string{"channel"},
		},
	})

	moved, err := MigrateLegacyEntries()
	if err != nil {
		t.Fatalf("MigrateLegacyEntries: %v", err)
	}
	if len(moved) != 2 {
		t.Fatalf("moved %v, want the two this installation wrote", moved)
	}

	servers := readServers(t, dir)
	for _, gone := range []string{"dashboard-channel", "dashboard-tasks"} {
		if _, found := servers[gone]; found {
			t.Errorf("%s survived under its old name — an agent would keep loading it", gone)
		}
	}
	for _, present := range []string{"kontor-channel", "kontor-tasks"} {
		if _, found := servers[present]; !found {
			t.Errorf("%s is missing — the registration was lost instead of renamed", present)
		}
	}
	// The credential inside the entry must survive the move.
	var tasks struct {
		URL string `json:"url"`
	}
	if err := json.Unmarshal(servers["kontor-tasks"], &tasks); err != nil {
		t.Fatalf("unmarshal moved entry: %v", err)
	}
	if tasks.URL != "http://127.0.0.1:13120/api/mcp" {
		t.Errorf("the moved entry is not the original: url = %q", tasks.URL)
	}
	for _, kept := range []string{"someone-elses", "dashboard-lookalike"} {
		if _, found := servers[kept]; !found {
			t.Errorf("%s was removed — it is not this installation's to delete", kept)
		}
	}
}

// A file with no legacy entries must not be rewritten at all: touching another
// program's configuration for nothing is how a backup ends up with churn.
func TestMigrateLegacyEntries_NothingToDo(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", dir)
	writeConfig(t, dir, map[string]any{"someone-elses": map[string]any{"command": "/usr/local/bin/other"}})

	before, err := os.Stat(filepath.Join(dir, ".claude.json"))
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	moved, err := MigrateLegacyEntries()
	if err != nil || len(moved) != 0 {
		t.Fatalf("MigrateLegacyEntries = (%v, %v), want (nothing, nil)", moved, err)
	}
	after, err := os.Stat(filepath.Join(dir, ".claude.json"))
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if !before.ModTime().Equal(after.ModTime()) {
		t.Error("the file was rewritten although there was nothing to remove")
	}
}
