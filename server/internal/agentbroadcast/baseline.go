// server/internal/agentbroadcast/baseline.go
package agentbroadcast

import (
	"context"
	"log/slog"
	"time"

	"github.com/lx-wnk/kontor/server/internal/db/rawrepo"
)

// baselineWindow is the look-back period for the per-session cost baseline.
const baselineWindow = 7 * 24 * time.Hour

// NewCostBaselineProvider returns a BaselineProvider that computes the average
// per-session cost over the past 7 days from the agent_cost_trends table.
//
// A nil repo (no database) yields a provider that always returns 0, disabling
// the health score's cost-spike penalty. Query failures and an empty window
// likewise yield 0 — the cost component is then neutral (no penalty), which is
// the documented behaviour when no baseline is available.
//
// agentbroadcast is a peer of merger and may freely import db/rawrepo, so this
// keeps the merger package free of any db dependency (Go layer direction).
func NewCostBaselineProvider(repo rawrepo.AnalyticsRepo) BaselineProvider {
	return func(ctx context.Context) float64 {
		if repo == nil {
			return 0
		}
		since := time.Now().Add(-baselineWindow)
		samples, err := repo.GetCostSince(ctx, since)
		if err != nil {
			slog.Warn("agent health: cost baseline query failed", "err", err)
			return 0
		}
		if len(samples) == 0 {
			return 0
		}
		var total float64
		for _, s := range samples {
			total += s.CostUSD
		}
		return total / float64(len(samples))
	}
}
