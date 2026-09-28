package channel

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lx-wnk/kontor/server/internal/channelconfig"
)

// syncBuf is a goroutine-safe writer standing in for the pty master.
type syncBuf struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (s *syncBuf) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.Write(p)
}
func (s *syncBuf) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.String()
}

func TestPtyHTTPServer_InjectsMessageWithCR(t *testing.T) {
	w := &syncBuf{}
	srv, port, err := startPtyHTTPServer(newPtyWriter(w), newPtyHub(1024), newRotatingToken("secret-token"))
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { _ = srv.Shutdown(context.Background()) })

	url := fmt.Sprintf("http://127.0.0.1:%d/message", port)

	// Wrong token → 401, nothing written.
	req, _ := http.NewRequest("POST", url, strings.NewReader(`{"message":"x"}`))
	req.Header.Set("Authorization", "Bearer nope")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("req: %v", err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("want 401, got %d", resp.StatusCode)
	}

	// Correct token → message + CR written to the pty.
	req2, _ := http.NewRequest("POST", url, strings.NewReader(`{"message":"/security-review"}`))
	req2.Header.Set("Authorization", "Bearer secret-token")
	resp2, err := http.DefaultClient.Do(req2)
	if err != nil {
		t.Fatalf("req2: %v", err)
	}
	_, _ = io.Copy(io.Discard, resp2.Body)
	_ = resp2.Body.Close()
	if resp2.StatusCode != http.StatusOK {
		t.Fatalf("want 200, got %d", resp2.StatusCode)
	}

	// allow the handler's write to land
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) && w.String() == "" {
		time.Sleep(5 * time.Millisecond)
	}
	if got := w.String(); got != "/security-review\r" {
		t.Fatalf("pty got %q, want %q", got, "/security-review\r")
	}
}

// TestPtyHTTPServer_KeysWritesRawBytesNoCR asserts that POST /keys writes the
// request body to the pty verbatim — unlike /message, no CR is appended.
func TestPtyHTTPServer_KeysWritesRawBytesNoCR(t *testing.T) {
	w := &syncBuf{}
	srv, port, err := startPtyHTTPServer(newPtyWriter(w), newPtyHub(1024), newRotatingToken("secret-token"))
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { _ = srv.Shutdown(context.Background()) })

	url := fmt.Sprintf("http://127.0.0.1:%d/keys", port)

	// No bearer token → 401, nothing written.
	req, _ := http.NewRequest("POST", url, strings.NewReader("1"))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("req: %v", err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("want 401, got %d", resp.StatusCode)
	}

	// Correct token → raw bytes written verbatim, no CR appended.
	req2, _ := http.NewRequest("POST", url, strings.NewReader("1\t\r"))
	req2.Header.Set("Authorization", "Bearer secret-token")
	resp2, err := http.DefaultClient.Do(req2)
	if err != nil {
		t.Fatalf("req2: %v", err)
	}
	_, _ = io.Copy(io.Discard, resp2.Body)
	_ = resp2.Body.Close()
	if resp2.StatusCode != http.StatusOK {
		t.Fatalf("want 200, got %d", resp2.StatusCode)
	}

	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) && w.String() == "" {
		time.Sleep(5 * time.Millisecond)
	}
	if got := w.String(); got != "1\t\r" {
		t.Fatalf("pty got %q, want %q", got, "1\t\r")
	}
}

// TestWritePtyDiscovery_WritesPtyJsonFile asserts that writePtyDiscovery writes
// a path ending in ".pty.json" (not ".json") so it never collides with the
// channel bridge's {pid}.json file.
func TestWritePtyDiscovery_WritesPtyJsonFile(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	discPath, err := writePtyDiscovery(12345, 9999, "test-token", time.Now())
	if err != nil {
		t.Fatalf("writePtyDiscovery: %v", err)
	}
	t.Cleanup(func() { _ = os.Remove(discPath) })

	// Path must end in .pty.json, not .json.
	if !strings.HasSuffix(discPath, ".pty.json") {
		t.Errorf("expected path ending in .pty.json, got %q", discPath)
	}

	// File must exist and contain ptyInject:true.
	data, err := os.ReadFile(discPath)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if !strings.Contains(string(data), `"ptyInject":true`) {
		t.Errorf("expected ptyInject:true in discovery file, got %s", data)
	}

	// The file must live in the canonical discovery directory.
	expectedDir := filepath.Join(home, channelconfig.DiscoveryDir)
	if filepath.Dir(discPath) != expectedDir {
		t.Errorf("expected file in %q, got %q", expectedDir, filepath.Dir(discPath))
	}
}
