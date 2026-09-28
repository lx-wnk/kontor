package claudeconfig_test

import (
	"context"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lx-wnk/kontor/server/internal/claudeconfig"
)

func setDebounce(t *testing.T, d time.Duration) {
	t.Helper()
	orig := claudeconfig.WatchDebounce
	claudeconfig.WatchDebounce = d
	t.Cleanup(func() { claudeconfig.WatchDebounce = orig })
}

func configPath(t *testing.T) string {
	t.Helper()
	path, err := claudeconfig.JSONPath()
	if err != nil {
		t.Fatalf("JSONPath: %v", err)
	}
	return path
}

func writeConfig(t *testing.T, path string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(`{}`), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
}

func TestWatchDebouncesBurst(t *testing.T) {
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
	setDebounce(t, 20*time.Millisecond)
	path := configPath(t)
	writeConfig(t, path)

	var calls atomic.Int32
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := claudeconfig.Watch(ctx, func() { calls.Add(1) }); err != nil {
		t.Fatalf("Watch: %v", err)
	}

	writeConfig(t, path)
	time.Sleep(5 * time.Millisecond)
	writeConfig(t, path)

	time.Sleep(10 * claudeconfig.WatchDebounce)
	if got := calls.Load(); got != 1 {
		t.Fatalf("calls = %d, want 1", got)
	}
}

func TestWatchReportsLaterWrite(t *testing.T) {
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
	setDebounce(t, 20*time.Millisecond)
	path := configPath(t)
	writeConfig(t, path)

	var calls atomic.Int32
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := claudeconfig.Watch(ctx, func() { calls.Add(1) }); err != nil {
		t.Fatalf("Watch: %v", err)
	}

	writeConfig(t, path)
	time.Sleep(10 * claudeconfig.WatchDebounce)
	if got := calls.Load(); got != 1 {
		t.Fatalf("calls after first write = %d, want 1", got)
	}

	writeConfig(t, path)
	time.Sleep(10 * claudeconfig.WatchDebounce)
	if got := calls.Load(); got != 2 {
		t.Fatalf("calls after second write = %d, want 2", got)
	}
}

func TestWatchStartsWithoutExistingFile(t *testing.T) {
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
	setDebounce(t, 20*time.Millisecond)
	path := configPath(t)

	var calls atomic.Int32
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := claudeconfig.Watch(ctx, func() { calls.Add(1) }); err != nil {
		t.Fatalf("Watch: %v", err)
	}

	writeConfig(t, path)
	time.Sleep(10 * claudeconfig.WatchDebounce)
	if got := calls.Load(); got != 1 {
		t.Fatalf("calls = %d, want 1", got)
	}
}

func TestWatchStopsOnCancel(t *testing.T) {
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
	setDebounce(t, 20*time.Millisecond)
	path := configPath(t)
	writeConfig(t, path)

	var calls atomic.Int32
	ctx, cancel := context.WithCancel(context.Background())
	if err := claudeconfig.Watch(ctx, func() { calls.Add(1) }); err != nil {
		t.Fatalf("Watch: %v", err)
	}

	writeConfig(t, path)
	time.Sleep(10 * claudeconfig.WatchDebounce)
	before := calls.Load()

	cancel()
	time.Sleep(10 * claudeconfig.WatchDebounce)
	writeConfig(t, path)
	time.Sleep(10 * claudeconfig.WatchDebounce)
	if got := calls.Load(); got != before {
		t.Fatalf("calls after cancel = %d, want %d", got, before)
	}
}
