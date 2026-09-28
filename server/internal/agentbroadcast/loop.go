// server/internal/agentbroadcast/loop.go
package agentbroadcast

import (
	"context"
	"encoding/json"
	"hash/fnv"
	"log/slog"
	"time"

	sdk "github.com/lx-wnk/kontor/sdk"
	"github.com/lx-wnk/kontor/server/internal/merger"
	"github.com/lx-wnk/kontor/server/internal/sse"
)

// emptyTrend is a pre-allocated empty slice used in every broadcast frame to
// avoid a per-tick heap allocation for the "trend" field. (F-PERF-014)
var emptyTrend = []any{}

// emptyCapabilityDecisions is the pre-allocated empty slice substituted when
// no CapabilityDecisionProvider is configured, mirroring emptyTrend.
// (F-PERF-014)
var emptyCapabilityDecisions = []sdk.PendingCapabilityDecision{}

// broadcastFrame is the JSON envelope expected by the SSE client.
// Typed struct avoids reflection-based marshaling of map[string]any.
type broadcastFrame struct {
	Agents                     []sdk.Agent                     `json:"agents"`
	Trend                      []any                           `json:"trend"`
	PendingCapabilityDecisions []sdk.PendingCapabilityDecision `json:"pendingCapabilityDecisions"`
}

// MarshalFrame renders one SSE envelope. Exported because the scan loop is not
// the only producer — a hook event or a new capability ask pushes a frame
// between ticks — and a second producer building the envelope by hand drops
// whichever field it was written before.
func MarshalFrame(agents []sdk.Agent, decisions []sdk.PendingCapabilityDecision) ([]byte, error) {
	if agents == nil {
		agents = []sdk.Agent{}
	}
	if decisions == nil {
		decisions = emptyCapabilityDecisions
	}
	return json.Marshal(broadcastFrame{
		Agents:                     agents,
		Trend:                      emptyTrend,
		PendingCapabilityDecisions: decisions,
	})
}

// BaselineProvider returns the average per-session cost over the past 7 days
// in USD, used by the agent health score's cost-spike component. A nil provider
// (or one that returns 0) disables the cost penalty. It is called once per scan
// tick.
type BaselineProvider func(ctx context.Context) float64

// CapabilityDecisionProvider returns the capability decisions currently
// awaiting a human at a server enforcement point, included in each broadcast
// frame for the SPA's approval queue. A nil provider (e.g. a server built
// without an asker, auth mode none) yields an empty list, never a panic. It
// is called once per scan tick.
type CapabilityDecisionProvider func(ctx context.Context) []sdk.PendingCapabilityDecision

// capabilityDecisionsOrEmpty resolves provider's result, substituting
// emptyCapabilityDecisions when provider is nil or returns nil so the frame
// always marshals "[]", never "null".
func capabilityDecisionsOrEmpty(ctx context.Context, provider CapabilityDecisionProvider) []sdk.PendingCapabilityDecision {
	if provider == nil {
		return emptyCapabilityDecisions
	}
	if d := provider(ctx); d != nil {
		return d
	}
	return emptyCapabilityDecisions
}

// RunOptions is the named input for Run. Named rather than positional because
// the call has more than four parameters, which is where this codebase's
// convention switches (see repo.CreateGrantInput).
type RunOptions struct {
	Merger              *merger.Merger
	Broadcaster         *sse.Broadcaster
	Interval            time.Duration
	Baseline            BaselineProvider
	Enricher            merger.Enricher
	CapabilityDecisions CapabilityDecisionProvider
}

// Run starts a ticker loop that scans agents every interval and broadcasts
// the JSON result to all SSE subscribers. Stops when ctx is cancelled.
//
// opts.Baseline, when non-nil, is consulted once per tick to derive the cost
// baseline injected into m.GetAgents for the health score. It may be nil
// (no baseline → no cost penalty), which preserves correct behaviour.
//
// opts.Enricher, when non-nil, annotates each scanned agent with its linked
// pipeline task (read-only SQLite crossing). It may be nil (no enrichment),
// which leaves PipelineTaskID/Title empty — the same as before the crossing
// existed.
//
// opts.CapabilityDecisions, when non-nil, is consulted once per tick to
// populate the frame's pending-decisions list. It may be nil.
//
// Optimisations applied:
//   - F-PERF-001: Skip scan entirely when SubscriberCount == 0.
//   - F-PERF-006: Hash-dedupe frames with FNV-64a; send a heartbeat comment
//     every 30 s so reverse-proxies do not close idle connections.
//   - F-PERF-014: emptyTrend is a package-level var, not a per-tick literal.
func Run(ctx context.Context, opts RunOptions) {
	ticker := time.NewTicker(opts.Interval)
	defer ticker.Stop()

	heartbeat := time.NewTicker(sse.HeartbeatInterval)
	defer heartbeat.Stop()

	var lastHash uint64
	var lastBroadcast time.Time

	for {
		select {
		case <-ticker.C:
			// F-PERF-001: no subscribers — skip the expensive scan entirely.
			if opts.Broadcaster.SubscriberCount() == 0 {
				continue
			}

			var baselineCost float64
			if opts.Baseline != nil {
				baselineCost = opts.Baseline(ctx)
			}

			agents, err := opts.Merger.GetAgents(ctx, merger.GetAgentsOpts{
				BaselinePerSessionCostUSD: baselineCost,
				Enricher:                  opts.Enricher,
			})
			if err != nil {
				slog.Error("agent scan failed", "err", err)
				continue
			}

			data, err := MarshalFrame(agents, capabilityDecisionsOrEmpty(ctx, opts.CapabilityDecisions))
			if err != nil {
				slog.Error("agent marshal failed", "err", err)
				continue
			}

			// F-PERF-006: hash-dedupe — skip broadcast if payload is identical
			// to the last one sent.
			h := fnv64a(data)
			if h == lastHash {
				continue
			}

			lastHash = h
			lastBroadcast = time.Now()
			opts.Broadcaster.Broadcast(data)

		case t := <-heartbeat.C:
			// F-PERF-006: send a heartbeat comment frame so proxies/load-balancers
			// do not close idle SSE connections when there is no data change.
			if opts.Broadcaster.SubscriberCount() == 0 {
				continue
			}
			if t.Sub(lastBroadcast) >= sse.HeartbeatInterval {
				opts.Broadcaster.BroadcastComment(sse.HeartbeatComment)
				lastBroadcast = t
			}

		case <-ctx.Done():
			return
		}
	}
}

// fnv64a returns the FNV-64a hash of b. Using stdlib hash/fnv avoids
// introducing a new dependency. (F-PERF-006)
func fnv64a(b []byte) uint64 {
	h := fnv.New64a()
	_, _ = h.Write(b)
	return h.Sum64()
}
