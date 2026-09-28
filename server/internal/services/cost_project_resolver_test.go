package services

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/lx-wnk/kontor/server/internal/db/ent"
)

func TestProjectFolderIndex_Match(t *testing.T) {
	idx := NewProjectFolderIndex([]*ent.ProjectFolder{
		{Path: "/code/acme", Edges: ent.ProjectFolderEdges{Project: &ent.Project{ID: "p-acme", Name: "Acme"}}},
		{Path: "/code/acme/sub/", Edges: ent.ProjectFolderEdges{Project: &ent.Project{ID: "p-sub", Name: "Sub"}}},
		{Path: "/code/orphan"},
	})

	id, name, ok := idx.Match("/code/acme/sub/pkg")
	assert.True(t, ok)
	assert.Equal(t, "p-sub", id)
	assert.Equal(t, "Sub", name)

	id, _, ok = idx.Match("/code/acme/other")
	assert.True(t, ok)
	assert.Equal(t, "p-acme", id)

	_, _, ok = idx.Match("/code/acme-tools")
	assert.False(t, ok, "prefix match must stop at a path separator")
	_, _, ok = idx.Match("/code/orphan/x")
	assert.False(t, ok, "a folder without a loaded project is no match")
	_, _, ok = idx.Match("")
	assert.False(t, ok)
}

func TestResolveUncached_DashboardFolderMatchWins(t *testing.T) {
	// ensureFoldersLocked sorts folders longest-first; mirror that ordering here.
	folders := []folderEntry{
		{path: "/code/acme/sub", name: "Acme Sub"},
		{path: "/code/acme", name: "Acme"},
	}

	// cwd under the more specific folder → that folder wins.
	path, name := resolveUncached(context.Background(), "/code/acme/sub/pkg", folders)
	assert.Equal(t, "/code/acme/sub", path)
	assert.Equal(t, "Acme Sub", name)

	// cwd under only the broader folder.
	path, name = resolveUncached(context.Background(), "/code/acme/other", folders)
	assert.Equal(t, "/code/acme", path)
	assert.Equal(t, "Acme", name)

	// Exact match on a folder path.
	path, name = resolveUncached(context.Background(), "/code/acme", folders)
	assert.Equal(t, "/code/acme", path)
	assert.Equal(t, "Acme", name)
}

func TestResolveUncached_PrefixIsBoundaryAware(t *testing.T) {
	folders := []folderEntry{{path: "/code/acme", name: "Acme"}}
	// "/code/acme-tools" must NOT match "/code/acme" (no trailing separator).
	// It is not a git repo here, so it falls through to the basename fallback.
	path, name := resolveUncached(context.Background(), "/code/acme-tools", folders)
	assert.Equal(t, "/code/acme-tools", path)
	assert.Equal(t, "acme-tools", name)
}

func TestResolveUncached_FallbackToBasename(t *testing.T) {
	// A non-git temp dir with no matching folder → fallback path = cwd, name = base.
	dir := t.TempDir()
	path, name := resolveUncached(context.Background(), dir, nil)
	assert.Equal(t, filepath.Clean(dir), path)
	assert.Equal(t, filepath.Base(dir), name)
}

func TestCostProjectResolver_EmptyCwd(t *testing.T) {
	r := NewCostProjectResolver(nil)
	path, name := r.Resolve(context.Background(), "")
	assert.Empty(t, path)
	assert.Empty(t, name)
}

func TestCostProjectResolver_CachesResult(t *testing.T) {
	r := NewCostProjectResolver(nil) // nil folders → dashboard match skipped
	dir := t.TempDir()
	p1, n1 := r.Resolve(context.Background(), dir)
	p2, n2 := r.Resolve(context.Background(), dir)
	assert.Equal(t, p1, p2)
	assert.Equal(t, n1, n2)
	// Second call must be served from cache.
	r.mu.Lock()
	_, cached := r.cache[dir]
	r.mu.Unlock()
	assert.True(t, cached, "result should be cached after first Resolve")
}
