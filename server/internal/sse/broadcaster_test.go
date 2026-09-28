package sse_test

import (
	"testing"
	"time"

	"github.com/lx-wnk/kontor/server/internal/sse"
	"github.com/stretchr/testify/require"
)

func TestBroadcaster_SubscribeReceivesData(t *testing.T) {
	b := sse.NewBroadcaster()
	ch := b.Subscribe()
	defer b.Unsubscribe(ch)

	b.Broadcast([]byte(`{"test":true}`))

	select {
	case data := <-ch:
		// Broadcaster formats fully-formed SSE data frames. (Issue 2b)
		require.Equal(t, "data: {\"test\":true}\n\n", string(data))
	case <-time.After(100 * time.Millisecond):
		t.Fatal("timeout: no data received")
	}
}

func TestBroadcaster_SlowSubscriberDropsFrame(t *testing.T) {
	b := sse.NewBroadcaster()
	ch := b.Subscribe()
	defer b.Unsubscribe(ch)

	// Fill buffer (capacity 10) without reading
	for i := 0; i < 15; i++ {
		b.Broadcast([]byte(`x`))
	}

	// Drain what's there — should be at most buffer capacity (10)
	drained := 0
	for len(ch) > 0 {
		<-ch
		drained++
	}
	require.LessOrEqual(t, drained, 10)
}

func TestBroadcaster_MultipleSubscribers(t *testing.T) {
	b := sse.NewBroadcaster()
	ch1 := b.Subscribe()
	ch2 := b.Subscribe()
	defer b.Unsubscribe(ch1)
	defer b.Unsubscribe(ch2)

	b.Broadcast([]byte("hello"))

	select {
	case data := <-ch1:
		require.Equal(t, "data: hello\n\n", string(data))
	case <-time.After(100 * time.Millisecond):
		t.Fatal("ch1 timeout")
	}
	select {
	case data := <-ch2:
		require.Equal(t, "data: hello\n\n", string(data))
	case <-time.After(100 * time.Millisecond):
		t.Fatal("ch2 timeout")
	}
}

func TestBroadcaster_UnsubscribeRemovesChannel(t *testing.T) {
	b := sse.NewBroadcaster()
	ch := b.Subscribe()
	b.Unsubscribe(ch)

	require.Equal(t, 0, b.SubscriberCount())
}

func TestBroadcaster_SubscriberCount(t *testing.T) {
	b := sse.NewBroadcaster()
	require.Equal(t, 0, b.SubscriberCount())
	ch1 := b.Subscribe()
	require.Equal(t, 1, b.SubscriberCount())
	ch2 := b.Subscribe()
	require.Equal(t, 2, b.SubscriberCount())
	b.Unsubscribe(ch1)
	require.Equal(t, 1, b.SubscriberCount())
	b.Unsubscribe(ch2)
	require.Equal(t, 0, b.SubscriberCount())
}

func TestBroadcaster_BroadcastToZeroSubscribers(t *testing.T) {
	b := sse.NewBroadcaster()
	// Must not panic
	b.Broadcast([]byte("no one listening"))
}

// TestBroadcaster_LastFrame_NilBeforeFirstBroadcast confirms LastFrame reports
// "no frame yet" (nil) on a fresh Broadcaster, so callers know to fall back to
// a scan (PERF-LOW2 cold-start case, e.g. a fresh install before the loop's
// first tick).
func TestBroadcaster_LastFrame_NilBeforeFirstBroadcast(t *testing.T) {
	b := sse.NewBroadcaster()
	require.Nil(t, b.LastFrame())
}

// TestBroadcaster_LastFrame_ReturnsLatestPayload confirms LastFrame returns the
// payload passed to the most recent Broadcast call — the raw payload, not the
// "data: ...\n\n" SSE-framed bytes — and that BroadcastComment (heartbeats)
// does not overwrite it. (PERF-LOW2)
func TestBroadcaster_LastFrame_ReturnsLatestPayload(t *testing.T) {
	b := sse.NewBroadcaster()

	b.Broadcast([]byte(`{"agents":[],"trend":[]}`))
	require.Equal(t, `{"agents":[],"trend":[]}`, string(b.LastFrame()))

	b.Broadcast([]byte(`{"agents":[{"id":"x"}],"trend":[]}`))
	require.Equal(t, `{"agents":[{"id":"x"}],"trend":[]}`, string(b.LastFrame()), "must reflect the latest Broadcast call")

	b.BroadcastComment([]byte("heartbeat"))
	require.Equal(t, `{"agents":[{"id":"x"}],"trend":[]}`, string(b.LastFrame()), "comment frames must not overwrite the last data frame")
}

// TestBroadcaster_UnsubscribeDuringBroadcast verifies that a concurrent
// Unsubscribe while send() is running does not panic with "send on closed channel".
// This is the regression test for Issue 2a (per-channel closed-flag with mutex).
func TestBroadcaster_UnsubscribeDuringBroadcast(t *testing.T) {
	const iterations = 500

	for i := 0; i < iterations; i++ {
		b := sse.NewBroadcaster()
		ch := b.Subscribe()

		ready := make(chan struct{})
		done := make(chan struct{})

		go func() {
			close(ready)
			// Rapidly unsubscribe while the main goroutine broadcasts.
			b.Unsubscribe(ch)
			close(done)
		}()

		<-ready
		// Must not panic even if Unsubscribe races with send().
		b.Broadcast([]byte("race"))

		<-done
	}
}
