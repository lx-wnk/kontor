package parser_test

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lx-wnk/kontor/server/internal/parser"
	"github.com/stretchr/testify/require"
)

// writeSession creates a UUID-named JSONL session file under
// <configDir>/projects/<encoded-cwd>/ and returns the file path.
// ts controls the assistant message timestamp, which determines LastActivity.
func writeSession(t *testing.T, configDir, cwd string, ts time.Time) string {
	t.Helper()
	encoded := parser.EncodePath(cwd)
	dir := filepath.Join(configDir, "projects", encoded)
	require.NoError(t, os.MkdirAll(dir, 0o750))

	// Use a stable UUID-ish name derived from the timestamp so tests can create
	// multiple distinguishable sessions.
	name := fmt.Sprintf("00000000-0000-0000-0000-%012x.jsonl",
		ts.UnixMilli()&0xffffffffffff)

	content, err := json.Marshal(map[string]any{
		"role":    "assistant",
		"content": []any{map[string]any{"type": "text", "text": "hello"}},
		"model":   "claude-test",
		"usage":   map[string]any{"input_tokens": 1, "output_tokens": 1},
	})
	require.NoError(t, err)

	entry, err := json.Marshal(map[string]any{
		"type":      "assistant",
		"timestamp": ts.UTC().Format(time.RFC3339Nano),
		"message":   json.RawMessage(content),
	})
	require.NoError(t, err)

	path := filepath.Join(dir, name)
	require.NoError(t, os.WriteFile(path, append(entry, '\n'), 0o640))
	return path
}

// TestFindSessionForProject_CacheHit verifies that a second call within the TTL
// returns the cached result without re-reading the file.
func TestFindSessionForProject_CacheHit(t *testing.T) {
	// Use a very long TTL so the second call is guaranteed to hit.
	orig := parser.SessionCacheTTL
	parser.SessionCacheTTL = 10 * time.Minute
	t.Cleanup(func() { parser.SessionCacheTTL = orig })

	configDir := t.TempDir()
	cwd := "/tmp/test-project-cache-hit"

	ts := time.Now() // recent enough to satisfy uptime check
	writeSession(t, configDir, cwd, ts)

	got1, err := parser.FindSessionForProject(cwd, 0, configDir)
	require.NoError(t, err)
	require.NotNil(t, got1)
	require.Equal(t, "claude-test", got1.Model)

	// Second call — should return same session ID from cache.
	got2, err := parser.FindSessionForProject(cwd, 0, configDir)
	require.NoError(t, err)
	require.Equal(t, got1.SessionID, got2.SessionID)
}

// TestFindSessionForProject_CacheMissOnMtimeChange verifies that modifying the
// session file (mtime changes) causes a cache miss on the next call.
func TestFindSessionForProject_CacheMissOnMtimeChange(t *testing.T) {
	orig := parser.SessionCacheTTL
	parser.SessionCacheTTL = 10 * time.Minute
	t.Cleanup(func() { parser.SessionCacheTTL = orig })

	configDir := t.TempDir()
	cwd := "/tmp/test-project-cache-mtime"

	ts := time.Now()
	path := writeSession(t, configDir, cwd, ts)

	_, err := parser.FindSessionForProject(cwd, 0, configDir)
	require.NoError(t, err)

	// Simulate file update: write new content with a distinct model name.
	newTs := time.Now().Add(time.Second)
	content, _ := json.Marshal(map[string]any{
		"role":    "assistant",
		"content": []any{map[string]any{"type": "text", "text": "updated"}},
		"model":   "claude-updated",
		"usage":   map[string]any{"input_tokens": 2, "output_tokens": 2},
	})
	entry, _ := json.Marshal(map[string]any{
		"type":      "assistant",
		"timestamp": newTs.UTC().Format(time.RFC3339Nano),
		"message":   json.RawMessage(content),
	})
	require.NoError(t, os.WriteFile(path, append(entry, '\n'), 0o640))

	// Touch mtime explicitly to guarantee the OS flushes the change.
	now := time.Now()
	require.NoError(t, os.Chtimes(path, now, now))

	got2, err := parser.FindSessionForProject(cwd, 0, configDir)
	require.NoError(t, err)
	require.Equal(t, "claude-updated", got2.Model, "cache should have been invalidated by mtime change")
}

// TestFindSessionForProject_CacheMissOnTTLExpiry verifies that an entry older
// than SessionCacheTTL is not reused.
func TestFindSessionForProject_CacheMissOnTTLExpiry(t *testing.T) {
	// Use a zero TTL so every call is a miss.
	orig := parser.SessionCacheTTL
	parser.SessionCacheTTL = 0
	t.Cleanup(func() { parser.SessionCacheTTL = orig })

	configDir := t.TempDir()
	cwd := "/tmp/test-project-cache-ttl"

	ts := time.Now()
	writeSession(t, configDir, cwd, ts)

	got1, err := parser.FindSessionForProject(cwd, 0, configDir)
	require.NoError(t, err)

	// With zero TTL, the second call must re-parse (no cache entry survives).
	got2, err := parser.FindSessionForProject(cwd, 0, configDir)
	require.NoError(t, err)

	// Both calls return valid data — the point is no panic and correct output.
	require.Equal(t, got1.SessionID, got2.SessionID)
}

// TestCandidateCache_ReuseOnSecondCall verifies that a second call within the TTL
// reuses the cached candidate list instead of re-scanning the directory.
func TestCandidateCache_ReuseOnSecondCall(t *testing.T) {
	orig := parser.SessionCacheTTL
	parser.SessionCacheTTL = 10 * time.Minute
	t.Cleanup(func() { parser.SessionCacheTTL = orig })
	parser.ResetStatSessionFilesCalls()

	configDir := t.TempDir()
	cwd := "/tmp/test-candidate-reuse"
	writeSession(t, configDir, cwd, time.Now())

	_, err := parser.FindSessionForProject(cwd, 0, configDir)
	require.NoError(t, err)
	require.Equal(t, int64(1), parser.StatSessionFilesCalls(), "first call should run one full scan")

	_, err = parser.FindSessionForProject(cwd, 0, configDir)
	require.NoError(t, err)
	require.Equal(t, int64(1), parser.StatSessionFilesCalls(), "second call should reuse the cached candidate list (no extra full scan)")
}

// TestCandidateCache_InvalidatedByNewFile verifies that adding a session file
// bumps the directory mtime and forces a fresh full scan.
func TestCandidateCache_InvalidatedByNewFile(t *testing.T) {
	orig := parser.SessionCacheTTL
	parser.SessionCacheTTL = 10 * time.Minute
	t.Cleanup(func() { parser.SessionCacheTTL = orig })
	parser.ResetStatSessionFilesCalls()

	configDir := t.TempDir()
	cwd := "/tmp/test-candidate-newfile"
	writeSession(t, configDir, cwd, time.Now().Add(-2*time.Second))

	_, err := parser.FindSessionForProject(cwd, 0, configDir)
	require.NoError(t, err)
	require.Equal(t, int64(1), parser.StatSessionFilesCalls())

	// Add a second, newer session file — directory mtime changes.
	encoded := parser.EncodePath(cwd)
	dir := filepath.Join(configDir, "projects", encoded)
	now := time.Now()
	writeSession(t, configDir, cwd, now)
	require.NoError(t, os.Chtimes(dir, now, now))

	_, err = parser.FindSessionForProject(cwd, 0, configDir)
	require.NoError(t, err)
	require.Equal(t, int64(2), parser.StatSessionFilesCalls(), "new file should invalidate the candidate cache")
}

// TestCandidateCache_AppendSurfacesFreshData verifies that overwriting the active
// session file (which leaves the directory mtime untouched) is still detected via
// the newest-candidate re-stat, so fresh data is returned.
func TestCandidateCache_AppendSurfacesFreshData(t *testing.T) {
	orig := parser.SessionCacheTTL
	parser.SessionCacheTTL = 10 * time.Minute
	t.Cleanup(func() { parser.SessionCacheTTL = orig })
	parser.ResetStatSessionFilesCalls()

	configDir := t.TempDir()
	cwd := "/tmp/test-candidate-append"
	path := writeSession(t, configDir, cwd, time.Now())

	got1, err := parser.FindSessionForProject(cwd, 0, configDir)
	require.NoError(t, err)
	require.Equal(t, "claude-test", got1.Model)

	// Overwrite the existing file (same path → directory mtime unchanged).
	newTs := time.Now().Add(time.Second)
	content, _ := json.Marshal(map[string]any{
		"role":    "assistant",
		"content": []any{map[string]any{"type": "text", "text": "updated"}},
		"model":   "claude-appended",
		"usage":   map[string]any{"input_tokens": 3, "output_tokens": 3},
	})
	entry, _ := json.Marshal(map[string]any{
		"type":      "assistant",
		"timestamp": newTs.UTC().Format(time.RFC3339Nano),
		"message":   json.RawMessage(content),
	})
	require.NoError(t, os.WriteFile(path, append(entry, '\n'), 0o640))
	require.NoError(t, os.Chtimes(path, newTs, newTs))

	got2, err := parser.FindSessionForProject(cwd, 0, configDir)
	require.NoError(t, err)
	require.Equal(t, "claude-appended", got2.Model, "overwrite of active file must surface fresh data despite candidate cache")
}

// TestFindSessionForProject_NoFiles verifies that an error is returned when no
// JSONL session files are present.
func TestFindSessionForProject_NoFiles(t *testing.T) {
	configDir := t.TempDir()
	cwd := "/tmp/test-project-no-files"

	encoded := parser.EncodePath(cwd)
	dir := filepath.Join(configDir, "projects", encoded)
	require.NoError(t, os.MkdirAll(dir, 0o750))

	_, err := parser.FindSessionForProject(cwd, 0, configDir)
	require.Error(t, err)
	require.True(t, strings.Contains(err.Error(), "no session files"), "unexpected error: %v", err)
}

// rewriteSessionModel overwrites the JSONL body at path with a new model name
// while preserving the file's inode and mtime, so the cache-validity check
// (path + inode + mtime) still passes. This lets a test distinguish a genuine
// cache hit (stale model returned) from an FS re-read (new model returned).
func rewriteSessionModel(t *testing.T, path, model string, ts time.Time) {
	t.Helper()
	info, err := os.Stat(path)
	require.NoError(t, err)

	content, err := json.Marshal(map[string]any{
		"role":    "assistant",
		"content": []any{map[string]any{"type": "text", "text": "rewritten"}},
		"model":   model,
		"usage":   map[string]any{"input_tokens": 1, "output_tokens": 1},
	})
	require.NoError(t, err)
	entry, err := json.Marshal(map[string]any{
		"type":      "assistant",
		"timestamp": ts.UTC().Format(time.RFC3339Nano),
		"message":   json.RawMessage(content),
	})
	require.NoError(t, err)

	// In-place truncate+write keeps the inode; Chtimes restores the mtime the
	// cache recorded on the first call.
	require.NoError(t, os.WriteFile(path, append(entry, '\n'), 0o640))
	require.NoError(t, os.Chtimes(path, info.ModTime(), info.ModTime()))
}

// TestFindSessionForProject_CacheHit_SkipsContentReread is the regression guard
// for the cache's actual purpose: avoiding the per-tick content tail-read.
//
// The directory is stat'd on every call (statSessionFiles always runs), so a
// "<=1 dir stat" assertion is not meaningful. The guarded invariant is instead
// that a cache hit skips findSessionByContent. Proof: after warming the cache,
// the file body is rewritten with a different model while inode and mtime are
// restored. A cache hit returns the STALE model (read skipped); an FS re-read
// would return the new model. Asserting the stale model proves the read was
// skipped — and the complementary TTL=0 control below proves the re-read path
// would otherwise pick up the change.
func TestFindSessionForProject_CacheHit_SkipsContentReread(t *testing.T) {
	orig := parser.SessionCacheTTL
	parser.SessionCacheTTL = 10 * time.Minute
	t.Cleanup(func() { parser.SessionCacheTTL = orig })

	configDir := t.TempDir()
	cwd := "/tmp/test-project-cache-hit-skip-reread"

	ts := time.Now()
	path := writeSession(t, configDir, cwd, ts)

	got1, err := parser.FindSessionForProject(cwd, 0, configDir)
	require.NoError(t, err)
	require.Equal(t, "claude-test", got1.Model)

	// Mutate the body but keep inode + mtime so the cache entry stays valid.
	rewriteSessionModel(t, path, "claude-MUTATED", ts)

	got2, err := parser.FindSessionForProject(cwd, 0, configDir)
	require.NoError(t, err)
	require.Equal(t, "claude-test", got2.Model,
		"cache hit must return the stale model — proving the content read was skipped")
}

// TestFindSessionForProject_TTLZero_ForcesContentReread is the negative control
// for the test above: with TTL=0 every call misses the cache, so the same
// mtime-preserving body rewrite IS observed on the second call.
func TestFindSessionForProject_TTLZero_ForcesContentReread(t *testing.T) {
	orig := parser.SessionCacheTTL
	parser.SessionCacheTTL = 0
	t.Cleanup(func() { parser.SessionCacheTTL = orig })

	configDir := t.TempDir()
	cwd := "/tmp/test-project-cache-ttl0-reread"

	ts := time.Now()
	path := writeSession(t, configDir, cwd, ts)

	got1, err := parser.FindSessionForProject(cwd, 0, configDir)
	require.NoError(t, err)
	require.Equal(t, "claude-test", got1.Model)

	rewriteSessionModel(t, path, "claude-MUTATED", ts)

	got2, err := parser.FindSessionForProject(cwd, 0, configDir)
	require.NoError(t, err)
	require.Equal(t, "claude-MUTATED", got2.Model,
		"TTL=0 forces an FS re-read — the mutated model must be observed")
}
