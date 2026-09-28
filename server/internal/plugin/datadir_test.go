package plugin

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEnsureModuleDataDir_CreatesItPrivately(t *testing.T) {
	root := t.TempDir()

	dir, err := EnsureModuleDataDir(root, "obsidian")
	if err != nil {
		t.Fatalf("EnsureModuleDataDir: %v", err)
	}
	if dir != filepath.Join(root, "obsidian") {
		t.Errorf("dir = %q, want it under the module's own name", dir)
	}
	info, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o700 {
		t.Errorf("mode = %o, want 0700: what a module stores is the operator's", perm)
	}

	// Calling it again must not disturb what is already there.
	if err := os.WriteFile(filepath.Join(dir, "state.db"), []byte("x"), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if _, err := EnsureModuleDataDir(root, "obsidian"); err != nil {
		t.Fatalf("second call: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "state.db")); err != nil {
		t.Errorf("the module's data did not survive a second start: %v", err)
	}
}

// Uninstalling a module is a decision about the module, not about what it
// collected, so its directory is moved aside rather than deleted.
func TestArchiveModuleDataDir_MovesItAsideInsteadOfDeleting(t *testing.T) {
	root := t.TempDir()
	dir, err := EnsureModuleDataDir(root, "obsidian")
	if err != nil {
		t.Fatalf("EnsureModuleDataDir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "state.db"), []byte("payload"), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}

	archive, err := ArchiveModuleDataDir(root, "obsidian")
	if err != nil {
		t.Fatalf("ArchiveModuleDataDir: %v", err)
	}
	if !strings.HasPrefix(archive, dir+".removed-") {
		t.Fatalf("archive = %q, want it beside the original", archive)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Errorf("the live directory must be gone, got %v", err)
	}
	payload, err := os.ReadFile(filepath.Join(archive, "state.db"))
	if err != nil || string(payload) != "payload" {
		t.Errorf("the archived data is not intact: %v %q", err, payload)
	}

	// A module that never stored anything archives to nothing, not an error.
	if got, err := ArchiveModuleDataDir(root, "never-ran"); err != nil || got != "" {
		t.Errorf("ArchiveModuleDataDir on an absent dir = (%q, %v), want (\"\", nil)", got, err)
	}
}
