package repo_test

import (
	"context"
	"testing"
	"time"

	"github.com/lx-wnk/kontor/server/internal/db"
	"github.com/lx-wnk/kontor/server/internal/db/repo"
)

func newSchedule(t *testing.T, r repo.TaskScheduleRepo, name string, in repo.CreateTaskScheduleInput) string {
	t.Helper()
	in.Name = name
	if in.CronExpr == "" {
		in.CronExpr = "0 9 * * *"
	}
	if in.SlugPrefix == "" {
		in.SlugPrefix = "nightly"
	}
	if in.Title == "" {
		in.Title = "Nightly run"
	}
	if in.Cwd == "" {
		in.Cwd = "/tmp"
	}
	if in.MaxIterations == 0 {
		in.MaxIterations = 20
	}
	if in.StageTimeoutSeconds == 0 {
		in.StageTimeoutSeconds = 1800
	}
	s, err := r.Create(context.Background(), in)
	if err != nil {
		t.Fatalf("create schedule %q: %v", name, err)
	}
	return s.ID
}

func TestTaskScheduleRepo_CRUD(t *testing.T) {
	r := repo.NewTaskScheduleRepo(openDB(t))
	ctx := context.Background()

	id := newSchedule(t, r, "daily", repo.CreateTaskScheduleInput{})
	got, err := r.GetByID(ctx, id)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if got.CronExpr != "0 9 * * *" || got.Timezone != "UTC" || !got.Enabled {
		t.Fatalf("unexpected defaults: %+v", got)
	}

	newCron := "0 18 * * 1-5"
	updated, err := r.Update(ctx, id, repo.UpdateTaskScheduleInput{CronExpr: &newCron})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if updated.CronExpr != newCron {
		t.Fatalf("cron not updated: %q", updated.CronExpr)
	}

	if _, err := r.SetEnabled(ctx, id, false); err != nil {
		t.Fatalf("SetEnabled: %v", err)
	}
	enabled, _ := r.ListEnabled(ctx)
	if len(enabled) != 0 {
		t.Fatalf("expected 0 enabled, got %d", len(enabled))
	}

	if err := r.Delete(ctx, id); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := r.GetByID(ctx, id); err == nil {
		t.Fatal("expected not-found after delete")
	}
}

func TestTaskScheduleRepo_ListDue(t *testing.T) {
	r := repo.NewTaskScheduleRepo(openDB(t))
	ctx := context.Background()
	now := time.Date(2026, 6, 18, 12, 0, 0, 0, time.UTC)

	past := now.Add(-time.Minute)
	future := now.Add(time.Hour)

	dueID := newSchedule(t, r, "due", repo.CreateTaskScheduleInput{NextRunAt: &past})
	newSchedule(t, r, "future", repo.CreateTaskScheduleInput{NextRunAt: &future})
	newSchedule(t, r, "no-next", repo.CreateTaskScheduleInput{}) // null next_run_at excluded

	due, err := r.ListDue(ctx, now)
	if err != nil {
		t.Fatalf("ListDue: %v", err)
	}
	if len(due) != 1 || due[0].ID != dueID {
		t.Fatalf("expected only the past schedule due, got %d", len(due))
	}
}

func TestTaskScheduleRepo_UpdateFireState(t *testing.T) {
	r := repo.NewTaskScheduleRepo(openDB(t))
	ctx := context.Background()
	id := newSchedule(t, r, "fire", repo.CreateTaskScheduleInput{})

	fireAt := time.Date(2026, 6, 18, 9, 0, 0, 0, time.UTC)
	next := fireAt.Add(24 * time.Hour)
	taskID := "task-123"
	s, err := r.UpdateFireState(ctx, id, repo.FireStateInput{
		LastRunAt:  fireAt,
		LastTaskID: &taskID,
		NextRunAt:  &next,
	})
	if err != nil {
		t.Fatalf("UpdateFireState: %v", err)
	}
	if s.LastTaskID == nil || *s.LastTaskID != taskID {
		t.Fatalf("last_task_id not set: %+v", s.LastTaskID)
	}
	if s.LastRunAt == nil || !s.LastRunAt.Equal(fireAt) {
		t.Fatalf("last_run_at mismatch: %+v", s.LastRunAt)
	}
	if s.NextRunAt == nil || !s.NextRunAt.Equal(next) {
		t.Fatalf("next_run_at mismatch: %+v", s.NextRunAt)
	}
}

// newScheduleRepoWithResources returns a TaskScheduleRepo wired to a ResourceRepo
// so CRUD operations propagate to the resource registry.
func newScheduleRepoWithResources(t *testing.T) (repo.TaskScheduleRepo, repo.ResourceRepo, context.Context) {
	t.Helper()
	bundle, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = bundle.Client.Close() })
	resources := repo.NewResourceRepo(bundle.Client)
	schedules := repo.NewTaskScheduleRepo(bundle.Client)
	return schedules, resources, context.Background()
}

func TestTaskScheduleRepo_CreateCreatesResourceRow(t *testing.T) {
	schedules, resources, ctx := newScheduleRepoWithResources(t)

	s, err := schedules.Create(ctx, repo.CreateTaskScheduleInput{
		Name:                "nightly",
		CronExpr:            "0 9 * * *",
		SlugPrefix:          "nightly",
		Title:               "Nightly run",
		Cwd:                 "/tmp",
		MaxIterations:       20,
		StageTimeoutSeconds: 1800,
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	// Resource row must exist with the schedule ID as slug
	res, err := resources.Get(ctx, repo.ResourceKindRoutine, repo.GlobalScope(), s.ID)
	if err != nil {
		t.Fatalf("resource not created: %v", err)
	}
	if res.State != repo.ResourceStateEnabled {
		t.Fatalf("expected enabled, got %s", res.State)
	}
	if res.Name != "nightly" {
		t.Fatalf("expected name nightly, got %s", res.Name)
	}
	if res.OriginRef != s.ID {
		t.Fatalf("origin_ref mismatch: %s != %s", res.OriginRef, s.ID)
	}

	// Schedule must have resource_id backlinked
	got, _ := schedules.GetByID(ctx, s.ID)
	if got.ResourceID != res.ID {
		t.Fatalf("resource_id not backlinked: %s != %s", got.ResourceID, res.ID)
	}
}

func TestTaskScheduleRepo_DeleteOrphansResourceRow(t *testing.T) {
	schedules, resources, ctx := newScheduleRepoWithResources(t)

	s, err := schedules.Create(ctx, repo.CreateTaskScheduleInput{
		Name:                "ephemeral",
		CronExpr:            "0 9 * * *",
		SlugPrefix:          "eph",
		Title:               "Ephemeral",
		Cwd:                 "/tmp",
		MaxIterations:       20,
		StageTimeoutSeconds: 1800,
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	if err := schedules.Delete(ctx, s.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	// Resource row must still exist, state orphaned
	res, err := resources.Get(ctx, repo.ResourceKindRoutine, repo.GlobalScope(), s.ID)
	if err != nil {
		t.Fatalf("resource row should survive delete: %v", err)
	}
	if res.State != repo.ResourceStateOrphaned {
		t.Fatalf("expected orphaned, got %s", res.State)
	}
}

func TestTaskScheduleRepo_SetEnabledSyncsResourceState(t *testing.T) {
	schedules, resources, ctx := newScheduleRepoWithResources(t)

	s, err := schedules.Create(ctx, repo.CreateTaskScheduleInput{
		Name:                "toggle",
		CronExpr:            "0 9 * * *",
		SlugPrefix:          "toggle",
		Title:               "Toggle",
		Cwd:                 "/tmp",
		MaxIterations:       20,
		StageTimeoutSeconds: 1800,
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	// Disable
	if _, err := schedules.SetEnabled(ctx, s.ID, false); err != nil {
		t.Fatalf("SetEnabled(false): %v", err)
	}
	res, _ := resources.Get(ctx, repo.ResourceKindRoutine, repo.GlobalScope(), s.ID)
	if res.State != repo.ResourceStateDisabled {
		t.Fatalf("expected disabled, got %s", res.State)
	}

	// Re-enable
	if _, err := schedules.SetEnabled(ctx, s.ID, true); err != nil {
		t.Fatalf("SetEnabled(true): %v", err)
	}
	res, _ = resources.Get(ctx, repo.ResourceKindRoutine, repo.GlobalScope(), s.ID)
	if res.State != repo.ResourceStateEnabled {
		t.Fatalf("expected enabled, got %s", res.State)
	}
}

func TestTaskScheduleRepo_UpdateRefreshesResourceName(t *testing.T) {
	schedules, resources, ctx := newScheduleRepoWithResources(t)

	s, err := schedules.Create(ctx, repo.CreateTaskScheduleInput{
		Name:                "original",
		CronExpr:            "0 9 * * *",
		SlugPrefix:          "orig",
		Title:               "Original",
		Cwd:                 "/tmp",
		MaxIterations:       20,
		StageTimeoutSeconds: 1800,
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	newName := "renamed"
	if _, err := schedules.Update(ctx, s.ID, repo.UpdateTaskScheduleInput{Name: &newName}); err != nil {
		t.Fatalf("Update: %v", err)
	}

	res, _ := resources.Get(ctx, repo.ResourceKindRoutine, repo.GlobalScope(), s.ID)
	if res.Name != "renamed" {
		t.Fatalf("expected renamed, got %s", res.Name)
	}
}

// TestTaskScheduleRepo_EveryConstructedRepoSyncsResources is the wiring test.
// The lifecycle helpers were reachable only through a repo somebody had handed a
// ResourceRepo, and every mutating call site in production — the REST schedules
// handler, the scheduler, and the MCP schedule tools — was constructed without
// one, so the whole sync was dead in production while every unit test passed.
// The constructor now builds its own, which is what makes that unreachable; this
// asserts it from the constructor's public surface rather than from a repo the
// test wired by hand.
func TestTaskScheduleRepo_EveryConstructedRepoSyncsResources(t *testing.T) {
	bundle, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = bundle.Client.Close() })
	ctx := context.Background()

	// Exactly the call every production site makes — no ResourceRepo in sight.
	schedules := repo.NewTaskScheduleRepo(bundle.Client)
	resources := repo.NewResourceRepo(bundle.Client)

	s, err := schedules.Create(ctx, repo.CreateTaskScheduleInput{
		Name:                "wired",
		CronExpr:            "0 9 * * *",
		SlugPrefix:          "wired",
		Title:               "Wired run",
		Cwd:                 "/tmp",
		MaxIterations:       20,
		StageTimeoutSeconds: 1800,
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := resources.Get(ctx, repo.ResourceKindRoutine, repo.GlobalScope(), s.ID); err != nil {
		t.Fatalf("create did not reach the registry: %v", err)
	}

	// SetEnabled is the MCP tools' path and has its own writer, separate from Update.
	if _, err := schedules.SetEnabled(ctx, s.ID, false); err != nil {
		t.Fatalf("SetEnabled: %v", err)
	}
	res, err := resources.Get(ctx, repo.ResourceKindRoutine, repo.GlobalScope(), s.ID)
	if err != nil {
		t.Fatalf("resource lookup after SetEnabled: %v", err)
	}
	if res.State != repo.ResourceStateDisabled {
		t.Errorf("state after disable: got %q, want %q", res.State, repo.ResourceStateDisabled)
	}

	// A deleted schedule leaves the row behind as orphaned so grants still resolve.
	if err := schedules.Delete(ctx, s.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	res, err = resources.Get(ctx, repo.ResourceKindRoutine, repo.GlobalScope(), s.ID)
	if err != nil {
		t.Fatalf("resource must survive the schedule: %v", err)
	}
	if res.State != repo.ResourceStateOrphaned {
		t.Errorf("state after delete: got %q, want %q", res.State, repo.ResourceStateOrphaned)
	}
}
