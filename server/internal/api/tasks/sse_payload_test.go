package tasks_test

// Tests for F-PERF-013: stage_run_updated and permission_request SSE events
// must carry the enriched task payload so the client can apply it without a
// refetch round-trip.

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/lx-wnk/kontor/server/internal/db"
	"github.com/lx-wnk/kontor/server/internal/db/ent"
	"github.com/lx-wnk/kontor/server/internal/db/repo"
	"github.com/lx-wnk/kontor/server/internal/sse"
)

// captureNextEvent subscribes, triggers fn, then reads the first event from the
// subscriber channel. Returns the decoded TaskEvent map.
func captureNextEvent(t *testing.T, tb *sse.TaskBroadcaster, fn func()) map[string]any {
	t.Helper()
	sub := tb.Subscribe()
	defer tb.Unsubscribe(sub)

	fn()

	select {
	case raw, ok := <-sub:
		if !ok {
			t.Fatal("subscriber channel closed unexpectedly")
		}
		// Broadcaster pre-formats SSE frames as "data: <json>\n\n" (see sse.Broadcaster).
		payload := bytes.TrimSuffix(bytes.TrimPrefix(raw, []byte("data: ")), []byte("\n\n"))
		var event map[string]any
		if err := json.Unmarshal(payload, &event); err != nil {
			t.Fatalf("unmarshal event: %v\nraw=%q", err, raw)
		}
		return event
	default:
		t.Fatal("no event broadcast within the synchronous window")
		return nil
	}
}

// seedRunningTask creates a task with "manual" autonomy and a stage run in
// "running" status, returning the task ID and stage run ID.
// Autonomy is pinned to "manual" so the task stays in the gated (non-allow-all)
// path regardless of the schema default ("spec_gated").
func seedRunningTask(t *testing.T, client *ent.Client, taskRepo repo.TaskRepo, srRepo repo.StageRunRepo) (taskID, stageRunID string) {
	t.Helper()
	ctx := testCtx(t)

	task, err := taskRepo.Create(ctx, repo.CreateTaskInput{
		Slug:          "sse-test-task",
		Title:         "SSE Test Task",
		Cwd:           "/tmp/sse-test",
		CurrentStage:  "implementation",
		Priority:      "medium",
		MaxIterations: 5,
	})
	if err != nil {
		t.Fatalf("create task: %v", err)
	}
	if _, err = client.Task.UpdateOneID(task.ID).SetAutonomy("manual").Save(ctx); err != nil {
		t.Fatalf("set autonomy manual: %v", err)
	}

	sr, err := srRepo.Create(ctx, repo.CreateStageRunInput{
		TaskID:    task.ID,
		Stage:     "implementation",
		Iteration: 1,
	})
	if err != nil {
		t.Fatalf("create stage run: %v", err)
	}

	running := "running"
	sr, err = srRepo.Update(ctx, sr.ID, repo.UpdateStageRunInput{Status: &running})
	if err != nil {
		t.Fatalf("set stage run running: %v", err)
	}

	return task.ID, sr.ID
}

func TestPermissionRequest_BroadcastsEnrichedPayload(t *testing.T) {
	bundle, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { _ = bundle.Client.Close() })

	taskRepo := repo.NewTaskRepo(bundle.Client)
	srRepo := repo.NewStageRunRepo(bundle.Client)

	tb, r := newTestHandlerWithBroadcaster(t, bundle.Client)
	taskID, stageRunID := seedRunningTask(t, bundle.Client, taskRepo, srRepo)

	body := map[string]any{
		"stageRunId": stageRunID,
		"tool":       "Read",
	}
	b, _ := json.Marshal(body)

	event := captureNextEvent(t, tb, func() {
		req := httptest.NewRequest(http.MethodPost, "/api/permission-requests", bytes.NewReader(b))
		req.Header.Set("Content-Type", "application/json")
		req = withAuth(t, req)
		rr := httptest.NewRecorder()
		r.ServeHTTP(rr, req)
		if rr.Code != http.StatusCreated {
			t.Fatalf("expected 201, got %d: %s", rr.Code, rr.Body.String())
		}
	})

	// The event must be "permission_request" and carry an enriched payload.
	if event["type"] != "permission_request" {
		t.Errorf("expected event type permission_request, got %v", event["type"])
	}
	if event["taskId"] != taskID {
		t.Errorf("expected taskId=%s, got %v", taskID, event["taskId"])
	}
	payload, ok := event["payload"].(map[string]any)
	if !ok || payload == nil {
		t.Fatalf("expected non-nil enriched payload, got %v", event["payload"])
	}
	if payload["id"] != taskID {
		t.Errorf("expected payload.id=%s, got %v", taskID, payload["id"])
	}
	// latestStageRunStatus should reflect the awaiting_user flip.
	if payload["latestStageRunStatus"] != "awaiting_user" {
		t.Errorf("expected latestStageRunStatus=awaiting_user, got %v", payload["latestStageRunStatus"])
	}
}

func TestBulkCreatePermissionRequests_BroadcastsEnrichedPayload(t *testing.T) {
	bundle, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { _ = bundle.Client.Close() })

	taskRepo := repo.NewTaskRepo(bundle.Client)
	srRepo := repo.NewStageRunRepo(bundle.Client)

	tb, r := newTestHandlerWithBroadcaster(t, bundle.Client)
	taskID, stageRunID := seedRunningTask(t, bundle.Client, taskRepo, srRepo)

	body := map[string]any{
		"stageRunId": stageRunID,
		"entries": []map[string]any{
			{"tool": "Write"},
		},
	}
	b, _ := json.Marshal(body)

	event := captureNextEvent(t, tb, func() {
		req := httptest.NewRequest(http.MethodPost, "/api/permission-requests/bulk", bytes.NewReader(b))
		req.Header.Set("Content-Type", "application/json")
		req = withAuth(t, req)
		rr := httptest.NewRecorder()
		r.ServeHTTP(rr, req)
		if rr.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
		}
	})

	// The event must be "permission_request" and carry an enriched payload.
	if event["type"] != "permission_request" {
		t.Errorf("expected event type permission_request, got %v", event["type"])
	}
	if event["taskId"] != taskID {
		t.Errorf("expected taskId=%s, got %v", taskID, event["taskId"])
	}
	payload, ok := event["payload"].(map[string]any)
	if !ok || payload == nil {
		t.Fatalf("expected non-nil enriched payload, got %v", event["payload"])
	}
	if payload["id"] != taskID {
		t.Errorf("expected payload.id=%s, got %v", taskID, payload["id"])
	}
	if payload["latestStageRunStatus"] != "awaiting_user" {
		t.Errorf("expected latestStageRunStatus=awaiting_user, got %v", payload["latestStageRunStatus"])
	}
}
