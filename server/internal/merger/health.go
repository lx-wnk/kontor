package merger

import (
	"math"

	"github.com/lx-wnk/kontor/server/internal/parser"
)

// Health-score component weights (sum to 1.0). Fixed in source — not user-configurable.
const (
	weightSuccessRate = 0.40
	weightCacheHit    = 0.25
	weightErrorRate   = 0.25
	weightCostSpike   = 0.10

	// errorStateHardCap bounds the score when a recognised error state is present.
	errorStateHardCap = 30

	// neutralValue is the score given to a component that has no data yet
	// (e.g. no cache tokens) — neither rewarded nor penalised.
	neutralValue = 50.0
)

// ComputeHealthScore returns a composite 0–100 health score for a session.
//
// The score is a weighted sum of four normalised components:
//
//	0.40 × successRate + 0.25 × cacheHitPct + 0.25 × errorRateComponent + 0.10 × costSpikePenalty
//
// where errorRateComponent = (1 − toolErrorRate) × 100, forced to 0 on a
// qualitative ErrorState. Tool errors thus influence two components by design.
//
// then rounded and clamped to [0, 100]. If the session carries a recognised
// error state (quota/rate-limit/auth), the result is additionally capped at 30.
//
// costEstimate and costUnknown describe the session's cost (computed by the
// caller, since SessionData has no cost field); baselineCost is the injected
// 7-day per-session average. A zero or negative baseline disables the cost
// penalty (returns 100 for that component).
func ComputeHealthScore(session *parser.SessionData, costEstimate float64, costUnknown bool, baselineCost float64) int {
	if session == nil {
		return int(neutralValue)
	}

	// Decision 5: no session content yet -> neutral.
	if session.ConversationTurns == 0 {
		return int(neutralValue)
	}

	successRate := successRateComponent(session)
	cacheHitPct := cacheHitComponent(session)
	errorComponent := errorRateComponent(session)
	costPenalty := costSpikeComponent(costEstimate, costUnknown, baselineCost)

	raw := weightSuccessRate*successRate +
		weightCacheHit*cacheHitPct +
		weightErrorRate*errorComponent +
		weightCostSpike*costPenalty

	score := clampInt(int(math.Round(raw)))

	// Decision 8 / A.3 hard cap: any non-empty error state caps the score.
	if session.ErrorState != "" && score > errorStateHardCap {
		score = errorStateHardCap
	}

	return score
}

// successRateComponent returns (successfulCalls / totalToolCalls) × 100.
// No calls -> 100 (neutral). Corrupt data (errors > calls) -> 0.
func successRateComponent(session *parser.SessionData) float64 {
	total := 0
	for _, n := range session.ToolCounts {
		total += n
	}
	if total == 0 {
		return 100
	}
	toolErrors := 0
	if session.Meta != nil {
		toolErrors = session.Meta.ToolErrors
	}
	successful := total - toolErrors
	if successful < 0 {
		successful = 0
	}
	return (float64(successful) / float64(total)) * 100
}

// cacheHitComponent returns (CacheReadTokens / cacheTokens) × 100, or the
// neutral value 50 when no cache tokens are present.
func cacheHitComponent(session *parser.SessionData) float64 {
	read := session.TokenUsage.CacheReadTokens
	create := session.TokenUsage.CacheCreationTokens
	total := read + create
	if total <= 0 {
		return neutralValue
	}
	return (float64(read) / float64(total)) * 100
}

// errorRateComponent reflects the tool-error RATE: (1 − toolErrorRate) × 100,
// where toolErrorRate = clamp(ToolErrors / totalToolCalls, 0, 1) and is 0 when
// no tool calls have been made (neutral — no calls, no failures).
//
// Tool errors deliberately feed two components (this one AND successRate): a
// session failing most of its tool calls is dragged down by both, so tool
// failures matter more than either signal alone. A qualitative ErrorState
// (quota/rate-limit/auth) overrides the rate and forces this slot to 0 — the
// same flag also triggers the post-compute hard cap in ComputeHealthScore.
func errorRateComponent(session *parser.SessionData) float64 {
	if session.ErrorState != "" {
		return 0
	}
	total := 0
	for _, n := range session.ToolCounts {
		total += n
	}
	if total == 0 {
		return 100
	}
	toolErrors := 0
	if session.Meta != nil {
		toolErrors = session.Meta.ToolErrors
	}
	toolErrorRate := clampFloat(float64(toolErrors)/float64(total), 0, 1)
	return (1 - toolErrorRate) * 100
}

// costSpikeComponent returns the cost-spike penalty component (higher is better).
// No baseline or unknown cost -> 100 (no penalty). Otherwise the penalty curve
// is 100 − (ratio − 1) × 50, clamped to [0, 100].
func costSpikeComponent(costEstimate float64, costUnknown bool, baselineCost float64) float64 {
	if costUnknown || baselineCost <= 0 {
		return 100
	}
	ratio := costEstimate / baselineCost
	penalty := 100 - (ratio-1)*50
	return clampFloat(penalty, 0, 100)
}

func clampFloat(v, lo, hi float64) float64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func clampInt(v int) int {
	if v < 0 {
		return 0
	}
	if v > 100 {
		return 100
	}
	return v
}
