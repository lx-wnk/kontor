// Package pricing provides per-model cost estimation for Claude API token usage.
// It is intentionally kept free of parser, pipeline, and db dependencies so it
// can be imported from any layer without creating a cycle.
package pricing

import (
	"regexp"

	"github.com/lx-wnk/kontor/sdk"
	"github.com/lx-wnk/kontor/server/internal/claudemodel"
)

// modelPricingEntry holds per-million-token USD prices for a single model.
type modelPricingEntry struct {
	Input, Output, CacheRead, CacheCreate float64
}

// modelPricing stores per-million-token USD prices for known Claude models.
// Claude rates: platform.claude.com/docs/en/about-claude/pricing, read
// 2026-09-16. CacheCreate is the 5-minute cache write.
var modelPricing = map[string]modelPricingEntry{
	"claude-fable-5-1":  {10, 50, 0.25, 12.5},
	"claude-fable-5":    {10, 50, 1, 12.5},
	"claude-opus-5":     {5, 25, 0.5, 6.25},
	"claude-opus-4-8":   {5, 25, 0.5, 6.25},
	"claude-opus-4-7":   {5, 25, 0.5, 6.25},
	"claude-opus-4-6":   {5, 25, 0.5, 6.25},
	"claude-opus-4-5":   {5, 25, 0.5, 6.25},
	"claude-opus-4-1":   {15, 75, 1.5, 18.75},
	"claude-opus-4-0":   {15, 75, 1.5, 18.75},
	"claude-sonnet-5":   {2, 10, 0.2, 2.5},
	"claude-sonnet-4-6": {3, 15, 0.3, 3.75},
	"claude-sonnet-4-5": {3, 15, 0.3, 3.75},
	"claude-haiku-4-5":  {1, 5, 0.1, 1.25},

	// OpenAI — source: platform.openai.com/pricing (verify before releasing).
	// Cache read = 50% of input price per OpenAI caching docs; cache write = $0.
	"gpt-5":       {5, 20, 2.5, 0},
	"gpt-5-codex": {5, 20, 2.5, 0}, // fixture model; same rate until a separate entry is published

	// Google Gemini — source: ai.google.dev/pricing (verify before releasing).
	// Context caching prices omitted (tier-dependent); set once confirmed.
	"gemini-2.5-pro":   {1.25, 10, 0, 0},
	"gemini-2.5-flash": {0.075, 0.30, 0, 0},
}

// defaultModel prices an unrecognised model: the newest Sonnet.
var defaultModel = claudemodel.Latest(claudemodel.Sonnet)

// datedSuffix matches the snapshot date some IDs carry, e.g. the -20251001 in
// claude-haiku-4-5-20251001, which is priced like its alias.
var datedSuffix = regexp.MustCompile(`-\d{8}$`)

// lookupModel returns the pricing entry for the given model, falling back to
// defaultModel when the model string is not recognised.
func lookupModel(model string) modelPricingEntry {
	p, ok := modelPricing[datedSuffix.ReplaceAllString(model, "")]
	if !ok {
		p = modelPricing[defaultModel]
	}
	return p
}

// HasPricing reports whether the pricing table contains an exact entry for the
// given model. Used by callers that need to distinguish "we have no idea what
// this costs" (e.g. Codex / Gemini models) from "Claude model, default-priced".
func HasPricing(model string) bool {
	_, ok := modelPricing[datedSuffix.ReplaceAllString(model, "")]
	return ok
}

// EstimateCost returns the estimated USD cost for a given token usage and model.
func EstimateCost(usage sdk.TokenUsage, model string) float64 {
	p := lookupModel(model)
	const m = 1_000_000.0
	return float64(usage.InputTokens)*p.Input/m +
		float64(usage.OutputTokens)*p.Output/m +
		float64(usage.CacheReadTokens)*p.CacheRead/m +
		float64(usage.CacheCreationTokens)*p.CacheCreate/m
}

// EstimateCacheCreationCost returns only the cache-write cost component.
func EstimateCacheCreationCost(usage sdk.TokenUsage, model string) float64 {
	p := lookupModel(model)
	return float64(usage.CacheCreationTokens) * p.CacheCreate / 1_000_000.0
}

// EstimateCacheReadCost returns only the cache-read cost component.
func EstimateCacheReadCost(usage sdk.TokenUsage, model string) float64 {
	p := lookupModel(model)
	return float64(usage.CacheReadTokens) * p.CacheRead / 1_000_000.0
}
