// Package fakespawn simulates the on-disk + process-scan artifacts a
// controllable Claude agent leaves behind, so flow tests can exercise the
// live → finished → dismiss lifecycle without launching a real process.
package fakespawn

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/lx-wnk/kontor/sdk"
	"github.com/lx-wnk/kontor/server/internal/channelconfig"
	"github.com/lx-wnk/kontor/server/internal/claudemodel"
	"github.com/lx-wnk/kontor/server/internal/parser"
	"github.com/lx-wnk/kontor/server/internal/scanner"
)

// Spawner owns one fake agent's temp HOME and the set of currently-live fake
// processes. Construct it with New; one instance per test.
type Spawner struct {
	Home string

	mu      sync.Mutex
	live    map[int]scanner.ProcessInfo
	nextPID int
}

type SpawnOpts struct {
	CWD            string       // default: <Home>/work/project
	Provider       sdk.Provider // default: sdk.ProviderClaude
	Model          string       // default: the newest Opus
	LiveInjectable bool         // when true, discovery file has a non-empty tmuxPane
	NoChannel      bool         // when true, do NOT write a discovery file (plain terminal agent)
	Pty            bool         // when true, also write a {pid}.pty.json pty-broker discovery file with ptyInject:true
}

type Agent struct {
	PID       int
	SessionID string
	CWD       string
	JSONLPath string
}

// New seeds a Spawner rooted at a temp HOME. The starting PID is in the high
// range so it is virtually never a real live process — important so the real
// dismiss handler's liveness guard treats it as dead.
func New(t *testing.T) *Spawner {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	return &Spawner{
		Home:    home,
		live:    map[int]scanner.ProcessInfo{},
		nextPID: 2_000_000_000,
	}
}

func (s *Spawner) Spawn(opts SpawnOpts) Agent {
	cwd := opts.CWD
	if cwd == "" {
		cwd = filepath.Join(s.Home, "work", "project")
	}
	provider := opts.Provider
	if provider == "" {
		provider = sdk.ProviderClaude
	}
	model := opts.Model
	if model == "" {
		model = claudemodel.Latest(claudemodel.Opus)
	}

	s.mu.Lock()
	pid := s.nextPID
	s.nextPID++
	s.mu.Unlock()

	// Real UUID, parser-valid by construction (8-4-4-4-12 lowercase hex).
	sessionID := uuid.NewString()

	projectDir := filepath.Join(s.Home, ".claude", "projects", parser.EncodePath(cwd))
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		panic(err)
	}
	ts := time.Now().UTC().Format(time.RFC3339)
	lines := `{"type":"user","sessionId":"` + sessionID + `","timestamp":"` + ts +
		`","message":{"role":"user","content":"hi"}}` + "\n" +
		`{"type":"assistant","sessionId":"` + sessionID + `","timestamp":"` + ts +
		`","message":{"role":"assistant","model":"` + model + `","usage":{"input_tokens":10,"output_tokens":5}}}` + "\n"
	jsonlPath := filepath.Join(projectDir, sessionID+".jsonl")
	if err := os.WriteFile(jsonlPath, []byte(lines), 0o600); err != nil {
		panic(err)
	}

	if !opts.NoChannel {
		discoveryDir := filepath.Join(s.Home, channelconfig.DiscoveryDir)
		if err := os.MkdirAll(discoveryDir, 0o755); err != nil {
			panic(err)
		}
		content := `{"port":1}`
		if opts.LiveInjectable {
			content = `{"port":1,"tmuxPane":"%1"}`
		}
		if err := os.WriteFile(channelconfig.DiscoveryFile(s.Home, pid), []byte(content), 0o600); err != nil {
			panic(err)
		}
	}

	if opts.Pty {
		discoveryDir := filepath.Join(s.Home, channelconfig.DiscoveryDir)
		if err := os.MkdirAll(discoveryDir, 0o755); err != nil {
			panic(err)
		}
		if err := os.WriteFile(channelconfig.DiscoveryPtyFile(s.Home, pid), []byte(`{"port":1,"ptyInject":true}`), 0o600); err != nil {
			panic(err)
		}
	}

	s.mu.Lock()
	s.live[pid] = scanner.ProcessInfo{PID: pid, CWD: cwd, Uptime: 30, Provider: provider}
	s.mu.Unlock()

	return Agent{PID: pid, SessionID: sessionID, CWD: cwd, JSONLPath: jsonlPath}
}

// Exit removes the process from the scan set; on-disk files remain so the agent
// becomes a finished card.
func (s *Spawner) Exit(pid int) {
	s.mu.Lock()
	delete(s.live, pid)
	s.mu.Unlock()
}

// Dismiss removes the discovery files, mirroring the real DELETE endpoint for
// tests that don't drive the HTTP handler.
func (s *Spawner) Dismiss(pid int) error {
	files := []string{
		channelconfig.DiscoveryFile(s.Home, pid),
		channelconfig.DiscoveryPtyFile(s.Home, pid),
	}
	for _, path := range files {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	return nil
}

// ScanFn returns a scan closure suitable for merger.WithScanFn.
func (s *Spawner) ScanFn() func(context.Context) ([]scanner.ProcessInfo, error) {
	return func(context.Context) ([]scanner.ProcessInfo, error) {
		s.mu.Lock()
		defer s.mu.Unlock()
		out := make([]scanner.ProcessInfo, 0, len(s.live))
		for _, p := range s.live {
			out = append(out, p)
		}
		return out, nil
	}
}

func (s *Spawner) DiscoveryPath(pid int) string {
	return channelconfig.DiscoveryFile(s.Home, pid)
}

func (s *Spawner) DiscoveryPtyPath(pid int) string {
	return channelconfig.DiscoveryPtyFile(s.Home, pid)
}
