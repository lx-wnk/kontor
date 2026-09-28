package repo_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/lx-wnk/kontor/server/internal/db/ent"
	"github.com/lx-wnk/kontor/server/internal/db/repo"
)

func TestTaskSchedule_RunMode_DefaultsToJob(t *testing.T) {
	r := repo.NewTaskScheduleRepo(openDB(t))
	id := newSchedule(t, r, "default-run-mode", repo.CreateTaskScheduleInput{})

	got, err := r.GetByID(context.Background(), id)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if got.RunMode != repo.RunModeJob {
		t.Fatalf("RunMode = %q, want %q", got.RunMode, repo.RunModeJob)
	}
}

func TestTaskSchedule_RunMode_CreatePipeline(t *testing.T) {
	r := repo.NewTaskScheduleRepo(openDB(t))
	id := newSchedule(t, r, "pipeline-run-mode", repo.CreateTaskScheduleInput{RunMode: repo.RunModePipeline})

	got, err := r.GetByID(context.Background(), id)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if got.RunMode != repo.RunModePipeline {
		t.Fatalf("RunMode = %q, want %q", got.RunMode, repo.RunModePipeline)
	}
}

func TestTaskSchedule_RunMode_CreateInvalid(t *testing.T) {
	r := repo.NewTaskScheduleRepo(openDB(t))
	_, err := r.Create(context.Background(), repo.CreateTaskScheduleInput{
		Name: "invalid-run-mode", CronExpr: "0 9 * * *", SlugPrefix: "nightly", Title: "t", Cwd: "/tmp",
		MaxIterations: 20, StageTimeoutSeconds: 1800, RunMode: "cron",
	})
	if err == nil || !strings.Contains(err.Error(), "run mode") {
		t.Fatalf("err = %v, want error containing %q", err, "run mode")
	}
}

func TestTaskSchedule_RunMode_UpdatePersists(t *testing.T) {
	r := repo.NewTaskScheduleRepo(openDB(t))
	id := newSchedule(t, r, "update-run-mode", repo.CreateTaskScheduleInput{})

	mode := repo.RunModePipeline
	if _, err := r.Update(context.Background(), id, repo.UpdateTaskScheduleInput{RunMode: &mode}); err != nil {
		t.Fatalf("Update: %v", err)
	}
	got, err := r.GetByID(context.Background(), id)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if got.RunMode != repo.RunModePipeline {
		t.Fatalf("RunMode = %q, want %q", got.RunMode, repo.RunModePipeline)
	}
}

func TestTaskSchedule_RunMode_UpdateInvalid(t *testing.T) {
	r := repo.NewTaskScheduleRepo(openDB(t))
	id := newSchedule(t, r, "update-invalid-run-mode", repo.CreateTaskScheduleInput{})

	mode := "cron"
	_, err := r.Update(context.Background(), id, repo.UpdateTaskScheduleInput{RunMode: &mode})
	if err == nil || !strings.Contains(err.Error(), "run mode") {
		t.Fatalf("err = %v, want error containing %q", err, "run mode")
	}
}

func TestTaskSchedule_RecordSkip_IncrementsAndStamps(t *testing.T) {
	r := repo.NewTaskScheduleRepo(openDB(t))
	id := newSchedule(t, r, "record-skip", repo.CreateTaskScheduleInput{})
	ctx := context.Background()

	first := time.Now().Add(-time.Minute).Truncate(time.Second)
	if _, err := r.RecordSkip(ctx, id, first); err != nil {
		t.Fatalf("RecordSkip #1: %v", err)
	}
	second := time.Now().Truncate(time.Second)
	got, err := r.RecordSkip(ctx, id, second)
	if err != nil {
		t.Fatalf("RecordSkip #2: %v", err)
	}

	if got.SkippedCount != 2 {
		t.Fatalf("SkippedCount = %d, want 2", got.SkippedCount)
	}
	if got.LastSkippedAt == nil || !got.LastSkippedAt.Truncate(time.Second).Equal(second) {
		t.Fatalf("LastSkippedAt = %v, want %v", got.LastSkippedAt, second)
	}
}

func TestTaskSchedule_RecordSkip_UnknownID(t *testing.T) {
	r := repo.NewTaskScheduleRepo(openDB(t))
	_, err := r.RecordSkip(context.Background(), "does-not-exist", time.Now())
	if !ent.IsNotFound(err) {
		t.Fatalf("err = %v, want ent.IsNotFound", err)
	}
}
