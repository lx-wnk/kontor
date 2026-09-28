// Package worktree holds stdlib-only primitives for deriving git-worktree
// locations/branches and running git commands with a bounded timeout. It is a
// leaf: both the lifecycle layer (pipeline) and the inspection layer (services)
// consume it, but it imports nothing from the project itself.
package worktree

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

const (
	// DefaultRootDirName is the directory under $HOME that holds auto-created
	// worktrees when no explicit root is configured.
	DefaultRootDirName = "kontor-worktrees"
	// RenamedRootDirName is what that directory was called before the project
	// was renamed. An installation that already has one keeps using it: the
	// checkouts inside are what running agents are working in.
	RenamedRootDirName = "dashboard-worktrees"
	// BranchPrefix is prepended to a task slug when no source branch is set.
	BranchPrefix = "feat/"
)

const (
	// defaultReadTimeout bounds inspection reads (Output) — fast metadata
	// queries that should never hang.
	defaultReadTimeout = 15 * time.Second
	// defaultMutationTimeout bounds mutations (Combined) such as
	// `git worktree add`, which can legitimately run long on large repos or
	// slow filesystems; the tighter read bound would SIGKILL a valid checkout.
	defaultMutationTimeout = 120 * time.Second
)

// Runner executes git commands with a bounded per-call timeout that depends on
// whether the call is a read (Output) or a mutation (Combined).
type Runner struct {
	bin             string
	readTimeout     time.Duration
	mutationTimeout time.Duration
}

// NewRunner resolves the git binary via PATH (falling back to "git") and
// returns a Runner with the default read/mutation timeouts.
func NewRunner() *Runner {
	bin, err := exec.LookPath("git")
	if err != nil || bin == "" {
		bin = "git"
	}
	return &Runner{bin: bin, readTimeout: defaultReadTimeout, mutationTimeout: defaultMutationTimeout}
}

// Output runs git in cwd and returns stdout only — for read-only inspection
// where stderr noise would corrupt the parsed value.
func (r *Runner) Output(ctx context.Context, cwd string, args ...string) (string, error) {
	return r.run(ctx, cwd, false, args...)
}

// Combined runs git in cwd and returns stdout+stderr merged — for mutations
// where the error message must surface git's diagnostics.
func (r *Runner) Combined(ctx context.Context, cwd string, args ...string) (string, error) {
	return r.run(ctx, cwd, true, args...)
}

func (r *Runner) run(ctx context.Context, cwd string, combined bool, args ...string) (string, error) {
	timeout := r.readTimeout
	if combined {
		timeout = r.mutationTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	// #nosec G204 -- r.bin is the PATH-resolved git binary from NewRunner, never caller-supplied; caller-supplied branch names and paths reach argv only, which exec.CommandContext passes without a shell.
	cmd := exec.CommandContext(ctx, r.bin, args...)
	cmd.Dir = cwd
	// No terminal: a credential prompt would otherwise hang until the timeout.
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	if combined {
		out, err := cmd.CombinedOutput()
		return string(out), err
	}
	out, err := cmd.Output()
	return string(out), err
}

// WorktreeBranches returns a map of short branch name → worktree path for every
// worktree registered in the repo at repoCwd, parsed from
// `git worktree list --porcelain`. Detached worktrees (no branch line) are
// skipped. A git error is returned verbatim so callers can decide whether to
// treat a missing repo as fatal.
func (r *Runner) WorktreeBranches(ctx context.Context, repoCwd string) (map[string]string, error) {
	out, err := r.Output(ctx, repoCwd, "worktree", "list", "--porcelain")
	if err != nil {
		return nil, err
	}
	return parseWorktreeBranches(out), nil
}

// parseWorktreeBranches maps short branch names to worktree paths from porcelain
// output. Each record is a block separated by a blank line; "worktree <path>"
// opens a record and "branch refs/heads/<name>" names its checked-out branch.
func parseWorktreeBranches(porcelain string) map[string]string {
	branches := make(map[string]string)
	var curPath string
	for line := range strings.SplitSeq(porcelain, "\n") {
		switch {
		case strings.HasPrefix(line, "worktree "):
			curPath = strings.TrimPrefix(line, "worktree ")
		case strings.HasPrefix(line, "branch "):
			ref := strings.TrimPrefix(line, "branch ")
			name := strings.TrimPrefix(ref, "refs/heads/")
			if curPath != "" && name != "" {
				branches[name] = curPath
			}
		case line == "":
			curPath = ""
		}
	}
	return branches
}

// BranchCheckedOutAt reports the worktree path where branch is currently checked
// out in the repo at repoCwd, or "" when no worktree holds it. Best-effort: a
// git failure returns ("", err) and callers may choose to ignore it.
func BranchCheckedOutAt(ctx context.Context, repoCwd, branch string) (string, error) {
	branches, err := NewRunner().WorktreeBranches(ctx, repoCwd)
	if err != nil {
		return "", err
	}
	return branches[branch], nil
}

// IsGitWorkTree reports whether dir lies inside a git working tree.
func IsGitWorkTree(ctx context.Context, dir string) bool {
	out, err := NewRunner().Output(ctx, dir, "rev-parse", "--is-inside-work-tree")
	return err == nil && strings.TrimSpace(out) == "true"
}

// DefaultRoot returns root unchanged when set, else $HOME/dashboard-worktrees.
func DefaultRoot(root string) string {
	if root != "" {
		return root
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, DefaultRootDirName)
}

// PathFor returns the worktree directory for slug under the resolved root.
func PathFor(root, slug string) string {
	return filepath.Join(DefaultRoot(root), slug)
}

// CreateBranch returns *sourceBranch when set and non-empty, else BranchPrefix+slug.
func CreateBranch(sourceBranch *string, slug string) string {
	if sourceBranch != nil && *sourceBranch != "" {
		return *sourceBranch
	}
	return BranchPrefix + slug
}
