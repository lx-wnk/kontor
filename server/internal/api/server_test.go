package api_test

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/lx-wnk/kontor/server/internal/api"
)

// freePort asks the OS for an unused loopback port and immediately releases
// it. A subsequent bind can, in principle, race another process for the same
// port, but this is the standard Go test idiom for picking an ephemeral port.
func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("freePort: %v", err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

// TestServer_Run_ShutsDownWhileSSEStreamOpen reproduces the desktop-quit hang:
// a streaming handler that only observes r.Context().Done() must unblock and
// let Shutdown return promptly once the Run context is cancelled, instead of
// burning the full shutdown timeout because net/http.Server.Shutdown never
// cancels in-flight request contexts on its own.
func TestServer_Run_ShutsDownWhileSSEStreamOpen(t *testing.T) {
	port := freePort(t)
	addr := fmt.Sprintf("127.0.0.1:%d", port)

	handlerStarted := make(chan struct{})
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		if _, err := w.Write([]byte("x")); err != nil {
			return
		}
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		close(handlerStarted)
		<-r.Context().Done()
	})

	const shutdownTimeout = 10 * time.Second
	srv := api.NewServer(addr, handler, shutdownTimeout)
	srv.SetRequestGrace(200 * time.Millisecond) // default grace (2s) would blow the 2s assertion below

	ctx, cancel := context.WithCancel(context.Background())
	runErrCh := make(chan error, 1)
	go func() {
		runErrCh <- srv.Run(ctx)
	}()

	client := &http.Client{Timeout: 0}
	var resp *http.Response
	deadline := time.Now().Add(5 * time.Second)
	for {
		var err error
		resp, err = client.Get("http://" + addr + "/stream")
		if err == nil {
			break
		}
		if time.Now().After(deadline) {
			cancel()
			t.Fatalf("server never became reachable: %v", err)
		}
		time.Sleep(20 * time.Millisecond)
	}
	defer resp.Body.Close()

	reader := bufio.NewReader(resp.Body)
	firstByte, err := reader.ReadByte()
	if err != nil {
		cancel()
		t.Fatalf("reading first byte from stream: %v", err)
	}
	if firstByte != 'x' {
		cancel()
		t.Fatalf("unexpected first byte: %q", firstByte)
	}

	select {
	case <-handlerStarted:
	case <-time.After(2 * time.Second):
		cancel()
		t.Fatal("handler never signalled it started streaming")
	}

	start := time.Now()
	cancel()

	select {
	case err := <-runErrCh:
		elapsed := time.Since(start)
		if err != nil {
			t.Fatalf("Run returned error after %v: %v", elapsed, err)
		}
		if elapsed >= 2*time.Second {
			t.Fatalf("Run took %v to return, expected well under the %v shutdown timeout", elapsed, shutdownTimeout)
		}
		t.Logf("Run returned nil after %v", elapsed)
	case <-time.After(shutdownTimeout + 2*time.Second):
		t.Fatal("Run did not return even after the shutdown timeout elapsed")
	}
}

// waitReachable blocks until addr accepts TCP connections, or fails the test
// once timeout elapses. There is no readiness signal from Server itself, so
// polling is the only option here; it is not used to synchronise assertions.
func waitReachable(t *testing.T, addr string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		conn, err := net.DialTimeout("tcp", addr, 200*time.Millisecond)
		if err == nil {
			conn.Close()
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("server never became reachable at %s: %v", addr, err)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// TestServer_Run_SlowRequestSurvivesShutdownGrace is the regression guard for
// the defect BaseContext-as-run-context introduced: an ordinary in-flight
// mutation must be allowed to finish even though shutdown started while it
// was running, instead of having its context cancelled out from under it the
// instant Run's context is cancelled.
func TestServer_Run_SlowRequestSurvivesShutdownGrace(t *testing.T) {
	port := freePort(t)
	addr := fmt.Sprintf("127.0.0.1:%d", port)

	const (
		grace           = 300 * time.Millisecond
		shutdownTimeout = 2 * time.Second
		handlerSleep    = 100 * time.Millisecond
	)

	handlerStarted := make(chan struct{})
	ctxCancelledDuringWork := make(chan bool, 1)
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(handlerStarted)
		time.Sleep(handlerSleep) // simulates in-flight work, e.g. a DB write
		ctxCancelledDuringWork <- (r.Context().Err() != nil)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("done"))
	})

	srv := api.NewServer(addr, handler, shutdownTimeout)
	srv.SetRequestGrace(grace)

	ctx, cancel := context.WithCancel(context.Background())
	runErrCh := make(chan error, 1)
	go func() {
		runErrCh <- srv.Run(ctx)
	}()

	waitReachable(t, addr, 5*time.Second)

	respCh := make(chan *http.Response, 1)
	reqErrCh := make(chan error, 1)
	go func() {
		client := &http.Client{Timeout: 5 * time.Second}
		resp, err := client.Get("http://" + addr + "/slow")
		if err != nil {
			reqErrCh <- err
			return
		}
		respCh <- resp
	}()

	select {
	case <-handlerStarted:
	case <-time.After(2 * time.Second):
		cancel()
		t.Fatal("handler never started")
	}

	// The handler is mid-sleep here (grace > handlerSleep), so this is
	// exactly the "shutdown starts while a request is in flight" case.
	cancel()

	var resp *http.Response
	select {
	case resp = <-respCh:
	case err := <-reqErrCh:
		t.Fatalf("request failed: %v", err)
	case <-time.After(3 * time.Second):
		t.Fatal("request did not complete")
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("reading response body: %v", err)
	}
	if string(body) != "done" {
		t.Fatalf("unexpected response body: %q", body)
	}

	select {
	case cancelledDuringWork := <-ctxCancelledDuringWork:
		if cancelledDuringWork {
			t.Fatal("handler's request context was cancelled before the grace window elapsed")
		}
	case <-time.After(1 * time.Second):
		t.Fatal("handler never reported its context state")
	}

	select {
	case err := <-runErrCh:
		if err != nil {
			t.Fatalf("Run returned error: %v", err)
		}
	case <-time.After(shutdownTimeout + 2*time.Second):
		t.Fatal("Run did not return even after the shutdown timeout elapsed")
	}
}

// TestServer_Run_StreamsReleasedAfterGrace confirms the grace window does not
// reintroduce the original quit hang: a streaming handler still unblocks and
// lets Run return once its context is cancelled after the grace window, well
// inside the shutdown timeout.
func TestServer_Run_StreamsReleasedAfterGrace(t *testing.T) {
	port := freePort(t)
	addr := fmt.Sprintf("127.0.0.1:%d", port)

	const (
		grace           = 200 * time.Millisecond
		shutdownTimeout = 3 * time.Second
	)

	handlerStarted := make(chan struct{})
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		close(handlerStarted)
		<-r.Context().Done()
	})

	srv := api.NewServer(addr, handler, shutdownTimeout)
	srv.SetRequestGrace(grace)

	ctx, cancel := context.WithCancel(context.Background())
	runErrCh := make(chan error, 1)
	go func() {
		runErrCh <- srv.Run(ctx)
	}()

	waitReachable(t, addr, 5*time.Second)

	resp, err := http.Get("http://" + addr + "/stream")
	if err != nil {
		cancel()
		t.Fatalf("GET /stream: %v", err)
	}
	defer resp.Body.Close()

	select {
	case <-handlerStarted:
	case <-time.After(2 * time.Second):
		cancel()
		t.Fatal("handler never signalled it started streaming")
	}

	start := time.Now()
	cancel()

	select {
	case err := <-runErrCh:
		elapsed := time.Since(start)
		if err != nil {
			t.Fatalf("Run returned error after %v: %v", elapsed, err)
		}
		if elapsed < grace {
			t.Fatalf("Run returned after %v, before the %v grace window even elapsed", elapsed, grace)
		}
		if elapsed >= shutdownTimeout {
			t.Fatalf("Run took %v, expected well inside the %v shutdown timeout", elapsed, shutdownTimeout)
		}
		t.Logf("Run returned nil after %v", elapsed)
	case <-time.After(shutdownTimeout + 2*time.Second):
		t.Fatal("Run did not return even after the shutdown timeout elapsed")
	}
}
