package agents

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/google/uuid"
	"github.com/lx-wnk/kontor/server/internal/auth"
	"github.com/lx-wnk/kontor/server/internal/channelconfig"
	"github.com/lx-wnk/kontor/server/internal/db/ent"
	"github.com/lx-wnk/kontor/server/internal/db/repo"
	"github.com/lx-wnk/kontor/server/internal/envsec"
	"github.com/lx-wnk/kontor/server/internal/httputil"
	"github.com/lx-wnk/kontor/server/internal/services"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

const (
	spawnStoreMaxAge  = time.Hour
	systemPromptMax   = 10000
	channelMsgTimeout = 5 * time.Second
)

var (
	claudeBin = func() string {
		if p, err := exec.LookPath("claude"); err == nil {
			return p
		}
		return "claude"
	}()
	uuidRE = regexp.MustCompile(`(?i)^[a-f0-9]{8}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{12}$`)
)

// execStart is the seam used by tests to intercept spawn args without
// actually launching a process. Production uses cmd.Start directly.
var execStart = func(cmd *exec.Cmd) error { return cmd.Start() }

// lookTmuxPath is a seam so tests can force tmux present/absent.
var lookTmuxPath = func() string { p, _ := exec.LookPath("tmux"); return p }

// SpawnStatus tracks the state of a user-initiated agent spawn.
type SpawnStatus struct {
	PID       int    `json:"pid"`
	Status    string `json:"status"` // "running" | "exited" | "error"
	ExitCode  *int   `json:"exitCode"`
	Stderr    string `json:"stderr"`
	StartedAt string `json:"startedAt"`
	Prompt    string `json:"prompt"`
	Cwd       string `json:"cwd"`
	SpawnerID string `json:"spawnerId,omitempty"`
}

// SpawnManager rate-limits and tracks user-initiated Claude agent spawns.
type SpawnManager struct {
	// rateLimitMax and rateLimitWindow mirror spawnLimiter fields for use in
	// 429 error message formatting in the Spawn handler.
	rateLimitMax    int
	rateLimitWindow time.Duration

	spawnerRepo       repo.SpawnerRepo
	spawnPolicy       services.SpawnPolicy
	projectFolderRepo repo.ProjectFolderRepo // may be nil

	spawnLimiter  *slidingWindowLimiter
	injectLimiter *slidingWindowLimiter

	// userAttempts aliases spawnLimiter.attempts (same underlying Go map) so
	// existing test helpers that set state via m.userAttempts still work.
	userAttempts map[string][]time.Time

	mu         sync.Mutex // protects spawnStore only
	spawnStore map[int]*SpawnStatus
}

// NewSpawnManager creates a SpawnManager with the given rate limit config.
// policy controls which working directories may be used as spawn cwd; pass nil
// to enforce only the sensitive-dir blacklist (development / bypass-auth mode).
// injectMax / injectWindowMs configure the per-user inject rate limit.
func NewSpawnManager(maxSpawns int, windowMs int, injectMax int, injectWindowMs int, spawnerRepo repo.SpawnerRepo, policy services.SpawnPolicy) *SpawnManager {
	const (
		defaultSpawnMax     = 5
		defaultSpawnWindow  = 60_000
		defaultInjectMax    = 30
		defaultInjectWindow = 60_000
	)
	if policy == nil {
		policy = services.NewSpawnPolicy(nil)
	}
	spawnLimiter := newSlidingWindowLimiter(
		maxSpawns, time.Duration(windowMs)*time.Millisecond,
		defaultSpawnMax, time.Duration(defaultSpawnWindow)*time.Millisecond,
	)
	injectLimiter := newSlidingWindowLimiter(
		injectMax, time.Duration(injectWindowMs)*time.Millisecond,
		defaultInjectMax, time.Duration(defaultInjectWindow)*time.Millisecond,
	)
	return &SpawnManager{
		rateLimitMax:    spawnLimiter.max,
		rateLimitWindow: spawnLimiter.window,
		spawnerRepo:     spawnerRepo,
		spawnPolicy:     policy,
		spawnLimiter:    spawnLimiter,
		injectLimiter:   injectLimiter,
		userAttempts:    spawnLimiter.attempts, // same underlying map — for test compat
		spawnStore:      make(map[int]*SpawnStatus),
	}
}

// SetProjectFolderRepo wires in a ProjectFolderRepo so that Spawn can inject
// --add-dir flags for multi-folder projects. Call once after NewSpawnManager
// before any spawns; safe to call with nil (disables --add-dir injection).
func (m *SpawnManager) SetProjectFolderRepo(r repo.ProjectFolderRepo) {
	m.projectFolderRepo = r
}

// IsSpawnAllowed reports whether a new spawn is allowed for the given user (sub).
// Pass "__global__" in bypass-auth mode.
func (m *SpawnManager) IsSpawnAllowed(sub string) bool {
	return m.spawnLimiter.Allow(sub)
}

// IsInjectAllowed reports whether a new live injection is allowed for sub.
func (m *SpawnManager) IsInjectAllowed(sub string) bool {
	return m.injectLimiter.Allow(sub)
}

// RecordInject records an inject attempt for sub.
func (m *SpawnManager) RecordInject(sub string) {
	m.injectLimiter.Record(sub)
}

// InjectAllowAndRecord atomically gates and records a live-injection attempt,
// closing the check-then-record race. Returns false without recording when sub
// is at the limit.
func (m *SpawnManager) InjectAllowAndRecord(sub string) bool {
	return m.injectLimiter.AllowAndRecord(sub)
}

func (m *SpawnManager) recordAttempt(sub string) {
	m.spawnLimiter.Record(sub)
}

// pruneAttempts prunes stale entries for sub from the spawn limiter.
// Retained as a method for backward-compat with existing tests.
func (m *SpawnManager) pruneAttempts(sub string) {
	m.spawnLimiter.mu.Lock()
	defer m.spawnLimiter.mu.Unlock()
	m.spawnLimiter.prune(sub)
}

// spawnRequest carries validated fields extracted from the raw request body.
type spawnRequest struct {
	prompt          string
	cwd             string
	resumeSessionID string
	model           string
	systemPrompt    string
	permissionMode  string
	projectID       string
	enableChannel   bool
}

// enforceSpawnPolicy validates the request body fields and applies the spawn
// policy gate. Rate-limit recordAttempt must be called before this method.
func (m *SpawnManager) enforceSpawnPolicy(body map[string]any) (*spawnRequest, error) {
	prompt, _ := body["prompt"].(string)
	if prompt == "" {
		return nil, fmt.Errorf("missing or invalid prompt")
	}
	cwd, _ := body["cwd"].(string)
	if cwd == "" {
		return nil, fmt.Errorf("missing or invalid cwd")
	}
	if _, err := os.Stat(cwd); err != nil {
		return nil, fmt.Errorf("directory does not exist: %s", cwd)
	}

	resumeSessionID, _ := body["resumeSessionId"].(string)
	if resumeSessionID != "" && !uuidRE.MatchString(resumeSessionID) {
		return nil, fmt.Errorf("invalid sessionId format")
	}

	// Resuming an existing monitored session runs in the cwd that session already
	// uses, so only the sensitive-dir blacklist applies. Fresh spawns must pass
	// the full project-roots allowlist.
	if resumeSessionID != "" {
		if err := m.spawnPolicy.AllowResume(cwd); err != nil {
			return nil, err
		}
	} else if err := m.spawnPolicy.Allow(context.Background(), cwd); err != nil {
		return nil, err
	}

	model, _ := body["model"].(string)
	systemPrompt, _ := body["systemPrompt"].(string)
	if len(systemPrompt) > systemPromptMax {
		systemPrompt = systemPrompt[:systemPromptMax]
	}

	permissionMode, _ := body["permissionMode"].(string)
	if permissionMode == "" {
		permissionMode = "default"
	} else {
		if _, ok := allowedPermissionModes[permissionMode]; !ok {
			return nil, fmt.Errorf("invalid permissionMode")
		}
	}

	projectID, _ := body["projectId"].(string)

	enableChannel, _ := body["enableChannel"].(bool)
	if _, hasChannel := body["enableChannel"]; !hasChannel {
		enableChannel = true // default on
	}

	return &spawnRequest{
		prompt:          prompt,
		cwd:             cwd,
		resumeSessionID: resumeSessionID,
		model:           model,
		systemPrompt:    systemPrompt,
		permissionMode:  permissionMode,
		projectID:       projectID,
		enableChannel:   enableChannel,
	}, nil
}

// resolveSpawner looks up the spawner row when spawnerId is set in the body,
// rejects unsupported adapter types, and hydrates req.model from ModelOverride
// when the caller did not supply one.
func (m *SpawnManager) resolveSpawner(body map[string]any, req *spawnRequest) (*ent.Spawner, error) {
	spawnerID, ok := body["spawnerId"].(string)
	if !ok || spawnerID == "" {
		return nil, nil
	}

	if m.spawnerRepo == nil {
		return nil, fmt.Errorf("spawner not configured")
	}
	row, err := m.spawnerRepo.GetByID(context.Background(), spawnerID)
	if err != nil {
		if ent.IsNotFound(err) {
			return nil, fmt.Errorf("spawner not found")
		}
		return nil, fmt.Errorf("spawner lookup failed: %w", err)
	}
	switch row.AdapterType {
	case "ollama", "openai":
		return nil, fmt.Errorf("adapter %s not supported for user-initiated spawns; use pipeline tasks instead", row.AdapterType)
	}

	// Hydrate model from override when caller didn't supply one.
	if req.model == "" && row.ModelOverride != nil && *row.ModelOverride != "" {
		req.model = *row.ModelOverride
	}

	return row, nil
}

// buildSpawnArgs assembles the binary path and full argument list for the
// claude process. Order: spawner args first, canonical args last.
func (m *SpawnManager) buildSpawnArgs(req *spawnRequest, spawnerRow *ent.Spawner) (binary string, args []string, err error) {
	binary = claudeBin
	var spawnerArgs []string

	if spawnerRow != nil {
		if ok, reason := services.ValidateSpawnerCommand(spawnerRow.Command); !ok {
			return "", nil, fmt.Errorf("spawner command not permitted: %s", reason)
		}
		if bad := firstReservedFlag(spawnerRow.Args); bad != "" {
			return "", nil, fmt.Errorf("spawner args may not include reserved flag %q", bad)
		}
		if spawnerRow.AdapterType == "custom" {
			binary = spawnerRow.Command
		}
		spawnerArgs = append(spawnerArgs, spawnerRow.Args...)
	}

	var canonicalArgs []string
	switch {
	case req.resumeSessionID != "":
		canonicalArgs = append(canonicalArgs, "--resume", req.resumeSessionID)
	case nativeClaudeAdapter(spawnerRow):
		// Pin the session up front. Claude writes sessions/{pid}.json only after
		// startup, and until it exists the resolver falls back to "newest session
		// file in this project with recent activity" — which is the *running*
		// agent's session when the folder already has one. The new agent then
		// shows the old agent's transcript. An id in argv is visible on the very
		// first scan tick (parser.SessionIDFromArgs).
		canonicalArgs = append(canonicalArgs, "--session-id", uuid.NewString())
	}
	if req.model != "" {
		canonicalArgs = append(canonicalArgs, "--model", req.model)
	}
	if req.systemPrompt != "" {
		canonicalArgs = append(canonicalArgs, "--system-prompt", req.systemPrompt)
	}
	// Only append --permission-mode when the resolved spawner has not declared
	// its own permission posture. A spawner that already passes --permission-mode
	// or --dangerously-skip-permissions would otherwise get a second, conflicting
	// flag which causes claude to error.
	if !spawnerArgsControlPermissionMode(spawnerArgs) {
		canonicalArgs = append(canonicalArgs, "--permission-mode", req.permissionMode)
	}

	// Inject --add-dir for additional project folders (multi-folder projects).
	// Only applies to native claude and custom adapters; ollama/openai are already
	// blocked above, so this guard is defence-in-depth for any future adapter types.
	if req.projectID != "" && m.projectFolderRepo != nil &&
		(spawnerRow == nil || spawnerRow.AdapterType == "" || spawnerRow.AdapterType == "claude" || spawnerRow.AdapterType == "custom") {
		folders, lerr := m.projectFolderRepo.ListByProject(context.Background(), req.projectID)
		if lerr != nil {
			slog.Warn("spawn: ListByProject failed; skipping --add-dir injection", "projectId", req.projectID, "err", lerr)
		} else {
			for _, dir := range services.AdditionalDirsForProject(folders, req.cwd) {
				canonicalArgs = append(canonicalArgs, "--add-dir", dir)
			}
		}
	}

	// Order: spawner args first, canonical args last so user-supplied flags win.
	// The prompt is passed as a trailing positional argument (no -p) so claude
	// starts an interactive session seeded with it instead of one-shot print mode.
	args = append(spawnerArgs, canonicalArgs...)
	if req.prompt != "" {
		args = append(args, req.prompt)
	}
	return binary, args, nil
}

// nativeClaudeAdapter reports whether the resolved spawner runs the claude CLI
// itself, so claude-only flags are safe to add. Custom adapters run an arbitrary
// binary and would choke on them.
func nativeClaudeAdapter(spawnerRow *ent.Spawner) bool {
	return spawnerRow == nil || spawnerRow.AdapterType == "" || spawnerRow.AdapterType == "claude"
}

// Spawn validates the request, spawns a claude process, and returns the PID.
// sub identifies the requesting user (JWT sub claim). Pass "__global__" in bypass-auth mode.
func (m *SpawnManager) Spawn(sub string, body map[string]any) (int, error) {
	m.recordAttempt(sub)

	req, err := m.enforceSpawnPolicy(body)
	if err != nil {
		return 0, err
	}

	spawnerRow, err := m.resolveSpawner(body, req)
	if err != nil {
		return 0, err
	}

	if req.projectID != "" {
		var spawnerID string
		if spawnerRow != nil {
			spawnerID = spawnerRow.ID
		}
		slog.Info("spawn: projectId attached", "sub", sub, "projectId", req.projectID, "spawnerId", spawnerID)
	}

	binary, args, err := m.buildSpawnArgs(req, spawnerRow)
	if err != nil {
		return 0, err
	}

	var channelCfgPath string
	if req.enableChannel {
		selfBin, selfErr := channelconfig.SelfBinaryPath()
		if selfErr != nil {
			slog.Warn("spawn: channel disabled — cannot resolve self binary", "err", selfErr)
		} else if cfgPath, cfgErr := channelconfig.WriteTempConfig(selfBin, nil, nil); cfgErr != nil {
			slog.Warn("spawn: channel disabled — cannot write MCP config", "err", cfgErr)
		} else {
			channelCfgPath = cfgPath
			channelArg := "--mcp-config"
			if spawnerRow != nil && spawnerRow.AdapterType == "custom" {
				if v, ok := spawnerRow.AdapterConfig["channel_arg"]; ok && v != "" {
					channelArg = v
				}
			}
			args = append(args, channelArg, cfgPath)
		}
	}

	env := resolveSpawnEnv(spawnerRow)
	pid, watch, err := m.launchInteractive(binary, args, env, req.cwd, channelCfgPath)
	if err != nil {
		if channelCfgPath != "" {
			_ = os.Remove(channelCfgPath)
		}
		return 0, fmt.Errorf("spawn failed: %w", err)
	}
	status := &SpawnStatus{
		PID: pid, Status: "running",
		StartedAt: time.Now().UTC().Format(time.RFC3339),
		Prompt:    req.prompt[:min(len(req.prompt), 200)],
		Cwd:       req.cwd,
	}
	if spawnerRow != nil {
		status.SpawnerID = spawnerRow.ID
	}
	m.mu.Lock()
	m.spawnStore[pid] = status
	m.mu.Unlock()
	go watch()
	return pid, nil
}

// launchInteractive starts the resolved command under a headless live transport
// so the spawned agent is injectable. With tmux on PATH it creates a detached
// session and captures the pane PID; otherwise it spawns a detached pty-host
// subprocess that owns the pty and prints the child PID on its first stdout line.
// It returns the claude PID and a watch closure to run in a goroutine.
func (m *SpawnManager) launchInteractive(binary string, args, env []string, cwd, channelCfgPath string) (int, func(), error) {
	switch selectHeadlessTransport(lookTmuxPath()) {
	case transportTmux:
		session := "claude-spawn-" + newSpawnID()
		// #nosec G204 -- the executed binary is the literal "tmux"; the child binary is claudeBin unless a spawner row passed services.ValidateSpawnerCommand in buildSpawnArgs, and request-derived values are separate argv elements of tmux's multi-arg form (execvp, no shell) — but req.prompt is appended as a bare trailing positional with no "--" terminator, so a prompt starting with "--" reaches the child as a flag; that grants nothing today because bypassPermissions is already an accepted permissionMode.
		cmd := exec.Command("tmux", buildTmuxArgs(session, env, binary, args)...)
		cmd.Dir = cwd
		var buf bytes.Buffer
		cmd.Stdout = &buf
		if err := execStart(cmd); err != nil {
			return 0, nil, err
		}
		_ = cmd.Wait() // tmux client exits immediately after creating the detached session
		// Empty output means the transport produced no pid (test stub or a failed
		// start), so there is no watch to clean up the cfg — remove it here.
		if strings.TrimSpace(buf.String()) == "" {
			if channelCfgPath != "" {
				_ = os.Remove(channelCfgPath)
			}
			return 0, func() {}, nil
		}
		pid, perr := parsePanePID(buf.String())
		if perr != nil {
			return 0, nil, perr
		}
		return pid, m.pollExitWatch(pid, channelCfgPath), nil
	default: // transportPTY
		return m.launchPTY(binary, args, env, cwd, channelCfgPath)
	}
}

// launchPTY starts binary under a detached pty-host subprocess that owns the
// pty and prints the child PID on its first stdout line. It returns the
// claude PID and a watch closure to run in a goroutine.
func (m *SpawnManager) launchPTY(binary string, args, env []string, cwd, channelCfgPath string) (int, func(), error) {
	self, serr := channelconfig.SelfBinaryPath()
	if serr != nil {
		return 0, nil, serr
	}
	hostArgs := append([]string{channelconfig.SubcommandPtyHost, "--", binary}, args...)
	cmd := exec.Command(self, hostArgs...)
	cmd.Dir = cwd
	cmd.Env = env
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	pipe, _ := cmd.StdoutPipe()
	if err := execStart(cmd); err != nil {
		return 0, nil, err
	}
	pid, empty, rerr := readFirstPID(pipe)
	// Empty output means the transport produced no pid (test stub or a failed
	// start), so there is no watch to clean up the cfg — remove it here.
	if empty {
		if channelCfgPath != "" {
			_ = os.Remove(channelCfgPath)
		}
		return 0, func() {}, nil
	}
	if rerr != nil {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		return 0, nil, rerr
	}
	return pid, m.subprocessExitWatch(cmd, pid, channelCfgPath), nil
}

// newSpawnID returns a short random hex token for naming a tmux session.
func newSpawnID() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return strconv.FormatInt(time.Now().UnixNano(), 16)
	}
	return hex.EncodeToString(b[:])
}

// readFirstPID reads the first line from r and parses it as a PID. empty is
// true when r yields no data (the pty-host subprocess printed nothing), which
// the caller treats as a no-op spawn rather than an error.
func readFirstPID(r io.Reader) (pid int, empty bool, err error) {
	if r == nil {
		return 0, true, nil
	}
	line, _ := bufio.NewReader(r).ReadString('\n')
	if strings.TrimSpace(line) == "" {
		return 0, true, nil
	}
	pid, err = parsePanePID(line)
	return pid, false, err
}

// markExited sets the spawnStore status for pid to "exited" and removes the
// channel config file. Safe to call once a transport's process has gone.
func (m *SpawnManager) markExited(pid int, channelCfgPath string) {
	m.mu.Lock()
	if s := m.spawnStore[pid]; s != nil {
		s.Status = "exited"
	}
	m.mu.Unlock()
	if channelCfgPath != "" {
		_ = os.Remove(channelCfgPath)
	}
}

// pollExitWatch returns a closure that polls the tmux pane PID until it exits,
// then marks the spawn exited. Capped at spawnStoreMaxAge to avoid an eternal
// goroutine when the PID is never reaped.
func (m *SpawnManager) pollExitWatch(pid int, channelCfgPath string) func() {
	return func() {
		deadline := time.Now().Add(spawnStoreMaxAge)
		for time.Now().Before(deadline) {
			if err := syscall.Kill(pid, 0); err != nil {
				break
			}
			time.Sleep(2 * time.Second)
		}
		m.markExited(pid, channelCfgPath)
	}
}

// subprocessExitWatch returns a closure that waits for the pty-host subprocess
// to exit, then marks the spawn exited.
func (m *SpawnManager) subprocessExitWatch(cmd *exec.Cmd, pid int, channelCfgPath string) func() {
	return func() {
		_ = cmd.Wait()
		m.markExited(pid, channelCfgPath)
	}
}

// StartPruner starts a background goroutine that prunes spawnStore and both
// rate limiters' stale entries. Returns when ctx is cancelled.
func (m *SpawnManager) StartPruner(ctx context.Context) {
	ticker := time.NewTicker(spawnStoreMaxAge / 2)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			now := time.Now()
			m.mu.Lock()
			for k, s := range m.spawnStore {
				t, err := time.Parse(time.RFC3339, s.StartedAt)
				if err == nil && now.Sub(t) > spawnStoreMaxAge {
					delete(m.spawnStore, k)
				}
			}
			m.mu.Unlock()
			m.spawnLimiter.pruneAll(now)
			m.injectLimiter.pruneAll(now)
		case <-ctx.Done():
			return
		}
	}
}

// GetStatus returns the status of a spawned agent by PID, or nil if unknown.
func (m *SpawnManager) GetStatus(pid int) *SpawnStatus {
	m.mu.Lock()
	defer m.mu.Unlock()
	s := m.spawnStore[pid]
	if s == nil {
		return nil
	}
	// Return a copy to avoid data races on the caller side.
	cp := *s
	return &cp
}

// SendMessageToChannel forwards a message to the running interactive Claude
// session identified by pid. It sanitizes the message before delivery and
// consults two discovery files written by the pty-broker and channel-bridge,
// applying the following delivery precedence:
//
//  1. Bridge file ({pid}.json) with a non-empty tmuxPane → tmux send-keys
//     (most reliable; the multiplexer owns the pty).
//  2. Pty file ({pid}.pty.json) with a non-zero port → POST to the pty-broker
//     HTTP endpoint (loopback; the broker owns the pty master).
//  3. Bridge file ({pid}.json) with a non-zero port → POST to the channel-bridge
//     HTTP endpoint (MCP log channel; legacy / pipeline-agent path).
//
// Returns the chosen transport ("tmux", "pty", or "bridge") and any error.
// transport is "" when no channel is available.
func (m *SpawnManager) SendMessageToChannel(ctx context.Context, pid int, message string) (transport string, err error) {
	message = sanitizeInjectMessage(message)

	home, herr := os.UserHomeDir()
	if herr != nil {
		return "", fmt.Errorf("UserHomeDir: %w", herr)
	}
	// Attempt 1: read the bridge file for tmux delivery.
	var bridgePort int
	var bridgeToken string
	if data, rerr := os.ReadFile(channelconfig.DiscoveryFile(home, pid)); rerr == nil {
		var disc struct {
			Port       int    `json:"port"`
			Token      string `json:"token"`
			TmuxPane   string `json:"tmuxPane"`
			TmuxSocket string `json:"tmuxSocket"`
		}
		if json.Unmarshal(data, &disc) == nil {
			if disc.TmuxPane != "" {
				// Highest-priority path: tmux send-keys.
				return "tmux", sendKeysToTmux(ctx, disc.TmuxSocket, disc.TmuxPane, message)
			}
			bridgePort = disc.Port
			bridgeToken = disc.Token
		}
	}

	// Attempt 2: read the pty file for loopback-HTTP delivery.
	if data, rerr := os.ReadFile(channelconfig.DiscoveryPtyFile(home, pid)); rerr == nil {
		var disc struct {
			Port  int    `json:"port"`
			Token string `json:"token"`
		}
		if json.Unmarshal(data, &disc) == nil && disc.Port != 0 {
			return "pty", sendHTTPMessage(ctx, disc.Port, disc.Token, message)
		}
	}

	// Attempt 3: fall back to the bridge HTTP endpoint (legacy/MCP-log path).
	if bridgePort != 0 {
		return "bridge", sendHTTPMessage(ctx, bridgePort, bridgeToken, message)
	}

	return "", fmt.Errorf("channel not available for PID %d", pid)
}

// SendAnswerKeys delivers a sequence of raw answer-keystroke tokens (the
// output of EncodeAnswer) to the running interactive Claude session
// identified by pid, driving its AskUserQuestion TUI selector directly.
// Unlike SendMessageToChannel, tokens are written byte-for-byte with no
// sanitization and no appended CR — the caller has already encoded the exact
// submit sequence (e.g. a single-select answer is a bare digit, no Enter).
//
// Delivery precedence mirrors SendMessageToChannel's discovery-file lookup,
// minus the legacy bridge-HTTP fallback: answer delivery drives a real
// terminal selector, which requires an actual pty or tmux pane, not the MCP
// log channel.
//
//  1. Bridge file ({pid}.json) with a non-empty tmuxPane → tmux send-keys,
//     one invocation per token.
//  2. Pty file ({pid}.pty.json) with a non-zero port → POST the concatenated
//     token bytes to the pty broker's /keys endpoint (raw, no CR appended).
//
// Returns the chosen transport ("tmux" or "pty") and any error. Returns
// ErrNoAnswerChannel (wrapped) when neither discovery file yields a channel.
func (m *SpawnManager) SendAnswerKeys(ctx context.Context, pid int, tokens []string) (transport string, err error) {
	home, herr := os.UserHomeDir()
	if herr != nil {
		return "", fmt.Errorf("UserHomeDir: %w", herr)
	}

	if data, rerr := os.ReadFile(channelconfig.DiscoveryFile(home, pid)); rerr == nil {
		var disc struct {
			TmuxPane   string `json:"tmuxPane"`
			TmuxSocket string `json:"tmuxSocket"`
		}
		if json.Unmarshal(data, &disc) == nil && disc.TmuxPane != "" {
			return "tmux", sendAnswerKeysToTmux(ctx, disc.TmuxSocket, disc.TmuxPane, tokens)
		}
	}

	if data, rerr := os.ReadFile(channelconfig.DiscoveryPtyFile(home, pid)); rerr == nil {
		var disc struct {
			Port  int    `json:"port"`
			Token string `json:"token"`
		}
		if json.Unmarshal(data, &disc) == nil && disc.Port != 0 {
			raw := strings.Join(tokens, "")
			return "pty", sendHTTPKeys(ctx, disc.Port, disc.Token, raw)
		}
	}

	return "", fmt.Errorf("%w (pid %d)", ErrNoAnswerChannel, pid)
}

// ErrNoTerminal indicates the session identified by pid has no pty-broker
// discovery file, meaning there is no live terminal to attach to.
var ErrNoTerminal = errors.New("session has no pty terminal")

// TerminalTarget resolves the pty-broker port and auth token for the running
// session identified by pid, reading the pty discovery file written by the
// pty broker for its /ws transport. Returns ErrNoTerminal when the session
// has no pty-broker discovery file (e.g. it never enabled the web terminal).
func (m *SpawnManager) TerminalTarget(pid int) (port int, token string, err error) {
	home, herr := os.UserHomeDir()
	if herr != nil {
		return 0, "", fmt.Errorf("UserHomeDir: %w", herr)
	}

	data, rerr := os.ReadFile(channelconfig.DiscoveryPtyFile(home, pid))
	if rerr != nil {
		if errors.Is(rerr, os.ErrNotExist) {
			return 0, "", ErrNoTerminal
		}
		return 0, "", fmt.Errorf("read pty discovery file: %w", rerr)
	}

	var disc struct {
		Port  int    `json:"port"`
		Token string `json:"token"`
	}
	if err := json.Unmarshal(data, &disc); err != nil {
		return 0, "", fmt.Errorf("parse pty discovery file: %w", err)
	}
	if disc.Port == 0 {
		return 0, "", ErrNoTerminal
	}

	return disc.Port, disc.Token, nil
}

// sanitizeInjectMessage strips control characters that could inject premature
// Enter/submit or produce unexpected terminal behaviour. Tab (0x09) is
// preserved. DEL (0x7F) and all CR/LF sequences are removed.
func sanitizeInjectMessage(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		switch {
		case r == '\t':
			b.WriteRune(r) // horizontal tab preserved
		case r == '\n' || r == '\r':
			// strip — pty appends its own \r to submit
		case r == 0x7F:
			// DEL stripped
		case r < 0x20:
			// C0 control characters stripped
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// sendHTTPMessage POSTs message to http://127.0.0.1:{port}/message authenticated
// with token as a Bearer credential. Shared by the pty-inject and bridge-HTTP paths.
func sendHTTPMessage(ctx context.Context, port int, token, message string) error {
	body, _ := json.Marshal(map[string]string{"message": message})
	req, err := http.NewRequestWithContext(
		ctx,
		"POST",
		fmt.Sprintf("http://127.0.0.1:%d/message", port),
		bytes.NewReader(body),
	)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)

	client := &http.Client{Timeout: channelMsgTimeout}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("channel unreachable: %w", err)
	}
	defer resp.Body.Close()
	if !httputil.Is2xx(resp.StatusCode) {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return fmt.Errorf("channel error %d: %s", resp.StatusCode, strings.TrimSpace(string(b)))
	}
	return nil
}

// sendHTTPKeys POSTs raw bytes to http://127.0.0.1:{port}/keys, authenticated
// with token as a Bearer credential. Unlike sendHTTPMessage, the body is
// written to the pty verbatim by the broker — no JSON envelope, no appended
// CR — so the caller controls the exact byte sequence (digits, Tab, Enter).
func sendHTTPKeys(ctx context.Context, port int, token, raw string) error {
	req, err := http.NewRequestWithContext(
		ctx,
		"POST",
		fmt.Sprintf("http://127.0.0.1:%d/keys", port),
		strings.NewReader(raw),
	)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/octet-stream")
	req.Header.Set("Authorization", "Bearer "+token)

	client := &http.Client{Timeout: channelMsgTimeout}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("channel unreachable: %w", err)
	}
	defer resp.Body.Close()
	if !httputil.Is2xx(resp.StatusCode) {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return fmt.Errorf("channel error %d: %s", resp.StatusCode, strings.TrimSpace(string(b)))
	}
	return nil
}

// AgentDismisser forgets a finished agent from the in-memory finished-card
// tracker. Satisfied by *merger.Merger; kept as an interface so this package
// does not import merger.
type AgentDismisser interface {
	DismissAgent(pid int)
}

// SpawnHandler handles spawn-related HTTP endpoints.
type SpawnHandler struct {
	manager   *SpawnManager
	auditRepo repo.AuditEventRepo // may be nil
	dismisser AgentDismisser      // may be nil
}

// NewSpawnHandler creates a SpawnHandler backed by the given manager.
func NewSpawnHandler(manager *SpawnManager) *SpawnHandler {
	return &SpawnHandler{manager: manager}
}

// SetAuditRepo wires an audit repository into the handler for injection audit logging.
func (h *SpawnHandler) SetAuditRepo(r repo.AuditEventRepo) {
	h.auditRepo = r
}

// SetAgentDismisser wires the finished-card tracker so DismissChannel can forget
// a dismissed agent. When nil, dismissal only cleans up discovery files.
func (h *SpawnHandler) SetAgentDismisser(d AgentDismisser) {
	h.dismisser = d
}

// Spawn handles POST /api/agents/spawn.
func (h *SpawnHandler) Spawn(w http.ResponseWriter, r *http.Request) {
	// Extract user identity for per-user rate limiting.
	sub := "__global__"
	if payload, ok := auth.PayloadFromContext(r.Context()); ok && payload.Sub != "" {
		sub = payload.Sub
	}

	if !h.manager.IsSpawnAllowed(sub) {
		http.Error(w, fmt.Sprintf(`{"error":"Too many spawn requests. Max %d per %ds."}`,
			h.manager.rateLimitMax,
			int(h.manager.rateLimitWindow.Seconds()),
		), http.StatusTooManyRequests)
		return
	}

	var body map[string]any
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, `{"error":"invalid JSON"}`, http.StatusBadRequest)
		return
	}

	pid, err := h.manager.Spawn(sub, body)
	if err != nil {
		code := http.StatusBadRequest
		if errors.Is(err, services.ErrCwdBlacklisted) || errors.Is(err, services.ErrCwdNotAllowed) {
			slog.Warn("spawn rejected", "cwd", body["cwd"], "user", sub, "reason", err.Error())
			code = http.StatusForbidden
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(code)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "pid": pid})
}

// Status handles GET /api/agents/spawn/{pid}/status.
func (h *SpawnHandler) Status(w http.ResponseWriter, r *http.Request) {
	pidStr := r.PathValue("pid")
	pid, err := strconv.Atoi(pidStr)
	if err != nil || pid <= 0 {
		http.Error(w, `{"error":"invalid pid"}`, http.StatusBadRequest)
		return
	}
	status := h.manager.GetStatus(pid)
	if status == nil {
		http.Error(w, `{"error":"unknown spawn PID"}`, http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(status)
}

// Message handles POST /api/agents/{pid}/message.
func (h *SpawnHandler) Message(w http.ResponseWriter, r *http.Request) {
	pidStr := r.PathValue("pid")
	pid, err := strconv.Atoi(pidStr)
	if err != nil || pid <= 0 {
		http.Error(w, `{"error":"invalid pid"}`, http.StatusBadRequest)
		return
	}

	sub := "__global__"
	if payload, ok := auth.PayloadFromContext(r.Context()); ok && payload.Sub != "" {
		sub = payload.Sub
	}

	var body struct {
		Message string `json:"message"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Message == "" {
		http.Error(w, `{"error":"missing message"}`, http.StatusBadRequest)
		return
	}

	target := fmt.Sprintf("pid:%d", pid)
	sanitized := sanitizeInjectMessage(body.Message)
	msgHash := sha256hex(sanitized)

	if !h.manager.InjectAllowAndRecord(sub) {
		h.recordAudit(r.Context(), sub, repo.AuditActionLiveInjectRejected, target, map[string]any{
			"outcome": "rejected",
		})
		http.Error(w, fmt.Sprintf(`{"error":"Too many message requests. Max %d per %ds."}`,
			h.manager.injectLimiter.max,
			int(h.manager.injectLimiter.window.Seconds()),
		), http.StatusTooManyRequests)
		return
	}

	transport, delivErr := h.manager.SendMessageToChannel(r.Context(), pid, sanitized)

	auditMeta := func(outcome string) map[string]any {
		return map[string]any{
			"transport": transport,
			"msgLen":    len(sanitized),
			"sha256":    msgHash,
			"outcome":   outcome,
		}
	}

	if delivErr != nil {
		h.recordAudit(r.Context(), sub, repo.AuditActionLiveInject, target, auditMeta("error"))
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadGateway)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": delivErr.Error()})
		return
	}

	h.recordAudit(r.Context(), sub, repo.AuditActionLiveInject, target, auditMeta("delivered"))

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]bool{"ok": true})
}

// recordAudit writes a best-effort audit row. Logs a warning on failure.
func (h *SpawnHandler) recordAudit(ctx context.Context, sub, action, target string, meta map[string]any) {
	if h.auditRepo == nil {
		return
	}
	var userID *string
	if sub != "__global__" {
		userID = &sub
	}
	if err := h.auditRepo.RecordAudit(ctx, userID, action, target, meta); err != nil {
		slog.Warn("inject: audit write failed", "action", action, "err", err)
	}
}

// sha256hex returns the first 12 hex chars of sha256(s).
func sha256hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])[:12]
}

// resolveSpawnEnv builds the child process env per ADR-0003:
//  1. start with os.Environ()
//  2. overlay spawner.Env
//  3. dashboard-controlled vars (DASHBOARD_*, CLAUDE_*) always win
//  4. strip the canonical secret deny-set (envsec.DeniedSecretEnvKeys).
//     Interactive spawns get no per-task DASHBOARD_MCP_TOKEN, so no
//     exclusion nuance applies here — unlike the pipeline spawner.
func resolveSpawnEnv(s *ent.Spawner) []string {
	merged := map[string]string{}
	dashboard := map[string]string{}
	for _, kv := range os.Environ() {
		i := strings.IndexByte(kv, '=')
		if i <= 0 {
			continue
		}
		k, v := kv[:i], kv[i+1:]
		if _, inherited := envsec.InheritedSessionEnvKeys[k]; inherited {
			continue
		}
		merged[k] = v
		if strings.HasPrefix(k, "DASHBOARD_") || strings.HasPrefix(k, "KONTOR_") || strings.HasPrefix(k, "CLAUDE_") {
			dashboard[k] = v
		}
	}
	if s != nil {
		for k, v := range s.Env {
			merged[k] = v
		}
	}
	// Dashboard-controlled vars always win over spawner.Env overrides.
	for k, v := range dashboard {
		merged[k] = v
	}
	for denied := range envsec.DeniedSecretEnvKeys {
		delete(merged, denied)
	}
	out := make([]string, 0, len(merged))
	for k, v := range merged {
		out = append(out, k+"="+v)
	}
	return out
}

// allowedPermissionModes is the exhaustive set of values the caller may pass
// as body["permissionMode"]. Any other non-empty value is rejected.
var allowedPermissionModes = map[string]struct{}{
	"default":           {},
	"acceptEdits":       {},
	"plan":              {},
	"auto":              {},
	"bypassPermissions": {},
	"dontAsk":           {},
}

// reservedSpawnerFlags are CLI flags the dashboard sets itself; spawner-row
// args must not re-declare them. The list mirrors the canonical args built
// in buildSpawnArgs(): --resume, -p, --model, --system-prompt, --mcp-config.
// --permission-mode is intentionally NOT listed here: spawners may legitimately
// set their own permission posture (handled by spawnerArgsControlPermissionMode).
var reservedSpawnerFlags = map[string]struct{}{
	"--resume":        {},
	"-p":              {},
	"--model":         {},
	"--system-prompt": {},
	"--mcp-config":    {},
}

// firstReservedFlag returns the first arg that names a reserved flag, or "".
// Matches both "--flag" and "--flag=value" forms.
func firstReservedFlag(args []string) string {
	for _, a := range args {
		head := a
		if i := strings.IndexByte(a, '='); i >= 0 {
			head = a[:i]
		}
		if _, bad := reservedSpawnerFlags[head]; bad {
			return head
		}
	}
	return ""
}

// spawnerArgsControlPermissionMode reports whether the provided spawner args
// already declare a permission posture — either an explicit --permission-mode
// (or --permission-mode=value) or one of the two dangerously-skip spellings.
// When true, Spawn must not append its own --permission-mode to avoid a
// duplicate / conflicting flag that would cause claude to error.
func spawnerArgsControlPermissionMode(args []string) bool {
	for _, a := range args {
		if a == "--permission-mode" || strings.HasPrefix(a, "--permission-mode=") {
			return true
		}
		if a == "--dangerously-skip-permissions" || a == "--allow-dangerously-skip-permissions" {
			return true
		}
	}
	return false
}
