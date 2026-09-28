package worktree_test

import (
	"context"
	"os/exec"
	"testing"

	"github.com/lx-wnk/kontor/server/internal/worktree"
)

func TestIsGitWorkTree(t *testing.T) {
	if got := worktree.IsGitWorkTree(context.Background(), t.TempDir()); got {
		t.Fatalf("plain temp dir: got true, want false")
	}

	if got := worktree.IsGitWorkTree(context.Background(), "/no/such/path/at/all"); got {
		t.Fatalf("nonexistent path: got true, want false")
	}

	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	dir := t.TempDir()
	if err := exec.Command("git", "init", dir).Run(); err != nil {
		t.Fatalf("git init: %v", err)
	}
	if got := worktree.IsGitWorkTree(context.Background(), dir); !got {
		t.Fatalf("git-initialized dir: got false, want true")
	}
}
