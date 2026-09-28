package parser

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lx-wnk/kontor/sdk"
	"github.com/stretchr/testify/require"
)

// incrementalLineTemplate is a single assistant JSONL line with a fixed,
// known usage payload — expectedUsage(n) is n multiples of these values.
const incrementalLineTemplate = `{"type":"assistant","timestamp":"2025-01-15T10:30:00.000Z","message":{"role":"assistant","model":"claude-sonnet-4-6","usage":{"input_tokens":100,"output_tokens":50,"cache_creation_input_tokens":10,"cache_read_input_tokens":5}}}` + "\n"

func expectedUsage(n int) sdk.TokenUsage {
	return sdk.TokenUsage{
		InputTokens:         100 * n,
		OutputTokens:        50 * n,
		CacheCreationTokens: 10 * n,
		CacheReadTokens:     5 * n,
	}
}

func resetTokenOffsetCache(t *testing.T) {
	t.Helper()
	tokenOffsetCacheMu.Lock()
	tokenOffsetCache = make(map[string]*tokenOffsetCacheEntry)
	tokenOffsetCacheMu.Unlock()
}

func writeIncrementalSession(t *testing.T, dir string, n int) string {
	t.Helper()
	path := filepath.Join(dir, "session.jsonl")
	f, err := os.Create(path)
	require.NoError(t, err)
	defer f.Close() //nolint:errcheck
	appendIncrementalLines(t, f, n)
	return path
}

func appendIncrementalLines(t *testing.T, f *os.File, n int) {
	t.Helper()
	_, err := f.WriteString(strings.Repeat(incrementalLineTemplate, n))
	require.NoError(t, err)
	require.NoError(t, f.Sync())
}

// TestTokenUsageForFile_IncrementalMatchesFullScan verifies that summing an
// appended region via the offset cache produces the exact same total a full
// re-scan of the whole file would — the correctness invariant the whole
// incremental design depends on (per-message usage is append-only/monotonic).
func TestTokenUsageForFile_IncrementalMatchesFullScan(t *testing.T) {
	resetTokenOffsetCache(t)
	dir := t.TempDir()
	path := writeIncrementalSession(t, dir, 20)

	first, err := tokenUsageForFile(path)
	require.NoError(t, err)
	require.Equal(t, expectedUsage(20), first.TokenUsage)

	info, err := os.Stat(path)
	require.NoError(t, err)

	tokenOffsetCacheMu.Lock()
	entry := tokenOffsetCache[path]
	tokenOffsetCacheMu.Unlock()
	require.NotNil(t, entry, "first call must seed a cache entry")
	require.Equal(t, info.Size(), entry.offset)
	require.Equal(t, inodeOf(info), entry.inode, "the entry must be pinned to the inode it was seeded from")

	// Append more lines — same inode, file grew.
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o640)
	require.NoError(t, err)
	appendIncrementalLines(t, f, 15)
	require.NoError(t, f.Close())

	second, err := tokenUsageForFile(path)
	require.NoError(t, err)
	require.Equal(t, expectedUsage(35), second.TokenUsage,
		"incremental total after append must equal the total a full scan of all 35 lines would produce")

	// Independent cross-check that bypasses the offset cache entirely.
	full, err := scanFullFileTokenUsage(path)
	require.NoError(t, err)
	require.Equal(t, full.TokenUsage, second.TokenUsage)
}

// TestTokenUsageForFile_ResetsOnTruncation verifies that a same-inode
// truncation (size shrinks below the cached offset) forces a full rescan
// instead of trusting the stale running total.
func TestTokenUsageForFile_ResetsOnTruncation(t *testing.T) {
	resetTokenOffsetCache(t)
	dir := t.TempDir()
	path := writeIncrementalSession(t, dir, 20)

	info1, err := os.Stat(path)
	require.NoError(t, err)
	inode1 := inodeOf(info1)

	_, err = tokenUsageForFile(path)
	require.NoError(t, err)

	// O_TRUNC on an existing path truncates the existing inode in place.
	require.NoError(t, os.WriteFile(path, []byte(strings.Repeat(incrementalLineTemplate, 5)), 0o640))

	info2, err := os.Stat(path)
	require.NoError(t, err)
	require.Equal(t, inode1, inodeOf(info2), "test setup must exercise the same-inode truncation path")

	got, err := tokenUsageForFile(path)
	require.NoError(t, err)
	require.Equal(t, expectedUsage(5), got.TokenUsage,
		"truncation must trigger a full rescan, not a stale incremental sum")
}

// TestTokenUsageForFile_ResetsOnInodeChange verifies that replacing the file
// at the same path with a new inode (e.g. rotation) forces a full rescan of
// the new file rather than reusing the old running total.
func TestTokenUsageForFile_ResetsOnInodeChange(t *testing.T) {
	resetTokenOffsetCache(t)
	dir := t.TempDir()
	path := writeIncrementalSession(t, dir, 20)

	info1, err := os.Stat(path)
	require.NoError(t, err)
	inode1 := inodeOf(info1)

	_, err = tokenUsageForFile(path)
	require.NoError(t, err)

	// Rotate rather than delete-then-recreate: writing the replacement while the
	// original is still linked keeps the old inode allocated, so the kernel
	// cannot hand the same number back (ext4 recycles freed inodes eagerly,
	// APFS does not — delete-then-recreate is only a new inode on APFS).
	replacement := filepath.Join(dir, "session.jsonl.rotated")
	require.NoError(t, os.WriteFile(replacement, []byte(strings.Repeat(incrementalLineTemplate, 7)), 0o640))
	require.NoError(t, os.Rename(replacement, path))

	info2, err := os.Stat(path)
	require.NoError(t, err)
	inode2 := inodeOf(info2)
	require.NotEqual(t, inode1, inode2, "test setup must actually produce a new inode")

	got, err := tokenUsageForFile(path)
	require.NoError(t, err)
	require.Equal(t, expectedUsage(7), got.TokenUsage,
		"inode change must trigger a full rescan of the new file, not reuse the old running total")
}

// TestTokenUsageForFile_FallsBackOnScanError verifies the "never trust a
// partial sum" invariant: any error from the incremental scan discards the
// delta and falls back to a one-shot full rescan.
func TestTokenUsageForFile_FallsBackOnScanError(t *testing.T) {
	resetTokenOffsetCache(t)
	dir := t.TempDir()
	path := writeIncrementalSession(t, dir, 20)

	_, err := tokenUsageForFile(path)
	require.NoError(t, err)

	info, err := os.Stat(path)
	require.NoError(t, err)

	// Corrupt the cached offset to a negative value — Seek(-1, io.SeekStart)
	// reliably errors, forcing ScanMessagesFrom to fail.
	tokenOffsetCacheMu.Lock()
	tokenOffsetCache[path].offset = -1
	tokenOffsetCacheMu.Unlock()

	got, err := tokenUsageForFile(path)
	require.NoError(t, err, "a broken incremental scan must fall back to a full rescan, not surface an error")
	require.Equal(t, expectedUsage(20), got.TokenUsage)

	tokenOffsetCacheMu.Lock()
	fixedOffset := tokenOffsetCache[path].offset
	tokenOffsetCacheMu.Unlock()
	require.Equal(t, info.Size(), fixedOffset, "the fallback must reseed the cache with a valid offset")
}

// TestScanMessagesFrom_TrailingPartialLineLeftForNextCall verifies that an
// in-progress write (no trailing newline yet) is not consumed — its bytes
// remain unaccounted-for until a later call sees the completed line.
func TestScanMessagesFrom_TrailingPartialLineLeftForNextCall(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "partial.jsonl")
	f, err := os.Create(path)
	require.NoError(t, err)
	_, err = f.WriteString(incrementalLineTemplate)
	require.NoError(t, err)
	partial := `{"type":"assistant","timestamp":"2025-01-15T10:30:0`
	_, err = f.WriteString(partial)
	require.NoError(t, err)
	require.NoError(t, f.Close())

	usage, newOffset, err := ScanMessagesFrom(path, 0)
	require.NoError(t, err)
	require.Equal(t, expectedUsage(1), usage)
	require.Equal(t, int64(len(incrementalLineTemplate)), newOffset,
		"offset must stop at the last complete line, leaving the trailing partial line unconsumed")

	usage2, newOffset2, err := ScanMessagesFrom(path, newOffset)
	require.NoError(t, err)
	require.Equal(t, sdk.TokenUsage{}, usage2)
	require.Equal(t, newOffset, newOffset2, "an unchanged partial line must not advance the offset")
}

// TestScanMessagesFrom_NoGrowthReturnsZero verifies the size<=offset fast path.
func TestScanMessagesFrom_NoGrowthReturnsZero(t *testing.T) {
	dir := t.TempDir()
	path := writeIncrementalSession(t, dir, 5)
	info, err := os.Stat(path)
	require.NoError(t, err)

	usage, newOffset, err := ScanMessagesFrom(path, info.Size())
	require.NoError(t, err)
	require.Equal(t, sdk.TokenUsage{}, usage)
	require.Equal(t, info.Size(), newOffset)
}
