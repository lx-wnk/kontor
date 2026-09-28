package mcp_test

import (
	"testing"
	"time"

	"github.com/lx-wnk/kontor/server/internal/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNotifier_BroadcastsToSubscribers(t *testing.T) {
	n := mcp.NewNotifier()
	ch1, unsub1 := n.Subscribe()
	defer unsub1()
	ch2, unsub2 := n.Subscribe()
	defer unsub2()

	n.NotifyToolsChanged()

	for _, ch := range []<-chan []byte{ch1, ch2} {
		select {
		case frame := <-ch:
			assert.Contains(t, string(frame), "notifications/tools/list_changed")
			assert.True(t, len(frame) > 6 && string(frame[:6]) == "data: ", "frame must be SSE-formatted")
		case <-time.After(time.Second):
			t.Fatal("subscriber did not receive notification within 1s")
		}
	}
}

func TestNotifier_UnsubscribedChannelReceivesNothing(t *testing.T) {
	n := mcp.NewNotifier()
	ch, unsub := n.Subscribe()
	unsub()

	n.NotifyToolsChanged()

	if frame, ok := <-ch; ok {
		t.Fatalf("unsubscribed channel received: %s", frame)
	}
}

func TestNotifier_SlowSubscriberDoesNotBlock(t *testing.T) {
	n := mcp.NewNotifier()
	ch, unsub := n.Subscribe()
	defer unsub()

	for i := 0; i < cap(ch); i++ {
		n.NotifyToolsChanged()
	}

	// The buffer is full: this call must drop the frame, not block.
	done := make(chan struct{})
	go func() {
		n.NotifyToolsChanged()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("NotifyToolsChanged blocked on a full subscriber channel")
	}

	require.Len(t, ch, cap(ch), "the full buffer keeps its frames; only the overflow is dropped")
}
