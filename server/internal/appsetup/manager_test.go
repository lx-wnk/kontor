package appsetup_test

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/lx-wnk/kontor/server/internal/appsetup"
	"github.com/lx-wnk/kontor/server/internal/mcpapps"
)

// TestHelperProcess is not a real test: Start re-executes the test binary
// with GO_WANT_HELPER_PROCESS=1 so a test can control what the "setup
// command" does. HELPER_MODE picks the behavior; "serve" reads the port the
// manager assigned from SETUP_PORT, which Start always sets on the child.
func TestHelperProcess(t *testing.T) {
	if os.Getenv("GO_WANT_HELPER_PROCESS") != "1" {
		return
	}
	defer os.Exit(0)
	switch os.Getenv("HELPER_MODE") {
	case "serve":
		port := os.Getenv("SETUP_PORT")
		mux := http.NewServeMux()
		mux.HandleFunc("/api/health", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
		_ = http.ListenAndServe("127.0.0.1:"+port, mux)
	case "exit":
		_, _ = os.Stderr.WriteString("setup failed: no config\n")
		os.Exit(1)
	case "silent":
		// Records its own pid so a test can prove the process was killed, not
		// merely forgotten: a child that never serves is invisible over HTTP.
		if path := os.Getenv("HELPER_PID_FILE"); path != "" {
			_ = os.WriteFile(path, []byte(strconv.Itoa(os.Getpid())), 0o600)
		}
		// A long sleep, not select{}: a test binary whose only goroutine blocks
		// forever trips the runtime's deadlock detector and the "unresponsive"
		// child would die at once, which is the opposite of this mode.
		time.Sleep(10 * time.Minute)
	}
}

func helperSetup() mcpapps.PresetSetup {
	return mcpapps.PresetSetup{
		Command:   os.Args[0],
		Args:      []string{"-test.run=TestHelperProcess", "--", "{port}"},
		Readiness: "/api/health",
	}
}

func helperEnv(mode string) map[string]string {
	return map[string]string{"GO_WANT_HELPER_PROCESS": "1", "HELPER_MODE": mode}
}

func TestManager_StartReturnsReachableSession(t *testing.T) {
	m := appsetup.NewManager(appsetup.Options{})
	sess, err := m.Start(context.Background(), "res-1", helperSetup(), helperEnv("serve"))
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = m.Stop("res-1") }()

	const wantWarning = "Setup is reachable on your local network until you click Done."
	if sess.Warning != wantWarning {
		t.Errorf("Warning = %q, want %q", sess.Warning, wantWarning)
	}
	if sess.URL != "http://127.0.0.1:"+strconv.Itoa(sess.Port) {
		t.Errorf("URL = %q, want it built from Port %d", sess.URL, sess.Port)
	}

	resp, err := http.Get(sess.URL + "/api/health")
	if err != nil {
		t.Fatalf("dial readiness path: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}

	got, ok := m.Get("res-1")
	if !ok || got.ResourceID != "res-1" || got.Port != sess.Port {
		t.Errorf("Get(res-1) = %+v, %v", got, ok)
	}
}

func TestManager_SecondStartStopsFirst(t *testing.T) {
	m := appsetup.NewManager(appsetup.Options{})
	first, err := m.Start(context.Background(), "res-2", helperSetup(), helperEnv("serve"))
	if err != nil {
		t.Fatalf("first Start: %v", err)
	}
	second, err := m.Start(context.Background(), "res-2", helperSetup(), helperEnv("serve"))
	if err != nil {
		t.Fatalf("second Start: %v", err)
	}
	defer func() { _ = m.Stop("res-2") }()

	if first.Port == second.Port {
		t.Fatalf("expected a fresh port on restart, both are %d", first.Port)
	}
	if _, err := http.Get(first.URL + "/api/health"); err == nil {
		t.Errorf("first session URL still answers after the second Start")
	}
}

func TestManager_StartFailsWhenCommandExitsImmediately(t *testing.T) {
	m := appsetup.NewManager(appsetup.Options{})
	_, err := m.Start(context.Background(), "res-exit", helperSetup(), helperEnv("exit"))
	if err == nil {
		t.Fatal("expected an error")
	}
	if !strings.Contains(err.Error(), "setup failed: no config") {
		t.Errorf("err = %v, want it to contain the child's stderr", err)
	}
}

func TestManager_StartFailsWhenReadinessTimesOut(t *testing.T) {
	m := appsetup.NewManager(appsetup.Options{
		ReadinessTimeout: 300 * time.Millisecond,
		PollInterval:     20 * time.Millisecond,
	})
	pidFile := filepath.Join(t.TempDir(), "helper.pid")
	env := helperEnv("silent")
	env["HELPER_PID_FILE"] = pidFile
	start := time.Now()
	_, err := m.Start(context.Background(), "res-silent", helperSetup(), env)
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("expected a timeout error")
	}
	if elapsed > 3*time.Second {
		t.Errorf("Start took %s — the unresponsive child was not killed promptly", elapsed)
	}
	if _, ok := m.Get("res-silent"); ok {
		t.Errorf("a session that never became ready must not be tracked")
	}

	// Forgetting the session is not enough — the process has to be gone, or a
	// setup command that hangs leaks a process per attempt.
	pid := 0
	for range 50 {
		if data, rerr := os.ReadFile(pidFile); rerr == nil {
			if pid, _ = strconv.Atoi(strings.TrimSpace(string(data))); pid > 0 {
				break
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	if pid == 0 {
		t.Fatal("the silent helper never recorded its pid")
	}
	gone := false
	for range 50 {
		if err := syscall.Kill(pid, 0); err != nil {
			gone = true
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !gone {
		_ = syscall.Kill(pid, syscall.SIGKILL)
		t.Fatalf("the child that missed its readiness deadline is still running (pid %d)", pid)
	}
}

func TestManager_StopTwiceIsNotAnError(t *testing.T) {
	m := appsetup.NewManager(appsetup.Options{})
	if err := m.Stop("never-started"); err != nil {
		t.Errorf("Stop on an unknown resource: %v", err)
	}
	if _, err := m.Start(context.Background(), "res-3", helperSetup(), helperEnv("serve")); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := m.Stop("res-3"); err != nil {
		t.Errorf("first Stop: %v", err)
	}
	if err := m.Stop("res-3"); err != nil {
		t.Errorf("second Stop: %v", err)
	}
}

func TestManager_SweepStopsIdleSessions(t *testing.T) {
	now := time.Now()
	m := appsetup.NewManager(appsetup.Options{
		Now:         func() time.Time { return now },
		IdleTimeout: time.Minute,
		MaxLifetime: time.Hour,
	})
	sess, err := m.Start(context.Background(), "res-idle", helperSetup(), helperEnv("serve"))
	if err != nil {
		t.Fatalf("Start: %v", err)
	}

	now = now.Add(2 * time.Minute)
	m.Sweep()

	if _, ok := m.Get("res-idle"); ok {
		t.Errorf("session should have been swept for idling")
	}
	if _, err := http.Get(sess.URL + "/api/health"); err == nil {
		t.Errorf("swept session's process should have been stopped")
	}
}

func TestManager_SweepStopsSessionsPastMaxLifetime(t *testing.T) {
	now := time.Now()
	m := appsetup.NewManager(appsetup.Options{
		Now:         func() time.Time { return now },
		IdleTimeout: time.Hour,
		MaxLifetime: time.Minute,
	})
	sess, err := m.Start(context.Background(), "res-life", helperSetup(), helperEnv("serve"))
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	// Touching the session keeps it fresh for IdleTimeout, isolating
	// MaxLifetime as the cause of the sweep below.
	if _, ok := m.Get("res-life"); !ok {
		t.Fatalf("expected the session to be tracked before it ages out")
	}

	now = now.Add(2 * time.Minute)
	m.Sweep()

	if _, ok := m.Get("res-life"); ok {
		t.Errorf("session should have been swept for exceeding MaxLifetime")
	}
	if _, err := http.Get(sess.URL + "/api/health"); err == nil {
		t.Errorf("swept session's process should have been stopped")
	}
}

func TestManager_StopAllEndsEverything(t *testing.T) {
	m := appsetup.NewManager(appsetup.Options{})
	sessA, err := m.Start(context.Background(), "res-a", helperSetup(), helperEnv("serve"))
	if err != nil {
		t.Fatalf("Start a: %v", err)
	}
	sessB, err := m.Start(context.Background(), "res-b", helperSetup(), helperEnv("serve"))
	if err != nil {
		t.Fatalf("Start b: %v", err)
	}

	m.StopAll()

	if _, ok := m.Get("res-a"); ok {
		t.Errorf("res-a still tracked after StopAll")
	}
	if _, ok := m.Get("res-b"); ok {
		t.Errorf("res-b still tracked after StopAll")
	}
	if _, err := http.Get(sessA.URL + "/api/health"); err == nil {
		t.Errorf("res-a process still serving after StopAll")
	}
	if _, err := http.Get(sessB.URL + "/api/health"); err == nil {
		t.Errorf("res-b process still serving after StopAll")
	}
}

// TestRunSweeps_EndsASessionNobodyClosed drives the loop past MaxLifetime
// rather than IdleTimeout on purpose: Get touches the idle clock, so polling
// with Get would keep an idle session alive forever, and the lifetime cap is
// the limit that holds even for a setup somebody keeps looking at.
func TestRunSweeps_EndsASessionNobodyClosed(t *testing.T) {
	var clock struct {
		sync.Mutex
		now time.Time
	}
	clock.now = time.Now()
	advance := func(d time.Duration) {
		clock.Lock()
		clock.now = clock.now.Add(d)
		clock.Unlock()
	}
	m := appsetup.NewManager(appsetup.Options{
		Now: func() time.Time {
			clock.Lock()
			defer clock.Unlock()
			return clock.now
		},
		ReadinessTimeout: 3 * time.Second,
		IdleTimeout:      time.Hour,
		MaxLifetime:      time.Minute,
		PollInterval:     20 * time.Millisecond,
	})
	t.Cleanup(m.StopAll)
	sess, err := m.Start(context.Background(), "res-1", helperSetup(), helperEnv("serve"))
	if err != nil {
		t.Fatalf("Start: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go m.RunSweeps(ctx, 20*time.Millisecond)

	advance(2 * time.Minute) // past MaxLifetime

	deadline := time.Now().Add(5 * time.Second)
	for {
		resp, derr := http.Get(sess.URL + "/api/health")
		if derr != nil {
			break
		}
		_ = resp.Body.Close()
		if time.Now().After(deadline) {
			t.Fatalf("the sweep loop must end a session nobody closed, and kill its child")
		}
		time.Sleep(50 * time.Millisecond)
	}
	if _, ok := m.Get("res-1"); ok {
		t.Fatalf("the swept session must be gone from the manager too")
	}
}
