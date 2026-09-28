package mcp

import (
	"time"

	"github.com/lx-wnk/kontor/server/internal/sse"
)

var toolsListChanged = []byte(`{"jsonrpc":"2.0","method":"notifications/tools/list_changed"}`)

// Notifier fans out MCP notifications to the GET /api/mcp SSE streams.
type Notifier struct {
	b         *sse.Broadcaster
	heartbeat time.Duration
}

// NewNotifier constructs a Notifier.
func NewNotifier() *Notifier {
	return &Notifier{b: sse.NewBroadcaster(), heartbeat: sse.HeartbeatInterval}
}

// Subscribe returns a channel of SSE frames and the function that closes it.
func (n *Notifier) Subscribe() (<-chan []byte, func()) {
	ch := n.b.Subscribe()
	return ch, func() { n.b.Unsubscribe(ch) }
}

// NotifyToolsChanged broadcasts notifications/tools/list_changed; a full
// subscriber drops the frame instead of blocking the caller.
func (n *Notifier) NotifyToolsChanged() { n.b.Broadcast(toolsListChanged) }
