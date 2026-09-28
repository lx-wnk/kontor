// Package appsetup runs an MCP server's own setup wizard as a child process
// so an operator can complete first-time configuration (OAuth, account
// picking, …) from the dashboard, then throws the child away.
package appsetup

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/lx-wnk/kontor/server/internal/mcpapps"
)

// warningText is shown next to a running setup session everywhere it
// appears — the API response and the UI both render the same sentence.
const warningText = "Setup is reachable on your local network until you click Done."

// stderrCapacity bounds how much of a child's stderr is kept, so a chatty
// child cannot grow the dashboard's memory.
const stderrCapacity = 2 * 1024

// terminateGrace is how long Stop waits for SIGTERM before escalating to
// SIGKILL.
const terminateGrace = 5 * time.Second

// Session describes one running setup wizard.
type Session struct {
	ResourceID string    `json:"resourceId"`
	Port       int       `json:"port"`
	URL        string    `json:"url"` // http://127.0.0.1:<port>
	StartedAt  time.Time `json:"startedAt"`
	Warning    string    `json:"warning"`
}

// Options configures a Manager. Zero values fall back to the defaults noted
// on each field.
type Options struct {
	Now              func() time.Time // default time.Now
	ReadinessTimeout time.Duration    // default 60s
	IdleTimeout      time.Duration    // default 15m
	MaxLifetime      time.Duration    // default 30m
	PollInterval     time.Duration    // default 200ms
}

func (o Options) withDefaults() Options {
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.ReadinessTimeout == 0 {
		o.ReadinessTimeout = 60 * time.Second
	}
	if o.IdleTimeout == 0 {
		o.IdleTimeout = 15 * time.Minute
	}
	if o.MaxLifetime == 0 {
		o.MaxLifetime = 30 * time.Minute
	}
	if o.PollInterval == 0 {
		o.PollInterval = 200 * time.Millisecond
	}
	return o
}

// runningSetup is the manager's private bookkeeping for one Session. cmd's
// Wait() is called exactly once, by the goroutine Start starts, which is the
// sole writer of exited — every other reader only ever receives from it.
type runningSetup struct {
	session    Session
	cmd        *exec.Cmd
	exited     chan error
	stderr     *ringBuffer
	lastAccess time.Time
}

// Manager runs and tracks setup sessions, at most one per application.
type Manager struct {
	mu       sync.Mutex
	sessions map[string]*runningSetup
	opts     Options
}

func NewManager(opts Options) *Manager {
	return &Manager{sessions: map[string]*runningSetup{}, opts: opts.withDefaults()}
}

// Start stops any session already running for resourceID, picks a free
// port, starts setup.Command with {port} substituted in its args, and waits
// for setup.Readiness to answer 2xx. The child also receives the chosen
// port as the SETUP_PORT environment variable, for setup commands that read
// their port from the environment rather than argv.
func (m *Manager) Start(ctx context.Context, resourceID string, setup mcpapps.PresetSetup, env map[string]string) (Session, error) {
	_ = m.Stop(resourceID)

	port, err := freePort()
	if err != nil {
		return Session{}, fmt.Errorf("appsetup: pick a free port: %w", err)
	}

	args := make([]string, len(setup.Args))
	for i, a := range setup.Args {
		args[i] = strings.ReplaceAll(a, "{port}", strconv.Itoa(port))
	}

	// #nosec G204 -- setup.Command/Args come from an embedded mcpapps preset (mcpapps.FindPreset), not from request input; the only substitution here is the {port} literal the preset itself declares.
	cmd := exec.Command(setup.Command, args...)
	cmd.Env = append(os.Environ(), envList(env)...)
	cmd.Env = append(cmd.Env, "SETUP_PORT="+strconv.Itoa(port))
	stderr := newRingBuffer(stderrCapacity)
	cmd.Stderr = stderr

	if err := cmd.Start(); err != nil {
		return Session{}, fmt.Errorf("appsetup: start setup command: %w", err)
	}

	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()

	readinessURL := fmt.Sprintf("http://127.0.0.1:%d%s", port, setup.Readiness)
	childExited, err := waitReady(ctx, readinessURL, m.opts.ReadinessTimeout, m.opts.PollInterval, exited)
	if err != nil {
		if !childExited {
			terminateProcess(cmd, exited)
		}
		msg := strings.TrimSpace(stderr.String())
		if msg != "" {
			return Session{}, fmt.Errorf("appsetup: %w: %s", err, msg)
		}
		return Session{}, fmt.Errorf("appsetup: %w", err)
	}

	sess := Session{
		ResourceID: resourceID,
		Port:       port,
		URL:        fmt.Sprintf("http://127.0.0.1:%d", port),
		StartedAt:  m.opts.Now(),
		Warning:    warningText,
	}

	m.mu.Lock()
	m.sessions[resourceID] = &runningSetup{session: sess, cmd: cmd, exited: exited, stderr: stderr, lastAccess: sess.StartedAt}
	m.mu.Unlock()

	return sess, nil
}

// waitReady polls url every pollInterval until it answers 2xx, the deadline
// passes, ctx is cancelled, or the child exits first. childExited reports
// whether the failure already drained exited's single buffered value — the
// caller must not wait on it again, or it would block forever.
func waitReady(ctx context.Context, url string, deadline, pollInterval time.Duration, exited <-chan error) (childExited bool, err error) {
	timeout := time.NewTimer(deadline)
	defer timeout.Stop()
	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()

	client := &http.Client{Timeout: pollInterval}
	for {
		select {
		case werr := <-exited:
			if werr == nil {
				werr = fmt.Errorf("exited before it became ready")
			}
			return true, fmt.Errorf("setup command exited before it was ready (%v)", werr)
		case <-timeout.C:
			return false, fmt.Errorf("setup did not become ready within %s", deadline)
		case <-ctx.Done():
			return false, ctx.Err()
		case <-ticker.C:
			resp, herr := client.Get(url)
			if herr != nil {
				continue
			}
			_ = resp.Body.Close()
			if resp.StatusCode >= 200 && resp.StatusCode < 300 {
				return false, nil
			}
		}
	}
}

// Stop ends resourceID's session, if any. It is idempotent.
func (m *Manager) Stop(resourceID string) error {
	m.mu.Lock()
	rs, ok := m.sessions[resourceID]
	if ok {
		delete(m.sessions, resourceID)
	}
	m.mu.Unlock()
	if !ok {
		return nil
	}
	terminateProcess(rs.cmd, rs.exited)
	return nil
}

// Get returns resourceID's session, if one is running, and touches its
// idle-timeout clock.
func (m *Manager) Get(resourceID string) (Session, bool) {
	m.mu.Lock()
	rs, ok := m.sessions[resourceID]
	if ok {
		rs.lastAccess = m.opts.Now()
	}
	m.mu.Unlock()
	if !ok {
		return Session{}, false
	}
	return rs.session, true
}

// Sweep stops every session that has been idle past IdleTimeout, or alive
// past MaxLifetime, measured against Now.
func (m *Manager) Sweep() {
	now := m.opts.Now()
	m.mu.Lock()
	var expired []*runningSetup
	for id, rs := range m.sessions {
		if now.Sub(rs.lastAccess) > m.opts.IdleTimeout || now.Sub(rs.session.StartedAt) > m.opts.MaxLifetime {
			expired = append(expired, rs)
			delete(m.sessions, id)
		}
	}
	m.mu.Unlock()
	for _, rs := range expired {
		terminateProcess(rs.cmd, rs.exited)
	}
}

// StopAll ends every running session.
func (m *Manager) StopAll() {
	m.mu.Lock()
	all := make([]*runningSetup, 0, len(m.sessions))
	for id, rs := range m.sessions {
		all = append(all, rs)
		delete(m.sessions, id)
	}
	m.mu.Unlock()
	for _, rs := range all {
		terminateProcess(rs.cmd, rs.exited)
	}
}

// terminateProcess sends SIGTERM and waits up to terminateGrace for exited to
// receive before escalating to SIGKILL. exited must be the channel the sole
// cmd.Wait() goroutine for cmd writes to.
func terminateProcess(cmd *exec.Cmd, exited <-chan error) {
	if cmd.Process == nil {
		return
	}
	_ = cmd.Process.Signal(syscall.SIGTERM)
	select {
	case <-exited:
	case <-time.After(terminateGrace):
		_ = cmd.Process.Kill()
		<-exited
	}
}

func freePort() (int, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	port := ln.Addr().(*net.TCPAddr).Port
	if err := ln.Close(); err != nil {
		return 0, err
	}
	return port, nil
}

func envList(env map[string]string) []string {
	out := make([]string, 0, len(env))
	for k, v := range env {
		out = append(out, k+"="+v)
	}
	return out
}

// ringBuffer is an io.Writer that keeps only the last N bytes written to it.
type ringBuffer struct {
	mu   sync.Mutex
	buf  []byte
	size int
}

func newRingBuffer(size int) *ringBuffer {
	return &ringBuffer{size: size}
}

func (r *ringBuffer) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.buf = append(r.buf, p...)
	if len(r.buf) > r.size {
		r.buf = r.buf[len(r.buf)-r.size:]
	}
	return len(p), nil
}

func (r *ringBuffer) String() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return string(r.buf)
}

// RunSweeps stops expired sessions every interval until ctx is done. The
// timeouts are what end a setup nobody closed — the operator may never click
// Done, and the browser tab may simply go away — so without this loop the
// idle and max-lifetime limits would never fire.
func (m *Manager) RunSweeps(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		interval = time.Minute
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			m.Sweep()
		}
	}
}
