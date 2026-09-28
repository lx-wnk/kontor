package services

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/lx-wnk/kontor/sdk"
	"github.com/lx-wnk/kontor/server/internal/db/ent"
	"github.com/lx-wnk/kontor/server/internal/db/repo"
	"github.com/lx-wnk/kontor/server/internal/worktree"
)

var (
	ErrNoWorktree    = errors.New("task has no worktree")
	ErrWorktreeDirty = errors.New("worktree has uncommitted changes")
)

// WorktreeManager exposes a task's git worktree status plus the user-initiated
// create/remove lifecycle. Run-driven mutations live in package pipeline (see
// `pipeline/worktree.go`); both layers share the primitives in package
// worktree. This service is consumed by the HTTP/MCP layers.
type WorktreeManager struct {
	taskRepo repo.TaskRepo
	runner   *worktree.Runner
}

// NewWorktreeManager returns a WorktreeManager backed by the given task repo.
func NewWorktreeManager(taskRepo repo.TaskRepo) *WorktreeManager {
	return &WorktreeManager{
		taskRepo: taskRepo,
		runner:   worktree.NewRunner(),
	}
}

// WorktreeStatus returns the worktree status for the given task: branch, ahead/
// behind counts vs the task's base branch on `origin`, dirty flag, and dirty
// file count.
//
// Behaviour notes:
//   - When the task has no worktree, returns (nil, nil) — the caller renders
//     "no worktree" gracefully.
//   - When `origin/<base>` cannot be resolved (e.g. the base branch is local-
//     only), Ahead and Behind are returned as nil pointers — never an error.
//   - Any underlying git error other than "task not found" is swallowed and
//     surfaces as zeroed counts; the goal is best-effort UI enrichment.
func (m *WorktreeManager) WorktreeStatus(ctx context.Context, taskID string) (*sdk.WorktreeStatusDTO, error) {
	task, err := m.taskRepo.GetByID(ctx, taskID)
	if err != nil {
		if ent.IsNotFound(err) {
			return nil, fmt.Errorf("worktree_status: task %s: %w", taskID, err)
		}
		return nil, fmt.Errorf("worktree_status: get task: %w", err)
	}

	if task.WorktreePath == nil || *task.WorktreePath == "" {
		return nil, nil
	}
	cwd := *task.WorktreePath

	branch := m.currentBranch(ctx, cwd)
	if branch == "" && task.SourceBranch != nil {
		branch = *task.SourceBranch
	}

	base := ""
	if task.TargetBranch != nil && *task.TargetBranch != "" {
		base = *task.TargetBranch
	} else if task.SourceBranch != nil && *task.SourceBranch != "" {
		base = *task.SourceBranch
	}

	dirty, fileCount := m.dirtyState(ctx, cwd)

	dto := &sdk.WorktreeStatusDTO{
		Branch:    branch,
		Dirty:     dirty,
		FileCount: fileCount,
	}

	if base != "" && branch != "" && m.remoteRefExists(ctx, cwd, base) {
		ahead := m.revListCount(ctx, cwd, "origin/"+base+".."+branch)
		behind := m.revListCount(ctx, cwd, branch+"..origin/"+base)
		if ahead != nil {
			dto.Ahead = ahead
		}
		if behind != nil {
			dto.Behind = behind
		}
	}

	return dto, nil
}

func (m *WorktreeManager) currentBranch(ctx context.Context, cwd string) string {
	out, err := m.runner.Output(ctx, cwd, "rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(out)
}

func (m *WorktreeManager) remoteRefExists(ctx context.Context, cwd, base string) bool {
	_, err := m.runner.Output(ctx, cwd, "rev-parse", "--verify", "--quiet", "refs/remotes/origin/"+base)
	return err == nil
}

// revListCount returns the integer count from `git rev-list --count <range>`,
// or nil if the command failed (e.g. ambiguous ref). Pointer return lets the
// API expose JSON null when the count is genuinely unknown.
func (m *WorktreeManager) revListCount(ctx context.Context, cwd, rng string) *int {
	out, err := m.runner.Output(ctx, cwd, "rev-list", "--count", rng)
	if err != nil {
		return nil
	}
	trimmed := strings.TrimSpace(out)
	n, err := strconv.Atoi(trimmed)
	if err != nil {
		return nil
	}
	return &n
}

// HasUnpushedWork reports whether the task's worktree holds work that would be
// lost by a force-remove: commits on the worktree branch not present on any
// origin remote ref, or uncommitted changes in the working tree.
func (m *WorktreeManager) HasUnpushedWork(ctx context.Context, task *ent.Task) bool {
	if task == nil || task.WorktreePath == nil || *task.WorktreePath == "" {
		return false
	}
	cwd := *task.WorktreePath
	// Directory already gone — nothing to retain; let the caller clear the stale path.
	if _, err := os.Stat(cwd); os.IsNotExist(err) {
		return false
	}
	if dirty, _ := m.dirtyState(ctx, cwd); dirty {
		return true
	}
	n := m.unpushedCommitCount(ctx, cwd)
	if n == nil {
		// Conservative: retain rather than risk losing work if the git command fails.
		return true
	}
	return *n > 0
}

// unpushedCommitCount counts commits reachable from HEAD that are not present
// on any origin remote ref. Each token is passed as a discrete argument because
// Runner.Output is variadic — a single "HEAD --not --remotes=origin" string
// would be treated as one argument and fail.
func (m *WorktreeManager) unpushedCommitCount(ctx context.Context, cwd string) *int {
	out, err := m.runner.Output(ctx, cwd, "rev-list", "--count", "HEAD", "--not", "--remotes=origin")
	if err != nil {
		return nil
	}
	n, err := strconv.Atoi(strings.TrimSpace(out))
	if err != nil {
		return nil
	}
	return &n
}

func (m *WorktreeManager) dirtyState(ctx context.Context, cwd string) (bool, int) {
	out, err := m.runner.Output(ctx, cwd, "status", "--porcelain")
	if err != nil {
		return false, 0
	}
	trimmed := strings.TrimRight(out, "\n")
	if trimmed == "" {
		return false, 0
	}
	count := strings.Count(trimmed, "\n") + 1
	return true, count
}

func (m *WorktreeManager) CreateWorktree(ctx context.Context, taskID string) (string, error) {
	task, err := m.taskRepo.GetByID(ctx, taskID)
	if err != nil {
		return "", fmt.Errorf("create_worktree: task %s: %w", taskID, err)
	}

	// Idempotent: return existing path if the directory is still present.
	if task.WorktreePath != nil && *task.WorktreePath != "" {
		if _, statErr := os.Stat(*task.WorktreePath); statErr == nil {
			return *task.WorktreePath, nil
		}
	}

	branch := worktree.CreateBranch(task.SourceBranch, task.Slug)
	root := worktree.DefaultRoot("")
	worktreePath := worktree.PathFor("", task.Slug)

	if err := os.MkdirAll(root, 0o750); err != nil {
		return "", fmt.Errorf("create_worktree: mkdir root: %w", err)
	}

	_, gitErr := m.runner.Combined(ctx, task.Cwd, "worktree", "add", "-b", branch, worktreePath)
	if gitErr != nil {
		// Fallback: branch may already exist — try without -b.
		if _, err2 := m.runner.Combined(ctx, task.Cwd, "worktree", "add", worktreePath, branch); err2 != nil {
			return "", fmt.Errorf("create_worktree: git worktree add: %w", gitErr)
		}
	}

	if _, err := m.taskRepo.Update(ctx, taskID, repo.UpdateTaskInput{WorktreePath: &worktreePath}); err != nil {
		return "", fmt.Errorf("create_worktree: persist path: %w", err)
	}
	return worktreePath, nil
}

func (m *WorktreeManager) RemoveWorktree(ctx context.Context, taskID string, force bool) error {
	task, err := m.taskRepo.GetByID(ctx, taskID)
	if err != nil {
		return fmt.Errorf("remove_worktree: task %s: %w", taskID, err)
	}

	if task.WorktreePath == nil || *task.WorktreePath == "" {
		return ErrNoWorktree
	}
	path := *task.WorktreePath

	dirty, _ := m.dirtyState(ctx, path)
	if dirty && !force {
		return ErrWorktreeDirty
	}

	args := []string{"worktree", "remove"}
	if force {
		args = append(args, "--force")
	}
	args = append(args, path)

	out, gitErr := m.runner.Combined(ctx, task.Cwd, args...)
	if gitErr != nil {
		// Directory already gone — prune stale metadata and treat as success.
		lower := strings.ToLower(out)
		if strings.Contains(lower, "is not a working tree") || strings.Contains(lower, "no such file") {
			_, _ = m.runner.Combined(ctx, task.Cwd, "worktree", "prune")
		} else {
			return fmt.Errorf("remove_worktree: git worktree remove: %w", gitErr)
		}
	}

	if _, err := m.taskRepo.Update(ctx, taskID, repo.UpdateTaskInput{ClearWorktreePath: true}); err != nil {
		return fmt.Errorf("remove_worktree: clear path: %w", err)
	}
	return nil
}
