package cloud

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"regexp"
	"strings"
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
	startPtyCols        = 1024
	startPtyRows        = 50
	trustPromptFragment = "Quick safety check"
)

var (
	// ErrInvalidDescription reports a task description that is blank or would read as a CLI flag.
	ErrInvalidDescription = errors.New(`the task description must not be blank or start with "-"`)
	// ErrFolderNotTrusted reports that claude stopped at its folder trust prompt.
	ErrFolderNotTrusted = errors.New("claude does not trust this folder yet; open it once with claude and accept the trust prompt")

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
	cmd.Env = childEnv(sp)
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
	go func() {
		scanc <- scanStartOutput(ptmx)
		// Keep draining so the child never blocks on a full pty.
		_, _ = io.Copy(io.Discard, ptmx)
	}()

	var scan startScan
	select {
	case scan = <-scanc:
	case <-runCtx.Done():
	}
	if scan.sessionID != "" {
		select {
		case <-waitc:
		case <-time.After(startExitGrace):
			cancel()
			<-waitc
		}
		return StartResult{SessionID: scan.sessionID, URL: SessionURL(scan.sessionID)}, nil
	}

	cancel()
	waitErr := <-waitc
	switch {
	case scan.err != nil:
		return StartResult{}, scan.err
	case ctx.Err() != nil:
		return StartResult{}, fmt.Errorf("claude --cloud: %w", ctx.Err())
	case errors.Is(runCtx.Err(), context.DeadlineExceeded):
		return StartResult{}, ErrTimeout
	}
	detail := cleanStderr([]byte(strings.ToValidUTF8(scan.output[max(0, len(scan.output)-maxStderrBytes):], "")))
	if detail == "" && waitErr != nil {
		detail = waitErr.Error()
	}
	if detail == "" {
		detail = "it exited without printing a session URL"
	}
	return StartResult{}, fmt.Errorf("claude --cloud failed: %s", detail)
}

// scanStartOutput reads terminal lines until one holds the session URL or the
// trust prompt, the pty closes, or maxStartOutput bytes were read.
func scanStartOutput(r io.Reader) startScan {
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
		if strings.Contains(line, trustPromptFragment) {
			return startScan{err: ErrFolderNotTrusted}
		}
		if id := sessionIDInLine(line); id != "" {
			return startScan{sessionID: id}
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
