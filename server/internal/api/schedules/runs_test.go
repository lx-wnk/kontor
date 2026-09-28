package schedules_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/require"

	"github.com/lx-wnk/kontor/server/internal/api/schedules"
	"github.com/lx-wnk/kontor/server/internal/db"
	"github.com/lx-wnk/kontor/server/internal/db/repo"
	"github.com/lx-wnk/kontor/server/internal/scheduler"
)

func newRunsServer(t *testing.T) (scheduleID, taskAID, taskBID string, get func(path string) *http.Response) {
	t.Helper()
	bundle, err := db.Open(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { _ = bundle.Client.Close() })

	schedRepo := repo.NewTaskScheduleRepo(bundle.Client)
	taskRepo := repo.NewTaskRepo(bundle.Client)
	srRepo := repo.NewStageRunRepo(bundle.Client)
	ctx := context.Background()

	s1, err := schedRepo.Create(ctx, repo.CreateTaskScheduleInput{
		Name: "inbox", CronExpr: "*/5 * * * *", SlugPrefix: "inbox",
		Title: "Inbox", Cwd: "/tmp", Priority: "medium",
		MaxIterations: 20, StageTimeoutSeconds: 1800,
	})
	require.NoError(t, err)

	otherRoutine := "other-routine"

	// task A: done job with one completed run bearing a summary.
	taskA, err := taskRepo.Create(ctx, repo.CreateTaskInput{
		Slug: "a", Title: "Task A", Cwd: "/tmp", CurrentStage: "done",
		Priority: "medium", MaxIterations: 20, StageTimeoutSeconds: 1800,
		Kind: "job", RoutineID: &s1.ID,
	})
	require.NoError(t, err)
	runA, err := srRepo.Create(ctx, repo.CreateStageRunInput{TaskID: taskA.ID, Stage: "job"})
	require.NoError(t, err)
	now := time.Now()
	status := "done"
	cost := 12
	_, err = srRepo.Update(ctx, runA.ID, repo.UpdateStageRunInput{
		Status:    &status,
		Output:    map[string]any{"summary": "sorted 3 mails", "result": "…"},
		CostCents: &cost,
		StartedAt: &now,
		EndedAt:   &now,
	})
	require.NoError(t, err)

	// task B: newer than A, still running, two runs summing to the same cost.
	time.Sleep(1100 * time.Millisecond)
	taskB, err := taskRepo.Create(ctx, repo.CreateTaskInput{
		Slug: "b", Title: "Task B", Cwd: "/tmp", CurrentStage: "job",
		Priority: "medium", MaxIterations: 20, StageTimeoutSeconds: 1800,
		Kind: "job", RoutineID: &s1.ID,
	})
	require.NoError(t, err)
	runB1, err := srRepo.Create(ctx, repo.CreateStageRunInput{TaskID: taskB.ID, Stage: "job"})
	require.NoError(t, err)
	awaiting := "awaiting_user"
	cost5 := 5
	_, err = srRepo.Update(ctx, runB1.ID, repo.UpdateStageRunInput{Status: &awaiting, CostCents: &cost5, StartedAt: &now})
	require.NoError(t, err)
	runB2, err := srRepo.Create(ctx, repo.CreateStageRunInput{TaskID: taskB.ID, Stage: "job", Iteration: 1})
	require.NoError(t, err)
	running := "running"
	cost7 := 7
	_, err = srRepo.Update(ctx, runB2.ID, repo.UpdateStageRunInput{Status: &running, CostCents: &cost7, StartedAt: &now})
	require.NoError(t, err)

	// task C belongs to a different routine and must not appear.
	_, err = taskRepo.Create(ctx, repo.CreateTaskInput{
		Slug: "c", Title: "Task C", Cwd: "/tmp", CurrentStage: "job",
		Priority: "medium", MaxIterations: 20, StageTimeoutSeconds: 1800,
		Kind: "job", RoutineID: &otherRoutine,
	})
	require.NoError(t, err)

	h := schedules.NewHandler(schedRepo, scheduler.NewNLCron(nil), nil, repo.NewMCPApplicationRepo(bundle.Client), taskRepo, srRepo, true)
	r := chi.NewRouter()
	h.Mount(r)
	srv := httptest.NewServer(r)
	t.Cleanup(srv.Close)

	get = func(path string) *http.Response {
		resp, err := http.Get(srv.URL + path)
		require.NoError(t, err)
		return resp
	}
	return s1.ID, taskA.ID, taskB.ID, get
}

func TestRoutineRuns(t *testing.T) {
	scheduleID, taskAID, taskBID, get := newRunsServer(t)

	resp := get("/api/schedules/" + scheduleID + "/runs")
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var views []map[string]any
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&views))
	require.Len(t, views, 2)

	b := views[0]
	require.Equal(t, taskBID, b["taskId"])
	require.Equal(t, "running", b["status"])
	require.InDelta(t, 12, b["costCents"], 0)
	require.Equal(t, "", b["summary"])
	require.Nil(t, b["endedAt"])

	a := views[1]
	require.Equal(t, taskAID, a["taskId"])
	require.Equal(t, "sorted 3 mails", a["summary"])
	require.Equal(t, "done", a["status"])
	require.InDelta(t, 12, a["costCents"], 0)

	notFound := get("/api/schedules/does-not-exist/runs")
	require.Equal(t, http.StatusNotFound, notFound.StatusCode)
}
