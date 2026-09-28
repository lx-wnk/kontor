package services_test

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lx-wnk/kontor/server/internal/db"
	"github.com/lx-wnk/kontor/server/internal/db/repo"
	"github.com/lx-wnk/kontor/server/internal/services"
)

// initGitRepo creates a bare git repo with an initial commit so that worktree
// operations have something to branch from. Uses -c flags to avoid GPG signing.
func initGitRepo(t *testing.T, dir string) {
	t.Helper()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init")
	run("config", "user.email", "t@t")
	run("config", "user.name", "t")
	run("config", "commit.gpgsign", "false")
	// Create an initial commit so HEAD is valid for worktree add.
	f := filepath.Join(dir, "README")
	if err := os.WriteFile(f, []byte("init"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", "README")
	run("commit", "-m", "init")
}

func newWTManager(t *testing.T) (*services.WorktreeManager, repo.TaskRepo) {
	t.Helper()
	// Redirect the worktree root (derived from $HOME) into a temp dir so created
	// worktrees auto-clean and never collide with a developer's real ~/dashboard-worktrees.
	t.Setenv("HOME", t.TempDir())
	bundle, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { _ = bundle.Client.Close() })
	taskRepo := repo.NewTaskRepo(bundle.Client)
	mgr := services.NewWorktreeManager(taskRepo)
	return mgr, taskRepo
}

func createTestTask(t *testing.T, taskRepo repo.TaskRepo, slug, cwd string) string {
	t.Helper()
	task, err := taskRepo.Create(t.Context(), repo.CreateTaskInput{
		Slug:          slug,
		Title:         "T-" + slug,
		Cwd:           cwd,
		CurrentStage:  "backlog",
		Priority:      "medium",
		MaxIterations: 5,
	})
	if err != nil {
		t.Fatalf("create task: %v", err)
	}
	return task.ID
}

func TestCreateWorktree_Fresh(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not found")
	}

	repoDir := t.TempDir()
	initGitRepo(t, repoDir)

	mgr, taskRepo := newWTManager(t)
	id := createTestTask(t, taskRepo, "fresh-wt", repoDir)

	path, err := mgr.CreateWorktree(t.Context(), id)
	if err != nil {
		t.Fatalf("CreateWorktree: %v", err)
	}
	if path == "" {
		t.Fatal("expected non-empty path")
	}

	// Dir must exist.
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("worktree dir not found: %v", err)
	}

	// Path must be persisted on the task.
	task, err := taskRepo.GetByID(t.Context(), id)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if task.WorktreePath == nil || *task.WorktreePath != path {
		t.Fatalf("WorktreePath not persisted: got %v", task.WorktreePath)
	}
}

func TestCreateWorktree_Idempotent(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not found")
	}

	repoDir := t.TempDir()
	initGitRepo(t, repoDir)

	mgr, taskRepo := newWTManager(t)
	id := createTestTask(t, taskRepo, "idem-wt", repoDir)

	path1, err := mgr.CreateWorktree(t.Context(), id)
	if err != nil {
		t.Fatalf("first CreateWorktree: %v", err)
	}

	path2, err := mgr.CreateWorktree(t.Context(), id)
	if err != nil {
		t.Fatalf("second CreateWorktree: %v", err)
	}
	if path1 != path2 {
		t.Fatalf("expected same path, got %q vs %q", path1, path2)
	}
}

func TestRemoveWorktree_Clean(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not found")
	}

	repoDir := t.TempDir()
	initGitRepo(t, repoDir)

	mgr, taskRepo := newWTManager(t)
	id := createTestTask(t, taskRepo, "rm-clean", repoDir)

	path, err := mgr.CreateWorktree(t.Context(), id)
	if err != nil {
		t.Fatalf("CreateWorktree: %v", err)
	}

	if err := mgr.RemoveWorktree(t.Context(), id, false); err != nil {
		t.Fatalf("RemoveWorktree: %v", err)
	}

	// Dir must be gone.
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("expected worktree dir to be removed, stat: %v", err)
	}

	// Path must be cleared on the task.
	task, err := taskRepo.GetByID(t.Context(), id)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if task.WorktreePath != nil && *task.WorktreePath != "" {
		t.Fatalf("expected WorktreePath cleared, got %q", *task.WorktreePath)
	}
}

func TestRemoveWorktree_DirtyNoForce(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not found")
	}

	repoDir := t.TempDir()
	initGitRepo(t, repoDir)

	mgr, taskRepo := newWTManager(t)
	id := createTestTask(t, taskRepo, "rm-dirty-noforce", repoDir)

	path, err := mgr.CreateWorktree(t.Context(), id)
	if err != nil {
		t.Fatalf("CreateWorktree: %v", err)
	}

	// Write an untracked file to make the worktree dirty.
	if err := os.WriteFile(filepath.Join(path, "dirty.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	err = mgr.RemoveWorktree(t.Context(), id, false)
	if !errors.Is(err, services.ErrWorktreeDirty) {
		t.Fatalf("expected ErrWorktreeDirty, got %v", err)
	}
}

func TestRemoveWorktree_DirtyForce(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not found")
	}

	repoDir := t.TempDir()
	initGitRepo(t, repoDir)

	mgr, taskRepo := newWTManager(t)
	id := createTestTask(t, taskRepo, "rm-dirty-force", repoDir)

	path, err := mgr.CreateWorktree(t.Context(), id)
	if err != nil {
		t.Fatalf("CreateWorktree: %v", err)
	}

	if err := os.WriteFile(filepath.Join(path, "dirty.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := mgr.RemoveWorktree(t.Context(), id, true); err != nil {
		t.Fatalf("RemoveWorktree(force): %v", err)
	}

	task, err := taskRepo.GetByID(t.Context(), id)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if task.WorktreePath != nil && *task.WorktreePath != "" {
		t.Fatalf("expected WorktreePath cleared, got %q", *task.WorktreePath)
	}
}

func TestRemoveWorktree_NoWorktree(t *testing.T) {
	mgr, taskRepo := newWTManager(t)
	id := createTestTask(t, taskRepo, "no-wt", "/tmp")

	err := mgr.RemoveWorktree(t.Context(), id, false)
	if !errors.Is(err, services.ErrNoWorktree) {
		t.Fatalf("expected ErrNoWorktree, got %v", err)
	}
}

// initRepoWithOrigin creates a main git repo (mainDir) with a bare repo
// (bareDir) as remote "origin" and an initial commit pushed to origin/main.
func initRepoWithOrigin(t *testing.T, mainDir, bareDir string) {
	t.Helper()
	runIn := func(dir string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v in %s: %v\n%s", args, dir, err, out)
		}
	}

	runIn(bareDir, "init", "--bare")

	runIn(mainDir, "init")
	runIn(mainDir, "config", "user.email", "t@t")
	runIn(mainDir, "config", "user.name", "t")
	runIn(mainDir, "config", "commit.gpgsign", "false")
	if err := os.WriteFile(filepath.Join(mainDir, "README"), []byte("init"), 0o644); err != nil {
		t.Fatal(err)
	}
	runIn(mainDir, "add", "README")
	runIn(mainDir, "commit", "-m", "init")
	runIn(mainDir, "remote", "add", "origin", bareDir)
	runIn(mainDir, "push", "-u", "origin", "HEAD:main")
}

func TestHasUnpushedWork_NilTask(t *testing.T) {
	mgr, _ := newWTManager(t)
	if mgr.HasUnpushedWork(t.Context(), nil) {
		t.Fatal("expected false for nil task")
	}
}

func TestHasUnpushedWork_NoWorktreePath(t *testing.T) {
	mgr, taskRepo := newWTManager(t)
	id := createTestTask(t, taskRepo, "no-path", "/tmp")
	task, err := taskRepo.GetByID(t.Context(), id)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if mgr.HasUnpushedWork(t.Context(), task) {
		t.Fatal("expected false for task with no worktree path")
	}
}

func TestHasUnpushedWork_CleanBranchOnOrigin(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not found")
	}

	mainDir := t.TempDir()
	bareDir := t.TempDir()
	initRepoWithOrigin(t, mainDir, bareDir)

	mgr, taskRepo := newWTManager(t)
	id := createTestTask(t, taskRepo, "clean-pushed", mainDir)

	// Create the worktree on a new branch.
	wtPath, err := mgr.CreateWorktree(t.Context(), id)
	if err != nil {
		t.Fatalf("CreateWorktree: %v", err)
	}

	// Push the new branch to origin so it is fully represented on remote.
	runIn := func(dir string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	// Determine which branch the worktree is on, then push it.
	branchCmd := exec.Command("git", "rev-parse", "--abbrev-ref", "HEAD")
	branchCmd.Dir = wtPath
	branchOut, err := branchCmd.Output()
	if err != nil {
		t.Fatalf("rev-parse branch: %v", err)
	}
	branch := strings.TrimSpace(string(branchOut))
	runIn(wtPath, "push", "origin", branch)

	task, err := taskRepo.GetByID(t.Context(), id)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if mgr.HasUnpushedWork(t.Context(), task) {
		t.Fatal("expected false: clean worktree with branch on origin")
	}
}

func TestHasUnpushedWork_UnpushedCommit(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not found")
	}

	mainDir := t.TempDir()
	bareDir := t.TempDir()
	initRepoWithOrigin(t, mainDir, bareDir)

	mgr, taskRepo := newWTManager(t)
	id := createTestTask(t, taskRepo, "unpushed-commit", mainDir)

	wtPath, err := mgr.CreateWorktree(t.Context(), id)
	if err != nil {
		t.Fatalf("CreateWorktree: %v", err)
	}

	// Make a commit in the worktree without pushing.
	if err := os.WriteFile(filepath.Join(wtPath, "work.txt"), []byte("work"), 0o644); err != nil {
		t.Fatal(err)
	}
	runIn := func(dir string, args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	runIn(wtPath, "config", "user.email", "t@t")
	runIn(wtPath, "config", "user.name", "t")
	runIn(wtPath, "config", "commit.gpgsign", "false")
	runIn(wtPath, "add", "work.txt")
	runIn(wtPath, "commit", "-m", "unpushed work")

	task, err := taskRepo.GetByID(t.Context(), id)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if !mgr.HasUnpushedWork(t.Context(), task) {
		t.Fatal("expected true: worktree has commit not on origin")
	}
}

func TestHasUnpushedWork_VanishedDirectory(t *testing.T) {
	mgr, taskRepo := newWTManager(t)
	id := createTestTask(t, taskRepo, "vanished-wt", "/tmp")

	// Point WorktreePath at a directory that never exists.
	nonExistentPath := filepath.Join(t.TempDir(), "does-not-exist")
	task, err := taskRepo.Update(t.Context(), id, repo.UpdateTaskInput{WorktreePath: &nonExistentPath})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}

	if mgr.HasUnpushedWork(t.Context(), task) {
		t.Fatal("expected false: directory does not exist on disk, nothing to retain")
	}
}

func TestHasUnpushedWork_DirtyWorktree(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not found")
	}

	mainDir := t.TempDir()
	bareDir := t.TempDir()
	initRepoWithOrigin(t, mainDir, bareDir)

	mgr, taskRepo := newWTManager(t)
	id := createTestTask(t, taskRepo, "dirty-wt", mainDir)

	wtPath, err := mgr.CreateWorktree(t.Context(), id)
	if err != nil {
		t.Fatalf("CreateWorktree: %v", err)
	}

	// Make the worktree dirty without committing.
	if err := os.WriteFile(filepath.Join(wtPath, "dirty.txt"), []byte("uncommitted"), 0o644); err != nil {
		t.Fatal(err)
	}

	task, err := taskRepo.GetByID(t.Context(), id)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if !mgr.HasUnpushedWork(t.Context(), task) {
		t.Fatal("expected true: worktree has uncommitted changes")
	}
}
