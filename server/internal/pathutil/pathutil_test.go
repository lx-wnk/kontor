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
