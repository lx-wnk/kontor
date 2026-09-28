// Package scanner discovers running Claude Code processes and their working directories.
package scanner

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/lx-wnk/kontor/sdk"
	"github.com/lx-wnk/kontor/server/internal/platform"
)

// ProcessInfo holds metadata about a running Claude Code process.
type ProcessInfo struct {
	PID             int
	CWD             string
	Uptime          int64 // seconds
	Command         string
	ClaudeConfigDir string // value of CLAUDE_CONFIG_DIR in the process env, or "" for default
	// ClaudeConfigDirKnown is true when the process environment was read at all.
	// False means unknown, not "default": ClaudeConfigDir is "" in both cases.
	ClaudeConfigDirKnown bool
	Provider             sdk.Provider // detected AI coding CLI provider; defaults to ProviderClaude
	// InternalProcess is true when Command invokes one of Claude Code's own
	// internal daemon/spare-pool subcommands (see internalCLISubcommands)
	// rather than a user-addressable interactive session.
	InternalProcess bool
}

// internalCLISubcommands lists Claude Code CLI subcommands that spawn internal
// daemon/spare-pool machinery (introduced in Claude Code 2.1.234) rather than a
// user-addressable session. A future upstream version may add more. Each entry
// is matched against argv[1:] (the tokens right after the claude binary, see
// IsInternalProcess) -- never a bare substring and never "anywhere in the
// line" -- so a prompt or resume ID that merely contains these words later in
// the command line does not produce a false positive.
var internalCLISubcommands = [][]string{
	{"bg-spare"},
	{"bg-pty-host"},
	{"daemon", "run"},
}

// IsInternalProcess reports whether comm -- a process's full command line as
// reported by `ps -eo args` -- invokes one of internalCLISubcommands as its
// subcommand, i.e. starting immediately at argv[1].
func IsInternalProcess(comm string) bool {
	tokens := strings.Fields(comm)
	if len(tokens) < 2 {
		return false
	}
	subcommand := tokens[1:]
	for _, marker := range internalCLISubcommands {
		if hasPrefix(subcommand, marker) {
			return true
		}
	}
	return false
}

// hasPrefix reports whether tokens begins with the exact sequence prefix.
func hasPrefix(tokens, prefix []string) bool {
	if len(tokens) < len(prefix) {
		return false
	}
	for i, s := range prefix {
		if tokens[i] != s {
			return false
		}
	}
	return true
}

var (
	// Anchored to a field boundary: the whole environment shares one line, so an
	// unanchored match also fires on any variable ending in the same name
	// (OLD_CLAUDE_CONFIG_DIR, a wrapper's saved copy) and returns its value.
	claudeConfigDirRE = regexp.MustCompile(`(?:^|\s)CLAUDE_CONFIG_DIR=(\S+)`)
	pidFieldRE        = regexp.MustCompile(`^\s*(\d+)\s`)
)

// getClaudeConfigDirsBatch fetches CLAUDE_CONFIG_DIR for all given PIDs.
// On Linux, reads /proc/{pid}/environ per-PID (file reads, no subprocess).
// On macOS, issues a single `ps ewww -p pid1,pid2,...` call instead of one per PID.
//
// A PID is present in the result exactly when its environment was read, with an
// empty value when the variable is not set. A PID whose environment could not be
// read — gone, owned by another user, hardened /proc, a failed `ps` — is absent,
// so callers can tell "runs on the default config dir" from "unknown".
func getClaudeConfigDirsBatch(pids []int) map[int]string {
	result := make(map[int]string, len(pids))
	if platform.IsLinux {
		for _, pid := range pids {
			raw, err := os.ReadFile(fmt.Sprintf("/proc/%d/environ", pid))
			if err != nil {
				continue
			}
			result[pid] = ""
			for _, kv := range strings.Split(string(raw), "\x00") {
				if strings.HasPrefix(kv, "CLAUDE_CONFIG_DIR=") {
					result[pid] = strings.TrimPrefix(kv, "CLAUDE_CONFIG_DIR=")
					break
				}
			}
		}
		return result
	}
	if len(pids) == 0 {
		return result
	}
	// macOS: one ps call for all PIDs — `ps ewww` prints full env after command,
	// one line per process (www = no truncation). Each line starts with the PID.
	pidStrs := make([]string, len(pids))
	for i, p := range pids {
		pidStrs[i] = strconv.Itoa(p)
	}
	out, err := exec.Command("ps", "ewww", "-p", strings.Join(pidStrs, ",")).Output() //nolint:gosec // pid ints formatted as strings
	if err != nil {
		return result
	}
	return parsePSEnvBatch(string(out))
}

// parsePSEnvBatch reads `ps ewww` output into pid → CLAUDE_CONFIG_DIR. Each line
// starts with the PID and carries the process's command line followed by its
// whole environment as space-separated KEY=value pairs.
func parsePSEnvBatch(out string) map[int]string {
	result := make(map[int]string)
	for _, line := range strings.Split(out, "\n") {
		m := pidFieldRE.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		pid, err := strconv.Atoi(m[1])
		if err != nil {
			continue
		}
		// The line is this process's environment, so it was read whether or not
		// it carries the variable.
		result[pid] = ""
		if cm := claudeConfigDirRE.FindStringSubmatch(line); cm != nil {
			result[pid] = cm[1]
		}
	}
	return result
}

// ParseElapsedTime converts ps etime format (e.g. "2-01:05:30") to seconds.
// Format: [[DD-]HH:]MM:SS
// The day component (before "-") is parsed independently so that "1-" yields
// 86400 rather than 60 (which would happen if "-" were blindly replaced by ":").
func ParseElapsedTime(etime string) int64 {
	etime = strings.TrimSpace(etime)
	if etime == "" {
		return 0
	}

	var days int64
	if idx := strings.Index(etime, "-"); idx != -1 {
		days, _ = strconv.ParseInt(etime[:idx], 10, 64)
		etime = etime[idx+1:]
	}

	// Remaining: [[HH:]MM:]SS — split by ":" and reverse so index 0 = seconds.
	parts := strings.Split(etime, ":")
	for i, j := 0, len(parts)-1; i < j; i, j = i+1, j-1 {
		parts[i], parts[j] = parts[j], parts[i]
	}
	multipliers := []int64{1, 60, 3600}
	var total int64
	for i, p := range parts {
		if i >= len(multipliers) {
			break
		}
		n, _ := strconv.ParseInt(strings.TrimSpace(p), 10, 64)
		total += n * multipliers[i]
	}
	return days*86400 + total
}

// ParseLsofBatch parses `lsof -a -d cwd -Fn` output into a pid→cwd map.
// Output format per process: p<pid>\nn<path>\n
func ParseLsofBatch(stdout string) map[int]string {
	result := make(map[int]string)
	var currentPID int
	for _, line := range strings.Split(stdout, "\n") {
		if strings.HasPrefix(line, "p") {
			pid, err := strconv.Atoi(strings.TrimPrefix(line, "p"))
			if err == nil {
				currentPID = pid
			}
		} else if strings.HasPrefix(line, "n") && currentPID != 0 {
			result[currentPID] = strings.TrimPrefix(line, "n")
			currentPID = 0
		}
	}
	return result
}

// ProjectName returns the last path component of a CWD.
func ProjectName(cwd string) string {
	return filepath.Base(cwd)
}

func getCWDsLinux(pids []int) map[int]string {
	result := make(map[int]string)
	for _, pid := range pids {
		target, err := os.Readlink(fmt.Sprintf("/proc/%d/cwd", pid))
		if err == nil {
			result[pid] = target
		}
	}
	return result
}

func getCWDsMac(ctx context.Context, pids []int) map[int]string {
	if len(pids) == 0 {
		return nil
	}
	pidStrs := make([]string, len(pids))
	for i, p := range pids {
		pidStrs[i] = strconv.Itoa(p)
	}
	out, err := exec.CommandContext(ctx, //nolint:gosec // pidStrs are integer PIDs parsed from ps output — not user-controlled input
		"lsof", "-a", "-d", "cwd", "-p", strings.Join(pidStrs, ","), "-Fn",
	).Output()
	if err != nil {
		return nil
	}
	return ParseLsofBatch(string(out))
}

// ScanProcessesWithDetector returns all running agent processes with their CWDs,
// detecting providers through the injected detector.
func ScanProcessesWithDetector(ctx context.Context, detector ProviderDetector) ([]ProcessInfo, error) {
	// Use `args` (full command line) rather than `comm` so flags like
	// `--resume <sessionId>` survive — the merger needs them to bind a process
	// to its exact session. The detector strips back to argv[0].
	out, err := exec.CommandContext(ctx, "ps", "-eo", "pid,etime,args").Output()
	if err != nil {
		return nil, fmt.Errorf("ps: %w", err)
	}

	type rawProc struct {
		pid     int
		etime   string
		command string
	}

	var raws []rawProc
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n")[1:] {
		fields := strings.Fields(line)
		if len(fields) < 3 {
			continue
		}
		comm := strings.Join(fields[2:], " ")
		if detectProviderVia(detector, comm) == "" {
			continue
		}
		pid, err := strconv.Atoi(fields[0])
		if err != nil {
			continue
		}
		raws = append(raws, rawProc{pid: pid, etime: fields[1], command: comm})
	}

	pids := make([]int, len(raws))
	for i, r := range raws {
		pids[i] = r.pid
	}

	var cwdMap map[int]string
	if platform.IsLinux {
		cwdMap = getCWDsLinux(pids)
	} else {
		cwdMap = getCWDsMac(ctx, pids)
	}

	// Batch-fetch CLAUDE_CONFIG_DIR for all PIDs in one call (macOS: one ps subprocess).
	configDirs := getClaudeConfigDirsBatch(pids)

	var result []ProcessInfo
	for _, r := range raws {
		cwd, ok := cwdMap[r.pid]
		if !ok || cwd == "" || cwd == "/" {
			continue
		}
		configDir, configDirKnown := configDirs[r.pid]
		result = append(result, ProcessInfo{
			PID:                  r.pid,
			CWD:                  cwd,
			Uptime:               ParseElapsedTime(r.etime),
			Command:              r.command,
			ClaudeConfigDir:      configDir,
			ClaudeConfigDirKnown: configDirKnown,
			Provider:             detectProviderVia(detector, r.command),
			InternalProcess:      IsInternalProcess(r.command),
		})
	}
	return result, nil
}

// ScanProcesses scans with Claude-only detection (no registry). Used by callers
// that cannot depend on the provider registry (e.g. the parser running-CWD set).
func ScanProcesses(ctx context.Context) ([]ProcessInfo, error) {
	return ScanProcessesWithDetector(ctx, nil)
}

// ProviderDetector maps a process command to a provider. The provider Registry
// implements this; tests can fake it.
type ProviderDetector interface {
	DetectProvider(comm string) sdk.Provider
}

// detectProviderVia resolves a process command through an injected detector,
// always honoring Claude even when no detector or a disabled detector is given.
func detectProviderVia(d ProviderDetector, comm string) sdk.Provider {
	if d != nil {
		if p := d.DetectProvider(comm); p != "" {
			return p
		}
	}
	if commBase(comm) == "claude" {
		return sdk.ProviderClaude
	}
	return ""
}

// commBase extracts argv[0]'s base name from a command string.
func commBase(comm string) string {
	comm = strings.TrimSpace(comm)
	if i := strings.IndexByte(comm, ' '); i >= 0 {
		comm = comm[:i]
	}
	return filepath.Base(comm)
}
