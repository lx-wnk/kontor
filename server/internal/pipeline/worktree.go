package pipeline

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/lx-wnk/kontor/server/internal/db/ent"
	"github.com/lx-wnk/kontor/server/internal/worktree"
)

// gitRunner runs git for worktree mutations with a bounded timeout.
var gitRunner = worktree.NewRunner()

// EnsureTaskWorktree is the exported production wiring for OrchestratorOptions.EnsureWorktreeFn.
var EnsureTaskWorktree = ensureTaskWorktree

// ensureTaskWorktree creates a git worktree for task at <worktreeRoot>/<slug>.
// Idempotent: returns the path immediately if the directory already exists.
// When task.SourceBranch is nil, falls back to "feat/<slug>" as the branch name.
// Returns the worktree path and the branch name that was used.
func ensureTaskWorktree(ctx context.Context, task *ent.Task, worktreeRoot string) (path, branch string, err error) {
	worktreeRoot = worktree.DefaultRoot(worktreeRoot)
	worktreePath := worktree.PathFor(worktreeRoot, task.Slug)
	branch = worktree.CreateBranch(task.SourceBranch, task.Slug)

	// Already exists — assume a prior run set it up correctly.
	if _, err := os.Stat(worktreePath); err == nil {
		return worktreePath, branch, nil
	}

	if err := os.MkdirAll(worktreeRoot, 0o750); err != nil {
		return "", "", fmt.Errorf("ensureTaskWorktree: mkdir %s: %w", worktreeRoot, err)
	}

	// Authoritative preflight: reject a branch already held by any worktree
	// (e.g. a terminal task's leftover) before git fails with a terse message.
	// Best-effort — a git error here is ignored so a transient failure does not
	// block an otherwise-valid checkout; `git worktree add` is the real gate.
	if held, hErr := worktree.BranchCheckedOutAt(ctx, task.Cwd, branch); hErr == nil && held != "" {
		return "", "", fmt.Errorf("ensureTaskWorktree: branch %q already checked out at %s", branch, held)
	}

	// Try creating a new branch + worktree from HEAD.
	out, cmdErr := gitRunner.Combined(ctx, task.Cwd, "worktree", "add", "-b", branch, worktreePath)
	if cmdErr != nil {
		// Branch already exists — check it out in the worktree without -b.
		out2, err2 := gitRunner.Combined(ctx, task.Cwd, "worktree", "add", worktreePath, branch)
		if err2 != nil {
			return "", "", fmt.Errorf("ensureTaskWorktree: git worktree add failed (%s / %s): %w",
				strings.TrimSpace(out), strings.TrimSpace(out2), err2)
		}
	}
	return worktreePath, branch, nil
}

// removeTaskWorktree runs `git worktree remove --force` for the given path.
// Non-fatal by design — callers log the error and continue.
func removeTaskWorktree(ctx context.Context, cwd, worktreePath string) error {
	if worktreePath == "" {
		return nil
	}
	out, err := gitRunner.Combined(ctx, cwd, "worktree", "remove", "--force", worktreePath)
	if err != nil {
		// Directory already gone — prune stale metadata and treat as success.
		lower := strings.ToLower(out)
		if strings.Contains(lower, "is not a working tree") || strings.Contains(lower, "no such file") {
			_, _ = gitRunner.Combined(ctx, cwd, "worktree", "prune")
			return nil
		}
		return fmt.Errorf("removeTaskWorktree: %s: %w", strings.TrimSpace(out), err)
	}
	return nil
}
