package merger_test

import (
	"testing"

	"github.com/lx-wnk/kontor/sdk"
	"github.com/lx-wnk/kontor/server/internal/merger"
	"github.com/lx-wnk/kontor/server/internal/parser"
	"github.com/stretchr/testify/require"
)

// toolCounts builds a ToolCounts map whose values sum to total.
func toolCounts(total int) map[string]int {
	if total <= 0 {
		return map[string]int{}
	}
	return map[string]int{"Bash": total}
}

// TestComputeHealthScore asserts EXACT deterministic scores. Loose min/max
// spans are intentionally avoided: a transposed weight or sign error must fail
// the test rather than slip through a wide range. Each case documents its
// component-by-component derivation.
func TestComputeHealthScore(t *testing.T) {
	tests := []struct {
		name         string
		session      *parser.SessionData
		costEstimate float64
		costUnknown  bool
		baseline     float64
		want         int
	}{
		{
			// ConversationTurns == 0 -> neutral short-circuit, no component math.
			name: "zero_turns_no_data",
			session: &parser.SessionData{
				ConversationTurns: 0,
				ToolCounts:        toolCounts(0),
			},
			want: 50,
		},
		{
			// successRate 100, cache 80 (80 read / 100 total), errorComp 100
			// (0/10 errors), cost 100 (ratio 1.0 at baseline).
			// 0.40*100 + 0.25*80 + 0.25*100 + 0.10*100 = 40+20+25+10 = 95.
			name: "perfect_session",
			session: &parser.SessionData{
				ConversationTurns: 5,
				ToolCounts:        toolCounts(10),
				TokenUsage:        sdk.TokenUsage{CacheReadTokens: 80, CacheCreationTokens: 20},
				Meta:              &sdk.SessionMeta{ToolErrors: 0},
			},
			costEstimate: 1.0,
			baseline:     1.0,
			want:         95,
		},
		{
			// Otherwise perfect (raw 70) but quota error -> hard cap min(70,30)=30.
			name: "quota_error_hard_cap",
			session: &parser.SessionData{
				ConversationTurns: 5,
				ToolCounts:        toolCounts(10),
				TokenUsage:        sdk.TokenUsage{CacheReadTokens: 80, CacheCreationTokens: 20},
				Meta:              &sdk.SessionMeta{ToolErrors: 0},
				ErrorState:        sdk.ErrorStateQuotaExhausted,
			},
			costEstimate: 1.0,
			baseline:     1.0,
			want:         30,
		},
		{
			// successRate 50 (5/10 ok), cache 50 (neutral, no cache),
			// errorComp 50 ((1-0.5)*100), cost 100 (no baseline).
			// 0.40*50 + 0.25*50 + 0.25*50 + 0.10*100 = 20+12.5+12.5+10 = 55.
			name: "high_tool_error_rate",
			session: &parser.SessionData{
				ConversationTurns: 5,
				ToolCounts:        toolCounts(10),
				Meta:              &sdk.SessionMeta{ToolErrors: 5},
			},
			want: 55,
		},
		{
			// 100% tool failure: successRate 0 (0/10 ok), cache 50 (neutral),
			// errorComp 0 ((1-1.0)*100), cost 100 (no baseline).
			// 0.40*0 + 0.25*50 + 0.25*0 + 0.10*100 = 0+12.5+0+10 = 22.5 -> 23.
			// This is the case the recalibration exists for: with no qualitative
			// ErrorState the old binary slot kept this amber; now it is RED (<40).
			name: "all_tools_fail",
			session: &parser.SessionData{
				ConversationTurns: 5,
				ToolCounts:        toolCounts(10),
				Meta:              &sdk.SessionMeta{ToolErrors: 10},
			},
			want: 23,
		},
		{
			// successRate 100, cache 50 (neutral), errorComp 100 (0/10), cost 100.
			// 0.40*100 + 0.25*50 + 0.25*100 + 0.10*100 = 40+12.5+25+10 = 87.5 -> 88.
			name: "no_cache_usage",
			session: &parser.SessionData{
				ConversationTurns: 5,
				ToolCounts:        toolCounts(10),
				Meta:              &sdk.SessionMeta{ToolErrors: 0},
			},
			want: 88,
		},
		{
			// successRate 100, cache 100, errorComp 100, cost 50 (ratio 2.0).
			// 0.40*100 + 0.25*100 + 0.25*100 + 0.10*50 = 40+25+25+5 = 95.
			name: "cost_spike_2x",
			session: &parser.SessionData{
				ConversationTurns: 5,
				ToolCounts:        toolCounts(10),
				TokenUsage:        sdk.TokenUsage{CacheReadTokens: 100, CacheCreationTokens: 0},
				Meta:              &sdk.SessionMeta{ToolErrors: 0},
			},
			costEstimate: 2.0,
			baseline:     1.0,
			want:         95,
		},
		{
			// successRate 100, cache 100, errorComp 100, cost 0 (ratio 3.0).
			// 0.40*100 + 0.25*100 + 0.25*100 + 0.10*0 = 40+25+25+0 = 90.
			name: "cost_spike_3x_plus",
			session: &parser.SessionData{
				ConversationTurns: 5,
				ToolCounts:        toolCounts(10),
				TokenUsage:        sdk.TokenUsage{CacheReadTokens: 100, CacheCreationTokens: 0},
				Meta:              &sdk.SessionMeta{ToolErrors: 0},
			},
			costEstimate: 3.0,
			baseline:     1.0,
			want:         90,
		},
		{
			// Perfect session, baseline 0 -> cost 100 (no penalty).
			// 0.40*100 + 0.25*80 + 0.25*100 + 0.10*100 = 95.
			name: "no_baseline_no_penalty",
			session: &parser.SessionData{
				ConversationTurns: 5,
				ToolCounts:        toolCounts(10),
				TokenUsage:        sdk.TokenUsage{CacheReadTokens: 80, CacheCreationTokens: 20},
				Meta:              &sdk.SessionMeta{ToolErrors: 0},
			},
			costEstimate: 5.0,
			baseline:     0,
			want:         95,
		},
		{
			// CostUnknown -> cost 100 even with baseline set. Same as perfect: 95.
			name: "cost_unknown",
			session: &parser.SessionData{
				ConversationTurns: 5,
				ToolCounts:        toolCounts(10),
				TokenUsage:        sdk.TokenUsage{CacheReadTokens: 80, CacheCreationTokens: 20},
				Meta:              &sdk.SessionMeta{ToolErrors: 0},
			},
			costEstimate: 0,
			costUnknown:  true,
			baseline:     1.0,
			want:         95,
		},
		{
			// Meta nil -> ToolErrors treated as 0 -> successRate 100, errorComp 100.
			// cache 50 (neutral), cost 100 (no baseline). 87.5 -> 88.
			name: "nil_meta",
			session: &parser.SessionData{
				ConversationTurns: 5,
				ToolCounts:        toolCounts(10),
				Meta:              nil,
			},
			want: 88,
		},
		{
			// All components max, cost ratio 0.5 -> penalty clamps to 100.
			// 0.40*100 + 0.25*100 + 0.25*100 + 0.10*100 = 100.
			name: "clamp_above_100",
			session: &parser.SessionData{
				ConversationTurns: 5,
				ToolCounts:        toolCounts(10),
				TokenUsage:        sdk.TokenUsage{CacheReadTokens: 100, CacheCreationTokens: 0},
				Meta:              &sdk.SessionMeta{ToolErrors: 0},
			},
			costEstimate: 0.5,
			baseline:     1.0,
			want:         100,
		},
		{
			// successRate 0 (errors > calls), cache 0 (create only),
			// errorComp 0 (ErrorState set), cost 0 (ratio 10). raw 0, hard cap 0.
			name: "clamp_below_0",
			session: &parser.SessionData{
				ConversationTurns: 5,
				ToolCounts:        toolCounts(10),
				TokenUsage:        sdk.TokenUsage{CacheReadTokens: 0, CacheCreationTokens: 100},
				Meta:              &sdk.SessionMeta{ToolErrors: 100},
				ErrorState:        sdk.ErrorStateRateLimited,
			},
			costEstimate: 10.0,
			baseline:     1.0,
			want:         0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := merger.ComputeHealthScore(tt.session, tt.costEstimate, tt.costUnknown, tt.baseline)
			require.Equal(t, tt.want, got, "exact health score mismatch")
		})
	}
}

// TestComputeHealthScore_AllToolsFailIsRed pins the recalibration's headline
// guarantee: a session failing 100% of its tool calls (no qualitative
// ErrorState) lands in the RED chip tier (< 40) instead of staying amber.
func TestComputeHealthScore_AllToolsFailIsRed(t *testing.T) {
	session := &parser.SessionData{
		ConversationTurns: 5,
		ToolCounts:        toolCounts(10),
		Meta:              &sdk.SessionMeta{ToolErrors: 10},
	}
	got := merger.ComputeHealthScore(session, 0, false, 0)
	require.Equal(t, 23, got)
	require.Less(t, got, 40, "100%-tool-failure session must render RED")
}
