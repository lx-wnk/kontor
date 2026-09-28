package parser

import "github.com/lx-wnk/kontor/sdk"

// Test-only accessors for the candidate-cache stat counter. Defined in an
// internal test file so the external parser_test package can assert how many
// full statSessionFiles directory scans the candidate cache actually triggers.

// StatSessionFilesCalls returns the number of full statSessionFiles scans so far.
func StatSessionFilesCalls() int64 { return statSessionFilesCalls.Load() }

// ResetStatSessionFilesCalls zeroes the scan counter and clears the candidate
// cache so each test starts from a clean slate.
func ResetStatSessionFilesCalls() {
	statSessionFilesCalls.Store(0)
	candidateCacheMu.Lock()
	candidateCache = make(map[string]*candidateCacheEntry)
	candidateCacheMu.Unlock()
}

// TokenUsageForFile exposes tokenUsageForFile (PERF-HOT1's incremental
// accumulator) to the external parser_test package for benchmarking.
func TokenUsageForFile(path string) (sdk.TokenUsage, error) {
	full, err := tokenUsageForFile(path)
	return full.TokenUsage, err
}

// ScanFullFileTokenUsage exposes the pre-PERF-HOT1 whole-file re-scan for a
// matched before/after benchmark comparison against TokenUsageForFile.
func ScanFullFileTokenUsage(path string) (sdk.TokenUsage, error) {
	full, err := scanFullFileTokenUsage(path)
	return full.TokenUsage, err
}

// ResetTokenOffsetCache clears the incremental token-scan offset cache so each
// benchmark/test starts from a clean slate.
func ResetTokenOffsetCache() {
	tokenOffsetCacheMu.Lock()
	tokenOffsetCache = make(map[string]*tokenOffsetCacheEntry)
	tokenOffsetCacheMu.Unlock()
}
