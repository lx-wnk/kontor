package services

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSpawnPolicy_ExtraRootAllowsCwdOutsideProjectRoots(t *testing.T) {
	home, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	t.Setenv("HOME", home)
	project := filepath.Join(home, "project")
	session := filepath.Join(home, "cache", "kontor", "session")
	require.NoError(t, os.MkdirAll(project, 0o700))
	require.NoError(t, os.MkdirAll(session, 0o700))
	roots := func(context.Context) ([]string, error) { return []string{project}, nil }

	require.ErrorIs(t, NewSpawnPolicy(roots).Allow(t.Context(), session), ErrCwdNotAllowed)
	require.NoError(t, NewSpawnPolicy(roots, session).Allow(t.Context(), session))
	require.ErrorIs(t, NewSpawnPolicy(roots, session).Allow(t.Context(), filepath.Join(home, "elsewhere")), ErrCwdNotAllowed)
}

func TestSpawnPolicy_BlacklistBeatsExtraRoot(t *testing.T) {
	home, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	t.Setenv("HOME", home)
	under := filepath.Join(home, ".claude", "kontor")
	require.NoError(t, os.MkdirAll(under, 0o700))

	require.ErrorIs(t, NewSpawnPolicy(nil, under).Allow(t.Context(), under), ErrCwdBlacklisted)
}
