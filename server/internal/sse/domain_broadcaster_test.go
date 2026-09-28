package sse_test

import (
	"bytes"
	"encoding/json"
	"testing"
	"time"

	"github.com/lx-wnk/kontor/server/internal/sse"
)

// readFrame waits up to 1 second for an SSE frame and returns the JSON payload,
// stripping the "data: " prefix and "\n\n" suffix that Broadcaster adds.
func readFrame(t *testing.T, ch chan []byte) []byte {
	t.Helper()
	select {
	case raw := <-ch:
		// Broadcaster wraps payloads as "data: <json>\n\n" (see sse.Broadcaster).
		return bytes.TrimSuffix(bytes.TrimPrefix(raw, []byte("data: ")), []byte("\n\n"))
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for SSE frame")
		return nil
	}
}

func TestSpawnerBroadcaster_EmitsTypedFrame(t *testing.T) {
	b := sse.NewSpawnerBroadcaster(sse.NewBroadcaster())
	ch := b.Subscribe()
	defer b.Unsubscribe(ch)

	b.Broadcast(sse.SpawnerEvent{Type: "spawner_created", SpawnerID: "s1", Payload: map[string]string{"id": "s1"}})

	var got map[string]any
	if err := json.Unmarshal(readFrame(t, ch), &got); err != nil {
		t.Fatalf("frame not JSON: %v", err)
	}
	if got["type"] != "spawner_created" || got["spawnerId"] != "s1" {
		t.Errorf("frame: got %v", got)
	}
	if got["payload"] == nil {
		t.Error("created event must carry payload")
	}
}

func TestSpawnerBroadcaster_DeletedHasNoPayload(t *testing.T) {
	b := sse.NewSpawnerBroadcaster(sse.NewBroadcaster())
	ch := b.Subscribe()
	defer b.Unsubscribe(ch)

	b.Broadcast(sse.SpawnerEvent{Type: "spawner_deleted", SpawnerID: "s1"})

	var got map[string]any
	_ = json.Unmarshal(readFrame(t, ch), &got)
	if _, ok := got["payload"]; ok {
		t.Errorf("deleted event must omit payload, got %v", got)
	}
}

func TestProjectBroadcaster_EmitsTypedFrame(t *testing.T) {
	b := sse.NewProjectBroadcaster(sse.NewBroadcaster())
	ch := b.Subscribe()
	defer b.Unsubscribe(ch)

	b.Broadcast(sse.ProjectEvent{Type: "project_updated", ProjectID: "p1", Payload: map[string]string{"id": "p1"}})

	var got map[string]any
	if err := json.Unmarshal(readFrame(t, ch), &got); err != nil {
		t.Fatalf("frame not JSON: %v", err)
	}
	if got["type"] != "project_updated" || got["projectId"] != "p1" {
		t.Errorf("frame: got %v", got)
	}
}
