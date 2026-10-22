package mcpapps_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/lx-wnk/kontor/server/internal/mcpapps"
)

func TestIsolatedHomeEnv(t *testing.T) {
	t.Run("creates a private directory under the root", func(t *testing.T) {
		root := filepath.Join(t.TempDir(), "apps", "homes")
		t.Setenv("npm_config_cache", "/cache/npm")

		env, err := mcpapps.IsolatedHomeEnv(root, "app:one.v2_x-1")
		require.NoError(t, err)

		want := filepath.Join(root, "app:one.v2_x-1")
		require.Equal(t, want, env["HOME"])
		info, err := os.Stat(want)
		require.NoError(t, err)
		require.True(t, info.IsDir())
		require.Equal(t, os.FileMode(0o700), info.Mode().Perm())
	})

	t.Run("tightens an existing wider directory", func(t *testing.T) {
		root := t.TempDir()
		dir := filepath.Join(root, "app")
		require.NoError(t, os.Mkdir(dir, 0o755))
		require.NoError(t, os.Chmod(dir, 0o755))

		_, err := mcpapps.IsolatedHomeEnv(root, "app")
		require.NoError(t, err)

		info, err := os.Stat(dir)
		require.NoError(t, err)
		require.Equal(t, os.FileMode(0o700), info.Mode().Perm())
	})

	t.Run("rejects an empty root", func(t *testing.T) {
		_, err := mcpapps.IsolatedHomeEnv("", "app")
		require.Error(t, err)
		_, statErr := os.Stat("app")
		require.True(t, os.IsNotExist(statErr), "an empty root created a cwd-relative directory")
	})

	t.Run("two ids get two directories", func(t *testing.T) {
		root := t.TempDir()
		a, err := mcpapps.IsolatedHomeEnv(root, "a")
		require.NoError(t, err)
		b, err := mcpapps.IsolatedHomeEnv(root, "b")
		require.NoError(t, err)
		require.NotEqual(t, a["HOME"], b["HOME"])
	})

	t.Run("rejects ids that could leave the root", func(t *testing.T) {
		for _, id := range []string{"../x", ".", "..", "a/b", "", `a\b`, "a b"} {
			root := t.TempDir()
			_, err := mcpapps.IsolatedHomeEnv(root, id)
			require.Error(t, err, "id %q", id)
			entries, readErr := os.ReadDir(root)
			require.NoError(t, readErr)
			require.Empty(t, entries, "id %q created a directory", id)
			_, statErr := os.Stat(filepath.Join(filepath.Dir(root), "x"))
			require.True(t, os.IsNotExist(statErr), "id %q escaped the root", id)
		}
	})

	t.Run("npm cache comes from the environment when set", func(t *testing.T) {
		t.Setenv("npm_config_cache", "/custom/cache")
		env, err := mcpapps.IsolatedHomeEnv(t.TempDir(), "app")
		require.NoError(t, err)
		require.Equal(t, "/custom/cache", env["npm_config_cache"])
	})

	t.Run("npm cache defaults to the server home", func(t *testing.T) {
		serverHome := t.TempDir()
		t.Setenv("HOME", serverHome)
		t.Setenv("npm_config_cache", "")
		env, err := mcpapps.IsolatedHomeEnv(t.TempDir(), "app")
		require.NoError(t, err)
		require.Equal(t, filepath.Join(serverHome, ".npm"), env["npm_config_cache"])
	})
}

func TestIsolateHome_OnlyTheImapPresetOptsIn(t *testing.T) {
	imap, err := mcpapps.LoadPreset("imap-mcp-server")
	require.NoError(t, err)
	require.True(t, imap.IsolateHome)

	files, err := os.ReadDir("presets")
	require.NoError(t, err)
	require.NotEmpty(t, files)
	for _, f := range files {
		name := strings.TrimSuffix(f.Name(), ".json")
		if name == "imap-mcp-server" {
			continue
		}
		p, err := mcpapps.LoadPreset(name)
		require.NoError(t, err)
		require.False(t, p.IsolateHome, "preset %q must not isolate HOME", name)
	}
}

func TestHomeRootUnder(t *testing.T) {
	require.Equal(t, filepath.Join("/u", ".claude", "kontor", "apps"), mcpapps.HomeRootUnder("/u"))
}

func TestWithIsolatedHome(t *testing.T) {
	t.Run("leaves a non-isolated preset's env alone", func(t *testing.T) {
		in := map[string]string{"A": "1"}
		out, err := mcpapps.WithIsolatedHome(in, mcpapps.Preset{}, t.TempDir(), "res-1")
		require.NoError(t, err)
		require.Equal(t, in, out)
		require.NotContains(t, out, "HOME")
	})

	t.Run("adds the isolated home without mutating the input", func(t *testing.T) {
		t.Setenv("npm_config_cache", "/cache/npm")
		root := t.TempDir()
		in := map[string]string{"A": "1", "HOME": "/shared"}
		out, err := mcpapps.WithIsolatedHome(in, mcpapps.Preset{IsolateHome: true}, root, "res-1")
		require.NoError(t, err)
		require.Equal(t, map[string]string{"A": "1", "HOME": filepath.Join(root, "res-1"), "npm_config_cache": "/cache/npm"}, out)
		require.Equal(t, map[string]string{"A": "1", "HOME": "/shared"}, in)
	})

	t.Run("fails closed without a home root", func(t *testing.T) {
		_, err := mcpapps.WithIsolatedHome(map[string]string{}, mcpapps.Preset{IsolateHome: true}, "", "res-1")
		require.Error(t, err)
	})
}

func TestRemoveIsolatedHome(t *testing.T) {
	t.Run("removes the application's home and nothing beside it", func(t *testing.T) {
		parent := t.TempDir()
		root := filepath.Join(parent, "apps")
		_, err := mcpapps.IsolatedHomeEnv(root, "res-1")
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(filepath.Join(root, "res-1", "mail.json"), []byte("{}"), 0o600))
		_, err = mcpapps.IsolatedHomeEnv(root, "res-2")
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(filepath.Join(parent, "keep"), nil, 0o600))

		require.NoError(t, mcpapps.RemoveIsolatedHome(root, "res-1"))

		_, statErr := os.Stat(filepath.Join(root, "res-1"))
		require.True(t, os.IsNotExist(statErr))
		_, err = os.Stat(filepath.Join(root, "res-2"))
		require.NoError(t, err)
		_, err = os.Stat(filepath.Join(parent, "keep"))
		require.NoError(t, err)
	})

	t.Run("a missing home is not an error", func(t *testing.T) {
		require.NoError(t, mcpapps.RemoveIsolatedHome(t.TempDir(), "res-1"))
	})

	t.Run("rejects an empty root", func(t *testing.T) {
		require.Error(t, mcpapps.RemoveIsolatedHome("", "res-1"))
	})

	t.Run("rejects ids that could leave the root", func(t *testing.T) {
		for _, id := range []string{"..", ".", "../x", "a/b", ""} {
			parent := t.TempDir()
			root := filepath.Join(parent, "apps")
			require.NoError(t, os.Mkdir(root, 0o700))
			require.NoError(t, os.WriteFile(filepath.Join(parent, "x"), nil, 0o600))

			require.Error(t, mcpapps.RemoveIsolatedHome(root, id), "id %q", id)

			_, err := os.Stat(root)
			require.NoError(t, err, "id %q removed the root", id)
			_, err = os.Stat(filepath.Join(parent, "x"))
			require.NoError(t, err, "id %q removed a file outside the root", id)
		}
	})
}
