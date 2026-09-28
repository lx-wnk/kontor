package agents

import (
	"bufio"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/lx-wnk/kontor/server/internal/channelconfig"
	"github.com/lx-wnk/kontor/server/internal/db/ent"
	"github.com/lx-wnk/kontor/server/internal/services"
)

// stubPIDExec replaces the transport with `sh -c 'echo $$'`: the pty path
// reads the printed pid, then the watcher sees sh exit. No agent is started.
func stubPIDExec(t *testing.T) *[]string {
	t.Helper()
	return stubPIDExecScript(t, "echo $$")
}

// stubPIDExecSleep is like stubPIDExec but keeps the stub process alive for
// sleepSeconds (or until signalled), so a test can observe exit timing and
// termination instead of an instant exit.
func stubPIDExecSleep(t *testing.T, sleepSeconds int) *[]string {
	t.Helper()
	return stubPIDExecScript(t, "echo $$; exec sleep "+strconv.Itoa(sleepSeconds))
}

func stubPIDExecScript(t *testing.T, script string) *[]string {
	t.Helper()
	prevLook := lookTmuxPath
	lookTmuxPath = func() string { return "" }
	sh, err := exec.LookPath("sh")
	require.NoError(t, err)
	var captured []string
	orig := execStart
	execStart = func(cmd *exec.Cmd) error {
		captured = slices.Clone(cmd.Args)
		cmd.Path, cmd.Args, cmd.Err = sh, []string{sh, "-c", script}, nil
		return cmd.Start()
	}
	t.Cleanup(func() { execStart, lookTmuxPath = orig, prevLook })
	return &captured
}

// writeBridgeDiscoveryFile writes a channel-bridge discovery file in the bridge's
// real JSON shape (channel.writeDiscovery's entry map), naming channelPid as
// the bridge process for parentPid. Used both for a genuinely live bridge and
// (by the caller choosing an unrelated channelPid) for a stale one.
func writeBridgeDiscoveryFile(t *testing.T, home string, parentPid, channelPid int) {
	t.Helper()
	dir := filepath.Join(home, channelconfig.DiscoveryDir)
	require.NoError(t, os.MkdirAll(dir, 0o700))
	entry := map[string]any{
		"port":       0,
		"channelPid": channelPid,
		"parentPid":  parentPid,
		"cwd":        home,
		"token":      "stale-token",
		"startedAt":  time.Now().UTC().Format(time.RFC3339),
	}
	data, err := json.Marshal(entry)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(channelconfig.DiscoveryFile(home, parentPid), data, 0o600))
}

func sessionHome(t *testing.T) string {
	t.Helper()
	home, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	t.Setenv("HOME", home)
	return home
}

func TestSpawnSession_BuildsKontorArgsAndReportsExit(t *testing.T) {
	home := sessionHome(t)
	captured := stubPIDExec(t)
	cwd := filepath.Join(home, "session")
	require.NoError(t, os.MkdirAll(cwd, 0o700))

	m := NewSpawnManager(0, 0, 0, 0, nil, services.NewSpawnPolicy(nil))
	exited := make(chan int, 1)
	pid, err := m.SpawnSession(t.Context(), SessionSpawnOptions{
		Cwd: cwd, Prompt: "hallo", AppendSystemPrompt: "brief", Name: "Kontor",
		MCPConfigPath: "/tmp/kontor.json",
		AllowedTools:  []string{"mcp__kontor-tasks__get_task", "mcp__kontor-tasks__list_tasks"},
		OnExit:        func(p int) { exited <- p },
	})
	require.NoError(t, err)
	require.Positive(t, pid)

	args := *captured
	i := slices.Index(args, "--session-id")
	require.GreaterOrEqual(t, i, 0)
	_, err = uuid.Parse(args[i+1])
	require.NoError(t, err)
	require.True(t, containsConsecutive(args, "--permission-mode", "auto"))
	require.True(t, containsConsecutive(args, "--append-system-prompt", "brief"))
	require.True(t, containsConsecutive(args, "-n", "Kontor"))
	require.True(t, containsConsecutive(args, "--allowedTools", "mcp__kontor-tasks__get_task"))
	require.True(t, containsConsecutive(args, "mcp__kontor-tasks__get_task", "mcp__kontor-tasks__list_tasks"))
	require.True(t, containsConsecutive(args, "--mcp-config", "/tmp/kontor.json"))
	require.Equal(t, []string{"--strict-mcp-config", "--", "hallo"}, args[len(args)-3:],
		"the prompt must follow a boolean flag and a -- terminator, never the variadic --allowedTools/--mcp-config")

	select {
	case got := <-exited:
		require.Equal(t, pid, got)
	case <-time.After(10 * time.Second):
		t.Fatal("OnExit was not called after the process exited")
	}
}

// A prompt that happens to look like a flag must still reach claude as the
// positional prompt, not be parsed as an option — which could silently
// override the pinned --permission-mode auto.
func TestSpawnSession_FlagShapedPromptStaysPositional(t *testing.T) {
	home := sessionHome(t)
	captured := stubPIDExec(t)
	cwd := filepath.Join(home, "session")
	require.NoError(t, os.MkdirAll(cwd, 0o700))

	m := NewSpawnManager(0, 0, 0, 0, nil, services.NewSpawnPolicy(nil))
	_, err := m.SpawnSession(t.Context(), SessionSpawnOptions{
		Cwd: cwd, Prompt: "--dangerously-skip-permissions", Name: "Kontor", MCPConfigPath: "/tmp/k.json",
	})
	require.NoError(t, err)

	args := *captured
	require.Equal(t, []string{"--strict-mcp-config", "--", "--dangerously-skip-permissions"}, args[len(args)-3:])
}

func TestSpawnSession_PolicyRefusalSpawnsNothing(t *testing.T) {
	home := sessionHome(t)
	captured := stubPIDExec(t)
	cwd := filepath.Join(home, ".ssh")
	require.NoError(t, os.MkdirAll(cwd, 0o700))

	m := NewSpawnManager(0, 0, 0, 0, nil, services.NewSpawnPolicy(nil))
	_, err := m.SpawnSession(t.Context(), SessionSpawnOptions{Cwd: cwd, Prompt: "x", Name: "Kontor", MCPConfigPath: "/tmp/k.json"})
	require.ErrorIs(t, err, services.ErrCwdBlacklisted)
	require.Nil(t, *captured)
}

// A Kontor session always runs in auto mode, whatever posture the default
// spawner carries: the tile has no terminal to answer a permission prompt in.
func TestSpawnSession_OverridesTheSpawnersPermissionPosture(t *testing.T) {
	home := sessionHome(t)
	captured := stubPIDExec(t)
	stored := []string{"--permission-mode", "plan", "--dangerously-skip-permissions", "--verbose"}
	row := &ent.Spawner{ID: "d", Name: "auto", AdapterType: "claude", Command: "claude", IsDefault: true, Args: slices.Clone(stored)}
	m := NewSpawnManager(0, 0, 0, 0, &fakeSpawnerRepo{byID: map[string]*ent.Spawner{"d": row}}, services.NewSpawnPolicy(nil))

	_, err := m.SpawnSession(t.Context(), SessionSpawnOptions{Cwd: home, Name: "Kontor", MCPConfigPath: "/tmp/k.json"})
	require.NoError(t, err)
	args := *captured
	require.True(t, containsConsecutive(args, "--permission-mode", "auto"))
	require.NotContains(t, args, "plan")
	require.NotContains(t, args, "--dangerously-skip-permissions")
	require.Contains(t, args, "--verbose", "other spawner args survive")
	require.Equal(t, "--strict-mcp-config", args[len(args)-1], "an empty prompt adds no positional argument")
	require.Equal(t, stored, row.Args, "the stored spawner row is not mutated")
}

func TestTerminateSession_RefusesAPidItDidNotSpawn(t *testing.T) {
	sessionHome(t)
	sleeper := exec.Command("sleep", "30")
	require.NoError(t, sleeper.Start())
	t.Cleanup(func() { _ = sleeper.Process.Kill(); _ = sleeper.Wait() })

	m := NewSpawnManager(0, 0, 0, 0, nil, services.NewSpawnPolicy(nil))
	require.Error(t, m.TerminateSession(sleeper.Process.Pid))
	require.True(t, ProcessAlive(sleeper.Process.Pid))
}

// The channel bridge only removes its discovery file on clean shutdown, so a
// crashed bridge can leave a stale <pid>.json naming a pid the OS has since
// reused for an unrelated process. channelPid here (this test binary's own
// pid) is alive but is not sleeper's child, so the file's mere existence must
// not be enough for TerminateSession to act on it.
func TestTerminateSession_RefusesAStalePidWithAnUnrelatedDiscoveryFile(t *testing.T) {
	home := sessionHome(t)
	sleeper := exec.Command("sleep", "30")
	require.NoError(t, sleeper.Start())
	t.Cleanup(func() { _ = sleeper.Process.Kill(); _ = sleeper.Wait() })

	writeBridgeDiscoveryFile(t, home, sleeper.Process.Pid, os.Getpid())

	m := NewSpawnManager(0, 0, 0, 0, nil, services.NewSpawnPolicy(nil))
	require.Error(t, m.TerminateSession(sleeper.Process.Pid))
	require.True(t, ProcessAlive(sleeper.Process.Pid))
}

// Every other TerminateSession/OwnsLiveSession test exercises the refuse
// path; if bridgeConfirmsParent's pid comparison were wrong (or it read the
// wrong JSON key) the whole suite would stay green while boot Reconcile
// quietly ended every live, reattached Kontor session. This is the one test
// that requires OwnsLiveSession to say yes: a fresh manager (nothing in
// spawnStore) is handed a real parent/child pair — sh backgrounds sleep and
// waits on it — and a discovery file naming sleep as sh's channel bridge.
func TestOwnsLiveSession_AcceptsAGenuinelyReattachedSession(t *testing.T) {
	home := sessionHome(t)

	sh := exec.Command("sh", "-c", "sleep 30 & echo $!; wait")
	stdout, err := sh.StdoutPipe()
	require.NoError(t, err)
	require.NoError(t, sh.Start())
	t.Cleanup(func() { _ = sh.Process.Kill(); _ = sh.Wait() })

	line, err := bufio.NewReader(stdout).ReadString('\n')
	require.NoError(t, err)
	sleepPid, err := strconv.Atoi(strings.TrimSpace(line))
	require.NoError(t, err)
	t.Cleanup(func() { _ = syscall.Kill(sleepPid, syscall.SIGKILL) })

	writeBridgeDiscoveryFile(t, home, sh.Process.Pid, sleepPid)

	m := NewSpawnManager(0, 0, 0, 0, nil, services.NewSpawnPolicy(nil))
	require.True(t, m.OwnsLiveSession(sh.Process.Pid))
}

// OnExit must only fire once the process is actually gone. The stub in
// TestSpawnSession_BuildsKontorArgsAndReportsExit exits in milliseconds, so a
// buggy implementation that fired OnExit right after launch — without
// waiting for exit — would also pass it. Here the stub outlives the launch
// call by a full second, so a premature OnExit is caught red-handed: the
// process would still be alive at the moment the callback runs.
func TestSpawnSession_OnExitFiresOnlyAfterTheProcessIsGone(t *testing.T) {
	home := sessionHome(t)
	stubPIDExecSleep(t, 1)
	cwd := filepath.Join(home, "session")
	require.NoError(t, os.MkdirAll(cwd, 0o700))

	m := NewSpawnManager(0, 0, 0, 0, nil, services.NewSpawnPolicy(nil))
	done := make(chan bool, 1)
	pid, err := m.SpawnSession(t.Context(), SessionSpawnOptions{
		Cwd: cwd, Name: "Kontor", MCPConfigPath: "/tmp/k.json",
		OnExit: func(p int) { done <- ProcessAlive(p) },
	})
	require.NoError(t, err)
	require.Positive(t, pid)

	select {
	case stillAlive := <-done:
		require.False(t, stillAlive, "OnExit fired while the process was still alive")
	case <-time.After(10 * time.Second):
		t.Fatal("OnExit was not called")
	}
}

// SpawnSession must always use the pty transport, even with tmux on PATH:
// only the pty host writes <pid>.pty.json, which the Kontor tile's terminal
// route needs (terminal.go -> TerminalTarget). If SpawnSession picked tmux
// whenever it is installed, the terminal would 409 forever.
func TestSpawnSession_AlwaysUsesThePtyHostEvenWithTmuxOnPath(t *testing.T) {
	home := sessionHome(t)
	prevLook := lookTmuxPath
	lookTmuxPath = func() string { return "/usr/bin/tmux" }
	t.Cleanup(func() { lookTmuxPath = prevLook })

	sh, err := exec.LookPath("sh")
	require.NoError(t, err)
	var captured []string
	orig := execStart
	execStart = func(cmd *exec.Cmd) error {
		captured = slices.Clone(cmd.Args)
		cmd.Path, cmd.Args, cmd.Err = sh, []string{sh, "-c", "echo $$"}, nil
		return cmd.Start()
	}
	t.Cleanup(func() { execStart = orig })

	cwd := filepath.Join(home, "session")
	require.NoError(t, os.MkdirAll(cwd, 0o700))

	m := NewSpawnManager(0, 0, 0, 0, nil, services.NewSpawnPolicy(nil))
	pid, err := m.SpawnSession(t.Context(), SessionSpawnOptions{Cwd: cwd, Name: "Kontor", MCPConfigPath: "/tmp/k.json"})
	require.NoError(t, err)
	require.Positive(t, pid)

	require.Contains(t, captured, channelconfig.SubcommandPtyHost,
		"SpawnSession must launch via the pty host, never tmux, so the terminal route works")
	require.NotEqual(t, "tmux", filepath.Base(captured[0]))
}

// The exit goroutine must forget pid once the process is gone. Otherwise a
// spawnStore entry for an exited session keeps counting as "owned" forever
// in OwnsLiveSession, and with pid reuse that would let TerminateSession
// signal a foreign process group.
func TestSpawnSession_ForgetsThePidAfterExit(t *testing.T) {
	home := sessionHome(t)
	stubPIDExec(t)
	cwd := filepath.Join(home, "session")
	require.NoError(t, os.MkdirAll(cwd, 0o700))

	m := NewSpawnManager(0, 0, 0, 0, nil, services.NewSpawnPolicy(nil))
	exited := make(chan int, 1)
	pid, err := m.SpawnSession(t.Context(), SessionSpawnOptions{
		Cwd: cwd, Name: "Kontor", MCPConfigPath: "/tmp/k.json",
		OnExit: func(p int) { exited <- p },
	})
	require.NoError(t, err)
	require.Positive(t, pid)

	select {
	case got := <-exited:
		require.Equal(t, pid, got)
	case <-time.After(10 * time.Second):
		t.Fatal("OnExit was not called")
	}

	require.Nil(t, m.GetStatus(pid), "the spawnStore entry must be forgotten once the process exits")
}

// TerminateSession must actually stop a session this manager spawned, and
// the exit-watch goroutine must notice and fire OnExit.
func TestTerminateSession_StopsASpawnedSession(t *testing.T) {
	home := sessionHome(t)
	stubPIDExecSleep(t, 30)
	cwd := filepath.Join(home, "session")
	require.NoError(t, os.MkdirAll(cwd, 0o700))

	m := NewSpawnManager(0, 0, 0, 0, nil, services.NewSpawnPolicy(nil))
	exited := make(chan int, 1)
	pid, err := m.SpawnSession(t.Context(), SessionSpawnOptions{
		Cwd: cwd, Name: "Kontor", MCPConfigPath: "/tmp/k.json",
		OnExit: func(p int) { exited <- p },
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = syscall.Kill(-pid, syscall.SIGKILL) })

	require.NoError(t, m.TerminateSession(pid))

	select {
	case got := <-exited:
		require.Equal(t, pid, got)
	case <-time.After(10 * time.Second):
		t.Fatal("OnExit was not called after TerminateSession")
	}
}
