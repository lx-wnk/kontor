package merger

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/lx-wnk/kontor/server/internal/channelconfig"
)

func TestRecentChannelOutput(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := filepath.Join(home, channelconfig.DiscoveryDir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}

	// recent pty output → working
	if err := os.WriteFile(filepath.Join(dir, "10.pty.json"),
		[]byte(`{"port":1,"ptyInject":true,"lastOutputAt":"`+time.Now().UTC().Format(time.RFC3339)+`"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if !recentChannelOutput(10) {
		t.Error("recent pty output → true")
	}

	// stale pty output → not working
	if err := os.WriteFile(filepath.Join(dir, "11.pty.json"),
		[]byte(`{"port":1,"ptyInject":true,"lastOutputAt":"`+time.Now().Add(-time.Minute).UTC().Format(time.RFC3339)+`"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if recentChannelOutput(11) {
		t.Error("stale pty output → false")
	}

	// no discovery files → false
	if recentChannelOutput(999) {
		t.Error("no files → false")
	}

	// tmux activity via seam
	prev := tmuxActivityFn
	tmuxActivityFn = func(pane string) (time.Time, bool) { return time.Now(), true }
	t.Cleanup(func() { tmuxActivityFn = prev })
	if err := os.WriteFile(filepath.Join(dir, "12.json"), []byte(`{"port":1,"tmuxPane":"%3"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if !recentChannelOutput(12) {
		t.Error("recent tmux activity → true")
	}

	// stale tmux activity → false
	tmuxActivityFn = func(pane string) (time.Time, bool) { return time.Now().Add(-time.Minute), true }
	if err := os.WriteFile(filepath.Join(dir, "13.json"), []byte(`{"port":1,"tmuxPane":"%4"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if recentChannelOutput(13) {
		t.Error("stale tmux activity → false")
	}
}
