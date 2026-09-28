package services

import (
	"context"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/lx-wnk/kontor/server/internal/db/ent"
	"github.com/lx-wnk/kontor/server/internal/db/repo"
)

// gitResolveTimeout bounds each git invocation so a slow/hung repo can never
// stall the cost scan.
const gitResolveTimeout = 3 * time.Second

// CostProjectResolver maps a session working directory to a stable grouping key
// (path) and a human display name. Resolution precedence, given an absolute cwd:
//
//  1. Dashboard project: the ProjectFolder whose path is the longest prefix of
//     cwd → that folder's owning Project name (key = folder path).
//  2. Git repo root: the main checkout, with linked worktrees collapsed onto it
//     via `git rev-parse --git-common-dir` → key = root, name = filepath.Base(root).
//  3. Fallback: the cwd itself → name = filepath.Base(cwd).
//
// Results are cached per cwd (many sessions share a handful of cwds), and the
// dashboard folder list is loaded once on first use. Safe for concurrent use.
//
// It satisfies the history.ProjectResolver interface structurally; the wiring in
// the composition root performs the assignment, so this package does not depend
// on the history package.
type CostProjectResolver struct {
	folders repo.ProjectFolderRepo

	mu       sync.Mutex
	cache    map[string][2]string // cwd → {path, name}
	folders0 ProjectFolderIndex   // loaded once
	loaded   bool
}

type folderEntry struct {
	path      string
	projectID string
	name      string
}

// ProjectFolderIndex matches a cwd to the registered project folder that
// contains it, most specific folder first.
type ProjectFolderIndex []folderEntry

// NewProjectFolderIndex indexes rows, which need their project edge loaded
// (ProjectFolderRepo.ListAll loads it).
func NewProjectFolderIndex(rows []*ent.ProjectFolder) ProjectFolderIndex {
	idx := make(ProjectFolderIndex, 0, len(rows))
	for _, f := range rows {
		e := folderEntry{path: filepath.Clean(f.Path)}
		if f.Edges.Project != nil {
			e.projectID = f.Edges.Project.ID
			e.name = f.Edges.Project.Name
		}
		idx = append(idx, e)
	}
	sort.Slice(idx, func(i, j int) bool { return len(idx[i].path) > len(idx[j].path) })
	return idx
}

// Match returns the ID and name of the project whose folder contains cwd.
func (idx ProjectFolderIndex) Match(cwd string) (projectID, name string, ok bool) {
	if cwd == "" {
		return "", "", false
	}
	f, found := matchFolder(filepath.Clean(cwd), idx)
	if !found || f.projectID == "" {
		return "", "", false
	}
	return f.projectID, f.name, true
}

// matchFolder returns the first folder that is clean or an ancestor of it;
// folders must be sorted longest path first.
func matchFolder(clean string, folders []folderEntry) (folderEntry, bool) {
	for _, f := range folders {
		if f.path == "" {
			continue
		}
		if clean == f.path || strings.HasPrefix(clean, f.path+string(filepath.Separator)) {
			return f, true
		}
	}
	return folderEntry{}, false
}

// NewCostProjectResolver builds a resolver backed by the dashboard ProjectFolder
// repo. folders may be nil, in which case dashboard-project matching is skipped
// and resolution falls through to git/basename.
func NewCostProjectResolver(folders repo.ProjectFolderRepo) *CostProjectResolver {
	return &CostProjectResolver{folders: folders, cache: make(map[string][2]string)}
}

// Resolve returns the grouping key (path) and display name for a session cwd.
func (r *CostProjectResolver) Resolve(ctx context.Context, cwd string) (string, string) {
	if cwd == "" {
		return "", ""
	}

	r.mu.Lock()
	if v, ok := r.cache[cwd]; ok {
		r.mu.Unlock()
		return v[0], v[1]
	}
	r.ensureFoldersLocked(ctx)
	folders := r.folders0
	r.mu.Unlock()

	path, name := resolveUncached(ctx, cwd, folders)

	r.mu.Lock()
	r.cache[cwd] = [2]string{path, name}
	r.mu.Unlock()
	return path, name
}

// ensureFoldersLocked loads the dashboard folder list once. Caller holds r.mu.
func (r *CostProjectResolver) ensureFoldersLocked(ctx context.Context) {
	if r.loaded || r.folders == nil {
		r.loaded = true
		return
	}
	r.loaded = true
	rows, err := r.folders.ListAll(ctx)
	if err != nil {
		return // leave folders0 empty; resolution falls through to git/basename
	}
	r.folders0 = NewProjectFolderIndex(rows)
}

// resolveUncached applies the three-tier precedence for a single cwd.
func resolveUncached(ctx context.Context, cwd string, folders []folderEntry) (string, string) {
	clean := filepath.Clean(cwd)

	// 1. Dashboard folder prefix match (folders are sorted longest-first).
	if f, ok := matchFolder(clean, folders); ok {
		name := f.name
		if name == "" {
			name = filepath.Base(f.path)
		}
		return f.path, name
	}

	// 2. Git repo root, with worktrees collapsed onto the main checkout.
	if root, ok := gitRepoRoot(ctx, clean); ok {
		return root, filepath.Base(root)
	}

	// 3. Fallback: the cwd itself.
	return clean, filepath.Base(clean)
}

// gitRepoRoot returns the main repository root for cwd, collapsing linked
// worktrees onto the primary checkout. Returns false on any git failure.
func gitRepoRoot(ctx context.Context, cwd string) (string, bool) {
	ctx, cancel := context.WithTimeout(ctx, gitResolveTimeout)
	defer cancel()

	// --git-common-dir points at the shared .git directory (the main repo's even
	// from inside a linked worktree). Its parent is the main working tree.
	if out, err := exec.CommandContext(ctx, "git", "-C", cwd,
		"rev-parse", "--path-format=absolute", "--git-common-dir").Output(); err == nil {
		commonDir := strings.TrimSpace(string(out))
		if commonDir != "" && filepath.Base(commonDir) == ".git" {
			return filepath.Dir(commonDir), true
		}
	}

	// Fallback: the working tree of the current checkout (not worktree-collapsed).
	if out, err := exec.CommandContext(ctx, "git", "-C", cwd,
		"rev-parse", "--show-toplevel").Output(); err == nil {
		if root := strings.TrimSpace(string(out)); root != "" {
			return root, true
		}
	}

	return "", false
}
