package pathutil_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/lx-wnk/kontor/server/internal/pathutil"
	"github.com/stretchr/testify/require"
)

func TestExpandLeadingTilde(t *testing.T) {
	home, err := os.UserHomeDir()
	require.NoError(t, err)

	tests := []struct {
		input string
		want  string
	}{
		{"~", home},
		{"~/foo", filepath.Join(home, "foo")},
		{"~/.claude-work", filepath.Join(home, ".claude-work")},
		{"/absolute/path", "/absolute/path"},
		{"relative/path", "relative/path"},
		{"", ""},
		{"~other", "~other"}, // ~user form — not expanded
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := pathutil.ExpandLeadingTilde(tt.input)
			require.Equal(t, tt.want, got)
		})
	}
}

func TestExpandLeadingTilde_FakeHome(t *testing.T) {
	t.Setenv("HOME", "/tmp/fakehome")
	require.Equal(t, "/tmp/fakehome/.claude-work", pathutil.ExpandLeadingTilde("~/.claude-work"))
	require.Equal(t, "/tmp/fakehome", pathutil.ExpandLeadingTilde("~"))
}

func TestCanonical(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	target := filepath.Join(home, ".claude-personal")
	require.NoError(t, os.Mkdir(target, 0o755))
	require.NoError(t, os.Symlink(target, filepath.Join(home, ".claude")))
	require.NoError(t, os.Symlink(filepath.Join(home, "gone"), filepath.Join(home, ".dangling")))
	resolvedTarget, err := filepath.EvalSymlinks(target)
	require.NoError(t, err)

	tests := []struct {
		name, input, want string
	}{
		{"tilde symlink resolves to its target", "~/.claude", resolvedTarget},
		{"target resolves to itself", target, resolvedTarget},
		{"missing dir keeps its expanded path", "~/.claude-new/", filepath.Join(home, ".claude-new")},
		{"dangling symlink keeps its own path", filepath.Join(home, ".dangling"), filepath.Join(home, ".dangling")},
		{"blank stays blank", "  ", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, pathutil.Canonical(tt.input))
		})
	}
}
