// Package restart performs a graceful, web-triggered server restart: either an
// in-place re-exec of the current binary (default) or a clean exit for an
// external supervisor to relaunch.
package restart

import (
	"context"
	"errors"
	"github.com/lx-wnk/kontor/server/internal/version"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"syscall"
	"time"
)

type Mode string

const (
	ModeReexec Mode = "reexec"
	ModeExit   Mode = "exit"
)

// buildTimeout bounds a single rebuild.
const buildTimeout = 2 * time.Minute

// Restarter performs the actual relaunch. Seam so the run-loop is testable
// without replacing the process.
type Restarter interface {
	Reexec() error
	Exit()
	// Build rebuilds the binary at its own running path from source and
	// returns the combined build output (populated on both success and
	// failure, so a caller can surface it on error).
	Build(ctx context.Context) ([]byte, error)
}

// OSRestarter is the production Restarter.
type OSRestarter struct{}

// Reexec replaces the current process image with a fresh run of the same binary,
// preserving args + environment (same PID). It only returns on error.
func (OSRestarter) Reexec() error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	return syscall.Exec(exe, os.Args, os.Environ())
}

// Exit terminates the process cleanly so a supervisor restarts it.
func (OSRestarter) Exit() { os.Exit(0) }

// buildArgs is the "go" argument list for a rebuild, split out so the stamp is
// testable without running a compiler. An unknown revision stamps nothing
// rather than stamping the empty string, which would read as a real version.
func buildArgs(exe, rev string) []string {
	args := []string{"build", "-o", exe}
	if rev != "" {
		args = append(args, "-ldflags", "-X main.version="+rev)
	}
	return append(args, "./cmd/serve/...")
}

// Build runs `go build -o <running binary's path> ./cmd/serve/...` — the same
// command Taskfile.yml's `build` task runs, minus the version ldflags. The
// command and its arguments are fixed constants; nothing dynamic (request
// body, env, flags) is interpolated into them, so this can never become a
// generic command-execution primitive.
//
// ctx is given a fixed timeout here rather than left entirely to the caller:
// a runaway `go build` must not hang the HTTP request that triggered it
// forever.
//
// exec.CommandContext's default Cancel is SIGKILL on ctx expiry, and that is
// left as-is deliberately: `go build -o` compiles to a scratch file and
// renames it onto the target path only on success, so a build killed
// mid-compile never touches that path and the binary still running is left
// intact — the fail-safe this endpoint needs.
//
// It is the rename that carries this, not any refusal by the OS. An earlier
// version of this comment claimed the kernel rejects overwriting a running
// binary ("text file busy"); that is wrong, and a rebuild through this
// endpoint was observed replacing its own running binary on macOS (the file's
// hash changed while the process kept its PID). ETXTBSY applies to writing
// into the file in place, which is not what the toolchain does.
func (OSRestarter) Build(ctx context.Context) ([]byte, error) {
	exe, err := os.Executable()
	if err != nil {
		return nil, err
	}
	srcDir, err := serverModuleDir()
	if err != nil {
		return nil, err
	}

	ctx, cancel := context.WithTimeout(ctx, buildTimeout)
	defer cancel()

	// Stamp the revision being compiled, exactly as Taskfile.yml does. Without
	// it the new binary reports "dev" while the source reports a revision, so
	// the staleness banner this rebuild exists to clear would instead be stuck
	// on forever -- the button would create the condition it removes.
	// #nosec G204 -- no part of this command line is caller-supplied: "go" is
	// PATH-resolved, exe comes from os.Executable(), srcDir from this package's
	// own compiled-in path, and rev from "git describe" run in that directory.
	// The restart handler reads only a boolean from its request body and passes
	// nothing through; argv reaches exec.CommandContext without a shell.
	cmd := exec.CommandContext(ctx, "go", buildArgs(exe, version.DescribeIn(ctx, srcDir))...)
	cmd.Dir = srcDir
	return cmd.CombinedOutput()
}

// serverModuleDir locates the server Go module root from this source file's
// own compiled-in path, so the build works regardless of the running
// process's current working directory (which a re-exec'd or desktop-launched
// binary can't rely on). Fails only for a -trimpath build (release/desktop
// binaries), which never call Build.
func serverModuleDir() (string, error) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		return "", errors.New("restart: source path unavailable (binary built with -trimpath)")
	}
	dir := filepath.Dir(file)
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", errors.New("restart: go.mod not found above " + file)
		}
		dir = parent
	}
}

// Execute relaunches per mode. For reexec, a failure is fatal (log + exit 1) so
// the process never hangs in a half-down state.
func Execute(mode Mode, r Restarter) {
	if mode == ModeExit {
		r.Exit()
		return
	}
	if err := r.Reexec(); err != nil {
		slog.Error("restart: re-exec failed", "err", err)
		os.Exit(1)
	}
}

// Controller carries the restart signal from the HTTP handler to the run-loop
// and records the configured mode.
type Controller struct {
	ch   chan struct{}
	mode Mode
}

func NewController(mode string) *Controller {
	m := Mode(mode)
	if m != ModeExit {
		m = ModeReexec
	}
	return &Controller{ch: make(chan struct{}, 1), mode: m}
}

// Trigger requests a restart; non-blocking and coalescing (buffered size 1).
func (c *Controller) Trigger() {
	select {
	case c.ch <- struct{}{}:
	default:
	}
}

// C is the receive end the run-loop selects on.
func (c *Controller) C() <-chan struct{} { return c.ch }

// Mode is the configured relaunch mode (for the 202 response + Execute).
func (c *Controller) Mode() Mode { return c.mode }
