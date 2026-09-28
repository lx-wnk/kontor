package serverapp

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/lx-wnk/kontor/server/internal/api/tasks"
	"github.com/lx-wnk/kontor/server/internal/db"
	"github.com/lx-wnk/kontor/server/internal/db/rawrepo"
	"github.com/lx-wnk/kontor/server/internal/db/repo"
	"github.com/lx-wnk/kontor/server/internal/refine"
	"github.com/lx-wnk/kontor/server/internal/sse"
)

func TestMCPTaskBroadcast_SendsDependencyAndRefineState(t *testing.T) {
	bundle, err := db.Open(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { _ = bundle.Client.Close() })

	ctx := context.Background()
	taskRepo := repo.NewTaskRepo(bundle.Client)
	createTask := func(slug string) string {
		task, err := taskRepo.Create(ctx, repo.CreateTaskInput{
			Slug:          slug,
			Title:         slug,
			Cwd:           t.TempDir(),
			MaxIterations: 3,
			Priority:      "normal",
			CurrentStage:  "backlog",
		})
		require.NoError(t, err)
		return task.ID
	}
	prereqID := createTask("prereq")
	taskID := createTask("dependent")
	depRepo := repo.NewDependencyRepo(bundle.Client)
	_, err = depRepo.Add(ctx, taskID, prereqID, "done", "on_hold")
	require.NoError(t, err)
	runner := refine.NewRunner(repo.NewRefinementTurnRepo(bundle.Client), nil)
	runner.MarkDraftReady(taskID)

	tb := sse.NewTaskBroadcaster(sse.NewBroadcaster())
	ch := tb.Subscribe()
	t.Cleanup(func() { tb.Unsubscribe(ch) })
	h := tasks.NewHandler(tasks.Deps{
		TaskRepo:     taskRepo,
		SRRepo:       repo.NewStageRunRepo(bundle.Client),
		SRBulkRepo:   rawrepo.NewStageRunBulkRepo(bundle.DB),
		PermRepo:     repo.NewPermissionRepo(bundle.Client),
		DepRepo:      depRepo,
		Broadcaster:  tb,
		RefineReader: runner,
	})

	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	h.BroadcastEnrichedEvent(cancelled, "task_created", taskID)

	var frame []byte
	select {
	case frame = <-ch:
	default:
		t.Fatal("broadcast must publish a frame even when the request context is cancelled")
	}
	frame = bytes.TrimSuffix(bytes.TrimPrefix(frame, []byte("data: ")), []byte("\n\n"))

	var event sse.TaskEvent
	require.NoError(t, json.Unmarshal(frame, &event))
	require.Equal(t, "task_created", event.Type)
	require.Equal(t, taskID, event.TaskID)

	payload, ok := event.Payload.(map[string]any)
	require.True(t, ok, "payload must be a JSON object")
	require.Equal(t, taskID, payload["id"])
	require.Equal(t, true, payload["isBlocked"], "a task waiting on an unfinished prerequisite must broadcast isBlocked")
	require.Equal(t, refine.StatusDraftReady, payload["refineStatus"], "an injected concept must broadcast its draft_ready refine status")
}

func TestScheduleBroadcastEmitsScheduleChanged(t *testing.T) {
	bundle, err := db.Open(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { _ = bundle.Client.Close() })

	schedRepo := repo.NewTaskScheduleRepo(bundle.Client)
	ctx := context.Background()

	tb := sse.NewTaskBroadcaster(sse.NewBroadcaster())
	ch := tb.Subscribe()
	t.Cleanup(func() { tb.Unsubscribe(ch) })

	broadcast := newMCPScheduleBroadcast(tb)

	s, err := schedRepo.Create(ctx, repo.CreateTaskScheduleInput{
		Name:       "test-sched",
		CronExpr:   "0 9 * * 1-5",
		Timezone:   "UTC",
		SlugPrefix: "test",
		Title:      "Test Schedule",
		Cwd:        t.TempDir(),
	})
	require.NoError(t, err)

	broadcast(s.ID, s)

	var frame []byte
	select {
	case frame = <-ch:
	default:
		t.Fatal("broadcast must have published a frame")
	}
	frame = bytes.TrimSuffix(bytes.TrimPrefix(frame, []byte("data: ")), []byte("\n\n"))

	var event sse.TaskEvent
	require.NoError(t, json.Unmarshal(frame, &event))
	require.Equal(t, "schedule_changed", event.Type)
	require.Equal(t, s.ID, event.TaskID)

	payload, ok := event.Payload.(map[string]any)
	require.True(t, ok, "payload must be a JSON object")
	require.Equal(t, s.ID, payload["id"])
	require.Equal(t, "test-sched", payload["name"])

	broadcast(s.ID, nil)
	frame = bytes.TrimSuffix(bytes.TrimPrefix(<-ch, []byte("data: ")), []byte("\n\n"))
	var deleted sse.TaskEvent
	require.NoError(t, json.Unmarshal(frame, &deleted))
	require.Equal(t, "schedule_changed", deleted.Type)
	require.Nil(t, deleted.Payload, "a delete must send no payload so clients re-fetch")
}
