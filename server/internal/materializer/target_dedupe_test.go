package materializer_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/lx-wnk/kontor/server/internal/db/repo"
	"github.com/lx-wnk/kontor/server/internal/materializer"
	"github.com/lx-wnk/kontor/server/internal/parser"
)

// TestTargets_TwoPathsToOneDirectoryProduceOneTarget covers the layout this
// machine actually has, found by the first dry run against real config
// directories: ~/.claude is a symlink to ~/.claude-personal, and
// AllClaudeConfigDirs returns both. They are one directory.
//
// Two targets for one directory is not a duplicate write — it is worse. The
// first target writes the file and records it under its own target_key. The
// second finds content it did not record, because the record belongs to the
// other key, and Classify calls that foreign: a file we ourselves just wrote,
// reported as somebody else's, on every run from now on. The refusal rules hold
// and nothing is lost; what breaks is the report, permanently.
func TestTargets_TwoPathsToOneDirectoryProduceOneTarget(t *testing.T) {
	base := t.TempDir()
	real := filepath.Join(base, "claude-personal")
	require.NoError(t, os.MkdirAll(real, 0o700))
	link := filepath.Join(base, "claude")
	require.NoError(t, os.Symlink(real, link))

	r := materializer.Resolver{
		NodeID:             "local",
		ClaudeConfigDirs:   func() []string { return []string{link, real} },
		ProviderConfigDirs: func() []parser.ProviderConfigDir { return nil },
	}

	got := r.Targets(repo.Scope{Kind: repo.ScopeGlobal})

	require.Len(t, got, 1,
		"a symlink and its destination are one directory and must yield one target, else the second run reports our own file as foreign forever")
}

// TestTargets_DistinctDirectoriesAreStillSeparate holds the other side of the
// line: deduplication must not collapse two config directories that really are
// different, which is the ordinary multi-profile setup.
func TestTargets_DistinctDirectoriesAreStillSeparate(t *testing.T) {
	base := t.TempDir()
	personal := filepath.Join(base, "claude-personal")
	work := filepath.Join(base, "claude-work")
	require.NoError(t, os.MkdirAll(personal, 0o700))
	require.NoError(t, os.MkdirAll(work, 0o700))

	r := materializer.Resolver{
		NodeID:             "local",
		ClaudeConfigDirs:   func() []string { return []string{personal, work} },
		ProviderConfigDirs: func() []parser.ProviderConfigDir { return nil },
	}

	require.Len(t, r.Targets(repo.Scope{Kind: repo.ScopeGlobal}), 2,
		"two genuinely different config directories must both be materialized into")
}
