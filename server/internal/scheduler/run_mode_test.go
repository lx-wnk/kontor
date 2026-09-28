package scheduler_test

import (
	"context"
	"os/exec"
	"testing"

	"github.com/lx-wnk/kontor/server/internal/scheduler"
)

func TestCheckRunMode(t *testing.T) {
	ctx := context.Background()

	if err := scheduler.CheckRunMode(ctx, "job", t.TempDir()); err != nil {
		t.Fatalf("job in non-git dir: got %v, want nil", err)
	}

	if err := scheduler.CheckRunMode(ctx, "pipeline", t.TempDir()); err == nil || err.Error() != "working directory is not a git repository" {
		t.Fatalf("pipeline in non-git dir: got %v, want git-repo error", err)
	}

	if err := scheduler.CheckRunMode(ctx, "cron", "/tmp"); err == nil || err.Error() != "runMode must be job or pipeline" {
		t.Fatalf("invalid runMode: got %v, want runMode error", err)
	}

	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	dir := t.TempDir()
	if err := exec.Command("git", "init", dir).Run(); err != nil {
		t.Fatalf("git init: %v", err)
	}
	if err := scheduler.CheckRunMode(ctx, "pipeline", dir); err != nil {
		t.Fatalf("pipeline in git dir: got %v, want nil", err)
	}
}
