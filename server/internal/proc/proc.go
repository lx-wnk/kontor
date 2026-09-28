// Package proc provides a stdlib-only process-liveness probe. It has no
// dependency on the pipeline orchestration core so it can be imported from
// any layer (infra, edge, or pipeline) without inverting the layer direction.
package proc

import (
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"

	"github.com/lx-wnk/kontor/server/internal/platform"
)

func isPidZombie(pid int) bool {
	if platform.IsLinux {
		data, err := os.ReadFile(fmt.Sprintf("/proc/%d/status", pid))
		if err != nil {
			return false
		}
		s := string(data)
		return strings.Contains(s, "State:\tZ") || strings.Contains(s, "State: Z")
	}
	cmd := exec.Command("ps", "-p", strconv.Itoa(pid), "-o", "stat=") //nolint:gosec
	out, err := cmd.Output()
	if err != nil {
		return false
	}
	return strings.HasPrefix(strings.TrimSpace(string(out)), "Z")
}

func IsPidAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	proc, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	err = proc.Signal(syscall.Signal(0))
	if err == nil {
		return !isPidZombie(pid)
	}
	if err == syscall.EPERM {
		return true
	}
	return false
}

// ParentPID returns pid's parent process id via `ps -o ppid=`, which BSD
// (macOS) and Linux ps both support with the same flags — one implementation
// instead of a platform-specific /proc/<pid>/stat parse.
func ParentPID(pid int) (int, error) {
	out, err := exec.Command("ps", "-o", "ppid=", "-p", strconv.Itoa(pid)).Output() //nolint:gosec
	if err != nil {
		return 0, fmt.Errorf("proc: ParentPID(%d): %w", pid, err)
	}
	ppid, err := strconv.Atoi(strings.TrimSpace(string(out)))
	if err != nil {
		return 0, fmt.Errorf("proc: ParentPID(%d): parse %q: %w", pid, out, err)
	}
	return ppid, nil
}
