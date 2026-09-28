package scheduler

import (
	"context"
	"testing"
	"time"

	"github.com/lx-wnk/kontor/server/internal/db"
	"github.com/lx-wnk/kontor/server/internal/db/repo"
)

// TestMaterialize_JobRunMode pins that a job-mode schedule fires a task that
// skips the pipeline: it starts directly on the "job" stage, not "ready".
func TestMaterialize_JobRunMode(t *testing.T) {
	bundle, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = bundle.Client.Close() })

	schedRepo := repo.NewTaskScheduleRepo(bundle.Client)
	sched := mkSchedule(t, schedRepo, repo.CreateTaskScheduleInput{RunMode: repo.RunModeJob})

	var got NewTaskSpec
	create := func(_ context.Context, spec NewTaskSpec) (string, error) {
		got = spec
		return "task-1", nil
	}

	m := NewMaterializer(create, nil, nil)
	if _, err := m.Materialize(context.Background(), sched, time.Now()); err != nil {
		t.Fatalf("materialize: %v", err)
	}
	if got.Kind != "job" {
		t.Fatalf("Kind = %q, want %q", got.Kind, "job")
	}
	if got.Stage != "job" {
		t.Fatalf("Stage = %q, want %q", got.Stage, "job")
	}
	if got.Autonomy == nil || *got.Autonomy != "full" {
		t.Fatalf("Autonomy = %v, want *\"full\"", got.Autonomy)
	}
}

// TestMaterialize_PipelineRunMode pins that a pipeline-mode schedule fires a
// task that enters the normal stage flow, starting on "ready".
func TestMaterialize_PipelineRunMode(t *testing.T) {
	bundle, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = bundle.Client.Close() })

	schedRepo := repo.NewTaskScheduleRepo(bundle.Client)
	sched := mkSchedule(t, schedRepo, repo.CreateTaskScheduleInput{RunMode: repo.RunModePipeline})

	var got NewTaskSpec
	create := func(_ context.Context, spec NewTaskSpec) (string, error) {
		got = spec
		return "task-1", nil
	}

	m := NewMaterializer(create, nil, nil)
	if _, err := m.Materialize(context.Background(), sched, time.Now()); err != nil {
		t.Fatalf("materialize: %v", err)
	}
	if got.Kind != "pipeline" {
		t.Fatalf("Kind = %q, want %q", got.Kind, "pipeline")
	}
	if got.Stage != "ready" {
		t.Fatalf("Stage = %q, want %q", got.Stage, "ready")
	}
	if got.Autonomy == nil || *got.Autonomy != "full" {
		t.Fatalf("Autonomy = %v, want *\"full\"", got.Autonomy)
	}
}
