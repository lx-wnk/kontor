package merger_test

import (
	"testing"
	"time"

	sdk "github.com/lx-wnk/kontor/sdk"
	"github.com/lx-wnk/kontor/server/internal/merger"
	"github.com/stretchr/testify/require"
)

// TestCalculateStatus_ExactBoundaries verifies the exact threshold boundaries.
// active < 30s, waiting < 5min (300s), idle >= 5min.
func TestCalculateStatus_ExactBoundaries(t *testing.T) {
	now := time.Now()
	tests := []struct {
		name         string
		lastActivity time.Time
		want         sdk.AgentStatus
	}{
		// Active threshold: < 30s
		{"30s ago is waiting not active", now.Add(-30 * time.Second), sdk.AgentStatusWaiting},
		{"31s ago is waiting", now.Add(-31 * time.Second), sdk.AgentStatusWaiting},
		{"1s ago is active", now.Add(-1 * time.Second), sdk.AgentStatusActive},
		{"just now is active", now, sdk.AgentStatusActive},
		// Waiting threshold: < 5min (300s)
		{"5min ago is idle", now.Add(-5 * time.Minute), sdk.AgentStatusIdle},
		{"299s ago is waiting", now.Add(-299 * time.Second), sdk.AgentStatusWaiting},
		{"300s ago is idle", now.Add(-300 * time.Second), sdk.AgentStatusIdle},
		// Far past should be idle.
		{"1 hour ago is idle", now.Add(-1 * time.Hour), sdk.AgentStatusIdle},
		{"1 day ago is idle", now.Add(-24 * time.Hour), sdk.AgentStatusIdle},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := merger.CalculateStatus(tt.lastActivity)
			require.Equal(t, tt.want, got)
		})
	}
}

// TestCalculateStatus_FutureTimestamp verifies that a future timestamp
// (e.g. clock skew) is treated as active.
func TestCalculateStatus_FutureTimestamp(t *testing.T) {
	future := time.Now().Add(5 * time.Second)
	got := merger.CalculateStatus(future)
	require.Equal(t, sdk.AgentStatusActive, got)
}

// TestEstimateCostForProvider verifies the cost-gate logic for the various
// provider/model combinations.
func TestEstimateCostForProvider(t *testing.T) {
	usage := sdk.TokenUsage{InputTokens: 1_000_000, OutputTokens: 1_000_000}

	t.Run("claude with known model uses pricing", func(t *testing.T) {
		got := merger.EstimateCostForProvider(sdk.ProviderClaude, usage, "claude-sonnet-4-6")
		require.False(t, got.Unknown)
		require.Greater(t, got.Total, 0.0)
	})

	t.Run("claude with unknown model falls back to default pricing", func(t *testing.T) {
		// Sentinel "unknown" Claude models silently default to sonnet pricing —
		// pre-existing behaviour preserved for backwards compatibility.
		got := merger.EstimateCostForProvider(sdk.ProviderClaude, usage, "claude-future-9")
		require.False(t, got.Unknown)
		require.Greater(t, got.Total, 0.0)
	})

	t.Run("codex with unknown model is CostUnknown", func(t *testing.T) {
		// A non-Claude model absent from the pricing table → cost is gated as unknown.
		// (Known Codex models like gpt-5 are now priced — see the pricing table.)
		got := merger.EstimateCostForProvider(sdk.ProviderCodex, usage, "codex-unpriced-model")
		require.True(t, got.Unknown)
		require.Equal(t, 0.0, got.Total)
		require.Equal(t, 0.0, got.CacheCreate)
		require.Equal(t, 0.0, got.CacheRead)
	})

	t.Run("gemini with unknown model is CostUnknown", func(t *testing.T) {
		got := merger.EstimateCostForProvider(sdk.ProviderGemini, usage, "gemini-unpriced-model")
		require.True(t, got.Unknown)
		require.Equal(t, 0.0, got.Total)
	})

	t.Run("empty provider defaults to claude pricing", func(t *testing.T) {
		got := merger.EstimateCostForProvider("", usage, "claude-sonnet-4-6")
		require.False(t, got.Unknown)
		require.Greater(t, got.Total, 0.0)
	})

	t.Run("codex with a known claude model name still prices", func(t *testing.T) {
		// Defensive: if a non-Claude session somehow records a known model name,
		// the table lookup wins — we don't artificially gate it.
		got := merger.EstimateCostForProvider(sdk.ProviderCodex, usage, "claude-sonnet-4-6")
		require.False(t, got.Unknown)
		require.Greater(t, got.Total, 0.0)
	})
}
