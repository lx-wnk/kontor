package claudeconfig_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lx-wnk/kontor/server/internal/claudeconfig"
)

func writeConfigFile(t *testing.T, dir, content string, mode os.FileMode) string {
	t.Helper()
	path := filepath.Join(dir, ".claude.json")
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	return path
}

func readConfigMap(t *testing.T, path string) map[string]any {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("unmarshal config: %v", err)
	}
	return m
}

func assertSingleFile(t *testing.T, dir string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read dir: %v", err)
	}
	if len(entries) != 1 {
		names := make([]string, len(entries))
		for i, e := range entries {
			names[i] = e.Name()
		}
		t.Fatalf("expected exactly one file in %s, got %v", dir, names)
	}
}

func TestWriteServerEntry_PreservesOtherKeys(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", dir)
	path := writeConfigFile(t, dir, `{"numStartups":7,"mcpServers":{"other":{"command":"x"}}}`, 0o600)

	if err := claudeconfig.WriteServerEntry("mine", json.RawMessage(`{"command":"y"}`)); err != nil {
		t.Fatalf("WriteServerEntry: %v", err)
	}

	m := readConfigMap(t, path)
	if got := m["numStartups"]; got != float64(7) {
		t.Fatalf("numStartups = %v, want 7", got)
	}
	servers, ok := m["mcpServers"].(map[string]any)
	if !ok {
		t.Fatalf("mcpServers not an object: %v", m["mcpServers"])
	}
	if _, ok := servers["other"]; !ok {
		t.Fatalf("expected 'other' server to survive, got %v", servers)
	}
	if _, ok := servers["mine"]; !ok {
		t.Fatalf("expected 'mine' server to be added, got %v", servers)
	}
	assertSingleFile(t, dir)
}

func TestWriteServerEntry_KeepsExistingMode(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", dir)
	path := writeConfigFile(t, dir, `{"mcpServers":{}}`, 0o600)

	if err := claudeconfig.WriteServerEntry("mine", json.RawMessage(`{"command":"y"}`)); err != nil {
		t.Fatalf("WriteServerEntry: %v", err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("mode = %v, want 0600", perm)
	}
}

func TestWriteServerEntry_CreatesMissingFileWith0600(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", dir)
	path := filepath.Join(dir, ".claude.json")

	if err := claudeconfig.WriteServerEntry("mine", json.RawMessage(`{"command":"y"}`)); err != nil {
		t.Fatalf("WriteServerEntry: %v", err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("mode = %v, want 0600", perm)
	}
	assertSingleFile(t, dir)
}

func TestWriteServerEntry_RefusesSymlink(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", dir)
	targetPath := filepath.Join(dir, "real.json")
	original := `{"mcpServers":{"other":{"command":"x"}}}`
	if err := os.WriteFile(targetPath, []byte(original), 0o600); err != nil {
		t.Fatalf("write target: %v", err)
	}
	linkPath := filepath.Join(dir, ".claude.json")
	if err := os.Symlink(targetPath, linkPath); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	err := claudeconfig.WriteServerEntry("mine", json.RawMessage(`{"command":"y"}`))
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("error = %q, want it to contain 'symlink'", err.Error())
	}

	data, err := os.ReadFile(targetPath)
	if err != nil {
		t.Fatalf("read target after failed write: %v", err)
	}
	if string(data) != original {
		t.Fatalf("target file changed: got %q, want %q", string(data), original)
	}
}

func TestRemoveServerEntry_DropsOnlyThatKey(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", dir)
	path := writeConfigFile(t, dir, `{"numStartups":3,"mcpServers":{"keep":{"command":"a"},"drop":{"command":"b"}}}`, 0o600)

	if err := claudeconfig.RemoveServerEntry("drop"); err != nil {
		t.Fatalf("RemoveServerEntry: %v", err)
	}

	m := readConfigMap(t, path)
	servers, ok := m["mcpServers"].(map[string]any)
	if !ok {
		t.Fatalf("mcpServers not an object: %v", m["mcpServers"])
	}
	if _, ok := servers["drop"]; ok {
		t.Fatalf("expected 'drop' to be removed, got %v", servers)
	}
	if _, ok := servers["keep"]; !ok {
		t.Fatalf("expected 'keep' to survive, got %v", servers)
	}
	if got := m["numStartups"]; got != float64(3) {
		t.Fatalf("numStartups = %v, want 3", got)
	}
	assertSingleFile(t, dir)
}

func TestRemoveServerEntry_MissingFileIsNotAnError(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", dir)

	if err := claudeconfig.RemoveServerEntry("anything"); err != nil {
		t.Fatalf("RemoveServerEntry: %v", err)
	}
}

func TestRemoveServerEntry_MissingKeyLeavesOthersAlone(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", dir)
	path := writeConfigFile(t, dir, `{"numStartups":5,"mcpServers":{"keep":{"command":"a"}}}`, 0o600)

	if err := claudeconfig.RemoveServerEntry("missing"); err != nil {
		t.Fatalf("RemoveServerEntry: %v", err)
	}

	m := readConfigMap(t, path)
	servers, ok := m["mcpServers"].(map[string]any)
	if !ok {
		t.Fatalf("mcpServers not an object: %v", m["mcpServers"])
	}
	if _, ok := servers["keep"]; !ok {
		t.Fatalf("expected 'keep' to survive, got %v", servers)
	}
	if got := m["numStartups"]; got != float64(5) {
		t.Fatalf("numStartups = %v, want 5", got)
	}
	assertSingleFile(t, dir)
}

func TestWriteServerEntry_ConfigHoldingNullIsNotACrash(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", dir)
	writeConfigFile(t, dir, "null", 0o600)

	if err := claudeconfig.WriteServerEntry("mail", json.RawMessage(`{"command":"x"}`)); err != nil {
		t.Fatalf("write into a null config: %v", err)
	}

	servers, err := claudeconfig.UserMCPServers()
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	var got map[string]string
	if err := json.Unmarshal(servers["mail"], &got); err != nil {
		t.Fatalf("parse the entry that was read back: %v", err)
	}
	if got["command"] != "x" {
		t.Fatalf("command = %q, want x", got["command"])
	}
}
