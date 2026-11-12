package history

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lx-wnk/kontor/sdk"
	"github.com/lx-wnk/kontor/server/internal/db/ent"
	"github.com/lx-wnk/kontor/server/internal/db/repo"
	"github.com/lx-wnk/kontor/server/internal/parser"
)

// ---- stub repo ----

type stubCostRepo struct {
	mu     sync.Mutex
	rows   []repo.AgentCostRow
	mtimes map[string]int64 // returned by ListSourceMtimes; nil → empty
}

func (r *stubCostRepo) Upsert(_ context.Context, rows []repo.AgentCostRow) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.rows = append(r.rows, rows...)
	return nil
}

func (r *stubCostRepo) ListByTimeRange(_ context.Context, _, _ time.Time) ([]*ent.AgentCostTrend, error) {
	return nil, nil
}

func (r *stubCostRepo) ListSourceMtimes(_ context.Context) (map[string]int64, error) {
	if r.mtimes == nil {
		return map[string]int64{}, nil
	}
	return r.mtimes, nil
}

func (r *stubCostRepo) all() []repo.AgentCostRow {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]repo.AgentCostRow(nil), r.rows...)
}

// buildJSONLLine returns a minimal JSONL assistant message entry with the given token counts.
func buildJSONLLine(inputTokens, outputTokens int, model, timestamp string) string {
	return `{"type":"message","timestamp":"` + timestamp + `","message":{"role":"assistant","model":"` + model + `","usage":{"input_tokens":` +
		itoa(inputTokens) + `,"output_tokens":` + itoa(outputTokens) + `,"cache_creation_input_tokens":0,"cache_read_input_tokens":0}}}`
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	// Simple positive-integer formatter sufficient for tests.
	s := []byte{}
	for n > 0 {
		s = append([]byte{byte('0' + n%10)}, s...)
		n /= 10
	}
	return string(s)
}

func TestParseSession_ValidJSONLWithUsage(t *testing.T) {
	ts := "2024-01-15T10:30:00.000Z"
	line := buildJSONLLine(100, 50, "claude-opus-4", ts)

	stats, err := parseSession(strings.NewReader(line))
	require.NoError(t, err)

	assert.Equal(t, 100, stats.Usage.InputTokens)
	assert.Equal(t, 50, stats.Usage.OutputTokens)
	assert.Equal(t, "claude-opus-4", stats.Model)
	// LastActivity should be the parsed timestamp.
	expected, _ := time.Parse(time.RFC3339Nano, ts)
	assert.True(t, stats.LastActivity.Equal(expected) || stats.LastActivity.After(expected),
		"lastActivity should be at or after the parsed timestamp")
}

func TestParseSession_MultipleEntriesAccumulatesTokens(t *testing.T) {
	ts1 := "2024-01-15T10:00:00.000Z"
	ts2 := "2024-01-15T11:00:00.000Z"
	line1 := buildJSONLLine(100, 50, "claude-sonnet-4-5", ts1)
	line2 := buildJSONLLine(200, 80, "claude-sonnet-4-5", ts2)
	raw := line1 + "\n" + line2

	stats, err := parseSession(strings.NewReader(raw))
	require.NoError(t, err)

	assert.Equal(t, 300, stats.Usage.InputTokens, "input tokens should be summed")
	assert.Equal(t, 130, stats.Usage.OutputTokens, "output tokens should be summed")
	assert.Equal(t, "claude-sonnet-4-5", stats.Model)

	// LastActivity should be ts2 (the later one).
	expected, _ := time.Parse(time.RFC3339Nano, ts2)
	assert.True(t, stats.LastActivity.Equal(expected) || stats.LastActivity.After(expected))
}

func TestParseSession_MissingUsageField(t *testing.T) {
	// Assistant message without "usage" field — should not panic and return zero tokens.
	raw := `{"type":"message","timestamp":"2024-01-15T10:00:00.000Z","message":{"role":"assistant","model":"claude-sonnet-4-5"}}`

	stats, err := parseSession(strings.NewReader(raw))
	require.NoError(t, err)
	assert.Equal(t, 0, stats.Usage.InputTokens)
	assert.Equal(t, 0, stats.Usage.OutputTokens)
}

func TestParseSession_NonAssistantRoleSkipped(t *testing.T) {
	// User messages should be ignored.
	raw := `{"type":"message","timestamp":"2024-01-15T10:00:00.000Z","message":{"role":"user","usage":{"input_tokens":999,"output_tokens":999}}}`

	stats, err := parseSession(strings.NewReader(raw))
	require.NoError(t, err)
	assert.Equal(t, 0, stats.Usage.InputTokens)
	assert.Equal(t, 0, stats.Usage.OutputTokens)
}

func TestParseSession_NonMessageTypeSkipped(t *testing.T) {
	// Non-turn entries (tool_result, attachment, …) must not contribute usage,
	// even when they carry an assistant role + usage block.
	raw := `{"type":"tool_result","timestamp":"2024-01-15T10:00:00.000Z","message":{"role":"assistant","usage":{"input_tokens":999,"output_tokens":999}}}`

	stats, err := parseSession(strings.NewReader(raw))
	require.NoError(t, err)
	assert.Equal(t, 0, stats.Usage.InputTokens)
	assert.Equal(t, 0, stats.Usage.OutputTokens)
}

// TestParseSession_AssistantType is the regression guard for the bug that
// left the cost table empty: real Claude Code logs write assistant turns with a
// top-level type of "assistant" (not "message"), and those entries carry the
// token usage. They must be counted.
func TestParseSession_AssistantType(t *testing.T) {
	raw := `{"type":"assistant","timestamp":"2024-01-15T10:00:00.000Z","message":{"role":"assistant","model":"claude-opus-4-7","usage":{"input_tokens":120,"output_tokens":45}}}`

	stats, err := parseSession(strings.NewReader(raw))
	require.NoError(t, err)
	assert.Equal(t, 120, stats.Usage.InputTokens)
	assert.Equal(t, 45, stats.Usage.OutputTokens)
	assert.Equal(t, "claude-opus-4-7", stats.Model)
}

func TestParseSession_MalformedJSONLineSkipped(t *testing.T) {
	// Malformed line followed by a valid line — the bad one is skipped, not an error.
	ts := "2024-01-15T12:00:00.000Z"
	validLine := buildJSONLLine(42, 21, "claude-opus-4", ts)
	raw := "THIS IS NOT JSON\n" + validLine

	stats, err := parseSession(strings.NewReader(raw))
	require.NoError(t, err, "malformed JSON should be skipped, not cause an error")
	assert.Equal(t, 42, stats.Usage.InputTokens)
	assert.Equal(t, 21, stats.Usage.OutputTokens)
}

func TestParseSession_EmptyInput(t *testing.T) {
	stats, err := parseSession(strings.NewReader(""))
	require.NoError(t, err)
	assert.Equal(t, 0, stats.Usage.InputTokens)
	assert.Equal(t, 0, stats.Usage.OutputTokens)
	// Default model should be set.
	assert.NotEmpty(t, stats.Model)
}

func TestParseSession_AllBlankLines(t *testing.T) {
	raw := strings.Repeat("\n", 10)
	stats, err := parseSession(strings.NewReader(raw))
	require.NoError(t, err)
	assert.Equal(t, 0, stats.Usage.InputTokens)
	assert.Equal(t, 0, stats.Usage.OutputTokens)
}

func TestParseSession_ModelUpdatedToLatestEntry(t *testing.T) {
	ts1 := "2024-01-15T10:00:00.000Z"
	ts2 := "2024-01-15T11:00:00.000Z"
	line1 := buildJSONLLine(10, 5, "claude-sonnet-4-5", ts1)
	line2 := buildJSONLLine(10, 5, "claude-opus-4", ts2)
	raw := line1 + "\n" + line2

	stats, err := parseSession(strings.NewReader(raw))
	require.NoError(t, err)
	// The last assistant entry's model wins.
	assert.Equal(t, "claude-opus-4", stats.Model)
}

func TestParseSession_AllMalformedLines(t *testing.T) {
	raw := "not json\nalso not json\n{broken"
	stats, err := parseSession(strings.NewReader(raw))
	require.NoError(t, err)
	assert.Equal(t, 0, stats.Usage.InputTokens)
	assert.Equal(t, 0, stats.Usage.OutputTokens)
}

// ---- multi-dir importer tests ----

// makeJSONLContent returns a minimal JSONL session log with one assistant message.
func makeJSONLContent(inputTokens, outputTokens int) string {
	return buildJSONLLine(inputTokens, outputTokens, "claude-sonnet-4-6", "2024-06-01T10:00:00.000Z") + "\n"
}

// TestImporter_MultiDir_CollectFnSeam uses WithCollectFn to inject a fake
// collector keyed on projects-dir path.  This is a pure unit test — no disk I/O
// is performed.  It proves that files from two distinct provider/account dirs are
// accumulated in progress.Total.
func TestImporter_MultiDir_CollectFnSeam(t *testing.T) {
	// Two synthetic dirs — the collect stub maps each to one fake path.
	dir1 := "/fake/account1/projects"
	dir2 := "/fake/account2/projects"
	path1 := filepath.Join(dir1, "enc1", "session-aaa.jsonl")
	path2 := filepath.Join(dir2, "enc2", "session-bbb.jsonl")

	fakePaths := map[string][]string{
		dir1: {path1},
		dir2: {path2},
	}
	collectStub := func(projectsDir string) ([]string, error) {
		if paths, ok := fakePaths[projectsDir]; ok {
			return paths, nil
		}
		return nil, nil
	}

	costRepo := &stubCostRepo{}
	imp := NewImporter(costRepo).WithCollectFn(collectStub)

	// Point DASHBOARD_CLAUDE_CONFIG_DIRS at our synthetic dirs (which don't
	// actually exist on disk; AllAgentConfigDirs won't read them, but the
	// collect stub handles lookups).  We also need Codex/Gemini dirs to be
	// absent so they don't pollute the count.
	t.Setenv("DASHBOARD_CLAUDE_CONFIG_DIRS", "/fake/account1,/fake/account2")
	// Suppress the real ~/.claude from contributing (CLAUDE_CONFIG_DIR empty is fine
	// because DASHBOARD_CLAUDE_CONFIG_DIRS takes full priority when set).
	t.Setenv("CLAUDE_CONFIG_DIR", "/fake/nonexistent")

	var progressSnapshots []ImportProgress
	var mu sync.Mutex
	onProgress := func(p ImportProgress) {
		mu.Lock()
		defer mu.Unlock()
		progressSnapshots = append(progressSnapshots, p)
	}

	err := imp.Run(t.Context(), onProgress)
	require.NoError(t, err)

	// Wait for the background goroutine to finish.
	require.Eventually(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		if len(progressSnapshots) == 0 {
			return false
		}
		return progressSnapshots[len(progressSnapshots)-1].Done
	}, 5*time.Second, 10*time.Millisecond, "import never completed")

	mu.Lock()
	final := progressSnapshots[len(progressSnapshots)-1]
	mu.Unlock()

	assert.Equal(t, 2, final.Total, "total should cover files from both dirs")
	assert.Equal(t, 2, final.Processed, "both files should be processed")
	assert.True(t, final.Done)
}

// TestImporter_MultiDir_Deduplication proves that the same path appearing
// under two configured dirs (e.g. a symlink scenario) is processed only once.
func TestImporter_MultiDir_Deduplication(t *testing.T) {
	sharedPath := "/fake/shared/projects/enc/session-dup.jsonl"
	collectStub := func(_ string) ([]string, error) {
		return []string{sharedPath}, nil
	}

	t.Setenv("DASHBOARD_CLAUDE_CONFIG_DIRS", "/fake/acc1,/fake/acc2")
	t.Setenv("CLAUDE_CONFIG_DIR", "/fake/nonexistent")

	costRepo := &stubCostRepo{}
	imp := NewImporter(costRepo).WithCollectFn(collectStub)

	var last ImportProgress
	var mu sync.Mutex
	err := imp.Run(t.Context(), func(p ImportProgress) {
		mu.Lock()
		defer mu.Unlock()
		last = p
	})
	require.NoError(t, err)

	require.Eventually(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return last.Done
	}, 5*time.Second, 10*time.Millisecond)

	mu.Lock()
	total := last.Total
	processed := last.Processed
	mu.Unlock()

	assert.Equal(t, 1, total, "duplicate path should be counted once")
	assert.Equal(t, 1, processed, "duplicate path should be processed once")
}

// TestImporter_MultiDir_OneErrorSkipped proves that a collect error on one dir
// does not abort the entire import — the other dir's files are still processed.
func TestImporter_MultiDir_OneErrorSkipped(t *testing.T) {
	goodPath := "/fake/good/projects/enc/session-ok.jsonl"
	callCount := 0
	collectStub := func(projectsDir string) ([]string, error) {
		callCount++
		if strings.Contains(projectsDir, "bad") {
			return nil, fmt.Errorf("simulated read error")
		}
		return []string{goodPath}, nil
	}

	t.Setenv("DASHBOARD_CLAUDE_CONFIG_DIRS", "/fake/bad,/fake/good")
	t.Setenv("CLAUDE_CONFIG_DIR", "/fake/nonexistent")

	costRepo := &stubCostRepo{}
	imp := NewImporter(costRepo).WithCollectFn(collectStub)

	var last ImportProgress
	var mu sync.Mutex
	err := imp.Run(t.Context(), func(p ImportProgress) {
		mu.Lock()
		defer mu.Unlock()
		last = p
	})
	require.NoError(t, err)

	require.Eventually(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return last.Done
	}, 5*time.Second, 10*time.Millisecond)

	mu.Lock()
	total := last.Total
	processed := last.Processed
	mu.Unlock()

	assert.Equal(t, 1, total, "only the good dir's file should be counted")
	assert.Equal(t, 1, processed)
}

// TestImporter_MultiDir_RealFS uses real temp directories and the real
// collectJSONLFiles function to verify end-to-end file discovery and parsing.
// The collect fn is restricted to only the two temp dirs so the test is
// hermetic (it does not pick up the developer's actual ~/.claude sessions).
func TestImporter_MultiDir_RealFS(t *testing.T) {
	// Build two Claude config dirs, each with a projects sub-tree.
	dir1 := t.TempDir()
	dir2 := t.TempDir()

	projects1 := filepath.Join(dir1, "projects")
	projects2 := filepath.Join(dir2, "projects")

	enc1 := filepath.Join(projects1, "-repo-alpha")
	enc2 := filepath.Join(projects2, "-repo-beta")
	require.NoError(t, os.MkdirAll(enc1, 0o755))
	require.NoError(t, os.MkdirAll(enc2, 0o755))

	file1 := filepath.Join(enc1, "session-111.jsonl")
	file2 := filepath.Join(enc2, "session-222.jsonl")
	require.NoError(t, os.WriteFile(file1, []byte(makeJSONLContent(100, 50)), 0o644))
	require.NoError(t, os.WriteFile(file2, []byte(makeJSONLContent(200, 80)), 0o644))

	// Use DASHBOARD_CLAUDE_CONFIG_DIRS so AllAgentConfigDirs returns our dirs first.
	// Restrict collection to only these two project dirs via a wrapped collectFn so
	// the test stays hermetic regardless of what is on the developer's machine.
	t.Setenv("DASHBOARD_CLAUDE_CONFIG_DIRS", dir1+","+dir2)
	allowedProjects := map[string]struct{}{
		filepath.Clean(projects1): {},
		filepath.Clean(projects2): {},
	}
	collectRestricted := func(projectsDir string) ([]string, error) {
		if _, ok := allowedProjects[filepath.Clean(projectsDir)]; !ok {
			return nil, nil // suppress real ~/.claude dirs
		}
		return collectJSONLFiles(projectsDir)
	}

	costRepo := &stubCostRepo{}
	imp := NewImporter(costRepo).WithCollectFn(collectRestricted)

	var last ImportProgress
	var mu sync.Mutex
	err := imp.Run(t.Context(), func(p ImportProgress) {
		mu.Lock()
		defer mu.Unlock()
		last = p
	})
	require.NoError(t, err)

	require.Eventually(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return last.Done
	}, 5*time.Second, 10*time.Millisecond)

	mu.Lock()
	final := last
	mu.Unlock()

	assert.Equal(t, 2, final.Total, "both dirs should contribute files")
	assert.Equal(t, 2, final.Processed)
	assert.Equal(t, 2, final.Imported, "both valid files should be imported")
	assert.Equal(t, 0, final.Errors)

	// Confirm both sessions are in the upserted rows.
	rows := costRepo.all()
	assert.Len(t, rows, 2)
	sessionIDs := make(map[string]bool, 2)
	for _, r := range rows {
		sessionIDs[r.SessionID] = true
	}
	assert.True(t, sessionIDs["session-111"])
	assert.True(t, sessionIDs["session-222"])
}

// TestDedupRowsBySession verifies the same session_id collapses to one row
// keeping the latest recorded_at, regardless of input order — so a session
// present under more than one config dir upserts deterministically.
func TestDedupRowsBySession(t *testing.T) {
	older := time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC)
	newer := time.Date(2026, 1, 2, 10, 0, 0, 0, time.UTC)

	rows := []repo.AgentCostRow{
		{SessionID: "A", Model: "m1", CostUSD: 1.0, RecordedAt: older},
		{SessionID: "B", Model: "m2", CostUSD: 5.0, RecordedAt: newer},
		{SessionID: "A", Model: "m1", CostUSD: 2.0, RecordedAt: newer}, // newer wins for A
	}

	out := dedupRowsBySession(rows)
	require.Len(t, out, 2)

	byID := map[string]repo.AgentCostRow{}
	for _, r := range out {
		byID[r.SessionID] = r
	}
	assert.Equal(t, 2.0, byID["A"].CostUSD, "latest recorded_at must win for session A")
	assert.Equal(t, newer, byID["A"].RecordedAt)
	assert.Equal(t, 5.0, byID["B"].CostUSD)

	// Reversed input must yield the same surviving values (determinism).
	reversed := []repo.AgentCostRow{rows[2], rows[1], rows[0]}
	out2 := dedupRowsBySession(reversed)
	require.Len(t, out2, 2)
	for _, r := range out2 {
		if r.SessionID == "A" {
			assert.Equal(t, 2.0, r.CostUSD, "order-independent: A keeps the newer row")
		}
	}
}

// ---- v2 extractCostRow behaviour tests ----

func writeSession(t *testing.T, dir, sessionID, content string) string {
	t.Helper()
	p := filepath.Join(dir, sessionID+".jsonl")
	require.NoError(t, os.WriteFile(p, []byte(content), 0o644))
	return p
}

// TestExtractCostRow_RecordedAtFromTimestamp is the regression guard for the
// "everything lands on the last two days" bug: recorded_at must reflect the
// session's real (old) timestamp, not a now-24h fallback.
func TestExtractCostRow_RecordedAtFromTimestamp(t *testing.T) {
	dir := t.TempDir()
	ts := "2025-03-04T08:15:00.000Z"
	p := writeSession(t, dir, "session-old", buildJSONLLine(100, 50, "claude-opus-4", ts)+"\n")

	imp := NewImporter(&stubCostRepo{})
	row, err := imp.extractCostRow(t.Context(), p)
	require.NoError(t, err)
	require.NotNil(t, row)

	want, _ := time.Parse(time.RFC3339Nano, ts)
	assert.True(t, row.RecordedAt.Equal(want), "recorded_at should equal the parsed timestamp %v, got %v", want, row.RecordedAt)
	assert.WithinDuration(t, want, row.RecordedAt, time.Second)
}

// TestExtractCostRow_RecordedAtFallsBackToMtime: when no timestamp parses, the
// row's recorded_at falls back to the file's mtime (not now-24h).
func TestExtractCostRow_RecordedAtFallsBackToMtime(t *testing.T) {
	dir := t.TempDir()
	// Assistant turn with usage but NO timestamp field.
	line := `{"type":"assistant","message":{"role":"assistant","model":"claude-opus-4","usage":{"input_tokens":10,"output_tokens":5}}}`
	p := writeSession(t, dir, "session-nots", line+"\n")

	info, err := os.Stat(p)
	require.NoError(t, err)

	imp := NewImporter(&stubCostRepo{})
	row, err := imp.extractCostRow(t.Context(), p)
	require.NoError(t, err)
	require.NotNil(t, row)
	assert.WithinDuration(t, info.ModTime(), row.RecordedAt, 2*time.Second)
}

// TestExtractCostRow_FullFileReadBeyondTail proves the importer reads the whole
// file, not just the trailing 32KB: the only token-bearing turn sits at the
// START, followed by >32KB of filler. A tail-only read would miss it.
func TestExtractCostRow_FullFileReadBeyondTail(t *testing.T) {
	dir := t.TempDir()
	head := buildJSONLLine(777, 111, "claude-opus-4", "2025-01-01T00:00:00.000Z") + "\n"
	// >32KB of non-assistant filler lines after the head.
	filler := strings.Repeat(`{"type":"attachment","message":{"role":"user"}}`+"\n", 1200)
	p := writeSession(t, dir, "session-big", head+filler)
	require.Greater(t, len(head+filler), 40*1024, "filler must exceed the 32KB tail window")

	imp := NewImporter(&stubCostRepo{})
	row, err := imp.extractCostRow(t.Context(), p)
	require.NoError(t, err)
	require.NotNil(t, row)
	assert.Equal(t, 777, row.InputTokens, "head turn beyond the tail window must still be counted")
	assert.Equal(t, 111, row.OutputTokens)
}

// TestExtractCostRow_ProjectFallbackBasename: with no resolver, project_name is
// the basename of the session's cwd.
func TestExtractCostRow_ProjectFallbackBasename(t *testing.T) {
	dir := t.TempDir()
	line := `{"type":"assistant","timestamp":"2025-02-02T02:02:02.000Z","cwd":"/Users/x/code/my-app","message":{"role":"assistant","model":"claude-opus-4","usage":{"input_tokens":3,"output_tokens":2}}}`
	p := writeSession(t, dir, "session-proj", line+"\n")

	imp := NewImporter(&stubCostRepo{}) // nil resolver → basename fallback
	row, err := imp.extractCostRow(t.Context(), p)
	require.NoError(t, err)
	require.NotNil(t, row)
	assert.Equal(t, "/Users/x/code/my-app", row.Cwd)
	assert.Equal(t, "/Users/x/code/my-app", row.ProjectPath)
	assert.Equal(t, "my-app", row.ProjectName)
}

// TestImporter_SkipsUnchangedFiles: a file whose stored mtime equals its current
// mtime is skipped (not re-parsed / not re-upserted).
func TestImporter_SkipsUnchangedFiles(t *testing.T) {
	dir := t.TempDir()
	p := writeSession(t, dir, "session-skip", makeJSONLContent(10, 5))
	info, err := os.Stat(p)
	require.NoError(t, err)

	repoStub := &stubCostRepo{mtimes: map[string]int64{"session-skip": info.ModTime().UnixNano()}}
	imp := NewImporter(repoStub).WithCollectFn(func(string) ([]string, error) { return []string{p}, nil })
	t.Setenv("DASHBOARD_CLAUDE_CONFIG_DIRS", "/fake/acct")
	t.Setenv("CLAUDE_CONFIG_DIR", "/fake/nonexistent")

	done := make(chan struct{})
	require.NoError(t, imp.Run(t.Context(), func(p ImportProgress) {
		if p.Done {
			select {
			case <-done:
			default:
				close(done)
			}
		}
	}))
	<-done
	require.Eventually(t, func() bool { return len(repoStub.all()) == 0 }, time.Second, 10*time.Millisecond,
		"unchanged file must be skipped — no rows upserted")
}

// TestParseSession_LongLine exercises a single JSONL line far larger than
// bufio.Scanner's default 64 KB token limit, proving the enlarged buffer keeps the
// row instead of dropping it with bufio.ErrTooLong.
func TestParseSession_LongLine(t *testing.T) {
	ts := "2025-01-15T10:30:00.000Z"
	// ~512 KB of filler embedded in a valid assistant entry's text content.
	filler := strings.Repeat("x", 512*1024)
	line := `{"type":"assistant","timestamp":"` + ts + `","cwd":"/work/proj","message":{"role":"assistant","model":"claude-opus-4","content":[{"type":"text","text":"` +
		filler + `"}],"usage":{"input_tokens":111,"output_tokens":22,"cache_creation_input_tokens":0,"cache_read_input_tokens":0}}}`

	stats, err := parseSession(strings.NewReader(line))
	require.NoError(t, err)
	require.Equal(t, 111, stats.Usage.InputTokens)
	require.Equal(t, 22, stats.Usage.OutputTokens)
	require.Equal(t, "claude-opus-4", stats.Model)
	require.Equal(t, "/work/proj", stats.Cwd)
}

// ---- subagent roll-up and row fill ----

func usageLine(ts, model string, in, out int) string {
	return fmt.Sprintf(`{"type":"assistant","timestamp":%q,"message":{"role":"assistant","model":%q,"usage":{"input_tokens":%d,"output_tokens":%d}}}`, ts, model, in, out)
}

func writeParentWithSub(t *testing.T, parent, sub string) (parentPath, subPath string) {
	t.Helper()
	dir := t.TempDir()
	parentPath = writeSession(t, dir, "session-roll", parent)
	subDir := filepath.Join(dir, "session-roll", "subagents")
	require.NoError(t, os.MkdirAll(subDir, 0o755))
	subPath = filepath.Join(subDir, "agent-a.jsonl")
	require.NoError(t, os.WriteFile(subPath, []byte(sub), 0o644))
	return parentPath, subPath
}

func runImportSync(t *testing.T, costRepo *stubCostRepo, files ...string) {
	t.Helper()
	t.Setenv("DASHBOARD_CLAUDE_CONFIG_DIRS", "/fake/acct")
	t.Setenv("CLAUDE_CONFIG_DIR", "/fake/nonexistent")
	imp := NewImporter(costRepo).WithCollectFn(func(string) ([]string, error) { return files, nil })
	imp.runImport(t.Context(), func(ImportProgress) {})
}

func TestImporter_RollsUpSubagents(t *testing.T) {
	opus, haiku := "claude-opus-4", "claude-haiku-4-5"
	parent := strings.Join([]string{
		promptLine("2025-01-15T10:00:00Z", "first"),
		usageLine("2025-01-15T10:00:05Z", opus, 60, 6),
		promptLine("2025-01-15T10:01:00Z", "second"),
		usageLine("2025-01-15T10:01:05Z", opus, 40, 4),
	}, "\n")
	sub := strings.Join([]string{
		promptLine("2025-01-15T10:00:01Z", "sub task"),
		usageLine("2025-01-15T10:00:02Z", haiku, 50, 5),
	}, "\n")
	parentPath, _ := writeParentWithSub(t, parent, sub)

	row, err := NewImporter(&stubCostRepo{}).extractCostRow(t.Context(), parentPath)
	require.NoError(t, err)
	require.NotNil(t, row)

	assert.Equal(t, 150, row.InputTokens)
	assert.Equal(t, 15, row.OutputTokens)
	want := parser.EstimateCost(sdk.TokenUsage{InputTokens: 100, OutputTokens: 10}, opus) +
		parser.EstimateCost(sdk.TokenUsage{InputTokens: 50, OutputTokens: 5}, haiku)
	assert.InDelta(t, want, row.CostUSD, 1e-12)
	assert.Equal(t, repo.SourceEstimate, row.CostSource)
	assert.Equal(t, repo.SourceEstimate, row.AgentTimeSource)
	assert.Equal(t, 2, row.Turns, "only the parent's turns count")
	assert.Equal(t, opus, row.Model)
	assert.Equal(t, "first", row.Title)
	assert.Equal(t, int64(10_000), row.AgentMs, "subagent time is not added on the estimate path")
}

func TestImporter_CostStateWins(t *testing.T) {
	parent := strings.Join([]string{
		promptLine("2025-01-15T10:00:00Z", "hi"),
		usageLine("2025-01-15T10:00:05Z", "claude-opus-4", 100, 10),
		`{"type":"cost-state","totalCostUSD":3.2,"totalAPIDuration":1000,"totalToolDuration":500,"totalLinesAdded":7,"totalLinesRemoved":2,"hasUnknownModelCost":true,"modelUsage":{"claude-opus-4":{"costUSD":3.0},"claude-haiku-4-5":{"costUSD":0.2}}}`,
	}, "\n")
	parentPath, _ := writeParentWithSub(t, parent, usageLine("2025-01-15T10:00:02Z", "claude-haiku-4-5", 50, 5))

	row, err := NewImporter(&stubCostRepo{}).extractCostRow(t.Context(), parentPath)
	require.NoError(t, err)
	require.NotNil(t, row)

	assert.InDelta(t, 3.2, row.CostUSD, 1e-12)
	assert.Equal(t, repo.SourceCostState, row.CostSource)
	assert.Equal(t, int64(1500), row.AgentMs)
	assert.Equal(t, repo.SourceCostState, row.AgentTimeSource)
	assert.Equal(t, 7, row.LinesAdded)
	assert.Equal(t, 2, row.LinesRemoved)
	assert.True(t, row.CostUnknownModel)
	assert.Equal(t, map[string]float64{"claude-opus-4": 3.0, "claude-haiku-4-5": 0.2}, row.ModelCosts)
	assert.Equal(t, 150, row.InputTokens, "tokens are summed even when cost-state wins")
}

func TestImporter_StaleCostStateFallsBack(t *testing.T) {
	parent := strings.Join([]string{
		promptLine("2025-01-15T10:00:00Z", "hi"),
		usageLine("2025-01-15T10:00:05Z", "claude-opus-4", 100, 10),
		`{"type":"cost-state","totalCostUSD":3.2,"totalAPIDuration":1000,"totalToolDuration":500,"totalLinesAdded":7,"totalLinesRemoved":2,"hasUnknownModelCost":true,"modelUsage":{"claude-opus-4":{"costUSD":3.2}}}`,
		usageLine("2025-01-15T10:00:09Z", "claude-opus-4", 1, 1),
	}, "\n")
	stats := mustParse(t, parent)
	p := writeSession(t, t.TempDir(), "session-stale", parent)

	row, err := NewImporter(&stubCostRepo{}).extractCostRow(t.Context(), p)
	require.NoError(t, err)
	require.NotNil(t, row)

	assert.Equal(t, repo.SourceEstimate, row.CostSource)
	assert.Equal(t, repo.SourceEstimate, row.AgentTimeSource)
	assert.Equal(t, stats.EstimateMs, row.AgentMs)
	assert.InDelta(t, parser.EstimateCost(stats.Usage, stats.Model), row.CostUSD, 1e-12)
	assert.Equal(t, 7, row.LinesAdded)
	assert.Equal(t, 2, row.LinesRemoved)
	assert.True(t, row.CostUnknownModel)
	assert.Equal(t, map[string]float64{"claude-opus-4": 3.2}, row.ModelCosts)
}

func TestImporter_ReparsesWhenOnlySubagentChanged(t *testing.T) {
	parentPath, subPath := writeParentWithSub(t,
		usageLine("2025-01-15T10:00:05Z", "claude-opus-4", 100, 10),
		usageLine("2025-01-15T10:00:02Z", "claude-haiku-4-5", 50, 5))
	info, err := os.Stat(parentPath)
	require.NoError(t, err)
	require.NoError(t, os.Chtimes(subPath, info.ModTime(), info.ModTime()))

	first := &stubCostRepo{mtimes: map[string]int64{"session-roll": info.ModTime().UnixNano()}}
	runImportSync(t, first, parentPath)
	assert.Empty(t, first.all(), "unchanged parent and subagent are skipped")

	later := info.ModTime().Add(time.Second)
	require.NoError(t, os.Chtimes(subPath, later, later))
	second := &stubCostRepo{mtimes: first.mtimes}
	runImportSync(t, second, parentPath)
	rows := second.all()
	require.Len(t, rows, 1)
	assert.Equal(t, later.UnixNano(), rows[0].SourceMtime)
}

func TestImporter_SubagentFilesAreNotRows(t *testing.T) {
	projects := t.TempDir()
	enc := filepath.Join(projects, "-repo")
	require.NoError(t, os.MkdirAll(filepath.Join(enc, "s1", "subagents"), 0o755))
	parentPath := writeSession(t, enc, "s1", makeJSONLContent(1, 1))
	require.NoError(t, os.WriteFile(filepath.Join(enc, "s1", "subagents", "agent-a.jsonl"), []byte(makeJSONLContent(1, 1)), 0o644))

	files, err := collectJSONLFiles(projects)
	require.NoError(t, err)
	assert.Equal(t, []string{parentPath}, files)
}

func TestSubagentFiles_AbsentDirIsNil(t *testing.T) {
	p := writeSession(t, t.TempDir(), "lonely", makeJSONLContent(1, 1))
	assert.Nil(t, subagentFiles(p))
}

func TestImporter_UnreadableSubagentIsSkipped(t *testing.T) {
	parentPath, subPath := writeParentWithSub(t,
		usageLine("2025-01-15T10:00:05Z", "claude-opus-4", 100, 10),
		usageLine("2025-01-15T10:00:02Z", "claude-haiku-4-5", 50, 5))
	require.NoError(t, os.Chmod(subPath, 0o000))
	if f, err := os.Open(subPath); err == nil {
		_ = f.Close()
		t.Skip("running with privileges that ignore file modes")
	}

	row, err := NewImporter(&stubCostRepo{}).extractCostRow(t.Context(), parentPath)
	require.NoError(t, err)
	require.NotNil(t, row)
	assert.Equal(t, 100, row.InputTokens)
}

func TestSubagentFiles_SkipsSymlinkToOversizeFile(t *testing.T) {
	parentPath, _ := writeParentWithSub(t,
		usageLine("2025-01-15T10:00:05Z", "claude-opus-4", 100, 10),
		usageLine("2025-01-15T10:00:02Z", "claude-haiku-4-5", 50, 5))
	big := filepath.Join(t.TempDir(), "big.jsonl")
	f, err := os.Create(big)
	require.NoError(t, err)
	require.NoError(t, f.Truncate(maxFileSizeBytes+1))
	require.NoError(t, f.Close())
	subDir := filepath.Join(strings.TrimSuffix(parentPath, ".jsonl"), "subagents")
	require.NoError(t, os.Symlink(big, filepath.Join(subDir, "agent-big.jsonl")))
	require.NoError(t, os.Symlink(t.TempDir(), filepath.Join(subDir, "agent-dir.jsonl")))

	assert.Equal(t, []string{filepath.Join(subDir, "agent-a.jsonl")}, subagentFiles(parentPath))
}

type touchingResolver struct {
	t    *testing.T
	path string
	at   time.Time
}

func (r touchingResolver) Resolve(_ context.Context, cwd string) (string, string) {
	require.NoError(r.t, os.Chtimes(r.path, r.at, r.at))
	return cwd, filepath.Base(cwd)
}

func TestExtractCostRow_SourceMtimeCapturedBeforeParse(t *testing.T) {
	line := `{"type":"assistant","timestamp":"2025-02-02T02:02:02.000Z","cwd":"/w/app","message":{"role":"assistant","model":"claude-opus-4","usage":{"input_tokens":3,"output_tokens":2}}}`
	parentPath, subPath := writeParentWithSub(t, line, usageLine("2025-02-02T02:02:03Z", "claude-haiku-4-5", 1, 1))
	before := time.Date(2025, 2, 2, 3, 0, 0, 0, time.UTC)
	require.NoError(t, os.Chtimes(parentPath, before, before))
	require.NoError(t, os.Chtimes(subPath, before, before))

	imp := NewImporter(&stubCostRepo{}).WithProjectResolver(touchingResolver{t: t, path: subPath, at: before.Add(time.Hour)})
	row, err := imp.extractCostRow(t.Context(), parentPath)
	require.NoError(t, err)
	require.NotNil(t, row)
	assert.Equal(t, before.UnixNano(), row.SourceMtime, "a write during the import must not be marked as imported")
}
