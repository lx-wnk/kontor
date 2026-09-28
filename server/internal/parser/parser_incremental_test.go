package parser

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lx-wnk/kontor/sdk"
	"github.com/stretchr/testify/assert"
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

	var usage sdk.TokenUsage
	newOffset, err := ScanMessagesFrom(path, 0, func(m Message) { addMessageUsage(&usage, m) })
	require.NoError(t, err)
	require.Equal(t, expectedUsage(1), usage)
	require.Equal(t, int64(len(incrementalLineTemplate)), newOffset,
		"offset must stop at the last complete line, leaving the trailing partial line unconsumed")

	var usage2 sdk.TokenUsage
	newOffset2, err := ScanMessagesFrom(path, newOffset, func(m Message) { addMessageUsage(&usage2, m) })
	require.NoError(t, err)
	require.Equal(t, sdk.TokenUsage{}, usage2)
	require.Equal(t, newOffset, newOffset2, "an unchanged partial line must not advance the offset")
}

func TestScanMessagesFrom_NoGrowthCallsNothing(t *testing.T) {
	dir := t.TempDir()
	path := writeIncrementalSession(t, dir, 5)
	info, err := os.Stat(path)
	require.NoError(t, err)

	calls := 0
	newOffset, err := ScanMessagesFrom(path, info.Size(), func(Message) { calls++ })
	require.NoError(t, err)
	require.Zero(t, calls)
	require.Equal(t, info.Size(), newOffset)
}

func appendNoteLine(t *testing.T, f *os.File, at time.Time, command string) {
	t.Helper()
	input, err := json.Marshal(map[string]string{"command": command})
	require.NoError(t, err)
	line, err := json.Marshal(map[string]any{
		"type":      "assistant",
		"timestamp": at.UTC().Format(time.RFC3339Nano),
		"message": map[string]any{
			"role":    "assistant",
			"content": []map[string]any{{"type": "tool_use", "id": "t", "name": "Bash", "input": json.RawMessage(input)}},
		},
	})
	require.NoError(t, err)
	_, err = f.Write(append(line, '\n'))
	require.NoError(t, err)
	require.NoError(t, f.Sync())
}

func notePaths(touches []NoteTouch) []string {
	out := make([]string, len(touches))
	for i, t := range touches {
		out[i] = t.Path
	}
	return out
}

func TestTokenUsageForFile_CollectsNoteTouchesIncrementally(t *testing.T) {
	resetTokenOffsetCache(t)
	path := filepath.Join(t.TempDir(), "session.jsonl")
	f, err := os.Create(path)
	require.NoError(t, err)
	defer f.Close() //nolint:errcheck

	appendNoteLine(t, f, time.Now(), `curl "$B/vault/claude-memory/a.md"`)
	first, err := tokenUsageForFile(path)
	require.NoError(t, err)
	assert.Equal(t, []string{"claude-memory/a.md"}, notePaths(first.notes))

	appendNoteLine(t, f, time.Now().Add(time.Second), `curl -X PUT "$B/vault/claude-memory/b.md"`)
	second, err := tokenUsageForFile(path)
	require.NoError(t, err)
	assert.Equal(t, []string{"claude-memory/b.md", "claude-memory/a.md"}, notePaths(second.notes))

	full, err := scanFullFileTokenUsage(path)
	require.NoError(t, err)
	assert.Equal(t, full.notes, second.notes, "the incremental window must equal a full rescan")
}

func TestTokenUsageForFile_RewrittenFileDropsOldNotes(t *testing.T) {
	resetTokenOffsetCache(t)
	path := filepath.Join(t.TempDir(), "session.jsonl")
	f, err := os.Create(path)
	require.NoError(t, err)
	appendNoteLine(t, f, time.Now(), `curl "$B/vault/claude-memory/old-one.md"`)
	appendNoteLine(t, f, time.Now(), `curl "$B/vault/claude-memory/old-two.md"`)
	require.NoError(t, f.Close())
	_, err = tokenUsageForFile(path)
	require.NoError(t, err)

	g, err := os.OpenFile(path, os.O_WRONLY|os.O_TRUNC, 0o600)
	require.NoError(t, err)
	appendNoteLine(t, g, time.Now(), `curl "$B/vault/claude-memory/new.md"`)
	require.NoError(t, g.Close())

	got, err := tokenUsageForFile(path)
	require.NoError(t, err)
	assert.Equal(t, []string{"claude-memory/new.md"}, notePaths(got.notes))
}

func TestParseSessionFile_CarriesRecentNotes(t *testing.T) {
	resetTokenOffsetCache(t)
	path := filepath.Join(t.TempDir(), "session.jsonl")
	f, err := os.Create(path)
	require.NoError(t, err)
	appendNoteLine(t, f, time.Now(), `curl "$B/vault/claude-memory/a.md"`)
	require.NoError(t, f.Close())

	data, err := ParseSessionFile(path)
	require.NoError(t, err)
	assert.Equal(t, []string{"claude-memory/a.md"}, notePaths(data.RecentNotes))
}

// TestTokenUsageForFile_ConcurrentCallersCountAppendOnce pins that callers
// scanning the same session file at the same moment — the broadcast loop and
// an HTTP read both missing the session cache — add an appended region once.
func TestTokenUsageForFile_ConcurrentCallersCountAppendOnce(t *testing.T) {
	resetTokenOffsetCache(t)
	path := writeIncrementalSession(t, t.TempDir(), 1)
	_, err := tokenUsageForFile(path)
	require.NoError(t, err)

	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o640)
	require.NoError(t, err)
	appendIncrementalLines(t, f, 5000)
	require.NoError(t, f.Close())

	const callers = 8
	start := make(chan struct{})
	errs := make(chan error, callers)
	for range callers {
		go func() {
			<-start
			_, err := tokenUsageForFile(path)
			errs <- err
		}()
	}
	close(start)
	for range callers {
		require.NoError(t, <-errs)
	}

	got, err := tokenUsageForFile(path)
	require.NoError(t, err)
	assert.Equal(t, expectedUsage(5001), got.TokenUsage)
}
