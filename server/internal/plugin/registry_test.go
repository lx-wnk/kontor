package plugin_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lx-wnk/kontor/server/internal/plugin"
)

func TestRegistry_EmptyDir_LoadsNothing(t *testing.T) {
	dir := t.TempDir()
	r := plugin.New(dir)
	require.NoError(t, r.Load(context.Background(), plugin.Hooks{}))
	assert.Nil(t, r.FindByCapability(plugin.CapAuthProvider))
}

func TestRegistry_NonexistentDir_NoError(t *testing.T) {
	r := plugin.New("/does/not/exist")
	require.NoError(t, r.Load(context.Background(), plugin.Hooks{}))
}

func TestRegistry_EmptyPluginDir_Skipped(t *testing.T) {
	r := plugin.New("")
	require.NoError(t, r.Load(context.Background(), plugin.Hooks{}))
}

func TestRegistry_InvalidPluginID_Uppercase_Skipped(t *testing.T) {
	dir := t.TempDir()
	pluginDir := filepath.Join(dir, "my-plugin")
	require.NoError(t, os.MkdirAll(pluginDir, 0o755))

	desc := plugin.Descriptor{
		Contract:     plugin.CurrentContract,
		Name:         "Test Plugin",
		ID:           "MY-PLUGIN",
		Version:      "1.0.0",
		Capabilities: []string{plugin.CapRouteExtension},
		Addr:         "127.0.0.1:19001",
	}
	data, _ := json.Marshal(desc)
	require.NoError(t, os.WriteFile(filepath.Join(pluginDir, "plugin.json"), data, 0o644))

	r := plugin.New(dir)
	require.NoError(t, r.Load(context.Background(), plugin.Hooks{}))
	require.Nil(t, r.FindByCapability(plugin.CapRouteExtension))
}

func TestRegistry_InvalidPluginID_WithSpace_Skipped(t *testing.T) {
	dir := t.TempDir()
	pluginDir := filepath.Join(dir, "my-plugin")
	require.NoError(t, os.MkdirAll(pluginDir, 0o755))

	desc := plugin.Descriptor{
		Contract:     plugin.CurrentContract,
		Name:         "Test Plugin",
		ID:           "my plugin",
		Version:      "1.0.0",
		Capabilities: []string{plugin.CapRouteExtension},
		Addr:         "127.0.0.1:19001",
	}
	data, _ := json.Marshal(desc)
	require.NoError(t, os.WriteFile(filepath.Join(pluginDir, "plugin.json"), data, 0o644))

	r := plugin.New(dir)
	require.NoError(t, r.Load(context.Background(), plugin.Hooks{}))
	require.Nil(t, r.FindByCapability(plugin.CapRouteExtension))
}

func TestRegistry_NonLoopbackIP_Skipped(t *testing.T) {
	dir := t.TempDir()
	pluginDir := filepath.Join(dir, "valid-plugin")
	require.NoError(t, os.MkdirAll(pluginDir, 0o755))

	desc := plugin.Descriptor{
		Contract:     plugin.CurrentContract,
		Name:         "Test Plugin",
		ID:           "valid-plugin",
		Version:      "1.0.0",
		Capabilities: []string{plugin.CapRouteExtension},
		Addr:         "192.168.1.100:8080",
	}
	data, _ := json.Marshal(desc)
	require.NoError(t, os.WriteFile(filepath.Join(pluginDir, "plugin.json"), data, 0o644))

	r := plugin.New(dir)
	require.NoError(t, r.Load(context.Background(), plugin.Hooks{}))
	require.Nil(t, r.FindByCapability(plugin.CapRouteExtension))
}

func TestRegistry_PluginWithHealthy_Loaded(t *testing.T) {
	// Start a real HTTP server acting as a plugin.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	// Extract host:port from srv.URL
	addr := srv.Listener.Addr().String()

	dir := t.TempDir()
	pluginDir := filepath.Join(dir, "test-plugin")
	require.NoError(t, os.MkdirAll(pluginDir, 0o755))

	desc := plugin.Descriptor{
		Contract:     plugin.CurrentContract,
		Name:         "Test Plugin",
		ID:           "test-plugin",
		Version:      "1.0.0",
		Capabilities: []string{plugin.CapRouteExtension},
		Addr:         addr,
		// No Command — server already running
	}
	data, _ := json.Marshal(desc)
	require.NoError(t, os.WriteFile(filepath.Join(pluginDir, "plugin.json"), data, 0o644))

	r := plugin.New(dir)
	require.NoError(t, r.Load(context.Background(), plugin.Hooks{}))

	entry := r.FindByCapability(plugin.CapRouteExtension)
	require.NotNil(t, entry)
	assert.Equal(t, "test-plugin", entry.Descriptor.ID)
}

func writePluginJSON(t *testing.T, dir, id string, caps []string) {
	t.Helper()
	pluginDir := filepath.Join(dir, id)
	require.NoError(t, os.MkdirAll(pluginDir, 0o755))
	desc := plugin.Descriptor{
		Contract:     plugin.CurrentContract,
		Name:         "Test Plugin",
		ID:           id,
		Version:      "1.0.0",
		Capabilities: caps,
		Addr:         "127.0.0.1:0",
	}
	data, _ := json.Marshal(desc)
	require.NoError(t, os.WriteFile(filepath.Join(pluginDir, "plugin.json"), data, 0o644))
}

// startHealthStub starts an in-process HTTP server on 127.0.0.1 that returns
// 200 on /health, registers cleanup, and returns it for its address.
func startHealthStub(t *testing.T) *httptest.Server {
	t.Helper()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(ts.Close)
	return ts
}

// writePluginJSONAddr writes a plugin.json with the given addr and NO Command
// field, so the registry only health-probes and never spawns a subprocess.
func writePluginJSONAddr(t *testing.T, dir, id string, caps []string, addr string) {
	t.Helper()
	pluginDir := filepath.Join(dir, id)
	require.NoError(t, os.MkdirAll(pluginDir, 0o755))
	desc := plugin.Descriptor{
		Contract:     plugin.CurrentContract,
		Name:         "Test Plugin",
		ID:           id,
		Version:      "1.0.0",
		Capabilities: caps,
		Addr:         addr,
	}
	data, _ := json.Marshal(desc)
	require.NoError(t, os.WriteFile(filepath.Join(pluginDir, "plugin.json"), data, 0o644))
}

func TestRegistry_StartOneStopOne(t *testing.T) {
	dir := t.TempDir()
	ts := startHealthStub(t)
	addr := strings.TrimPrefix(ts.URL, "http://")
	writePluginJSONAddr(t, dir, "live-plugin", []string{plugin.CapRouteExtension}, addr)

	r := plugin.New(dir)
	r.SetEnabled(func(string) bool { return false })
	require.NoError(t, r.Load(context.Background(), plugin.Hooks{}))
	require.Nil(t, r.FindByCapability(plugin.CapRouteExtension))

	require.NoError(t, r.StartOne(context.Background(), "live-plugin"))
	require.NotNil(t, r.FindByCapability(plugin.CapRouteExtension))

	// Idempotent start.
	require.NoError(t, r.StartOne(context.Background(), "live-plugin"))
	require.NotNil(t, r.FindByCapability(plugin.CapRouteExtension))

	require.NoError(t, r.StopOne("live-plugin"))
	require.Nil(t, r.FindByCapability(plugin.CapRouteExtension))

	// Idempotent stop.
	require.NoError(t, r.StopOne("live-plugin"))
	require.Nil(t, r.FindByCapability(plugin.CapRouteExtension))
}

// writeHealthyPlugin creates a plugin dir under dir/{id}/plugin.json backed by
// a real in-process HTTP server that returns 200 on every path including /health.
// Reused by Tasks 2, 5, 6, 7, 8.
func writeHealthyPlugin(t *testing.T, dir, id string) {
	t.Helper()
	ts := startHealthStub(t)
	addr := strings.TrimPrefix(ts.URL, "http://")
	writePluginJSONAddr(t, dir, id, []string{plugin.CapRouteExtension}, addr)
}

func TestStartedPluginIsHealthy(t *testing.T) {
	dir := t.TempDir()
	writeHealthyPlugin(t, dir, "alive")

	r := plugin.New(dir)
	require.NoError(t, r.Load(context.Background(), plugin.Hooks{}))
	t.Cleanup(r.Shutdown)

	got, ok := r.Lookup("alive")
	require.True(t, ok)
	require.True(t, got.Healthy())
}

func TestLookupReturnsHealthyEntryCopy(t *testing.T) {
	r := plugin.New("")
	require.False(t, exists(r, "missing"))

	// Inject a running, healthy entry through the exported test seam.
	r.InjectEntryForTest(plugin.Descriptor{ID: "voice", Addr: "127.0.0.1:19010"}, true)

	got, ok := r.Lookup("voice")
	require.True(t, ok)
	require.Equal(t, "voice", got.Descriptor.ID)
	require.True(t, got.Healthy())

	_, ok = r.Lookup("nope")
	require.False(t, ok)
}

func exists(r *plugin.Registry, id string) bool {
	_, ok := r.Lookup(id)
	return ok
}

func TestRegistry_LoadSkipsDisabled(t *testing.T) {
	dir := t.TempDir()
	writePluginJSON(t, dir, "enabled-one", []string{plugin.CapRouteExtension})
	writePluginJSON(t, dir, "disabled-auth", []string{plugin.CapAuthProvider})

	r := plugin.New(dir)
	r.SetEnabled(func(id string) bool { return id == "enabled-one" })

	// Neither plugin actually starts (no Command/health), but the capability
	// recording must reflect ONLY enabled plugins.
	_ = r.Load(context.Background(), plugin.Hooks{})

	assert.False(t, r.HasAttemptedCapability(plugin.CapAuthProvider),
		"disabled auth_provider plugin must not be recorded as attempted")
}

// writeForkingPlugin builds a real plugin binary under dir/{id}/ that:
//  1. Starts a /health HTTP server on a free port.
//  2. Spawns `sleep 600` as a child WITHOUT a new pgid (inherits the plugin's group).
//  3. Writes the child PID to childPidFile so the test can assert it was reaped.
//
// The binary is pre-compiled via `go build` so it starts fast enough to pass
// the registry's 5-second health-check window.
// Returns the absolute path of the child-PID file.
func writeForkingPlugin(t *testing.T, dir, id string) (childPidFile string) {
	t.Helper()
	pluginDir := filepath.Join(dir, id)
	require.NoError(t, os.MkdirAll(pluginDir, 0o755))

	// Pick a free port once; close the listener before binding in the plugin.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	addr := ln.Addr().String()
	require.NoError(t, ln.Close())

	childPidFile = filepath.Join(pluginDir, "childpid")

	mainGo := `package main

import (
	"fmt"
	"net/http"
	"os"
	"os/exec"
)

func main() {
	addr := os.Args[1]
	pidFile := os.Args[2]

	// Spawn a long-lived child in the SAME process group (no Setpgid on child).
	child := exec.Command("sleep", "600")
	if err := child.Start(); err != nil {
		fmt.Fprintln(os.Stderr, "child start:", err)
		os.Exit(1)
	}
	if err := os.WriteFile(pidFile, []byte(fmt.Sprintf("%d", child.Process.Pid)), 0o644); err != nil {
		fmt.Fprintln(os.Stderr, "write pid:", err)
		os.Exit(1)
	}

	http.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	if err := http.ListenAndServe(addr, nil); err != nil {
		fmt.Fprintln(os.Stderr, "server:", err)
		os.Exit(1)
	}
}
`
	require.NoError(t, os.WriteFile(filepath.Join(pluginDir, "main.go"), []byte(mainGo), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(pluginDir, "go.mod"), []byte("module forker-plugin\n\ngo 1.21\n"), 0o644))

	// Pre-compile so the binary starts instantly and health check passes well within 5s.
	binPath := filepath.Join(pluginDir, "plugin-bin")
	build := exec.Command("go", "build", "-o", binPath, ".")
	build.Dir = pluginDir
	build.Env = append(os.Environ(), "CGO_ENABLED=0")
	out, buildErr := build.CombinedOutput()
	require.NoError(t, buildErr, "go build failed:\n%s", out)

	desc := plugin.Descriptor{
		Contract:     plugin.CurrentContract,
		Name:         "Test Plugin",
		ID:           id,
		Version:      "1.0.0",
		Capabilities: []string{plugin.CapRouteExtension},
		Addr:         addr,
		Command:      []string{"./plugin-bin", addr, childPidFile},
	}
	data, _ := json.Marshal(desc)
	require.NoError(t, os.WriteFile(filepath.Join(pluginDir, "plugin.json"), data, 0o644))

	return childPidFile
}

func readPidFile(t *testing.T, path string) int {
	t.Helper()
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	require.NoError(t, err)
	return pid
}

func processAlive(pid int) bool {
	p, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	return p.Signal(syscall.Signal(0)) == nil
}

// writeRealHealthyPlugin builds a real plugin binary under dir/{id}/ that serves
// GET /health → 200 and runs until killed. No child processes are spawned.
// Returns the addr the plugin listens on, so callers can probe for orphan processes.
func writeRealHealthyPlugin(t *testing.T, dir, id string) string {
	t.Helper()
	pluginDir := filepath.Join(dir, id)
	require.NoError(t, os.MkdirAll(pluginDir, 0o755))

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	addr := ln.Addr().String()
	require.NoError(t, ln.Close())

	mainGo := `package main

import (
	"net/http"
	"os"
)

func main() {
	addr := os.Args[1]
	http.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	_ = http.ListenAndServe(addr, nil)
}
`
	require.NoError(t, os.WriteFile(filepath.Join(pluginDir, "main.go"), []byte(mainGo), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(pluginDir, "go.mod"), []byte("module healthy-plugin\n\ngo 1.21\n"), 0o644))

	binPath := filepath.Join(pluginDir, "plugin-bin")
	build := exec.Command("go", "build", "-o", binPath, ".")
	build.Dir = pluginDir
	build.Env = append(os.Environ(), "CGO_ENABLED=0")
	out, buildErr := build.CombinedOutput()
	require.NoError(t, buildErr, "go build failed:\n%s", out)

	desc := plugin.Descriptor{
		Contract:     plugin.CurrentContract,
		Name:         "Test Plugin",
		ID:           id,
		Version:      "1.0.0",
		Capabilities: []string{plugin.CapRouteExtension},
		Addr:         addr,
		Command:      []string{"./plugin-bin", addr},
	}
	data, _ := json.Marshal(desc)
	require.NoError(t, os.WriteFile(filepath.Join(pluginDir, "plugin.json"), data, 0o644))
	return addr
}

func TestStopOneDoesNotRespawn(t *testing.T) {
	dir := t.TempDir()
	addr := writeRealHealthyPlugin(t, dir, "stoppable")

	r := plugin.New(dir)
	require.NoError(t, r.Load(context.Background(), plugin.Hooks{}))
	t.Cleanup(r.Shutdown)

	_, ok := r.Lookup("stoppable")
	require.True(t, ok, "plugin must be registered after load")

	require.NoError(t, r.StopOne("stoppable"))

	// Phase 1: wait for the plugin process to actually exit.
	require.Eventually(t, func() bool {
		conn, err := net.DialTimeout("tcp", addr, 50*time.Millisecond)
		if err != nil {
			return true // port is closed — process has exited
		}
		conn.Close()
		return false
	}, 3*time.Second, 50*time.Millisecond, "plugin process must exit after StopOne")

	// Phase 2: verify the watcher does not restart the process (watcher's 1s
	// backoff is within the 3s window, so any spurious restart will be caught).
	require.Never(t, func() bool {
		if _, ok := r.Lookup("stoppable"); ok {
			return true // entry reappeared in registry
		}
		conn, err := net.DialTimeout("tcp", addr, 50*time.Millisecond)
		if err == nil {
			conn.Close()
			return true // orphan process is listening
		}
		return false
	}, 3*time.Second, 50*time.Millisecond, "intentional stop must not respawn")
}

func TestGroupKillReapsChildren(t *testing.T) {
	dir := t.TempDir()
	childPidFile := writeForkingPlugin(t, dir, "forker")

	r := plugin.New(dir)
	require.NoError(t, r.Load(context.Background(), plugin.Hooks{}))
	_, ok := r.Lookup("forker")
	require.True(t, ok, "plugin must be registered after load")

	r.Shutdown()

	childPid := readPidFile(t, childPidFile)
	require.Eventually(t, func() bool {
		return !processAlive(childPid)
	}, 10*time.Second, 100*time.Millisecond, "group-kill must reap the plugin's child process")
}

// writeSigtermIgnoringPlugin builds a real plugin binary under dir/{id}/ that
// serves GET /health → 200 and explicitly ignores SIGTERM (signal.Ignore), so
// it only dies to SIGKILL. Used to exercise Shutdown's escalation path.
func writeSigtermIgnoringPlugin(t *testing.T, dir, id string) string {
	t.Helper()
	pluginDir := filepath.Join(dir, id)
	require.NoError(t, os.MkdirAll(pluginDir, 0o755))

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	addr := ln.Addr().String()
	require.NoError(t, ln.Close())

	mainGo := `package main

import (
	"net/http"
	"os"
	"os/signal"
	"syscall"
)

func main() {
	addr := os.Args[1]
	signal.Ignore(syscall.SIGTERM)
	http.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	_ = http.ListenAndServe(addr, nil)
}
`
	require.NoError(t, os.WriteFile(filepath.Join(pluginDir, "main.go"), []byte(mainGo), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(pluginDir, "go.mod"),
		[]byte("module sigterm-ignoring-plugin\n\ngo 1.21\n"), 0o644))

	binPath := filepath.Join(pluginDir, "plugin-bin")
	build := exec.Command("go", "build", "-o", binPath, ".")
	build.Dir = pluginDir
	build.Env = append(os.Environ(), "CGO_ENABLED=0")
	out, buildErr := build.CombinedOutput()
	require.NoError(t, buildErr, "go build failed:\n%s", out)

	desc := plugin.Descriptor{
		Contract:     plugin.CurrentContract,
		Name:         "Test Plugin",
		ID:           id,
		Version:      "1.0.0",
		Capabilities: []string{plugin.CapRouteExtension},
		Addr:         addr,
		Command:      []string{"./plugin-bin", addr},
	}
	data, _ := json.Marshal(desc)
	require.NoError(t, os.WriteFile(filepath.Join(pluginDir, "plugin.json"), data, 0o644))
	return addr
}

// listening reports whether something is accepting TCP connections on addr —
// used here to tell whether a plugin process is still alive, since it closes
// its listening socket the instant it exits (normally or via SIGKILL).
func listening(addr string) bool {
	conn, err := net.DialTimeout("tcp", addr, 200*time.Millisecond)
	if err != nil {
		return false
	}
	conn.Close()
	return true
}

// TestShutdownWaitsForSigtermExit is the direct regression test for the bug:
// gracefulStop used to run its wait-then-escalate step in a detached
// goroutine, so Shutdown returned before the process had actually exited.
// Because a process closes its listening socket the moment it exits, checking
// the socket immediately after Shutdown returns — with no polling/Eventually
// — proves Shutdown itself blocked until the process was gone.
func TestShutdownWaitsForSigtermExit(t *testing.T) {
	dir := t.TempDir()
	addr := writeRealHealthyPlugin(t, dir, "graceful")

	r := plugin.New(dir)
	require.NoError(t, r.Load(context.Background(), plugin.Hooks{}))
	_, ok := r.Lookup("graceful")
	require.True(t, ok, "plugin must be registered after load")

	r.Shutdown()

	require.False(t, listening(addr),
		"plugin must have already exited by the time Shutdown returns, not eventually")
}

// TestContextCancelDoesNotPreemptShutdownStop guards the ordering that makes
// the SIGTERM-first stop sequence reachable at all. exec.CommandContext's
// default Cancel is Process.Kill(), so cancelling the server context used to
// SIGKILL every plugin immediately — and on the ordinary quit path that
// happens well before cleanup() calls Shutdown, meaning no plugin ever saw a
// SIGTERM. The plugin must survive the cancellation and be stopped by
// Shutdown.
func TestContextCancelDoesNotPreemptShutdownStop(t *testing.T) {
	dir := t.TempDir()
	addr := writeRealHealthyPlugin(t, dir, "graceful-ctx")

	ctx, cancel := context.WithCancel(context.Background())
	r := plugin.New(dir)
	require.NoError(t, r.Load(ctx, plugin.Hooks{}))
	_, ok := r.Lookup("graceful-ctx")
	require.True(t, ok, "plugin must be registered after load")

	cancel()
	// os/exec's kill goroutine fires as soon as the context is done; this
	// window is long enough for it to have taken effect if it were still armed.
	time.Sleep(500 * time.Millisecond)
	require.True(t, listening(addr),
		"context cancellation must not kill the plugin — Shutdown owns the stop sequence")

	r.Shutdown()
	require.False(t, listening(addr), "Shutdown must stop the plugin after the context was cancelled")
}

// TestContextCancelBackstopOutlastsShutdownBudget pins the ordering between the
// two clocks. The backstop is armed at context cancellation, but Shutdown does
// not start until the run loop has drained — the HTTP server alone may spend
// shutdown.timeoutSeconds (default 10s) first. A backstop sized to Shutdown's
// own budget (gracefulStopTimeout + shutdownKillGrace = 7s) therefore fires
// before Shutdown is ever called, SIGKILLing a plugin that was never asked to
// stop. The plugin must still be alive well past that 7s mark.
func TestContextCancelBackstopOutlastsShutdownBudget(t *testing.T) {
	dir := t.TempDir()
	addr := writeRealHealthyPlugin(t, dir, "graceful-backstop")

	ctx, cancel := context.WithCancel(context.Background())
	r := plugin.New(dir)
	require.NoError(t, r.Load(ctx, plugin.Hooks{}))
	_, ok := r.Lookup("graceful-backstop")
	require.True(t, ok, "plugin must be registered after load")

	cancel()
	// Past gracefulStopTimeout + shutdownKillGrace, and past the 2s request
	// grace the HTTP drain needs before it can even return.
	time.Sleep(8 * time.Second)
	require.True(t, listening(addr),
		"plugin must outlive Shutdown's own budget — the drain can delay Shutdown past it")

	r.Shutdown()
	require.False(t, listening(addr), "Shutdown must still stop the plugin afterwards")
}

// TestShutdownKillsSigtermIgnoringPlugin covers the second consequence of the
// bug: the SIGKILL escalation lived in a goroutine detached from Shutdown, so
// it died with the process and a plugin that ignored SIGTERM was never
// reaped. Shutdown must still SIGKILL it and return within a bounded time.
func TestShutdownKillsSigtermIgnoringPlugin(t *testing.T) {
	dir := t.TempDir()
	addr := writeSigtermIgnoringPlugin(t, dir, "stubborn")

	r := plugin.New(dir)
	require.NoError(t, r.Load(context.Background(), plugin.Hooks{}))
	_, ok := r.Lookup("stubborn")
	require.True(t, ok, "plugin must be registered after load")

	start := time.Now()
	r.Shutdown()
	elapsed := time.Since(start)
	t.Logf("Shutdown for one SIGTERM-ignoring plugin took %s", elapsed)

	require.Less(t, elapsed, 9*time.Second,
		"Shutdown must not hang: it must escalate to SIGKILL and return, bounded")
	require.False(t, listening(addr), "SIGTERM-ignoring plugin must be SIGKILLed by Shutdown")
}

// TestShutdownSharesDeadlineAcrossStragglers is the regression guard for the
// shared-deadline requirement: several plugins that all ignore SIGTERM must
// cost one gracefulStopTimeout in total, not one per plugin. Without the
// shared deadline this would take roughly numStragglers*5s (here ~15s); with
// it, it stays near a single 5s wait plus the bounded post-SIGKILL grace.
func TestShutdownSharesDeadlineAcrossStragglers(t *testing.T) {
	dir := t.TempDir()
	const numStragglers = 3
	addrs := make([]string, numStragglers)
	for i := range addrs {
		addrs[i] = writeSigtermIgnoringPlugin(t, dir, fmt.Sprintf("stubborn-%d", i))
	}

	r := plugin.New(dir)
	require.NoError(t, r.Load(context.Background(), plugin.Hooks{}))
	for i := range addrs {
		_, ok := r.Lookup(fmt.Sprintf("stubborn-%d", i))
		require.True(t, ok, "plugin %d must be registered after load", i)
	}

	start := time.Now()
	r.Shutdown()
	elapsed := time.Since(start)
	t.Logf("Shutdown for %d SIGTERM-ignoring plugins took %s (would be ~%s per-plugin timeouts without a shared deadline)",
		numStragglers, elapsed, numStragglers*5*time.Second)

	require.Less(t, elapsed, 9*time.Second,
		"stragglers must share one deadline: %d plugins must not cost %d*5s", numStragglers, numStragglers)

	for i, addr := range addrs {
		require.False(t, listening(addr), "stubborn-%d must be SIGKILLed by Shutdown", i)
	}
}

// writeCrashingPlugin builds a real plugin binary under dir/{id}/ that serves
// GET /health → 200 briefly, then exits non-zero ~400ms after start. Every
// restart attempt will crash the same way, exhausting the registry restart budget.
func writeCrashingPlugin(t *testing.T, dir, id string) {
	t.Helper()
	pluginDir := filepath.Join(dir, id)
	require.NoError(t, os.MkdirAll(pluginDir, 0o755))

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	addr := ln.Addr().String()
	require.NoError(t, ln.Close())

	mainGo := `package main

import (
	"net/http"
	"os"
	"time"
)

func main() {
	addr := os.Args[1]
	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	srv := &http.Server{Addr: addr, Handler: mux}
	go func() { _ = srv.ListenAndServe() }()
	time.Sleep(400 * time.Millisecond)
	os.Exit(1)
}
`
	require.NoError(t, os.WriteFile(filepath.Join(pluginDir, "main.go"), []byte(mainGo), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(pluginDir, "go.mod"), []byte("module crashing-plugin\n\ngo 1.21\n"), 0o644))

	binPath := filepath.Join(pluginDir, "plugin-bin")
	build := exec.Command("go", "build", "-o", binPath, ".")
	build.Dir = pluginDir
	build.Env = append(os.Environ(), "CGO_ENABLED=0")
	out, buildErr := build.CombinedOutput()
	require.NoError(t, buildErr, "go build failed:\n%s", out)

	desc := plugin.Descriptor{
		Contract:     plugin.CurrentContract,
		Name:         "Test Plugin",
		ID:           id,
		Version:      "1.0.0",
		Capabilities: []string{plugin.CapRouteExtension},
		Addr:         addr,
		Command:      []string{"./plugin-bin", addr},
	}
	data, _ := json.Marshal(desc)
	require.NoError(t, os.WriteFile(filepath.Join(pluginDir, "plugin.json"), data, 0o644))
}

func TestWithTransientStartsStoppedPluginAndStops(t *testing.T) {
	dir := t.TempDir()
	writeHealthyPlugin(t, dir, "trans")

	r := plugin.New(dir)
	r.SetEnabled(func(string) bool { return false }) // not started at Load
	require.NoError(t, r.Load(context.Background(), plugin.Hooks{}))
	t.Cleanup(r.Shutdown)

	_, ok := r.Lookup("trans")
	require.False(t, ok, "precondition: plugin not running")

	ran := false
	err := r.WithTransient(context.Background(), "trans", func() error {
		_, up := r.Lookup("trans")
		require.True(t, up, "plugin must be running inside the callback")
		ran = true
		return nil
	})
	require.NoError(t, err)
	require.True(t, ran)

	require.Eventually(t, func() bool {
		_, up := r.Lookup("trans")
		return !up
	}, 3*time.Second, 50*time.Millisecond, "transient plugin must be stopped after the callback")
}

func TestWithTransientLeavesRunningPluginUp(t *testing.T) {
	dir := t.TempDir()
	writeHealthyPlugin(t, dir, "persistent")

	r := plugin.New(dir)
	require.NoError(t, r.Load(context.Background(), plugin.Hooks{}))
	t.Cleanup(r.Shutdown)

	require.NoError(t, r.WithTransient(context.Background(), "persistent", func() error { return nil }))

	_, up := r.Lookup("persistent")
	require.True(t, up, "an already-running plugin must stay up after WithTransient")
}

func TestStartOneRestartsUnhealthyPlugin(t *testing.T) {
	dir := t.TempDir()
	writeHealthyPlugin(t, dir, "dead-plugin")

	r := plugin.New(dir)
	r.SetEnabled(func(string) bool { return false }) // don't start at Load
	require.NoError(t, r.Load(context.Background(), plugin.Hooks{}))
	t.Cleanup(r.Shutdown)

	// Inject a synthetic unhealthy entry (simulates a crashed plugin that exhausted restarts).
	r.InjectEntryForTest(plugin.Descriptor{
		Contract:     plugin.CurrentContract,
		Name:         "Test Plugin",
		ID:           "dead-plugin",
		Version:      "1.0.0",
		Capabilities: []string{plugin.CapRouteExtension},
		Addr:         "127.0.0.1:19997",
	}, false)

	e, ok := r.Lookup("dead-plugin")
	require.True(t, ok, "precondition: entry is present")
	require.False(t, e.Healthy(), "precondition: entry is unhealthy")

	// StartOne must evict the dead entry and start a fresh healthy process.
	require.NoError(t, r.StartOne(context.Background(), "dead-plugin"))

	e, ok = r.Lookup("dead-plugin")
	require.True(t, ok, "entry must be present after restart")
	require.True(t, e.Healthy(), "restarted plugin must be healthy")
}

func TestWithTransientAgainstUnhealthyEntryRestarts(t *testing.T) {
	dir := t.TempDir()
	writeHealthyPlugin(t, dir, "trans-dead")

	r := plugin.New(dir)
	r.SetEnabled(func(string) bool { return false })
	require.NoError(t, r.Load(context.Background(), plugin.Hooks{}))
	t.Cleanup(r.Shutdown)

	// Inject a synthetic unhealthy entry.
	r.InjectEntryForTest(plugin.Descriptor{
		Contract:     plugin.CurrentContract,
		Name:         "Test Plugin",
		ID:           "trans-dead",
		Version:      "1.0.0",
		Capabilities: []string{plugin.CapRouteExtension},
		Addr:         "127.0.0.1:19996",
	}, false)

	ran := false
	err := r.WithTransient(context.Background(), "trans-dead", func() error {
		e, ok := r.Lookup("trans-dead")
		require.True(t, ok, "plugin must be present inside callback")
		require.True(t, e.Healthy(), "plugin must be healthy inside callback")
		ran = true
		return nil
	})
	require.NoError(t, err)
	require.True(t, ran, "callback must have executed")
}

func TestExhaustedRestartsMarkUnhealthy(t *testing.T) {
	dir := t.TempDir()
	writeCrashingPlugin(t, dir, "flaky")

	var unhealthyID string
	var mu sync.Mutex
	r := plugin.New(dir)
	require.NoError(t, r.Load(context.Background(), plugin.Hooks{
		OnUnhealthy: func(id string) { mu.Lock(); unhealthyID = id; mu.Unlock() },
	}))
	t.Cleanup(r.Shutdown)

	require.Eventually(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return unhealthyID == "flaky"
	}, 60*time.Second, 200*time.Millisecond, "OnUnhealthy must fire after exhausted restarts")

	entry, ok := r.Lookup("flaky")
	require.True(t, ok, "entry must be retained so the dispatcher can answer 503")
	require.False(t, entry.Healthy())
}

// writeEnvDumpPlugin builds a plugin binary that writes all PLUGIN_SETTING_*
// env vars to envFile before serving GET /health → 200.
// The env file is written synchronously before ListenAndServe so it exists
// by the time the registry health-check returns.
func writeEnvDumpPlugin(t *testing.T, dir, id string) (addr, envFile string) {
	t.Helper()
	pluginDir := filepath.Join(dir, id)
	require.NoError(t, os.MkdirAll(pluginDir, 0o755))

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	addr = ln.Addr().String()
	require.NoError(t, ln.Close())

	envFile = filepath.Join(pluginDir, "plugin-env.txt")

	mainGo := `package main

import (
	"net/http"
	"os"
	"strings"
)

func main() {
	addr := os.Args[1]
	envFile := os.Args[2]

	var lines []string
	for _, kv := range os.Environ() {
		if strings.HasPrefix(kv, "PLUGIN_SETTING_") {
			lines = append(lines, kv)
		}
	}
	_ = os.WriteFile(envFile, []byte(strings.Join(lines, "\n")), 0o644)

	http.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	_ = http.ListenAndServe(addr, nil)
}
`
	require.NoError(t, os.WriteFile(filepath.Join(pluginDir, "main.go"), []byte(mainGo), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(pluginDir, "go.mod"),
		[]byte("module env-dump-plugin\n\ngo 1.21\n"), 0o644))

	binPath := filepath.Join(pluginDir, "plugin-bin")
	build := exec.Command("go", "build", "-o", binPath, ".")
	build.Dir = pluginDir
	build.Env = append(os.Environ(), "CGO_ENABLED=0")
	out, buildErr := build.CombinedOutput()
	require.NoError(t, buildErr, "go build failed:\n%s", out)

	desc := plugin.Descriptor{
		Contract:     plugin.CurrentContract,
		Name:         "Test Plugin",
		ID:           id,
		Version:      "1.0.0",
		Capabilities: []string{plugin.CapRouteExtension},
		Addr:         addr,
		Settings: []plugin.SettingField{
			{Key: "api-key", Type: "string", Secret: true},
			{Key: "endpoint", Type: "url"},
		},
		Command: []string{"./plugin-bin", addr, envFile},
	}
	data, _ := json.Marshal(desc)
	require.NoError(t, os.WriteFile(filepath.Join(pluginDir, "plugin.json"), data, 0o644))
	return addr, envFile
}

func TestRegistry_SettingsInjectedAtStart(t *testing.T) {
	dir := t.TempDir()
	_, envFile := writeEnvDumpPlugin(t, dir, "env-plugin")

	r := plugin.New(dir)
	r.SetSettingsProvider(func(_ context.Context, _ string) (map[string]string, error) {
		return map[string]string{
			"api-key":  "s3cr3t",
			"endpoint": "https://example.com",
		}, nil
	})

	require.NoError(t, r.Load(context.Background(), plugin.Hooks{}))
	t.Cleanup(r.Shutdown)

	data, err := os.ReadFile(envFile)
	require.NoError(t, err)
	content := string(data)
	assert.Contains(t, content, "PLUGIN_SETTING_API_KEY=s3cr3t",
		"hyphen in key must be sanitized to underscore and uppercased")
	assert.Contains(t, content, "PLUGIN_SETTING_ENDPOINT=https://example.com")
}

func TestRegistry_NoProvider_NoSettingVars(t *testing.T) {
	dir := t.TempDir()
	_, envFile := writeEnvDumpPlugin(t, dir, "env-plugin-noprovider")

	r := plugin.New(dir) // no SetSettingsProvider

	require.NoError(t, r.Load(context.Background(), plugin.Hooks{}))
	t.Cleanup(r.Shutdown)

	data, err := os.ReadFile(envFile)
	require.NoError(t, err)
	assert.NotContains(t, string(data), "PLUGIN_SETTING_",
		"no provider means no PLUGIN_SETTING_ vars injected")
}
