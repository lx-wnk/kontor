package sse

import "encoding/json"

const (
	CloudSessionCreated = "cloud_session_created"
	CloudSessionUpdated = "cloud_session_updated"
	CloudSessionDeleted = "cloud_session_deleted"
)

// CloudSessionEvent is a server-sent event for cloud-session CRUD. JSON keys MUST match the
// frontend CloudSessionEvent contract.
type CloudSessionEvent struct {
	Type           string `json:"type"`
	CloudSessionID string `json:"cloudSessionId"`
	Payload        any    `json:"payload,omitempty"`
}

// TypedBroadcaster wraps Broadcaster with typed event publishing.
type TypedBroadcaster[E any] struct {
	b *Broadcaster
}

// NewTypedBroadcaster creates a TypedBroadcaster backed by the given Broadcaster.
func NewTypedBroadcaster[E any](b *Broadcaster) *TypedBroadcaster[E] {
	return &TypedBroadcaster[E]{b: b}
}

// Broadcast serializes the event and sends it to all SSE subscribers.
// Marshalling errors are dropped — the next reconnect/poll delivers fresh state.
func (t *TypedBroadcaster[E]) Broadcast(event E) {
	data, err := json.Marshal(event)
	if err != nil {
		return
	}
	t.b.Broadcast(data)
}

// Subscribe returns a channel that receives raw SSE frames.
func (t *TypedBroadcaster[E]) Subscribe() chan []byte { return t.b.Subscribe() }

// Unsubscribe removes a subscriber channel.
func (t *TypedBroadcaster[E]) Unsubscribe(ch chan []byte) { t.b.Unsubscribe(ch) }
