package tools

import (
	"context"
	"os/exec"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/lx-wnk/kontor/server/internal/db"
	"github.com/lx-wnk/kontor/server/internal/db/repo"
	mcp "github.com/lx-wnk/kontor/server/internal/mcp"
	"github.com/lx-wnk/kontor/server/internal/scheduler"
)

func newScheduleRegistry(t *testing.T) (mcp.ToolRegistry, repo.TaskScheduleRepo) {
	t.Helper()
	bundle, err := db.Open(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { _ = bundle.Client.Close() })
	r := repo.NewTaskScheduleRepo(bundle.Client)
	registry := mcp.ToolRegistry{}
	RegisterScheduleTools(registry, ScheduleDeps{
		Repo:       r,
		Translator: scheduler.NewNLCron(nil),
	})
	return registry, r
}

func TestRegisterScheduleTools_Present(t *testing.T) {
	registry, _ := newScheduleRegistry(t)
	for _, name := range []string{"manage_schedule", "list_schedules"} {
		if _, ok := registry[name]; !ok {
			t.Errorf("expected tool %q registered", name)
		}
	}
}

func TestManageSchedule_CreateAndList(t *testing.T) {
	registry, _ := newScheduleRegistry(t)
	ctx := context.Background()

	_, err := registry["manage_schedule"].Handler(ctx, map[string]any{
		"action":     "create",
		"name":       "nightly",
		"nlText":     "every day at 3am",
		"slugPrefix": "nightly",
		"title":      "Nightly",
		"cwd":        "/tmp",
	})
	require.NoError(t, err)

	res, err := registry["list_schedules"].Handler(ctx, map[string]any{})
	require.NoError(t, err)
	require.NotNil(t, res)
}

func TestManageSchedule_InvalidPhraseFails(t *testing.T) {
	registry, _ := newScheduleRegistry(t)
	_, err := registry["manage_schedule"].Handler(context.Background(), map[string]any{
		"action":     "create",
		"name":       "bad",
		"nlText":     "no idea when",
		"slugPrefix": "bad",
		"title":      "Bad",
		"cwd":        "/tmp",
	})
	require.Error(t, err)
}

func TestManageSchedule_EnableDisableDelete(t *testing.T) {
	registry, r := newScheduleRegistry(t)
	ctx := context.Background()

	s, err := r.Create(ctx, repo.CreateTaskScheduleInput{
		Name: "s", CronExpr: "0 9 * * *", SlugPrefix: "s", Title: "S", Cwd: "/tmp",
		MaxIterations: 20, StageTimeoutSeconds: 1800,
	})
	require.NoError(t, err)

	_, err = registry["manage_schedule"].Handler(ctx, map[string]any{"action": "disable", "id": s.ID})
	require.NoError(t, err)
	got, _ := r.GetByID(ctx, s.ID)
	require.False(t, got.Enabled)

	_, err = registry["manage_schedule"].Handler(ctx, map[string]any{"action": "delete", "id": s.ID})
	require.NoError(t, err)
	_, err = r.GetByID(ctx, s.ID)
	require.Error(t, err)
}

// MCP-created schedules are owned by the calling API key, and list_schedules
// is scoped to that key — a different key must not see them.
func TestManageSchedule_ScopedToCallingKey(t *testing.T) {
	registry, _ := newScheduleRegistry(t)
	keyA := mcp.ContextWithAuth(context.Background(), &mcp.MCPAuthInfo{KeyID: "key-a"})
	keyB := mcp.ContextWithAuth(context.Background(), &mcp.MCPAuthInfo{KeyID: "key-b"})

	_, err := registry["manage_schedule"].Handler(keyA, map[string]any{
		"action": "create", "name": "owned", "nlText": "every day at 3am",
		"slugPrefix": "owned", "title": "Owned", "cwd": "/tmp",
	})
	require.NoError(t, err)

	resA, err := registry["list_schedules"].Handler(keyA, map[string]any{})
	require.NoError(t, err)
	require.Contains(t, resA.Content[0].Text, "owned")

	resB, err := registry["list_schedules"].Handler(keyB, map[string]any{})
	require.NoError(t, err)
	require.Equal(t, "[]", resB.Content[0].Text)
}

func TestManageSchedule_CreateDefaultsToJobRunMode(t *testing.T) {
	registry, r := newScheduleRegistry(t)
	ctx := context.Background()

	res, err := registry["manage_schedule"].Handler(ctx, map[string]any{
		"action": "create", "name": "job-default", "nlText": "every day at 3am",
		"slugPrefix": "job-default", "title": "Job Default", "cwd": "/tmp",
	})
	require.NoError(t, err)
	require.NotNil(t, res)

	rows, err := r.ListForUser(ctx, "", true)
	require.NoError(t, err)
	require.NotEmpty(t, rows)
	require.Equal(t, repo.RunModeJob, rows[len(rows)-1].RunMode)
}

func TestManageSchedule_CreatePipelineNonGitCwdFails(t *testing.T) {
	registry, _ := newScheduleRegistry(t)
	_, err := registry["manage_schedule"].Handler(context.Background(), map[string]any{
		"action": "create", "name": "bad-pipeline", "nlText": "every day at 3am",
		"slugPrefix": "bad-pipeline", "title": "Bad Pipeline", "cwd": t.TempDir(), "runMode": "pipeline",
	})
	require.ErrorContains(t, err, "working directory is not a git repository")
}

func TestManageSchedule_CreatePipelineGitCwdPersists(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	dir := t.TempDir()
	require.NoError(t, exec.Command("git", "init", dir).Run())

	registry, r := newScheduleRegistry(t)
	ctx := context.Background()
	_, err := registry["manage_schedule"].Handler(ctx, map[string]any{
		"action": "create", "name": "pipeline", "nlText": "every day at 3am",
		"slugPrefix": "pipeline", "title": "Pipeline", "cwd": dir, "runMode": "pipeline",
	})
	require.NoError(t, err)

	rows, err := r.ListForUser(ctx, "", true)
	require.NoError(t, err)
	require.NotEmpty(t, rows)
	require.Equal(t, repo.RunModePipeline, rows[len(rows)-1].RunMode)
}

func TestManageSchedule_UpdateInvalidRunModeFails(t *testing.T) {
	registry, r := newScheduleRegistry(t)
	ctx := context.Background()

	s, err := r.Create(ctx, repo.CreateTaskScheduleInput{
		Name: "u", CronExpr: "0 9 * * *", SlugPrefix: "u", Title: "U", Cwd: "/tmp",
		MaxIterations: 20, StageTimeoutSeconds: 1800,
	})
	require.NoError(t, err)

	_, err = registry["manage_schedule"].Handler(ctx, map[string]any{
		"action": "update", "id": s.ID, "runMode": "cron",
	})
	require.ErrorContains(t, err, "runMode must be job or pipeline")
}

func TestManageSchedule_UpdateToPipelineOutsideGitFails(t *testing.T) {
	registry, r := newScheduleRegistry(t)
	ctx := context.Background()

	s, err := r.Create(ctx, repo.CreateTaskScheduleInput{
		Name: "g", CronExpr: "0 9 * * *", SlugPrefix: "g", Title: "G", Cwd: t.TempDir(),
		MaxIterations: 20, StageTimeoutSeconds: 1800,
	})
	require.NoError(t, err)

	_, err = registry["manage_schedule"].Handler(ctx, map[string]any{
		"action": "update", "id": s.ID, "runMode": "pipeline",
	})
	require.ErrorContains(t, err, "working directory is not a git repository")

	got, err := r.GetByID(ctx, s.ID)
	require.NoError(t, err)
	require.Equal(t, repo.RunModeJob, got.RunMode, "a refused update must not change the run mode")
}

// Regression: a timezone-only update (no nlText/cronExpr) must persist the new
// timezone. Previously the field was read only inside the cron-change branch,
// so updating the zone alone was a silent no-op.
func TestManageSchedule_TimezoneOnlyUpdatePersists(t *testing.T) {
	registry, r := newScheduleRegistry(t)
	ctx := context.Background()

	s, err := r.Create(ctx, repo.CreateTaskScheduleInput{
		Name: "tz", CronExpr: "0 9 * * *", Timezone: "UTC", SlugPrefix: "tz", Title: "TZ", Cwd: "/tmp",
		MaxIterations: 20, StageTimeoutSeconds: 1800,
	})
	require.NoError(t, err)

	_, err = registry["manage_schedule"].Handler(ctx, map[string]any{
		"action": "update", "id": s.ID, "timezone": "Europe/Berlin",
	})
	require.NoError(t, err)

	got, err := r.GetByID(ctx, s.ID)
	require.NoError(t, err)
	require.Equal(t, "Europe/Berlin", got.Timezone)
}
