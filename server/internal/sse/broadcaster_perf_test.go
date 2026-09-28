package sse_test

import (
	"strings"
	"testing"
	"time"

	"github.com/lx-wnk/kontor/server/internal/sse"
	"github.com/stretchr/testify/require"
)

// TestBroadcaster_BroadcastComment verifies that the heartbeat comment frame
// is delivered to subscribers in valid SSE comment syntax. (F-PERF-006)
func TestBroadcaster_BroadcastComment(t *testing.T) {
	b := sse.NewBroadcaster()
	ch := b.Subscribe()
	defer b.Unsubscribe(ch)

	b.BroadcastComment([]byte("heartbeat"))

	select {
	case data := <-ch:
		s := string(data)
		require.True(t, strings.HasPrefix(s, ": "), "comment frame must start with ': '")
		require.True(t, strings.Contains(s, "heartbeat"), "comment frame must contain the text")
		require.True(t, strings.HasSuffix(s, "\n\n"), "comment frame must end with \\n\\n")
	case <-time.After(100 * time.Millisecond):
		t.Fatal("timeout: no comment frame received")
	}
}

// TestBroadcaster_BroadcastNoLockContention verifies that a concurrent
// Subscribe does not deadlock while Broadcast is iterating. (F-PERF-018)
func TestBroadcaster_BroadcastNoLockContention(t *testing.T) {
	b := sse.NewBroadcaster()

	// Start several subscribers.
	chs := make([]chan []byte, 5)
	for i := range chs {
		chs[i] = b.Subscribe()
	}

	done := make(chan struct{})

	// Concurrently subscribe and unsubscribe while broadcasting.
	go func() {
		defer close(done)
		for i := 0; i < 50; i++ {
			ch := b.Subscribe()
			b.Broadcast([]byte("ping"))
			b.Unsubscribe(ch)
		}
	}()

	// Drain channels to prevent buffer-full drops from blocking the test.
	for i := 0; i < 20; i++ {
		for _, ch := range chs {
			select {
			case <-ch:
			default:
			}
		}
		time.Sleep(time.Millisecond)
	}

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("deadlock detected: concurrent subscribe/broadcast did not complete")
	}

	for _, ch := range chs {
		b.Unsubscribe(ch)
	}
}

// TestBroadcaster_DroppedFrameObservable verifies that frames are silently
// dropped (not panicked) when the buffer is full. The actual slog.Debug
// message is not asserted here — the test confirms the non-blocking
// contract. (F-PERF-015)
func TestBroadcaster_DroppedFrameObservable(t *testing.T) {
	b := sse.NewBroadcaster()
	ch := b.Subscribe()
	defer b.Unsubscribe(ch)

	const overFill = 20 // more than subscriberBufferSize (10)
	for i := 0; i < overFill; i++ {
		// Must not block or panic even when buffer is full.
		require.NotPanics(t, func() { b.Broadcast([]byte("x")) })
	}
}
