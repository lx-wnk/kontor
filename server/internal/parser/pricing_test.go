package parser_test

import (
	"testing"

	"github.com/lx-wnk/kontor/sdk"
	"github.com/lx-wnk/kontor/server/internal/claudemodel"
	"github.com/lx-wnk/kontor/server/internal/parser"
	"github.com/stretchr/testify/require"
)

func TestEstimateCost(t *testing.T) {
	usage := sdk.TokenUsage{InputTokens: 1_000_000, OutputTokens: 1_000_000}
	got := parser.EstimateCost(usage, "claude-sonnet-4-6")
	// sonnet-4-6: input $3 + output $15 = $18 per 1M tokens each
	require.InDelta(t, 18.0, got, 0.001)
}

func TestEstimateCost_UnknownModel_UsesDefault(t *testing.T) {
	usage := sdk.TokenUsage{InputTokens: 1_000_000, OutputTokens: 1_000_000}
	got := parser.EstimateCost(usage, "claude-unknown-model")
	require.InDelta(t, parser.EstimateCost(usage, claudemodel.Latest(claudemodel.Sonnet)), got, 0.001)
}

func TestEstimateCacheCreationCost(t *testing.T) {
	usage := sdk.TokenUsage{CacheCreationTokens: 1_000_000}
	got := parser.EstimateCacheCreationCost(usage, "claude-sonnet-4-6")
	require.InDelta(t, 3.75, got, 0.001)
}

func TestEstimateCacheReadCost(t *testing.T) {
	usage := sdk.TokenUsage{CacheReadTokens: 1_000_000}
	got := parser.EstimateCacheReadCost(usage, "claude-sonnet-4-6")
	require.InDelta(t, 0.3, got, 0.001)
}
