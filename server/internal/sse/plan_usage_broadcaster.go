package sse

import "encoding/json"

// PlanUsageEvent is a server-sent event for plan-usage updates.
type PlanUsageEvent struct {
	Type    string `json:"type"`
	Payload any    `json:"payload,omitempty"`
}

// PlanUsageBroadcaster wraps Broadcaster with typed plan-usage-event publishing.
type PlanUsageBroadcaster struct {
	b *Broadcaster
}

// NewPlanUsageBroadcaster creates a PlanUsageBroadcaster backed by the given Broadcaster.
func NewPlanUsageBroadcaster(b *Broadcaster) *PlanUsageBroadcaster {
	return &PlanUsageBroadcaster{b: b}
}

// Broadcast serializes the event and sends it to all SSE subscribers.
func (p *PlanUsageBroadcaster) Broadcast(event PlanUsageEvent) {
	data, err := json.Marshal(event)
	if err != nil {
		return
	}
	p.b.Broadcast(data)
}

// Subscribe returns a channel that receives raw SSE frames for plan-usage events.
func (p *PlanUsageBroadcaster) Subscribe() chan []byte { return p.b.Subscribe() }

// Unsubscribe removes a subscriber channel.
func (p *PlanUsageBroadcaster) Unsubscribe(ch chan []byte) { p.b.Unsubscribe(ch) }
