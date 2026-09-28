package mcp

import "time"

func NewNotifierWithHeartbeat(d time.Duration) *Notifier {
	n := NewNotifier()
	n.heartbeat = d
	return n
}
