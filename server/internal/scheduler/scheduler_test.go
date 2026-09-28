package scheduler

import (
	"context"
	"testing"
	"time"

	"github.com/lx-wnk/kontor/server/internal/db"
	"github.com/lx-wnk/kontor/server/internal/db/ent"
	"github.com/lx-wnk/kontor/server/internal/db/repo"
)

// testHarness wires real in-memory ent repos with a recording task creator and
// a fixed clock so the scheduler's policy branches can be driven deterministically.
type testHarness struct {
	schedules repo.TaskScheduleRepo
	tasks     repo.TaskRepo
	sched     *Scheduler
	created   *[]string
}

func newHarness(t *testing.T, now time.Time) *testHarness {
	t.Helper()
	bundle, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = bundle.Client.Close() })
	client := bundle.Client

	schedRepo := repo.NewTaskScheduleRepo(client)
	taskRepo := repo.NewTaskRepo(client)
	permRepo := repo.NewPermissionRepo(client)

	created := &[]string{}
	createFn := func(ctx context.Context, spec NewTaskSpec) (string, error) {
		task, err := taskRepo.Create(ctx, repo.CreateTaskInput{
			Slug:          spec.Slug,
			Title:         spec.Title,
			Cwd:           spec.Cwd,
			CurrentStage:  "backlog",
			Priority:      "medium",
			MaxIterations: 20,
		})
		if err != nil {
			return "", err
		}
		*created = append(*created, task.ID)
		return task.ID, nil
	}

	m := NewMaterializer(createFn, taskRepo, permRepo)
	s := New(Options{
		Schedules:    schedRepo,
		Tasks:        taskRepo,
		Materializer: m,
		Now:          func() time.Time { return now },
	})
	return &testHarness{schedules: schedRepo, tasks: taskRepo, sched: s, created: created}
}

func mkSchedule(t *testing.T, r repo.TaskScheduleRepo, in repo.CreateTaskScheduleInput) *ent.TaskSchedule {
	t.Helper()
	in.Name = "s"
	in.CronExpr = "0 9 * * *"
	in.SlugPrefix = "nightly"
	in.Title = "Nightly"
	in.Cwd = "/tmp"
	in.MaxIterations = 20
	s, err := r.Create(context.Background(), in)
	if err != nil {
		t.Fatalf("create schedule: %v", err)
	}
	return s
}

func TestTick_FiresWhenDue_CatchupOnce(t *testing.T) {
	now := time.Date(2026, 6, 18, 12, 0, 0, 0, time.UTC)
	h := newHarness(t, now)
	past := now.Add(-time.Minute)
	s := mkSchedule(t, h.schedules, repo.CreateTaskScheduleInput{NextRunAt: &past, Catchup: "once"})

	h.sched.tick(context.Background(), now)

	if len(*h.created) != 1 {
		t.Fatalf("expected 1 task created, got %d", len(*h.created))
	}
	got, _ := h.schedules.GetByID(context.Background(), s.ID)
	if got.LastTaskID == nil {
		t.Fatal("last_task_id not set after fire")
	}
	if got.NextRunAt == nil || !got.NextRunAt.After(now) {
		t.Fatalf("next_run_at not advanced past now: %v", got.NextRunAt)
	}
}

func TestTick_SkipOnOverlap(t *testing.T) {
	now := time.Date(2026, 6, 18, 12, 0, 0, 0, time.UTC)
	h := newHarness(t, now)
	past := now.Add(-time.Minute)
	s := mkSchedule(t, h.schedules, repo.CreateTaskScheduleInput{NextRunAt: &past, Catchup: "once"})

	// Simulate a prior in-flight task (non-terminal stage).
	prior, err := h.tasks.Create(context.Background(), repo.CreateTaskInput{
		Slug: "prior", Title: "Prior", Cwd: "/tmp", CurrentStage: "implementation",
		Priority: "medium", MaxIterations: 20,
	})
	if err != nil {
		t.Fatalf("create prior task: %v", err)
	}
	last := now.Add(-24 * time.Hour)
	if _, err := h.schedules.UpdateFireState(context.Background(), s.ID, repo.FireStateInput{
		LastRunAt: last, LastTaskID: &prior.ID, NextRunAt: &past,
	}); err != nil {
		t.Fatalf("seed fire state: %v", err)
	}

	h.sched.tick(context.Background(), now)

	if len(*h.created) != 0 {
		t.Fatalf("expected 0 new tasks on overlap, got %d", len(*h.created))
	}
	got, _ := h.schedules.GetByID(context.Background(), s.ID)
	if got.NextRunAt == nil || !got.NextRunAt.After(now) {
		t.Fatalf("next_run_at must still advance on skip: %v", got.NextRunAt)
	}
	if got.SkippedCount != 1 {
		t.Fatalf("expected skipped_count 1, got %d", got.SkippedCount)
	}
	if got.LastSkippedAt == nil || !got.LastSkippedAt.Equal(now) {
		t.Fatalf("expected last_skipped_at %v, got %v", now, got.LastSkippedAt)
	}
}

// TestTick_SkipWhileRunWaitsForHuman covers a prior task stuck in a
// human-in-the-loop stage run (awaiting_user, no PID). overlaps() keys only
// on the prior task's CurrentStage, so a task parked in "implementation"
// already reproduces this without a real StageRun row (testHarness does not
// expose the ent client needed to create one).
func TestTick_SkipWhileRunWaitsForHuman(t *testing.T) {
	now := time.Date(2026, 6, 18, 12, 0, 0, 0, time.UTC)
	h := newHarness(t, now)
	past := now.Add(-time.Minute)
	s := mkSchedule(t, h.schedules, repo.CreateTaskScheduleInput{NextRunAt: &past, Catchup: "once"})

	prior, err := h.tasks.Create(context.Background(), repo.CreateTaskInput{
		Slug: "prior-waiting", Title: "Prior Waiting", Cwd: "/tmp", CurrentStage: "implementation",
		Priority: "medium", MaxIterations: 20,
	})
	if err != nil {
		t.Fatalf("create prior task: %v", err)
	}
	last := now.Add(-24 * time.Hour)
	if _, err := h.schedules.UpdateFireState(context.Background(), s.ID, repo.FireStateInput{
		LastRunAt: last, LastTaskID: &prior.ID, NextRunAt: &past,
	}); err != nil {
		t.Fatalf("seed fire state: %v", err)
	}

	h.sched.tick(context.Background(), now)

	if len(*h.created) != 0 {
		t.Fatalf("expected 0 new tasks while prior run awaits a human, got %d", len(*h.created))
	}
	got, _ := h.schedules.GetByID(context.Background(), s.ID)
	if got.SkippedCount != 1 {
		t.Fatalf("expected skipped_count 1, got %d", got.SkippedCount)
	}
}

func TestTick_CatchupNoneSkipsMissedWindow(t *testing.T) {
	now := time.Date(2026, 6, 18, 12, 0, 0, 0, time.UTC)
	h := newHarness(t, now)
	past := now.Add(-time.Minute)
	s := mkSchedule(t, h.schedules, repo.CreateTaskScheduleInput{NextRunAt: &past, Catchup: "none"})
	// Mark as previously fired so the due tick is a genuine missed window.
	last := now.Add(-48 * time.Hour)
	taskID := "old"
	if _, err := h.schedules.UpdateFireState(context.Background(), s.ID, repo.FireStateInput{
		LastRunAt: last, LastTaskID: &taskID, NextRunAt: &past,
	}); err != nil {
		t.Fatalf("seed fire state: %v", err)
	}

	h.sched.tick(context.Background(), now)

	if len(*h.created) != 0 {
		t.Fatalf("catchup=none must skip missed window, created %d", len(*h.created))
	}
	got, _ := h.schedules.GetByID(context.Background(), s.ID)
	if got.NextRunAt == nil || !got.NextRunAt.After(now) {
		t.Fatalf("next_run_at must advance on skip: %v", got.NextRunAt)
	}
}

func TestTick_InitNextRunDoesNotFire(t *testing.T) {
	now := time.Date(2026, 6, 18, 12, 0, 0, 0, time.UTC)
	h := newHarness(t, now)
	s := mkSchedule(t, h.schedules, repo.CreateTaskScheduleInput{}) // nil next_run_at

	h.sched.tick(context.Background(), now)

	if len(*h.created) != 0 {
		t.Fatalf("init tick must not fire, created %d", len(*h.created))
	}
	got, _ := h.schedules.GetByID(context.Background(), s.ID)
	if got.NextRunAt == nil || !got.NextRunAt.After(now) {
		t.Fatalf("next_run_at must be initialized: %v", got.NextRunAt)
	}
}

func TestRunNow_FiresImmediately(t *testing.T) {
	now := time.Date(2026, 6, 18, 12, 0, 0, 0, time.UTC)
	h := newHarness(t, now)
	future := now.Add(time.Hour)
	s := mkSchedule(t, h.schedules, repo.CreateTaskScheduleInput{NextRunAt: &future})

	taskID, err := h.sched.RunNow(context.Background(), s.ID)
	if err != nil {
		t.Fatalf("RunNow: %v", err)
	}
	if taskID == "" || len(*h.created) != 1 {
		t.Fatalf("RunNow did not create a task")
	}
	got, _ := h.schedules.GetByID(context.Background(), s.ID)
	if got.NextRunAt == nil || !got.NextRunAt.Equal(future) {
		t.Fatalf("RunNow must not change next_run_at: %v", got.NextRunAt)
	}
}
