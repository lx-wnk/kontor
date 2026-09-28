// Package agents provides HTTP handlers for the agent monitoring API.
package agents

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/lx-wnk/kontor/sdk"
	"github.com/lx-wnk/kontor/server/internal/sse"
)

// Frames received from the broadcaster are fully-formed SSE frames
// (produced by Broadcaster.Broadcast / BroadcastComment).
// Handlers must write them raw — no additional "data: " prefix.

// GetAgentsFn is the function signature for retrieving the current agent list.
// Defined as a named type to allow substitution in tests.
type GetAgentsFn func(ctx context.Context) ([]sdk.Agent, error)

// Handler handles /api/agents HTTP requests.
type Handler struct {
	getAgents   GetAgentsFn
	broadcaster *sse.Broadcaster
}

// NewHandler creates a Handler with the given dependencies.
func NewHandler(getAgents GetAgentsFn, broadcaster *sse.Broadcaster) *Handler {
	return &Handler{getAgents: getAgents, broadcaster: broadcaster}
}

// List handles GET /api/agents — returns the current agent list as JSON.
// A nil slice is normalized to an empty slice so the frontend always receives [].
// Serves the broadcaster's last frame (PERF-LOW2) instead of a fresh scan when
// available, so a burst of requests costs one scan per broadcast tick rather
// than one per request. Falls back to a scan before the loop's first tick.
func (h *Handler) List(w http.ResponseWriter, r *http.Request) error {
	agents, ok := h.agentsFromLastFrame()
	if !ok {
		var err error
		agents, err = h.getAgents(r.Context())
		if err != nil {
			return fmt.Errorf("get agents: %w", err)
		}
	}
	if agents == nil {
		agents = []sdk.Agent{}
	}
	w.Header().Set("Content-Type", "application/json")
	return json.NewEncoder(w).Encode(agents)
}

// agentsFromLastFrame extracts the agent list from the broadcaster's last
// frame. Returns ok=false if no frame has been broadcast yet or the frame
// cannot be decoded, signalling the caller to fall back to a fresh scan.
func (h *Handler) agentsFromLastFrame() ([]sdk.Agent, bool) {
	frame := h.broadcaster.LastFrame()
	if frame == nil {
		return nil, false
	}
	var envelope struct {
		Agents []sdk.Agent `json:"agents"`
	}
	if err := json.Unmarshal(frame, &envelope); err != nil {
		return nil, false
	}
	return envelope.Agents, true
}

// Stream handles GET /api/agents/stream — SSE endpoint.
// Sends the current agent list immediately, then on every broadcaster tick.
func (h *Handler) Stream(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "SSE not supported", http.StatusInternalServerError)
		return
	}

	sse.WriteHeaders(w)

	// Send current state immediately so client doesn't wait for first tick.
	// PERF-LOW2: reuse the broadcaster's last frame — already {agents, trend}
	// shaped — instead of a fresh scan. Falls back to a scan before the first tick.
	if frame := h.broadcaster.LastFrame(); frame != nil {
		fmt.Fprintf(w, "data: %s\n\n", frame)
		flusher.Flush()
	} else if agents, err := h.getAgents(r.Context()); err == nil {
		if data, err := json.Marshal(map[string]any{"agents": agents, "trend": []any{}}); err == nil {
			fmt.Fprintf(w, "data: %s\n\n", data)
			flusher.Flush()
		}
	}

	sub := h.broadcaster.Subscribe()
	defer h.broadcaster.Unsubscribe(sub)

	for {
		select {
		case data, ok := <-sub:
			if !ok {
				return
			}
			// data is a fully-formed SSE frame from the broadcaster — write raw.
			w.Write(data) //nolint:errcheck
			flusher.Flush()
		case <-r.Context().Done():
			return
		}
	}
}
