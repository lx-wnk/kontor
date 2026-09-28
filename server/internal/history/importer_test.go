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

	"github.com/lx-wnk/kontor/server/internal/db/ent"
	"github.com/lx-wnk/kontor/server/internal/db/repo"
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

func TestParseTokensFromRaw_ValidJSONLWithUsage(t *testing.T) {
	ts := "2024-01-15T10:30:00.000Z"
	line := buildJSONLLine(100, 50, "claude-opus-4", ts)

	usage, model, lastActivity, _, err := parseTokensFromRaw(line)
	require.NoError(t, err)

	assert.Equal(t, 100, usage.InputTokens)
	assert.Equal(t, 50, usage.OutputTokens)
	assert.Equal(t, "claude-opus-4", model)
	// lastActivity should be the parsed timestamp.
	expected, _ := time.Parse(time.RFC3339Nano, ts)
	assert.True(t, lastActivity.Equal(expected) || lastActivity.After(expected),
		"lastActivity should be at or after the parsed timestamp")
}

func TestParseTokensFromRaw_MultipleEntriesAccumulatesTokens(t *testing.T) {
	ts1 := "2024-01-15T10:00:00.000Z"
	ts2 := "2024-01-15T11:00:00.000Z"
	line1 := buildJSONLLine(100, 50, "claude-sonnet-4-5", ts1)
	line2 := buildJSONLLine(200, 80, "claude-sonnet-4-5", ts2)
	raw := line1 + "\n" + line2

	usage, model, lastActivity, _, err := parseTokensFromRaw(raw)
	require.NoError(t, err)

	assert.Equal(t, 300, usage.InputTokens, "input tokens should be summed")
	assert.Equal(t, 130, usage.OutputTokens, "output tokens should be summed")
	assert.Equal(t, "claude-sonnet-4-5", model)

	// lastActivity should be ts2 (the later one).
	expected, _ := time.Parse(time.RFC3339Nano, ts2)
	assert.True(t, lastActivity.Equal(expected) || lastActivity.After(expected))
}

func TestParseTokensFromRaw_MissingUsageField(t *testing.T) {
	// Assistant message without "usage" field — should not panic and return zero tokens.
	raw := `{"type":"message","timestamp":"2024-01-15T10:00:00.000Z","message":{"role":"assistant","model":"claude-sonnet-4-5"}}`

	usage, _, _, _, err := parseTokensFromRaw(raw)
	require.NoError(t, err)
	assert.Equal(t, 0, usage.InputTokens)
	assert.Equal(t, 0, usage.OutputTokens)
}

func TestParseTokensFromRaw_NonAssistantRoleSkipped(t *testing.T) {
	// User messages should be ignored.
	raw := `{"type":"message","timestamp":"2024-01-15T10:00:00.000Z","message":{"role":"user","usage":{"input_tokens":999,"output_tokens":999}}}`

	usage, _, _, _, err := parseTokensFromRaw(raw)
	require.NoError(t, err)
	assert.Equal(t, 0, usage.InputTokens)
	assert.Equal(t, 0, usage.OutputTokens)
}

func TestParseTokensFromRaw_NonMessageTypeSkipped(t *testing.T) {
	// Non-turn entries (tool_result, attachment, …) must not contribute usage,
	// even when they carry an assistant role + usage block.
	raw := `{"type":"tool_result","timestamp":"2024-01-15T10:00:00.000Z","message":{"role":"assistant","usage":{"input_tokens":999,"output_tokens":999}}}`

	usage, _, _, _, err := parseTokensFromRaw(raw)
	require.NoError(t, err)
	assert.Equal(t, 0, usage.InputTokens)
	assert.Equal(t, 0, usage.OutputTokens)
}

// TestParseTokensFromRaw_AssistantType is the regression guard for the bug that
// left the cost table empty: real Claude Code logs write assistant turns with a
// top-level type of "assistant" (not "message"), and those entries carry the
// token usage. They must be counted.
func TestParseTokensFromRaw_AssistantType(t *testing.T) {
	raw := `{"type":"assistant","timestamp":"2024-01-15T10:00:00.000Z","message":{"role":"assistant","model":"claude-opus-4-7","usage":{"input_tokens":120,"output_tokens":45}}}`

	usage, model, _, _, err := parseTokensFromRaw(raw)
	require.NoError(t, err)
	assert.Equal(t, 120, usage.InputTokens)
	assert.Equal(t, 45, usage.OutputTokens)
	assert.Equal(t, "claude-opus-4-7", model)
}

func TestParseTokensFromRaw_MalformedJSONLineSkipped(t *testing.T) {
	// Malformed line followed by a valid line — the bad one is skipped, not an error.
	ts := "2024-01-15T12:00:00.000Z"
	validLine := buildJSONLLine(42, 21, "claude-opus-4", ts)
	raw := "THIS IS NOT JSON\n" + validLine

	usage, _, _, _, err := parseTokensFromRaw(raw)
	require.NoError(t, err, "malformed JSON should be skipped, not cause an error")
	assert.Equal(t, 42, usage.InputTokens)
	assert.Equal(t, 21, usage.OutputTokens)
}

func TestParseTokensFromRaw_EmptyInput(t *testing.T) {
	usage, model, _, _, err := parseTokensFromRaw("")
	require.NoError(t, err)
	assert.Equal(t, 0, usage.InputTokens)
	assert.Equal(t, 0, usage.OutputTokens)
	// Default model should be set.
	assert.NotEmpty(t, model)
}

func TestParseTokensFromRaw_AllBlankLines(t *testing.T) {
	raw := strings.Repeat("\n", 10)
	usage, _, _, _, err := parseTokensFromRaw(raw)
	require.NoError(t, err)
	assert.Equal(t, 0, usage.InputTokens)
	assert.Equal(t, 0, usage.OutputTokens)
}

func TestParseTokensFromRaw_ModelUpdatedToLatestEntry(t *testing.T) {
	ts1 := "2024-01-15T10:00:00.000Z"
	ts2 := "2024-01-15T11:00:00.000Z"
	line1 := buildJSONLLine(10, 5, "claude-sonnet-4-5", ts1)
	line2 := buildJSONLLine(10, 5, "claude-opus-4", ts2)
	raw := line1 + "\n" + line2

	_, model, _, _, err := parseTokensFromRaw(raw)
	require.NoError(t, err)
	// The last assistant entry's model wins.
	assert.Equal(t, "claude-opus-4", model)
}

func TestParseTokensFromRaw_AllMalformedLines(t *testing.T) {
	raw := "not json\nalso not json\n{broken"
	usage, _, _, _, err := parseTokensFromRaw(raw)
	require.NoError(t, err)
	assert.Equal(t, 0, usage.InputTokens)
	assert.Equal(t, 0, usage.OutputTokens)
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

// TestParseTokensFromReader_EquivalentToRaw verifies the streaming reader path
// returns identical results to the string-based wrapper.
func TestParseTokensFromReader_EquivalentToRaw(t *testing.T) {
	ts1 := "2025-01-15T10:30:00.000Z"
	ts2 := "2025-01-15T10:31:00.000Z"
	raw := buildJSONLLine(100, 50, "claude-opus-4", ts1) + "\n" +
		buildJSONLLine(200, 80, "claude-opus-4", ts2)

	rawUsage, rawModel, rawActivity, rawCwd, rawErr := parseTokensFromRaw(raw)
	rdUsage, rdModel, rdActivity, rdCwd, rdErr := parseTokensFromReader(strings.NewReader(raw))

	require.NoError(t, rawErr)
	require.NoError(t, rdErr)
	require.Equal(t, rawUsage, rdUsage)
	require.Equal(t, rawModel, rdModel)
	require.True(t, rawActivity.Equal(rdActivity))
	require.Equal(t, rawCwd, rdCwd)
	require.Equal(t, 300, rdUsage.InputTokens)
	require.Equal(t, 130, rdUsage.OutputTokens)
}

// TestParseTokensFromReader_LongLine exercises a single JSONL line far larger than
// bufio.Scanner's default 64 KB token limit, proving the enlarged buffer keeps the
// row instead of dropping it with bufio.ErrTooLong.
func TestParseTokensFromReader_LongLine(t *testing.T) {
	ts := "2025-01-15T10:30:00.000Z"
	// ~512 KB of filler embedded in a valid assistant entry's text content.
	filler := strings.Repeat("x", 512*1024)
	line := `{"type":"assistant","timestamp":"` + ts + `","cwd":"/work/proj","message":{"role":"assistant","model":"claude-opus-4","content":[{"type":"text","text":"` +
		filler + `"}],"usage":{"input_tokens":111,"output_tokens":22,"cache_creation_input_tokens":0,"cache_read_input_tokens":0}}}`

	usage, model, _, cwd, err := parseTokensFromReader(strings.NewReader(line))
	require.NoError(t, err)
	require.Equal(t, 111, usage.InputTokens)
	require.Equal(t, 22, usage.OutputTokens)
	require.Equal(t, "claude-opus-4", model)
	require.Equal(t, "/work/proj", cwd)
}
