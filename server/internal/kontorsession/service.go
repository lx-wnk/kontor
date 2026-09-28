package kontorsession

import (
	"context"
	_ "embed"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"sync"

	"github.com/lx-wnk/kontor/server/internal/channelconfig"
	"github.com/lx-wnk/kontor/server/internal/claudeconfig"
	"github.com/lx-wnk/kontor/server/internal/db/repo"
	"github.com/lx-wnk/kontor/server/internal/mcp"
)

//go:embed briefing.md
var briefing string

// SpawnOptions mirrors agents.SessionSpawnOptions field for field, so DI adapts
// with a plain conversion and this package imports nothing from internal/api.
type SpawnOptions struct {
	Cwd                string
	Prompt             string
	AppendSystemPrompt string
	Name               string
	MCPConfigPath      string
	AllowedTools       []string
	OnExit             func(pid int)
}

// Service owns the one Kontor session. The active kontor_session key is the
// session record; mu serialises every transition. ConfigPath is fixed rather
// than remembered per spawn, so end() can find and remove the MCP config even
// after a restart, when no in-memory state survived.
type Service struct {
	Keys       mcp.KontorSessionKeyIssuer
	Audit      repo.AuditEventRepo
	TaskAPIURL string
	Dir        string
	ConfigPath string
	Spawn      func(context.Context, SpawnOptions) (int, error)
	Terminate  func(pid int) error
	Alive      func(pid int) bool
	WaitExit   func(pid int)

	mu sync.Mutex
}

func (s *Service) Current(ctx context.Context) (int, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	pid, ok, err := s.current(ctx)
	if err != nil || !ok || s.Alive(pid) {
		return pid, ok, err
	}
	if err := s.end(ctx, "process gone"); err != nil {
		return 0, false, err
	}
	return 0, false, nil
}

func (s *Service) current(ctx context.Context) (int, bool, error) {
	pid, ok, err := s.Keys.Current(ctx)
	if err != nil || !ok || pid == 0 {
		return 0, false, err
	}
	return pid, true, nil
}

// Start reports the pid and whether it started a fresh process: false means a
// running session was returned unchanged, so a caller with a dropped prompt
// knows not to treat it as delivered.
func (s *Service) Start(ctx context.Context, prompt string) (int, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	pid, ok, err := s.current(ctx)
	if err != nil {
		return 0, false, err
	}
	if ok && !s.Alive(pid) {
		if err := s.end(ctx, "process gone"); err != nil {
			return 0, false, err
		}
		ok = false
	}
	if ok {
		return pid, false, nil
	}
	pid, err = s.start(ctx, prompt)
	if err != nil {
		return 0, false, err
	}
	return pid, true, nil
}

func (s *Service) Renew(ctx context.Context, prompt string) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.end(ctx, "renewed"); err != nil {
		return 0, err
	}
	return s.start(ctx, prompt)
}

func (s *Service) End(ctx context.Context, reason string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.end(ctx, reason)
}

// Reconcile runs once at boot: a recorded pid that is gone (or never
// attached) ends the session, a live one gets its exit watcher back.
func (s *Service) Reconcile(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	pid, ok, err := s.Keys.Current(ctx)
	if err != nil || !ok {
		return err
	}
	if pid == 0 || !s.Alive(pid) {
		return s.end(ctx, "process gone at boot")
	}
	go func() {
		s.WaitExit(pid)
		s.exited(pid)
	}()
	return nil
}

func (s *Service) start(ctx context.Context, prompt string) (int, error) {
	if err := os.MkdirAll(s.Dir, 0o700); err != nil {
		return 0, fmt.Errorf("kontorsession: session dir: %w", err)
	}
	// A symlinked Dir would make the removal below follow it and delete the
	// target's .claude tree instead of the session's own.
	if fi, err := os.Lstat(s.Dir); err != nil {
		return 0, fmt.Errorf("kontorsession: session dir: %w", err)
	} else if fi.Mode()&os.ModeSymlink != 0 {
		return 0, fmt.Errorf("kontorsession: session dir %s is a symlink", s.Dir)
	}
	// A prior session's "don't ask again" answers, written by Claude Code to
	// <Dir>/.claude/settings.local.json, must not silently apply to this one.
	if err := os.RemoveAll(filepath.Join(s.Dir, ".claude")); err != nil && !errors.Is(err, os.ErrNotExist) {
		slog.Warn("kontorsession: remove stale settings failed", "dir", s.Dir, "err", err)
		return 0, fmt.Errorf("kontorsession: remove stale settings: %w", err)
	}
	// A client disconnect can cancel ctx after Spawn already started the
	// process; the cleanup writes below must still land, so they run on a
	// context detached from that cancellation.
	cleanupCtx := context.WithoutCancel(ctx)
	token, err := s.Keys.Issue(ctx)
	if err != nil {
		return 0, err
	}
	cfg, err := s.writeConfig(token)
	if err != nil {
		return 0, errors.Join(err, s.Keys.Revoke(cleanupCtx))
	}
	pid, err := s.Spawn(ctx, SpawnOptions{
		Cwd: s.Dir, Prompt: prompt, AppendSystemPrompt: briefing, Name: "Kontor",
		MCPConfigPath: cfg, AllowedTools: mcp.KontorSessionAllowedTools(), OnExit: s.exited,
	})
	if err == nil {
		err = s.Keys.Attach(ctx, pid)
		if err != nil {
			err = errors.Join(err, s.Terminate(pid))
		}
	}
	if err != nil {
		s.removeConfig()
		return 0, errors.Join(err, s.Keys.Revoke(cleanupCtx))
	}
	s.audit(ctx, "kontor_session.start", pid, "")
	return pid, nil
}

// end is the single exit path: stop the process, revoke the key, remove the
// config, audit. Ending no session is not an error. The revoke runs on a
// context detached from ctx's cancellation for the same reason start's
// cleanup does — a caller disconnecting must not leave the key active.
func (s *Service) end(ctx context.Context, reason string) error {
	pid, ok, err := s.Keys.Current(ctx)
	if err != nil {
		return err
	}
	if ok && pid > 0 && s.Alive(pid) {
		if err := s.Terminate(pid); err != nil {
			return fmt.Errorf("kontorsession: stop pid %d: %w", pid, err)
		}
	}
	if !ok {
		s.removeConfig()
		return nil
	}
	revokeErr := s.Keys.Revoke(context.WithoutCancel(ctx))
	s.removeConfig()
	if revokeErr != nil {
		return revokeErr
	}
	s.audit(ctx, "kontor_session.end", pid, reason)
	return nil
}

// exited fires from the watcher of every session ever started, so it ends
// only the session still recorded for that pid — never a renewed one. The
// lookup gets one retry: a transient DB error at the moment the process
// exits must not strand the record on a dead pid forever.
func (s *Service) exited(pid int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ctx := context.Background()
	cur, ok, err := s.current(ctx)
	if err != nil {
		slog.Warn("kontorsession: lookup after exit failed, retrying once", "pid", pid, "err", err)
		cur, ok, err = s.current(ctx)
	}
	if err != nil || !ok || cur != pid {
		if err != nil {
			slog.Warn("kontorsession: lookup after exit failed twice, giving up", "pid", pid, "err", err)
		}
		return
	}
	if err := s.end(ctx, "process exited"); err != nil {
		slog.Warn("kontorsession: end after exit failed", "pid", pid, "err", err)
	}
}

// writeConfig writes the MCP config to the service's fixed ConfigPath, so
// end() can find and remove it even after a restart. channelconfig has no
// write-to-path helper, so this builds the config through WriteTempConfig —
// the single definition of the config shape — and copies its bytes into
// place. A copy, not a rename: the temp directory and ConfigPath's directory
// are not guaranteed to share a filesystem.
func (s *Service) writeConfig(token string) (string, error) {
	self, err := channelconfig.SelfBinaryPath()
	if err != nil {
		return "", fmt.Errorf("kontorsession: self binary: %w", err)
	}
	servers, err := claudeconfig.UserMCPServers()
	if err != nil {
		slog.Warn("kontorsession: user MCP servers unreadable, starting without them", "err", err)
	}
	tmp, err := channelconfig.WriteTempConfig(self, &channelconfig.TaskAPI{URL: s.TaskAPIURL, Token: token}, servers)
	if err != nil {
		return "", err
	}
	defer func() { _ = os.Remove(tmp) }()
	data, err := os.ReadFile(tmp)
	if err != nil {
		return "", fmt.Errorf("kontorsession: read temp config: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(s.ConfigPath), 0o700); err != nil {
		return "", fmt.Errorf("kontorsession: config dir: %w", err)
	}
	if err := os.WriteFile(s.ConfigPath, data, 0o600); err != nil {
		return "", fmt.Errorf("kontorsession: write config: %w", err)
	}
	return s.ConfigPath, nil
}

func (s *Service) removeConfig() {
	if s.ConfigPath == "" {
		return
	}
	if err := os.Remove(s.ConfigPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		slog.Warn("kontorsession: remove config failed", "path", s.ConfigPath, "err", err)
	}
}

func (s *Service) audit(ctx context.Context, action string, pid int, reason string) {
	if s.Audit == nil {
		return
	}
	if err := s.Audit.RecordAudit(ctx, nil, action, strconv.Itoa(pid), map[string]any{"reason": reason}); err != nil {
		slog.Warn("kontorsession: audit write failed", "action", action, "err", err)
	}
}
