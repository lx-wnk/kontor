package scheduler

import (
	"context"
	"errors"

	"github.com/lx-wnk/kontor/server/internal/db/repo"
	"github.com/lx-wnk/kontor/server/internal/worktree"
)

// CheckRunMode reports why a routine with runMode and cwd cannot be saved.
func CheckRunMode(ctx context.Context, runMode, cwd string) error {
	if !repo.IsValidRunMode(runMode) {
		return errors.New("runMode must be job or pipeline")
	}
	if runMode == repo.RunModePipeline && !worktree.IsGitWorkTree(ctx, cwd) {
		return errors.New("working directory is not a git repository")
	}
	return nil
}
