package services

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"

	"github.com/lx-wnk/kontor/server/internal/db/repo"
)

// DefaultAllowedCommands are the bare command names always permitted in the
// spawners.command field. Extra entries can be appended via the
// spawn.allowedCommands setting (each entry is either a bare name or an absolute
// trusted bin directory).
var DefaultAllowedCommands = []string{"claude", "claude-code", "npx"}

// extraAllowedCommands holds the configured extra spawner allow-list (bare names
// and absolute trusted bin dirs), set from the spawn.allowedCommands setting at
// startup. Guarded by extraAllowedMu.
var (
	extraAllowedMu       sync.RWMutex
	extraAllowedCommands []string
)

// SetSpawnerAllowedCommands replaces the configured extra spawner allow-list.
// Whitespace is trimmed and empty entries dropped. Call at startup (ApplyRestart).
// Safe for concurrent use.
func SetSpawnerAllowedCommands(cmds []string) {
	next := make([]string, 0, len(cmds))
	for _, c := range cmds {
		if s := strings.TrimSpace(c); s != "" {
			next = append(next, s)
		}
	}
	extraAllowedMu.Lock()
	extraAllowedCommands = next
	extraAllowedMu.Unlock()
}

// ValidateSpawnerCommand reports whether command is permitted for a spawner row
// and, when not, returns a human-readable reason for a 400 response.
//
// Bare names: allowed only when listed in DefaultAllowedCommands or as a bare
// entry of the spawn.allowedCommands setting.
//
// Absolute paths: must EvalSymlinks-resolve (the file must exist), and either
// the resolved binary's parent OR the named path's own (pre-symlink) parent
// must lie under a trusted bin directory (see trustedBinDirs). A trusted dir is
// a statement about who could write the name found there -- a package manager's
// own symlink indirection (Homebrew's `npx` resolves to
// `../lib/node_modules/npm/bin/npx-cli.js`) is written by the same trusted party
// as the name itself, so accepting on the named parent alone is correct there.
// EvalSymlinks is still required: a name whose own parent is untrusted (e.g.
// under /tmp) is only accepted if it resolves into a trusted dir, and a chain
// where neither end is trusted is denied.
func ValidateSpawnerCommand(command string) (bool, string) {
	if command == "" {
		return false, "command must not be empty"
	}

	if strings.HasPrefix(command, "/") {
		resolved, err := filepath.EvalSymlinks(command)
		if err != nil {
			return false, fmt.Sprintf("command path %q could not be resolved", command)
		}
		if otherWritable(resolved) {
			return false, fmt.Sprintf("command path %q resolves to an other-writable file", command)
		}
		if underTrustedBinDir(filepath.Dir(resolved)) || underTrustedBinDir(filepath.Dir(filepath.Clean(command))) {
			return true, ""
		}
		// Name the exclusion rather than the generic miss: an operator reading
		// "not under a trusted bin directory" would find the directory listed in
		// trustedBinDirs and conclude the check is broken.
		for _, parent := range []string{filepath.Dir(resolved), filepath.Dir(filepath.Clean(command))} {
			if otherWritable(parent) {
				return false, fmt.Sprintf("command path %q is under %q, which is other-writable and therefore not trusted", command, parent)
			}
		}
		return false, fmt.Sprintf("command path %q is not under a trusted bin directory", command)
	}

	if slices.Contains(DefaultAllowedCommands, command) {
		return true, ""
	}
	for _, extra := range configuredExtraAllowedCommands() {
		if !strings.HasPrefix(extra, "/") && command == extra {
			return true, ""
		}
	}
	return false, fmt.Sprintf("command %q is not in the allow-list", command)
}

// underTrustedBinDir reports whether parent lies under any configured trusted
// bin directory (see trustedBinDirs).
func underTrustedBinDir(parent string) bool {
	for _, dir := range trustedBinDirs() {
		trusted, err := canonicalize(dir)
		if err != nil {
			continue
		}
		if isUnder(parent, trusted) {
			return true
		}
	}
	return false
}

// trustedBinDirs returns the set of directories under which an absolute spawner
// command is permitted: the standard system/Homebrew bin dirs, the user's
// ~/.local/bin, the resolved directory of the `claude` binary on PATH, and any
// absolute-path entries of the spawn.allowedCommands setting. A directory that
// is other-writable (mode&0o002) is dropped -- any local user could replace or
// plant a binary there, which would make the allow-list decorative for that
// path. Group-writable directories are kept: they are still only writable by
// an owning group, not by every local user.
func trustedBinDirs() []string {
	dirs := []string{"/usr/bin", "/bin", "/usr/local/bin", "/opt/homebrew/bin"}
	if home, err := os.UserHomeDir(); err == nil {
		dirs = append(dirs, filepath.Join(home, ".local", "bin"))
	}
	if p, err := exec.LookPath("claude"); err == nil {
		if real, err := filepath.EvalSymlinks(p); err == nil {
			p = real
		}
		dirs = append(dirs, filepath.Dir(p))
	}
	extraDirs := make(map[string]bool)
	for _, extra := range configuredExtraAllowedCommands() {
		if strings.HasPrefix(extra, "/") {
			dirs = append(dirs, extra)
			extraDirs[extra] = true
		}
	}

	trusted := make([]string, 0, len(dirs))
	for _, dir := range dirs {
		if otherWritable(dir) {
			if extraDirs[dir] {
				warnOtherWritableTrustedDirOnce(dir)
			}
			continue
		}
		trusted = append(trusted, dir)
	}
	return trusted
}

// otherWritable reports whether path's file mode grants write access to
// "others" (mode&0o002). A stat failure (missing path, permission denied)
// is treated as not-writable -- existence and resolvability are already
// enforced separately by EvalSymlinks in the callers.
func otherWritable(path string) bool {
	info, err := os.Stat(path)
	if err != nil {
		return false
	}
	return info.Mode().Perm()&0o002 != 0
}

// warnedOtherWritableDirs tracks directories already reported by
// warnOtherWritableTrustedDirOnce, so a configured trusted dir that stays
// world-writable for the life of the process logs exactly once instead of on
// every ValidateSpawnerCommand call (trustedBinDirs is on that hot path).
var (
	warnedOtherWritableMu   sync.Mutex
	warnedOtherWritableDirs = map[string]struct{}{}
)

// warnOtherWritableTrustedDirOnce logs, at most once per distinct dir for the
// process lifetime, that a configured spawn.allowedCommands directory was
// excluded from the trusted-bin allow-list for being other-writable -- an
// operator whose spawner validation starts failing needs to find out why.
func warnOtherWritableTrustedDirOnce(dir string) {
	warnedOtherWritableMu.Lock()
	defer warnedOtherWritableMu.Unlock()
	if _, done := warnedOtherWritableDirs[dir]; done {
		return
	}
	warnedOtherWritableDirs[dir] = struct{}{}
	slog.Warn("spawn policy: configured trusted bin dir is other-writable, excluding it from the allow-list", "dir", dir)
}

// configuredExtraAllowedCommands returns a copy of the configured extra spawner
// allow-list set via SetSpawnerAllowedCommands. Safe for concurrent use.
func configuredExtraAllowedCommands() []string {
	extraAllowedMu.RLock()
	defer extraAllowedMu.RUnlock()
	if len(extraAllowedCommands) == 0 {
		return nil
	}
	out := make([]string, len(extraAllowedCommands))
	copy(out, extraAllowedCommands)
	return out
}

// sensitiveHomeDirs are home-relative directory names that are unconditionally
// blacklisted as spawn working directories, regardless of any project roots.
// F-SEC-001 / CWE-269 / OWASP A04.
var sensitiveHomeDirs = []string{
	".ssh",
	".aws",
	".gnupg",
	".config",
	".claude",
}

// ErrCwdBlacklisted is returned when the requested cwd falls inside a
// sensitive home directory that is always blocked.
var ErrCwdBlacklisted = errors.New("cwd is inside a sensitive directory and cannot be used as a spawn working directory")

// ErrCwdNotAllowed is returned when the requested cwd does not fall under
// any registered project root.
var ErrCwdNotAllowed = errors.New("cwd outside allowed project roots")

// RootsProvider returns the set of allowed root paths. Each path should be an
// absolute, canonical path to a project folder. The SpawnPolicy canonicalises
// paths from the provider before comparison, so minor inconsistencies (trailing
// slashes, un-resolved symlinks) are tolerated.
type RootsProvider func(ctx context.Context) ([]string, error)

// SpawnPolicy decides whether a requested working directory is safe to use
// as the cwd of a spawned agent process.
type SpawnPolicy interface {
	// Allow returns nil when cwd is permitted, or a descriptive error otherwise.
	// The returned error is either ErrCwdBlacklisted or ErrCwdNotAllowed.
	Allow(ctx context.Context, cwd string) error
	// AllowResume validates the cwd for resuming an EXISTING session. The session
	// already runs at this cwd (it is monitored), so only the unconditional
	// sensitive-dir blacklist applies — not the new-spawn project-roots allowlist.
	// Returns ErrCwdBlacklisted or nil.
	AllowResume(cwd string) error
}

// spawnPolicy is the production implementation.
type spawnPolicy struct {
	roots      RootsProvider // may be nil → no project-root restriction
	extraRoots []string
}

// NewSpawnPolicy constructs a SpawnPolicy. roots provides the set of allowed
// project root paths; pass nil to enforce only the sensitive-dir blacklist (for
// use in dev/bypass-auth mode where no DB is available).
// extraRoots are fixed directories the server owns (e.g. the Kontor session
// dir); they pass the root check but never the blacklist.
func NewSpawnPolicy(roots RootsProvider, extraRoots ...string) SpawnPolicy {
	return &spawnPolicy{roots: roots, extraRoots: extraRoots}
}

// canonicalize resolves cwd to an absolute, symlink-resolved path.
func canonicalize(cwd string) (string, error) {
	cwdAbs, err := filepath.Abs(cwd)
	if err != nil {
		return "", fmt.Errorf("cwd resolution failed: %w", err)
	}
	if real, err := filepath.EvalSymlinks(cwdAbs); err == nil {
		cwdAbs = real
	}
	return cwdAbs, nil
}

// AllowResume implements SpawnPolicy: blacklist-only check for resuming an
// existing session at its own cwd.
func (p *spawnPolicy) AllowResume(cwd string) error {
	cwdAbs, err := canonicalize(cwd)
	if err != nil {
		return err
	}
	return checkBlacklist(cwdAbs)
}

// Allow implements SpawnPolicy.
func (p *spawnPolicy) Allow(ctx context.Context, cwd string) error {
	// Resolve to absolute, canonical path (follow symlinks).
	cwdAbs, err := canonicalize(cwd)
	if err != nil {
		return err
	}

	// Unconditional blacklist — sensitive home directories always blocked.
	if err := checkBlacklist(cwdAbs); err != nil {
		return err
	}

	for _, root := range p.extraRoots {
		if rootAbs, err := canonicalize(root); err == nil && isUnder(cwdAbs, rootAbs) {
			return nil
		}
	}

	// When no roots provider is configured, skip the project-root check.
	if p.roots == nil {
		return nil
	}

	roots, err := p.roots(ctx)
	if err != nil {
		// Fail closed: the allow-list is the primary control. A DB hiccup must not
		// silently permit arbitrary cwd values — the blacklist alone is insufficient.
		slog.Error("spawn policy: roots() failed", "err", err)
		return ErrCwdNotAllowed
	}
	if len(roots) == 0 {
		// No project roots registered — only the blacklist applies.
		return nil
	}

	for _, root := range roots {
		rootAbs, err := filepath.Abs(root)
		if err != nil {
			continue
		}
		if real, err := filepath.EvalSymlinks(rootAbs); err == nil {
			rootAbs = real
		}
		if isUnder(cwdAbs, rootAbs) {
			return nil
		}
	}
	return ErrCwdNotAllowed
}

// checkBlacklist returns ErrCwdBlacklisted when cwdAbs falls inside one of the
// sensitive home directories. Fails closed: if the home directory cannot be
// determined the spawn is denied rather than permitted.
func checkBlacklist(cwdAbs string) error {
	home, err := os.UserHomeDir()
	if err != nil {
		// Cannot determine home dir — deny rather than allow an unknown state.
		return ErrCwdBlacklisted
	}
	homeAbs, err := filepath.Abs(home)
	if err != nil {
		return ErrCwdBlacklisted
	}
	if real, err := filepath.EvalSymlinks(homeAbs); err == nil {
		homeAbs = real
	}

	for _, rel := range sensitiveHomeDirs {
		sensitive := filepath.Join(homeAbs, rel)
		// Canonicalise the sensitive path so case-insensitive or symlinked filesystems
		// (e.g. macOS where ~/.SSH resolves to the same inode as ~/.ssh) cannot bypass
		// the check. Ignore EvalSymlinks errors: the unresolved path is still used.
		if resolved, err := filepath.EvalSymlinks(sensitive); err == nil {
			sensitive = resolved
		}
		if isUnder(cwdAbs, sensitive) {
			return ErrCwdBlacklisted
		}
	}
	return nil
}

// isUnder reports whether target is equal to root or is a descendant of root.
// Both paths must already be absolute; filepath.Clean is applied internally.
func isUnder(target, root string) bool {
	root = filepath.Clean(root)
	target = filepath.Clean(target)
	return target == root || strings.HasPrefix(target, root+string(filepath.Separator))
}

// ProjectFolderRootsProvider returns a RootsProvider that fetches all project
// folder paths from the database at call time. It lists all projects via
// projectRepo, then collects every folder path via folderRepo — suitable for
// small-to-medium deployments where the total number of registered folders is
// in the tens.
//
// When either repo is nil the provider fails open (returns nil, nil), leaving
// only the sensitive-dir blacklist active.
func ProjectFolderRootsProvider(projectRepo repo.ProjectRepo, folderRepo repo.ProjectFolderRepo) RootsProvider {
	return func(ctx context.Context) ([]string, error) {
		if projectRepo == nil || folderRepo == nil {
			return nil, nil
		}
		projects, err := projectRepo.List(ctx)
		if err != nil {
			return nil, fmt.Errorf("spawn_policy: list projects: %w", err)
		}
		var paths []string
		for _, proj := range projects {
			folders, err := folderRepo.ListByProject(ctx, proj.ID)
			if err != nil {
				// Skip this project on error; don't block all spawns.
				continue
			}
			for _, f := range folders {
				if f.Path != "" {
					paths = append(paths, f.Path)
				}
			}
		}
		return paths, nil
	}
}
