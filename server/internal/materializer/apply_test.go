package materializer_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/lx-wnk/kontor/server/internal/materializer"
)

func TestApply_WritesTheFileAndCreatesItsDirectories(t *testing.T) {
	root := t.TempDir()
	target := userTarget(root)
	path, err := materializer.SkillPath(target, "code-review")
	require.NoError(t, err)

	content := want(t, "v1")
	require.NoError(t, materializer.Apply(target, path, content))

	got, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, content, got)

	info, err := os.Stat(path)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o600), info.Mode().Perm(), "a skill file sits beside session transcripts")
}

func TestApply_RefusesASymlinkedDirectoryBelowTheRoot(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "outside")
	require.NoError(t, os.MkdirAll(outside, 0o700))
	require.NoError(t, os.Symlink(outside, filepath.Join(root, "skills")))

	target := userTarget(root)
	path, err := materializer.SkillPath(target, "code-review")
	require.NoError(t, err)

	err = materializer.Apply(target, path, want(t, "v1"))
	require.Error(t, err)
	require.Contains(t, err.Error(), "symlink")

	entries, rerr := os.ReadDir(outside)
	require.NoError(t, rerr)
	require.Empty(t, entries, "nothing may be written through a symlink out of the configured root")
}

func TestApply_ARootThatIsItselfASymlinkIsFine(t *testing.T) {
	real := filepath.Join(t.TempDir(), "claude-personal")
	require.NoError(t, os.MkdirAll(real, 0o700))
	link := filepath.Join(t.TempDir(), "claude")
	require.NoError(t, os.Symlink(real, link))

	target := userTarget(link)
	path, err := materializer.SkillPath(target, "code-review")
	require.NoError(t, err)

	require.NoError(t, materializer.Apply(target, path, want(t, "v1")),
		"~/.claude linked into ~/.claude-personal is an ordinary dotfiles layout, not an attack")
}

func TestApply_AFailedRenameLeavesNoPartialFileAndNoTempFile(t *testing.T) {
	root := t.TempDir()
	target := userTarget(root)
	path, err := materializer.SkillPath(target, "code-review")
	require.NoError(t, err)

	// A directory where the file belongs makes the rename fail after the temp
	// file is already written and synced — the §9 "rename fails" row.
	require.NoError(t, os.MkdirAll(path, 0o700))

	require.Error(t, materializer.Apply(target, path, want(t, "v1")))

	entries, rerr := os.ReadDir(filepath.Dir(path))
	require.NoError(t, rerr)
	require.Len(t, entries, 1, "only the pre-existing entry survives; the temp file is cleaned up")
	require.Equal(t, "SKILL.md", entries[0].Name())
	require.True(t, entries[0].IsDir(), "the target was untouched")
}
