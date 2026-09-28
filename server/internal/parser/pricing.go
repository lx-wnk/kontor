// Package parser re-exports cost estimation from the canonical pricing package.
// All logic lives in server/internal/pricing — this file keeps the parser package API
// unchanged so existing callers (merger, history) continue to compile.
package parser

import (
	"github.com/lx-wnk/kontor/sdk"
	"github.com/lx-wnk/kontor/server/internal/pricing"
)

// EstimateCost returns the estimated USD cost for a given token usage and model.
func EstimateCost(usage sdk.TokenUsage, model string) float64 {
	return pricing.EstimateCost(usage, model)
}

// EstimateCacheCreationCost returns only the cache-write cost component.
func EstimateCacheCreationCost(usage sdk.TokenUsage, model string) float64 {
	return pricing.EstimateCacheCreationCost(usage, model)
}

// EstimateCacheReadCost returns only the cache-read cost component.
func EstimateCacheReadCost(usage sdk.TokenUsage, model string) float64 {
	return pricing.EstimateCacheReadCost(usage, model)
}

// HasPricing reports whether the pricing table contains an exact entry for the
// given model.
func HasPricing(model string) bool {
	return pricing.HasPricing(model)
}
