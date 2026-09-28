// Package sse provides an SSE broadcaster for pushing real-time updates to clients.
package sse

import (
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
)

const subscriberBufferSize = 10

// subscriber wraps a channel with a closed flag guarded by its own mutex.
// The per-subscriber mutex allows Unsubscribe to mark the channel closed
// while send() concurrently holds an RLock on the broadcaster — avoiding
// the "send on closed channel" panic that arises when snapshot() releases
// the broadcaster RLock and Unsubscribe closes the channel before send()
// completes. (Issue 2a)
type subscriber struct {
	ch     chan []byte
	mu     sync.Mutex
	closed bool
}

// Broadcaster distributes byte slices to all active SSE subscribers.
// Sends are non-blocking: if a subscriber's buffer is full, the frame is
// dropped and a debug log entry is emitted (F-PERF-015).
//
// Frames written into the channel are fully-formed SSE frames:
//   - Data events:    "data: <payload>\n\n"
//   - Comment frames: ": <text>\n\n"
//
// Handlers must write the raw bytes to the response without adding additional
// framing. (Issue 2b)
type Broadcaster struct {
	mu          sync.RWMutex
	subscribers map[*subscriber]struct{}
	// lastFrame holds the most recent data payload passed to Broadcast (the raw
	// payload, not the "data: ...\n\n" SSE-framed bytes), so read paths that would
	// otherwise re-scan on every request/reconnect (PERF-LOW2) can serve it
	// instead. nil until the first Broadcast call. Comment frames (heartbeats)
	// do not update it. atomic.Pointer avoids lock contention with Subscribe/
	// Unsubscribe/snapshot, which guard the subscriber map separately.
	lastFrame atomic.Pointer[[]byte]
}

// NewBroadcaster creates a ready-to-use Broadcaster.
func NewBroadcaster() *Broadcaster {
	return &Broadcaster{
		subscribers: make(map[*subscriber]struct{}),
	}
}

// Subscribe returns a buffered channel that will receive fully-formed SSE frames.
// Call Unsubscribe when done to avoid goroutine leaks.
// The returned channel is the underlying channel of the internal subscriber
// wrapper; pass it back to Unsubscribe to identify the correct subscriber.
func (b *Broadcaster) Subscribe() chan []byte {
	s := &subscriber{ch: make(chan []byte, subscriberBufferSize)}
	b.mu.Lock()
	b.subscribers[s] = struct{}{}
	b.mu.Unlock()
	return s.ch
}

// Unsubscribe removes a subscriber and closes its channel.
// It is safe to call concurrently with ongoing broadcasts — the per-subscriber
// mutex guarantees that send() never writes to a closed channel.
func (b *Broadcaster) Unsubscribe(ch chan []byte) {
	b.mu.Lock()
	var found *subscriber
	for s := range b.subscribers {
		if s.ch == ch {
			found = s
			break
		}
	}
	if found != nil {
		delete(b.subscribers, found)
	}
	b.mu.Unlock()

	if found == nil {
		return
	}

	found.mu.Lock()
	found.closed = true
	close(found.ch)
	found.mu.Unlock()
}

// snapshot returns a point-in-time copy of all subscriber wrappers.
// Callers can iterate the slice without holding the broadcaster lock. (F-PERF-018)
func (b *Broadcaster) snapshot() []*subscriber {
	b.mu.RLock()
	subs := make([]*subscriber, 0, len(b.subscribers))
	for s := range b.subscribers {
		subs = append(subs, s)
	}
	b.mu.RUnlock()
	return subs
}

// send attempts a non-blocking send of data to s. It holds the per-subscriber
// mutex while checking the closed flag and sending, so it is race-free against
// a concurrent Unsubscribe. If the channel buffer is full the frame is dropped
// and a debug message is logged. (F-PERF-015, Issue 2a)
func send(s *subscriber, data []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	select {
	case s.ch <- data:
	default:
		// subscriber buffer full — drop frame and record it for observability.
		slog.Debug("sse: dropped frame", "buffered", len(s.ch), "cap", cap(s.ch))
	}
}

// Broadcast formats payload as a fully-formed SSE data frame
// ("data: <payload>\n\n") and sends it to all subscribers.
// Non-blocking: frames are dropped for slow consumers rather than
// blocking the broadcaster goroutine.
// Subscribers are snapshotted under RLock; iteration happens without lock. (F-PERF-018)
func (b *Broadcaster) Broadcast(payload []byte) {
	b.lastFrame.Store(&payload)
	frame := fmt.Appendf(nil, "data: %s\n\n", payload)
	for _, s := range b.snapshot() {
		send(s, frame)
	}
}

// LastFrame returns the payload passed to the most recent Broadcast call, or
// nil if Broadcast has never been called (e.g. a fresh install before the
// broadcast loop's first tick). Callers must fall back to a fresh scan when
// it returns nil. Safe for concurrent use.
func (b *Broadcaster) LastFrame() []byte {
	p := b.lastFrame.Load()
	if p == nil {
		return nil
	}
	return *p
}

// BroadcastComment sends a fully-formed SSE comment frame (": <text>\n\n") to
// all subscribers. Comment frames are used as heartbeats to prevent idle
// connections from being closed by reverse-proxies. (F-PERF-006)
func (b *Broadcaster) BroadcastComment(text []byte) {
	frame := CommentFrame(text)
	for _, s := range b.snapshot() {
		send(s, frame)
	}
}

// SubscriberCount returns the number of active subscribers.
func (b *Broadcaster) SubscriberCount() int {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return len(b.subscribers)
}

// CommentFrame renders text as an SSE comment frame (": <text>\n\n").
func CommentFrame(text []byte) []byte {
	frame := make([]byte, 0, 2+len(text)+2)
	frame = append(frame, ':', ' ')
	frame = append(frame, text...)
	return append(frame, '\n', '\n')
}
