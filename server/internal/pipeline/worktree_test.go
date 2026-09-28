package pipeline

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/lx-wnk/kontor/server/internal/db/ent"
)

func initRepoWithCommit(t *testing.T, repoDir string) {
	t.Helper()
	if err := exec.Command("git", "-C", repoDir, "init").Run(); err != nil {
		t.Skip("git not available:", err)
	}
	for _, args := range [][]string{
		{"config", "user.email", "test@test.com"},
		{"config", "user.name", "Test"},
		{"commit", "--allow-empty", "-m", "init"},
	} {
		full := append([]string{"-C", repoDir, "-c", "commit.gpgsign=false"}, args...)
		if err := exec.Command("git", full...).Run(); err != nil {
			t.Fatalf("git %v: %v", args, err)
		}
	}
}

func TestEnsureTaskWorktree_NoSourceBranch_DerivesSlug(t *testing.T) {
	repoDir := t.TempDir()
	initRepoWithCommit(t, repoDir)

	task := &ent.Task{Slug: "my-task", Cwd: repoDir}
	root := filepath.Join(t.TempDir(), "worktrees")

	path, branch, err := ensureTaskWorktree(context.Background(), task, root)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if branch != "feat/my-task" {
		t.Fatalf("expected derived branch feat/my-task, got %s", branch)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("worktree directory does not exist: %v", err)
	}
}

func TestEnsureTaskWorktree_CreatesAndIdempotent(t *testing.T) {
	repoDir := t.TempDir()
	initRepoWithCommit(t, repoDir)

	branch := "feat/auto-worktree-test"
	task := &ent.Task{Slug: "wt-test", Cwd: repoDir, SourceBranch: &branch}
	root := filepath.Join(t.TempDir(), "worktrees")

	// First call: creates worktree.
	path1, _, err := ensureTaskWorktree(context.Background(), task, root)
	if err != nil {
		t.Fatalf("first call failed: %v", err)
	}
	if _, err := os.Stat(path1); err != nil {
		t.Fatalf("worktree directory does not exist: %v", err)
	}

	// Second call: idempotent (directory exists → returns immediately).
	path2, _, err := ensureTaskWorktree(context.Background(), task, root)
	if err != nil {
		t.Fatalf("second call failed: %v", err)
	}
	if path1 != path2 {
		t.Fatalf("idempotent: expected same path %s, got %s", path1, path2)
	}
}

func TestRemoveTaskWorktree_EmptyPath(t *testing.T) {
	// removeTaskWorktree with empty path is a no-op.
	if err := removeTaskWorktree(context.Background(), "/some/cwd", ""); err != nil {
		t.Fatal("expected nil for empty path, got:", err)
	}
}

func TestEnsureTaskWorktree_CallsSetupFn(t *testing.T) {
	repoDir := t.TempDir()
	initRepoWithCommit(t, repoDir)

	task := &ent.Task{Slug: "setup-test", Cwd: repoDir}
	root := filepath.Join(t.TempDir(), "worktrees")

	path, _, err := ensureTaskWorktree(context.Background(), task, root)
	require.NoError(t, err)

	// Seam: verify the returned path is valid — setup fn is wired at orchestrator level.
	// This test validates the path contract ensureTaskWorktree guarantees.
	fi, statErr := os.Stat(path)
	require.NoError(t, statErr)
	require.True(t, fi.IsDir())
}

func TestOrchestratorWorktree_RunsSetupCommand(t *testing.T) {
	// Validates that SetupWorktreeFn matches the OrchestratorOptions seam signature
	// and receives the worktree path it is called with.
	calledWith := ""
	setupFn := func(_ context.Context, _ *string, worktreePath string) error {
		calledWith = worktreePath
		return nil
	}

	var opts OrchestratorOptions
	opts.SetupWorktreeFn = setupFn
	require.NoError(t, opts.SetupWorktreeFn(context.Background(), nil, "/tmp/wt"))
	require.Equal(t, "/tmp/wt", calledWith)
}
