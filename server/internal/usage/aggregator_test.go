package usage_test

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lx-wnk/kontor/server/internal/usage"
)

func makeProjectDir(t *testing.T, configDir string) string {
	t.Helper()
	p := filepath.Join(configDir, "projects", "enc-proj-1")
	if err := os.MkdirAll(p, 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

func writeJSONL(t *testing.T, path string, lines []string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close() //nolint:errcheck
	for _, l := range lines {
		fmt.Fprintln(f, l)
	}
}

func assistantLine(ts time.Time, model string, input, output int) string {
	type usageCnts struct {
		Input       int `json:"input_tokens"`
		Output      int `json:"output_tokens"`
		CacheCreate int `json:"cache_creation_input_tokens"`
		CacheRead   int `json:"cache_read_input_tokens"`
	}
	type msg struct {
		Role  string    `json:"role"`
		Model string    `json:"model"`
		Usage usageCnts `json:"usage"`
	}
	type entry struct {
		Timestamp string `json:"timestamp"`
		Message   msg    `json:"message"`
	}
	b, _ := json.Marshal(entry{
		Timestamp: ts.UTC().Format(time.RFC3339Nano),
		Message:   msg{Role: "assistant", Model: model, Usage: usageCnts{Input: input, Output: output}},
	})
	return string(b)
}

// TestAggregate_Windows asserts messages are bucketed into the correct windows.
func TestAggregate_Windows(t *testing.T) {
	now := time.Date(2026, 6, 29, 12, 0, 0, 0, time.UTC)
	dir := t.TempDir()
	projDir := makeProjectDir(t, dir)

	inside5h := now.Add(-1 * time.Hour)
	outside5h := now.Add(-6 * time.Hour)

	writeJSONL(t, filepath.Join(projDir, "aaaaaaaa-0000-0000-0000-000000000001.jsonl"), []string{
		assistantLine(inside5h, "claude-sonnet-4-6", 1000, 500),                                     // 1500 tokens, inside 5h
		assistantLine(outside5h, "claude-sonnet-4-6", 2000, 100),                                    // 2100 tokens, 7d only
		`{"timestamp":"` + inside5h.UTC().Format(time.RFC3339Nano) + `","message":{"role":"user"}}`, // user msg: skip
		`{this is not valid json}`, // malformed: skip
	})

	agg := usage.NewAggregator(usage.Options{
		ConfigDirs: func() []string { return []string{dir} },
		Now:        func() time.Time { return now },
	})

	res, err := agg.Aggregate()
	if err != nil {
		t.Fatal(err)
	}

	if want := int64(1500); res.W5h.Tokens != want {
		t.Errorf("w5h tokens: got %d, want %d", res.W5h.Tokens, want)
	}
	if want := int64(3600); res.W7d.Tokens != want { // 1500 + 2100
		t.Errorf("w7d tokens: got %d, want %d", res.W7d.Tokens, want)
	}
}

// TestAggregate_Cost asserts cost matches pricing.EstimateCost for a known model.
func TestAggregate_Cost(t *testing.T) {
	// claude-sonnet-4-6: $3/M input + $15/M output
	// 1000 input + 500 output = 0.003 + 0.0075 = 0.0105 USD → 1 cent (rounded)
	now := time.Date(2026, 6, 29, 12, 0, 0, 0, time.UTC)
	dir := t.TempDir()
	projDir := makeProjectDir(t, dir)

	writeJSONL(t, filepath.Join(projDir, "aaaaaaaa-0000-0000-0000-000000000002.jsonl"), []string{
		assistantLine(now.Add(-1*time.Hour), "claude-sonnet-4-6", 1000, 500),
	})

	agg := usage.NewAggregator(usage.Options{
		ConfigDirs: func() []string { return []string{dir} },
		Now:        func() time.Time { return now },
	})
	res, err := agg.Aggregate()
	if err != nil {
		t.Fatal(err)
	}

	wantCents := int64(math.Round(0.0105 * 100)) // = 1
	if res.W5h.CostCents != wantCents {
		t.Errorf("w5h costCents: got %d, want %d", res.W5h.CostCents, wantCents)
	}
}

// TestAggregate_MultiDir asserts per-dir grouping and correct total.
func TestAggregate_MultiDir(t *testing.T) {
	now := time.Date(2026, 6, 29, 12, 0, 0, 0, time.UTC)
	dirA := t.TempDir()
	dirB := t.TempDir()

	writeJSONL(t, filepath.Join(dirA, "projects", "p1", "aaaaaaaa-0000-0000-0000-000000000003.jsonl"), []string{
		assistantLine(now.Add(-30*time.Minute), "claude-sonnet-4-6", 100, 0),
	})
	writeJSONL(t, filepath.Join(dirB, "projects", "p2", "aaaaaaaa-0000-0000-0000-000000000004.jsonl"), []string{
		assistantLine(now.Add(-30*time.Minute), "claude-sonnet-4-6", 200, 0),
	})

	agg := usage.NewAggregator(usage.Options{
		ConfigDirs: func() []string { return []string{dirA, dirB} },
		Now:        func() time.Time { return now },
	})
	res, err := agg.Aggregate()
	if err != nil {
		t.Fatal(err)
	}

	if len(res.Accounts) != 2 {
		t.Fatalf("expected 2 accounts, got %d", len(res.Accounts))
	}
	if want := int64(300); res.W5h.Tokens != want {
		t.Errorf("total w5h tokens: got %d, want %d", res.W5h.Tokens, want)
	}
}

// TestAggregate_FutureTimestamp asserts that a message dated after now (clock skew)
// is excluded from both windows.
func TestAggregate_FutureTimestamp(t *testing.T) {
	now := time.Date(2026, 6, 29, 12, 0, 0, 0, time.UTC)
	dir := t.TempDir()
	projDir := makeProjectDir(t, dir)

	writeJSONL(t, filepath.Join(projDir, "aaaaaaaa-0000-0000-0000-00000000000f.jsonl"), []string{
		assistantLine(now.Add(-1*time.Hour), "claude-sonnet-4-6", 1000, 0), // counted
		assistantLine(now.Add(2*time.Hour), "claude-sonnet-4-6", 5000, 0),  // future: excluded
	})

	agg := usage.NewAggregator(usage.Options{
		ConfigDirs: func() []string { return []string{dir} },
		Now:        func() time.Time { return now },
	})
	res, err := agg.Aggregate()
	if err != nil {
		t.Fatal(err)
	}
	if want := int64(1000); res.W5h.Tokens != want {
		t.Errorf("w5h tokens: got %d, want %d (future ts must be excluded)", res.W5h.Tokens, want)
	}
	if want := int64(1000); res.W7d.Tokens != want {
		t.Errorf("w7d tokens: got %d, want %d (future ts must be excluded)", res.W7d.Tokens, want)
	}
}

// TestAggregate_MtimeFilter asserts that JSONL files older than 7d are skipped.
func TestAggregate_MtimeFilter(t *testing.T) {
	now := time.Date(2026, 6, 29, 12, 0, 0, 0, time.UTC)
	dir := t.TempDir()
	projDir := makeProjectDir(t, dir)

	p := filepath.Join(projDir, "aaaaaaaa-0000-0000-0000-000000000005.jsonl")
	writeJSONL(t, p, []string{
		assistantLine(now.Add(-1*time.Hour), "claude-sonnet-4-6", 9999, 0),
	})
	oldTime := now.Add(-8 * 24 * time.Hour)
	if err := os.Chtimes(p, oldTime, oldTime); err != nil {
		t.Fatal(err)
	}

	agg := usage.NewAggregator(usage.Options{
		ConfigDirs: func() []string { return []string{dir} },
		Now:        func() time.Time { return now },
	})
	res, err := agg.Aggregate()
	if err != nil {
		t.Fatal(err)
	}
	if res.W7d.Tokens != 0 {
		t.Errorf("expected 0 tokens from mtime-filtered file, got %d", res.W7d.Tokens)
	}
}

// TestAggregate_Cache asserts the 60 s cache prevents redundant scans.
func TestAggregate_Cache(t *testing.T) {
	now := time.Date(2026, 6, 29, 12, 0, 0, 0, time.UTC)
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "projects", "p"), 0o755) //nolint:errcheck

	scanCount := 0
	agg := usage.NewAggregator(usage.Options{
		ConfigDirs: func() []string { return []string{dir} },
		Now:        func() time.Time { return now },
		OnScan:     func() { scanCount++ },
	})

	if _, err := agg.Aggregate(); err != nil {
		t.Fatal(err)
	}
	if _, err := agg.Aggregate(); err != nil {
		t.Fatal(err)
	}
	if scanCount != 1 {
		t.Errorf("expected 1 scan within 60 s, got %d", scanCount)
	}

	now = now.Add(61 * time.Second) // advance captured variable — closure sees update
	if _, err := agg.Aggregate(); err != nil {
		t.Fatal(err)
	}
	if scanCount != 2 {
		t.Errorf("expected 2 scans after cache expiry, got %d", scanCount)
	}
}

// TestAggregate_BasenameCollision asserts that two config dirs sharing a basename
// receive distinct account labels.
func TestAggregate_BasenameCollision(t *testing.T) {
	now := time.Date(2026, 6, 29, 12, 0, 0, 0, time.UTC)
	dirA := filepath.Join(t.TempDir(), ".claude")
	dirB := filepath.Join(t.TempDir(), ".claude")
	for _, d := range []string{dirA, dirB} {
		if err := os.MkdirAll(filepath.Join(d, "projects"), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	agg := usage.NewAggregator(usage.Options{
		ConfigDirs: func() []string { return []string{dirA, dirB} },
		Now:        func() time.Time { return now },
	})
	res, err := agg.Aggregate()
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Accounts) != 2 {
		t.Fatalf("want 2 accounts, got %d", len(res.Accounts))
	}
	if res.Accounts[0].Label == res.Accounts[1].Label {
		t.Errorf("accounts share label %q after basename collision", res.Accounts[0].Label)
	}
}

// TestAggregate_BasenameUnique asserts that a single dir keeps the bare basename.
func TestAggregate_BasenameUnique(t *testing.T) {
	now := time.Date(2026, 6, 29, 12, 0, 0, 0, time.UTC)
	dir := filepath.Join(t.TempDir(), ".claude")
	os.MkdirAll(filepath.Join(dir, "projects"), 0o755) //nolint:errcheck

	agg := usage.NewAggregator(usage.Options{
		ConfigDirs: func() []string { return []string{dir} },
		Now:        func() time.Time { return now },
	})
	res, err := agg.Aggregate()
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Accounts) != 1 {
		t.Fatalf("want 1 account, got %d", len(res.Accounts))
	}
	if res.Accounts[0].Label != ".claude" {
		t.Errorf("want bare basename .claude, got %q", res.Accounts[0].Label)
	}
}

// TestAggregate_Singleflight asserts that N concurrent cold-cache calls trigger
// exactly one real scan via singleflight de-duplication.
func TestAggregate_Singleflight(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "projects"), 0o755) //nolint:errcheck

	const N = 8
	var scanCount atomic.Int64
	// proceed gates the in-flight scan so all N goroutines block inside Aggregate
	// before the scan completes, making de-duplication observable.
	proceed := make(chan struct{})

	agg := usage.NewAggregator(usage.Options{
		ConfigDirs: func() []string { return []string{dir} },
		Now:        func() time.Time { return time.Date(2026, 6, 29, 12, 0, 0, 0, time.UTC) },
		OnScan:     func() { scanCount.Add(1); <-proceed },
	})

	var wg sync.WaitGroup
	wg.Add(N)
	for range N {
		go func() {
			defer wg.Done()
			_, _ = agg.Aggregate()
		}()
	}

	// Allow goroutines to reach Aggregate and block on the singleflight before
	// releasing the scan. 20 ms is generous for goroutine scheduling on all CI platforms.
	time.Sleep(20 * time.Millisecond)
	close(proceed)
	wg.Wait()

	if got := scanCount.Load(); got != 1 {
		t.Errorf("want 1 scan for %d concurrent cold-cache calls, got %d", N, got)
	}
}
