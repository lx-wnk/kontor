package channel

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/lx-wnk/kontor/server/internal/channelconfig"
)

func TestRunHeadlessPTY_WritesDiscoveryAndReportsPID(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	pidCh := make(chan int, 1)
	done := make(chan error, 1)
	go func() {
		done <- RunHeadlessPTY(ctx, "cat", nil, nil, "", func(pid int) { pidCh <- pid })
	}()

	var childPID int
	select {
	case childPID = <-pidCh:
	case <-time.After(5 * time.Second):
		t.Fatal("RunHeadlessPTY never reported a pid")
	}
	if childPID <= 0 {
		t.Fatalf("bad child pid %d", childPID)
	}

	discFile := filepath.Join(home, channelconfig.DiscoveryDir, strconv.Itoa(childPID)+".pty.json")
	var data []byte
	for i := 0; i < 100; i++ {
		if b, err := os.ReadFile(discFile); err == nil {
			data = b
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if data == nil {
		t.Fatalf("pty discovery file not written: %s", discFile)
	}
	if !strings.Contains(string(data), `"ptyInject":true`) {
		t.Errorf("discovery missing ptyInject:true: %s", data)
	}

	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("RunHeadlessPTY did not return after cancel")
	}
	if _, err := os.Stat(discFile); !os.IsNotExist(err) {
		t.Errorf("discovery file not cleaned up after exit")
	}
}

func TestRunHeadlessPTY_StampsLastOutputAt(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	pidCh := make(chan int, 1)
	go func() {
		_ = RunHeadlessPTY(ctx, "sh", []string{"-c", "while true; do echo tick; sleep 0.2; done"}, nil, "", func(p int) { pidCh <- p })
	}()
	pid := <-pidCh
	disc := filepath.Join(home, channelconfig.DiscoveryDir, strconv.Itoa(pid)+".pty.json")
	var lastOut string
	for i := 0; i < 150; i++ {
		if b, err := os.ReadFile(disc); err == nil {
			var m map[string]any
			if json.Unmarshal(b, &m) == nil {
				if v, ok := m["lastOutputAt"].(string); ok && v != "" {
					lastOut = v
					break
				}
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	if lastOut == "" {
		t.Fatal("lastOutputAt never stamped despite continuous output")
	}
	ts, err := time.Parse(time.RFC3339, lastOut)
	if err != nil {
		t.Fatalf("bad lastOutputAt %q: %v", lastOut, err)
	}
	if time.Since(ts) > 10*time.Second {
		t.Errorf("lastOutputAt stale: %v", ts)
	}
}
