package sse_test

import (
	"encoding/json"
	"testing"

	"github.com/lx-wnk/kontor/server/internal/sse"
)

func TestTypedBroadcaster_DeliversOneFrameWithEventKeys(t *testing.T) {
	b := sse.NewTypedBroadcaster[sse.CloudSessionEvent](sse.NewBroadcaster())
	ch := b.Subscribe()
	defer b.Unsubscribe(ch)

	b.Broadcast(sse.CloudSessionEvent{Type: sse.CloudSessionCreated, CloudSessionID: "c1", Payload: map[string]string{"id": "c1"}})

	var got map[string]any
	if err := json.Unmarshal(readFrame(t, ch), &got); err != nil {
		t.Fatalf("frame not JSON: %v", err)
	}
	if got["type"] != "cloud_session_created" || got["cloudSessionId"] != "c1" || got["payload"] == nil {
		t.Errorf("frame: got %v", got)
	}
	select {
	case extra := <-ch:
		t.Errorf("expected exactly one frame, got extra %q", extra)
	default:
	}
}

func TestTypedBroadcaster_OmitsNilPayload(t *testing.T) {
	b := sse.NewTypedBroadcaster[sse.CloudSessionEvent](sse.NewBroadcaster())
	ch := b.Subscribe()
	defer b.Unsubscribe(ch)

	b.Broadcast(sse.CloudSessionEvent{Type: sse.CloudSessionDeleted, CloudSessionID: "c1"})

	var got map[string]any
	if err := json.Unmarshal(readFrame(t, ch), &got); err != nil {
		t.Fatalf("frame not JSON: %v", err)
	}
	if _, ok := got["payload"]; ok {
		t.Errorf("nil payload must be omitted, got %v", got)
	}
}
