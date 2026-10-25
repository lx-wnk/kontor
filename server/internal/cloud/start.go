package cloud

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"regexp"
	"slices"
	"strings"
	"sync/atomic"
	"time"

	"github.com/creack/pty"

	"github.com/lx-wnk/kontor/server/internal/db/ent"
	"github.com/lx-wnk/kontor/server/internal/services"
)

// DefaultStartTimeout bounds starting a cloud session when Runner.Timeout is zero.
const DefaultStartTimeout = 90 * time.Second

const (
	// startExitGrace is how long the CLI may linger after printing the session URL.
	startExitGrace = 10 * time.Second
	// maxStartOutput bounds the CLI output scanned for the session URL.
	maxStartOutput = 64 << 10
	// startPtyCols is wide enough that the CLI never wraps the URL onto a second line.
	startPtyCols = 1024
	startPtyRows = 50
	// startDefaultTerm stands in when the server has no TERM, e.g. under launchd.
	startDefaultTerm    = "xterm-256color"
	trustPromptFragment = "Quick safety check"
	// The title line can echo the description, so only the View line is
	// trusted to carry the session URL.
	startTitlePrefix = "Created cloud session:"
	startURLPrefix   = "View:"
)

var (
	// ErrInvalidDescription reports a task description that is blank or would read as a CLI flag.
	ErrInvalidDescription = errors.New(`the task description must not be blank or start with "-"`)
	// ErrFolderNotTrusted reports that claude stopped at its folder trust prompt.
	ErrFolderNotTrusted = errors.New("claude does not trust this folder yet; open it once with claude and accept the trust prompt")
	// ErrStartUnconfirmed reports a failure after claude printed the session title: the session may exist.
	ErrStartUnconfirmed = errors.New("claude may have created the cloud session; check claude.ai before starting again")

	errStartOutputTooLong = fmt.Errorf("claude --cloud printed more than %d bytes without a session URL", maxStartOutput)
)

// ansiEscape matches CSI, OSC and other ESC sequences a terminal app emits.
var ansiEscape = regexp.MustCompile(`\x1b(?:\[[0-?]*[ -/]*[@-~]|\][^\x07\x1b]*(?:\x07|\x1b\\)|[ -/]*[0-~])`)

// StartResult is the cloud session `claude --cloud` created.
type StartResult struct {
	SessionID string
	URL       string
}

// startScan is the first decisive line of CLI output, or why reading stopped
// without one.
type startScan struct {
	sessionID string
	err       error
	output    string
}

// Start creates a cloud session for description from dir. `claude --cloud`
// refuses to run without a terminal, so it gets a pseudo-terminal.
func (r Runner) Start(ctx context.Context, sp *ent.Spawner, dir, description string) (StartResult, error) {
	if strings.TrimSpace(description) == "" || strings.HasPrefix(description, "-") {
		return StartResult{}, ErrInvalidDescription
	}
	if ok, reason := services.ValidateSpawnerCommand(claudeCommand); !ok {
		return StartResult{}, fmt.Errorf("claude command not permitted: %s", reason)
	}
	runCtx, cancel := context.WithTimeout(ctx, r.timeout(DefaultStartTimeout))
	defer cancel()

	// #nosec G204 -- the command is the constant "claude" (allow-list checked
	// above) and exec.CommandContext passes argv without a shell. The
	// description cannot start with "-", so claude never reads it as a flag.
	cmd := exec.CommandContext(runCtx, claudeCommand, "--cloud", description)
	cmd.Env = startEnv(sp)
	cmd.Dir = dir
	// pty.Start forces Setsid, which already makes the child a process group
	// leader; Setpgid on top of it fails with EPERM.
	cmd.Cancel = func() error { return killGroup(cmd.Process.Pid) }
	cmd.WaitDelay = killGrace
	ptmx, err := pty.StartWithSize(cmd, &pty.Winsize{Rows: startPtyRows, Cols: startPtyCols})
	if err != nil {
		if errors.Is(err, exec.ErrNotFound) {
			return StartResult{}, ErrClaudeNotFound
		}
		return StartResult{}, fmt.Errorf("claude --cloud: %w", err)
	}
	defer func() { _ = ptmx.Close() }()

	waitc := make(chan error, 1)
	go func() { waitc <- cmd.Wait() }()
	scanc := make(chan startScan, 1)
	var titleSeen atomic.Bool
	go func() {
		scanc <- scanStartOutput(ptmx, &titleSeen)
		// Keep draining so the child never blocks on a full pty.
		_, _ = io.Copy(io.Discard, ptmx)
	}()

	var scan startScan
	select {
	case scan = <-scanc:
	case <-runCtx.Done():
		// A URL printed right at the deadline is a session that exists; the
		// kill closes the pty, so the scan ends within killGrace.
		select {
		case scan = <-scanc:
		case <-time.After(killGrace):
		}
	}
	if scan.sessionID != "" {
		select {
		case <-waitc:
			if err := killGroup(cmd.Process.Pid); err != nil && !errors.Is(err, os.ErrProcessDone) {
				slog.Warn("cloud start: leftover claude process group not killed", "pid", cmd.Process.Pid, "error", err)
			}
		case <-time.After(startExitGrace):
			cancel()
			<-waitc
		}
		return StartResult{SessionID: scan.sessionID, URL: SessionURL(scan.sessionID)}, nil
	}

	cancel()
	waitErr := <-waitc
	err = startFailure(ctx, runCtx, scan, waitErr)
	if titleSeen.Load() {
		err = fmt.Errorf("%w: %w", ErrStartUnconfirmed, err)
	}
	return StartResult{}, err
}

// startEnv is the child env plus a TERM the pty-bound CLI can use.
func startEnv(sp *ent.Spawner) []string {
	env := childEnv(sp)
	if slices.ContainsFunc(env, func(e string) bool { v, ok := strings.CutPrefix(e, "TERM="); return ok && v != "" }) {
		return env
	}
	// exec keeps the last of duplicate keys, so this also replaces an empty TERM=.
	return append(env, "TERM="+startDefaultTerm)
}

func startFailure(ctx, runCtx context.Context, scan startScan, waitErr error) error {
	tail := outputTail(scan.output)
	switch {
	case scan.err != nil:
		return scan.err
	case ctx.Err() != nil:
		return fmt.Errorf("claude --cloud: %w", ctx.Err())
	case errors.Is(runCtx.Err(), context.DeadlineExceeded):
		if tail == "" {
			return ErrTimeout
		}
		return fmt.Errorf("%w: %s", ErrTimeout, tail)
	case tail != "":
		return fmt.Errorf("claude --cloud failed: %s", tail)
	case waitErr != nil:
		return fmt.Errorf("claude --cloud failed: %s", waitErr)
	}
	return errors.New("claude --cloud failed: it exited without printing a session URL")
}

func outputTail(output string) string {
	return cleanStderr([]byte(strings.ToValidUTF8(output[max(0, len(output)-maxStderrBytes):], "")))
}

// scanStartOutput reads terminal lines until one holds the session URL or the
// trust prompt, the pty closes, or maxStartOutput bytes were read.
func scanStartOutput(r io.Reader, titleSeen *atomic.Bool) startScan {
	sc := bufio.NewScanner(r)
	sc.Buffer(nil, maxStartOutput)
	sc.Split(splitTerminalLines)
	var out strings.Builder
	read := 0
	for sc.Scan() {
		if read += len(sc.Bytes()) + 1; read > maxStartOutput {
			return startScan{err: errStartOutputTooLong}
		}
		line := ansiEscape.ReplaceAllString(sc.Text(), "")
		trimmed := strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(trimmed, startTitlePrefix):
			titleSeen.Store(true)
		case strings.HasPrefix(trimmed, startURLPrefix):
			if id := sessionIDInLine(trimmed); id != "" {
				return startScan{sessionID: id}
			}
		case strings.Contains(line, trustPromptFragment):
			return startScan{err: ErrFolderNotTrusted}
		}
		out.WriteString(line)
		out.WriteByte('\n')
	}
	if errors.Is(sc.Err(), bufio.ErrTooLong) {
		return startScan{err: errStartOutputTooLong}
	}
	return startScan{output: out.String()}
}

func sessionIDInLine(line string) string {
	for _, field := range strings.Fields(line) {
		if !strings.HasPrefix(field, SessionURL("")) {
			continue
		}
		if id, err := ParseSessionRef(field); err == nil {
			return id
		}
	}
	return ""
}

// splitTerminalLines is bufio.ScanLines for terminal output, where a bare \r
// also ends a line.
func splitTerminalLines(data []byte, atEOF bool) (int, []byte, error) {
	if i := bytes.IndexAny(data, "\r\n"); i >= 0 {
		return i + 1, data[:i], nil
	}
	if atEOF && len(data) > 0 {
		return len(data), data, nil
	}
	return 0, nil, nil
}
