package checkpoint_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lx-wnk/kontor/server/internal/checkpoint"
)

func initRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %s: %v", args, out, err)
		}
	}
	run("init", "-b", "main")
	run("config", "user.email", "test@example.com")
	run("config", "user.name", "Test")
	run("commit", "--allow-empty", "-m", "init")
	return dir
}

func writeFile(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestSnapshot_CapturesTrackedAndUntracked(t *testing.T) {
	dir := initRepo(t)
	writeFile(t, dir, "tracked.go", "package main")
	_ = exec.Command("git", "-C", dir, "add", "tracked.go").Run()
	_ = exec.Command("git", "-C", dir, "commit", "-m", "add tracked").Run()
	writeFile(t, dir, "untracked.txt", "hello untracked")

	res, err := checkpoint.Snapshot(context.Background(), dir, "task-1", 1, "")
	if err != nil {
		t.Fatal(err)
	}
	if res.TreeSHA == "" || res.CommitSHA == "" {
		t.Fatal("empty SHA")
	}
	out, _ := exec.Command("git", "-C", dir, "ls-tree", "-r", "--name-only", res.TreeSHA).Output()
	if !strings.Contains(string(out), "untracked.txt") {
		t.Fatalf("untracked.txt not in tree:\n%s", out)
	}
	if res.FilesChanged < 2 {
		t.Fatalf("expected >=2 files, got %d", res.FilesChanged)
	}
}

func TestSnapshot_NodeModulesSkipped(t *testing.T) {
	dir := initRepo(t)
	if err := os.MkdirAll(filepath.Join(dir, "node_modules", "pkg"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, dir, "node_modules/pkg/index.js", "module.exports={}")
	writeFile(t, dir, "real.go", "package main")

	res, err := checkpoint.Snapshot(context.Background(), dir, "task-nm", 1, "")
	if err != nil {
		t.Fatal(err)
	}
	out, _ := exec.Command("git", "-C", dir, "ls-tree", "-r", "--name-only", res.TreeSHA).Output()
	if strings.Contains(string(out), "node_modules") {
		t.Fatal("node_modules must not appear in checkpoint tree")
	}
}

func TestSnapshot_NestedNodeModulesSkipped(t *testing.T) {
	dir := initRepo(t)
	if err := os.MkdirAll(filepath.Join(dir, "packages", "x", "node_modules", "pkg"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, dir, "packages/x/node_modules/pkg/index.js", "module.exports={}")
	writeFile(t, dir, "packages/x/real.go", "package x")

	res, err := checkpoint.Snapshot(context.Background(), dir, "task-nnm", 1, "")
	if err != nil {
		t.Fatal(err)
	}
	out, _ := exec.Command("git", "-C", dir, "ls-tree", "-r", "--name-only", res.TreeSHA).Output()
	if strings.Contains(string(out), "node_modules") {
		t.Fatalf("nested node_modules must not appear in checkpoint tree:\n%s", out)
	}
	if !strings.Contains(string(out), "packages/x/real.go") {
		t.Fatalf("real source under packages/x must be captured:\n%s", out)
	}
}

func TestSnapshot_IdenticalTreeSkipped(t *testing.T) {
	dir := initRepo(t)
	writeFile(t, dir, "a.go", "package main")

	res1, err := checkpoint.Snapshot(context.Background(), dir, "task-x", 1, "")
	if err != nil {
		t.Fatal(err)
	}
	res2, err := checkpoint.Snapshot(context.Background(), dir, "task-x", 2, res1.TreeSHA)
	if err != nil {
		t.Fatal(err)
	}
	if !res2.Skipped {
		t.Fatal("expected Skipped=true for identical tree")
	}
}

func TestRestore_ExactMatch(t *testing.T) {
	dir := initRepo(t)
	writeFile(t, dir, "main.go", "package main\n\nfunc main(){}")
	writeFile(t, dir, "extra.txt", "untracked too")

	res, err := checkpoint.Snapshot(context.Background(), dir, "task-r", 1, "")
	if err != nil {
		t.Fatal(err)
	}

	// Simulate agent damage: overwrite a file, add a new file, delete original.
	writeFile(t, dir, "main.go", "CORRUPTED")
	writeFile(t, dir, "new_file_after.go", "package x")
	_ = os.Remove(filepath.Join(dir, "extra.txt"))

	if err := checkpoint.Restore(context.Background(), dir, dir, res.TreeSHA); err != nil {
		t.Fatal(err)
	}

	got, _ := os.ReadFile(filepath.Join(dir, "main.go"))
	if string(got) != "package main\n\nfunc main(){}" {
		t.Fatalf("main.go not restored: %q", got)
	}
	if _, err := os.Stat(filepath.Join(dir, "extra.txt")); err != nil {
		t.Fatal("extra.txt should be restored")
	}
	if _, err := os.Stat(filepath.Join(dir, "new_file_after.go")); err == nil {
		t.Fatal("new_file_after.go should be removed after restore")
	}
}

func TestDeleteCheckpointRefs(t *testing.T) {
	dir := initRepo(t)
	writeFile(t, dir, "a.go", "package a")
	res, _ := checkpoint.Snapshot(context.Background(), dir, "task-del", 1, "")
	if res.Skipped {
		t.Fatal("expected non-skipped snapshot")
	}
	out, _ := exec.Command("git", "-C", dir, "for-each-ref", "refs/checkpoints/task-del/").Output()
	if !strings.Contains(string(out), "task-del") {
		t.Fatal("ref not found before delete")
	}
	if err := checkpoint.DeleteCheckpointRefs(context.Background(), dir, "task-del"); err != nil {
		t.Fatal(err)
	}
	out2, _ := exec.Command("git", "-C", dir, "for-each-ref", "refs/checkpoints/task-del/").Output()
	if strings.Contains(string(out2), "task-del") {
		t.Fatal("ref should be gone after delete")
	}
}
