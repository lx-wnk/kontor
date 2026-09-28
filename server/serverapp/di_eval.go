package serverapp

import (
	"log/slog"

	"github.com/lx-wnk/kontor/server/internal/eval"
	"github.com/lx-wnk/kontor/server/internal/sse"
)

// evalOnDrift returns the onDrift callback wired to the task SSE broadcaster.
// Extracted so it can be tested without constructing the full DI graph.
func evalOnDrift(tb *sse.TaskBroadcaster) func([]eval.DriftFinding) {
	return func(findings []eval.DriftFinding) {
		for _, f := range findings {
			slog.Warn("eval: agent drift detected",
				"stage", f.Dim.Stage, "spawnerID", f.Dim.SpawnerID, "model", f.Dim.Model,
				"metric", f.MetricKey, "direction", f.Direction,
				"baseline", f.BaselineValue, "recent", f.RecentValue, "delta", f.Delta)
		}
		tb.Broadcast(sse.TaskEvent{Type: "eval_drift", Payload: len(findings)})
	}
}
