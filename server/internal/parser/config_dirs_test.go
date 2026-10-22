package parser_test

import (
	"os"
	"path/filepath"
	"testing"

	sdk "github.com/lx-wnk/kontor/sdk"
	"github.com/lx-wnk/kontor/server/internal/parser"
)

// TestAllAgentConfigDirs_DefaultAlwaysVariantsOnlyWhenPresent pins the two
// halves of the discovery contract apart.
//
// The configured dirs (CLAUDE_CONFIG_DIR, ~/.claude) are returned whether or
// not they exist on disk: serverapp/di.go snapshots this list once at boot into
// claudesettings.NewReader, so a ~/.claude created after the server started
// would otherwise never be searched for deny rules. Consumers stat lazily.
//
// The optional variants (~/.claude-personal, …) are guesses, not configuration,
// and are only returned when they hold a projects/ dir.
func TestAllAgentConfigDirs_DefaultAlwaysVariantsOnlyWhenPresent(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	t.Setenv("DASHBOARD_CLAUDE_CONFIG_DIRS", "")

	present := filepath.Join(home, ".claude-personal")
	if err := os.MkdirAll(filepath.Join(present, "projects"), 0o755); err != nil {
		t.Fatal(err)
	}

	var got []string
	for _, d := range parser.AllAgentConfigDirs() {
		if d.Provider != sdk.ProviderClaude {
			t.Errorf("unexpected provider %s for %q — this path is Claude-only", d.Provider, d.Path)
		}
		got = append(got, d.Path)
	}

	want := []string{filepath.Join(home, ".claude"), present}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
}

// ~/.claude as a symlink to ~/.claude-personal is one account: listing both
// made every per-account view count its sessions twice.
func TestAllClaudeConfigDirs_SymlinkToVariantIsListedOnce(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	t.Setenv("DASHBOARD_CLAUDE_CONFIG_DIRS", "")

	target := filepath.Join(home, ".claude-personal")
	if err := os.MkdirAll(filepath.Join(target, "projects"), 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(home, ".claude")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}

	got := parser.AllClaudeConfigDirs()
	if len(got) != 1 || got[0] != link {
		t.Fatalf("got %v, want [%s]", got, link)
	}
}
