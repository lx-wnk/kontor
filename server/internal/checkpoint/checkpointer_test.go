package checkpoint_test

import (
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lx-wnk/kontor/server/internal/checkpoint"
)

func TestCheckpointer_FiresOnWrite(t *testing.T) {
	dir := initRepo(t)
	var snapshots atomic.Int32
	c := checkpoint.NewCheckpointer(checkpoint.CheckpointerOptions{
		DebounceInterval: 50 * time.Millisecond,
		OnSnapshot:       func(_, _ string) { snapshots.Add(1) },
	})
	c.Start("task-1", dir)
	defer c.Stop("task-1")

	writeFile(t, dir, "new.go", "package main")
	time.Sleep(300 * time.Millisecond)

	if snapshots.Load() == 0 {
		t.Fatal("expected at least one snapshot callback")
	}
}

func TestCheckpointer_DotGitIgnored(t *testing.T) {
	dir := initRepo(t)
	var snapshots atomic.Int32
	c := checkpoint.NewCheckpointer(checkpoint.CheckpointerOptions{
		DebounceInterval: 30 * time.Millisecond,
		OnSnapshot:       func(_, _ string) { snapshots.Add(1) },
	})
	c.Start("task-2", dir)
	defer c.Stop("task-2")

	_ = os.WriteFile(filepath.Join(dir, ".git", "COMMIT_EDITMSG"), []byte("x"), 0o644)
	time.Sleep(150 * time.Millisecond)

	if snapshots.Load() != 0 {
		t.Fatalf("expected no snapshots from .git write, got %d", snapshots.Load())
	}
}

func TestCheckpointer_StopCleans(t *testing.T) {
	dir := initRepo(t)
	var snapshots atomic.Int32
	c := checkpoint.NewCheckpointer(checkpoint.CheckpointerOptions{
		DebounceInterval: 30 * time.Millisecond,
		OnSnapshot:       func(_, _ string) { snapshots.Add(1) },
	})
	c.Start("task-3", dir)
	c.Stop("task-3")

	writeFile(t, dir, "after.go", "package x")
	time.Sleep(150 * time.Millisecond)
	if snapshots.Load() != 0 {
		t.Fatal("no snapshots expected after Stop")
	}
}
