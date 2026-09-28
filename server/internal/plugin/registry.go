package plugin

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/lx-wnk/kontor/server/internal/envsec"
)

// Registry discovers, starts, and health-checks plugins from a directory.
type Registry struct {
	mu                    sync.RWMutex
	dir                   string
	plugins               []Entry
	attemptedCapabilities map[string]bool // capabilities seen in any plugin.json, regardless of health
	// generationByID tracks a monotonically increasing counter per plugin ID.
	// It survives removeByID so a watcher from a previous lifecycle can detect
	// that a fresh entry (at a higher generation) now owns the ID.
	generationByID map[string]int
	enabled        func(id string) bool
	serverCtx      context.Context
	hooks          Hooks
	// settings is the optional provider that fetches decrypted per-plugin values
	// for env injection at every spawn. Nil means no settings are injected.
	settings SettingsProvider
	// credentials is the optional issuer of the token a module calls back with.
	// Nil means modules start without one and can be called but cannot call.
	credentials CredentialIssuer
	// dataRoot is where each module's own directory lives. Empty means modules
	// are started without one, the same way they start without settings.
	dataRoot string
}

// Entry is a loaded plugin with its descriptor and running process (if started by us).
type Entry struct {
	Descriptor Descriptor
	cmd        *exec.Cmd
	// cmdDone is closed by the watchPlugin goroutine when cmd.Wait() returns.
	// It is nil when no watcher runs (no Command field in descriptor).
	// Shutdown waits on this channel instead of calling cmd.Wait() itself,
	// preventing two goroutines from calling Wait() on the same *exec.Cmd
	// (which is undefined behavior in Go). On each restart a fresh channel is
	// installed so Shutdown waits on the live process, not the original one.
	cmdDone      chan struct{}
	BaseURL      string // http://{addr}
	restartCount int
	pluginDir    string // directory containing plugin.json, needed for restarts
	// generation is incremented by startEntry on every (re)start of this id.
	// Watchers capture their generation at spawn; a mismatch means a newer
	// lifecycle now owns the id, and the stale watcher must not touch the entry.
	generation int

	// healthy is true once the process passed its health check and false once a
	// give-up path (exhausted restarts / failed restart) marks it dead. The
	// dispatcher serves 503 for an unhealthy entry.
	healthy bool
	// intentionalStop is set by StopOne before signalling so the watcher knows
	// the exit was deliberate and must NOT respawn (the real orphan-restart fix).
	intentionalStop bool
}

// New creates a Registry that will discover plugins in dir.
// If dir is empty, the registry does nothing (no plugins).
func New(dir string) *Registry {
	return &Registry{
		dir:                   dir,
		attemptedCapabilities: make(map[string]bool),
		generationByID:        make(map[string]int),
	}
}

// SetEnabled installs the predicate that decides which plugins Load starts and
// records. Defaults to all-enabled if never set (callers should set it).
func (r *Registry) SetEnabled(fn func(id string) bool) { r.enabled = fn }

// SetSettingsProvider installs the provider that fetches decrypted settings
// for env injection at every spawn. Call before Load.
func (r *Registry) SetSettingsProvider(fn SettingsProvider) { r.settings = fn }

// SetCredentialIssuer wires the issuer that mints a module's callback token.
// Optional, like the settings provider: a registry without one still starts
// modules, they simply receive no credential.
func (r *Registry) SetCredentialIssuer(issuer CredentialIssuer) { r.credentials = issuer }

// SetDataRoot names the directory under which each module gets one of its own.
func (r *Registry) SetDataRoot(root string) { r.dataRoot = root }

// appendSettingsEnv returns base with PLUGIN_SETTING_<KEY> vars from the settings
// provider appended. A nil provider or a provider error leaves base unchanged
// (the plugin starts without settings rather than not at all).
// appendModuleDataDirEnv creates the module's own directory and names it. A
// failure is logged and the module starts without one: it can still serve what
// core asks of it, which is a smaller problem than refusing to start.
func (r *Registry) appendModuleDataDirEnv(base []string, id string) []string {
	if r.dataRoot == "" {
		return base
	}
	dir, err := EnsureModuleDataDir(r.dataRoot, id)
	if err != nil {
		slog.Warn("plugin: no data directory", "id", id, "err", err)
		return base
	}
	return append(base, ModuleDataDirEnvVar+"="+dir)
}

// appendModuleTokenEnv mints the module's callback credential and appends it.
// A failure is logged and the module starts without one: it can still serve
// what core asks of it, and a module that cannot call back is a smaller
// problem than a module that will not start.
func (r *Registry) appendModuleTokenEnv(ctx context.Context, base []string, desc Descriptor) []string {
	if r.credentials == nil {
		return base
	}
	token, err := r.credentials.Issue(ctx, desc.ID, desc.Uses)
	if err != nil {
		slog.Warn("plugin: no callback credential issued", "id", desc.ID, "err", err)
		return base
	}
	return append(base, ModuleTokenEnvVar+"="+token)
}

func (r *Registry) appendSettingsEnv(ctx context.Context, base []string, id string) []string {
	if r.settings == nil {
		return base
	}
	vals, err := r.settings(ctx, id)
	if err != nil {
		slog.Warn("plugin: settings fetch failed — starting without settings", "id", id, "err", err)
		return base
	}
	// Two distinct setting keys can sanitize to the same env name (e.g. "api-key"
	// and "api.key" both → API_KEY); map iteration makes the winner arbitrary.
	// Warn so the collision is observable; the winning value stays arbitrary.
	seen := make(map[string]string, len(vals))
	for k, v := range vals {
		envName := "PLUGIN_SETTING_" + sanitizeSettingKey(k)
		if prev, dup := seen[envName]; dup {
			slog.Warn("plugin: setting key collision after sanitization — one value silently wins",
				"id", id, "key", k, "collidesWith", prev, "envName", envName)
		}
		seen[envName] = k
		base = append(base, envName+"="+v)
	}
	return base
}

func (r *Registry) isEnabled(id string) bool {
	if r.enabled == nil {
		return true
	}
	return r.enabled(id)
}

// Load scans dir, starts each plugin process, and performs health checks.
// Call once during server startup. serverCtx is the server's lifetime context
// and is cancelled on SIGTERM/SIGINT. Health checks run with an internal
// 30-second timeout derived from serverCtx.
func (r *Registry) Load(serverCtx context.Context, hooks Hooks) error {
	startupCtx, cancel := context.WithTimeout(serverCtx, 30*time.Second)
	defer cancel()
	r.serverCtx = serverCtx
	r.hooks = hooks
	if r.dir == "" {
		return nil
	}
	entries, err := os.ReadDir(r.dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil // no plugin dir is fine
		}
		return fmt.Errorf("plugin: read dir %s: %w", r.dir, err)
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		descPath := filepath.Join(r.dir, entry.Name(), "plugin.json")
		data, err := os.ReadFile(descPath)
		if err != nil {
			slog.Warn("plugin: skip — no plugin.json", "dir", entry.Name())
			continue
		}
		var desc Descriptor
		if err := json.Unmarshal(data, &desc); err != nil {
			slog.Warn("plugin: skip — invalid plugin.json", "dir", entry.Name(), "err", err)
			continue
		}
		if err := desc.Validate(); err != nil {
			// Refused, not skipped quietly: the manifest is the module's
			// statement about what it needs, and a core that ignores an
			// unreadable one loads a module it cannot honour.
			slog.Warn("plugin: refused", "dir", entry.Name(), "err", err)
			continue
		}
		if !r.isEnabled(desc.ID) {
			slog.Info("plugin: skip — disabled", "id", desc.ID)
			continue
		}
		// Record every capability seen in plugin.json regardless of whether the
		// plugin passes health-check. Used by HasAttemptedCapability so callers
		// can distinguish "no plugin configured" from "plugin configured but broken".
		for _, cap := range desc.Capabilities {
			r.attemptedCapabilities[cap] = true
		}
		pluginDir := filepath.Join(r.dir, entry.Name())
		if err := r.startEntry(serverCtx, startupCtx, pluginDir, desc, hooks); err != nil {
			slog.Error("plugin: skip — startup failed", "id", desc.ID, "err", err)
			continue
		}
		slog.Info("plugin: loaded", "id", desc.ID, "capabilities", desc.Capabilities)
	}
	return nil
}

// startEntry starts (if it has a Command), health-checks, registers, and wires
// hooks for one descriptor. The caller holds no lock.
func (r *Registry) startEntry(serverCtx, startupCtx context.Context, pluginDir string, desc Descriptor, hooks Hooks) error {
	host, _, err := net.SplitHostPort(desc.Addr)
	if err != nil {
		return fmt.Errorf("invalid addr %q: %w", desc.Addr, err)
	}
	if ip := net.ParseIP(host); ip == nil || !ip.IsLoopback() {
		return fmt.Errorf("addr %q must be loopback", desc.Addr)
	}
	entry := Entry{Descriptor: desc, BaseURL: "http://" + desc.Addr, pluginDir: pluginDir}
	if len(desc.Command) > 0 {
		// #nosec G204 -- desc.Command is read from a plugin.json in the local plugin directory at Load; no HTTP payload can define it, and argv is passed without a shell.
		cmd := exec.CommandContext(serverCtx, desc.Command[0], desc.Command[1:]...)
		disableContextKill(cmd)
		cmd.Dir = pluginDir
		cmd.Env = r.appendModuleDataDirEnv(r.appendModuleTokenEnv(serverCtx, r.appendSettingsEnv(serverCtx, buildPluginEnv(desc.Env), desc.ID), desc), desc.ID)
		cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		if err := cmd.Start(); err != nil {
			return fmt.Errorf("start: %w", err)
		}
		entry.cmd = cmd
	}
	if err := r.waitHealthy(startupCtx, entry.BaseURL); err != nil {
		if entry.cmd != nil {
			gracefulStop(entry.cmd, nil)
		}
		return fmt.Errorf("health: %w", err)
	}
	// Assign generation before spawning the watcher so the watcher can capture
	// it atomically. The generation survives removeByID and advances on every
	// (re)start, letting stale watchers detect they are no longer the owner.
	r.mu.Lock()
	r.generationByID[desc.ID]++
	gen := r.generationByID[desc.ID]
	r.mu.Unlock()
	entry.generation = gen

	if entry.cmd != nil {
		// Health check passed — safe to launch the watcher now. Starting it
		// before the health check would race with gracefulStop (both call
		// cmd.Wait). Exponential backoff: 1s → 5s → 30s, max 3 retries.
		done := make(chan struct{})
		entry.cmdDone = done
		armKillBackstop(serverCtx, entry.cmd, done)
		go r.watchPlugin(serverCtx, entry.pluginDir, desc, entry.cmd, done, gen)
	}
	entry.healthy = true
	r.mu.Lock()
	r.plugins = append(r.plugins, entry)
	r.mu.Unlock()
	if desc.HasCapability(CapAuthProvider) && hooks.SetAuth != nil {
		hooks.SetAuth(NewAuthProvider(entry), entry.BaseURL+"/login")
	}
	return nil
}

// StartOne starts a single plugin by id (reads its plugin.json fresh from the
// dir). Used by the live-enable path. No-op if already running.
func (r *Registry) StartOne(ctx context.Context, id string) error {
	if !ValidID(id) {
		return fmt.Errorf("plugin: invalid id %q", id)
	}
	r.mu.RLock()
	var (
		found     bool
		isHealthy bool
	)
	for i := range r.plugins {
		if r.plugins[i].Descriptor.ID == id {
			found = true
			isHealthy = r.plugins[i].healthy
			break
		}
	}
	serverCtx, hooks := r.serverCtx, r.hooks
	r.mu.RUnlock()

	if found {
		if isHealthy {
			return nil // already running and healthy
		}
		// Entry exists but is unhealthy — evict before restarting so the fresh
		// process replaces the dead one rather than being treated as a no-op.
		r.removeByID(id)
	}

	if serverCtx == nil {
		serverCtx = ctx
	}
	descPath := filepath.Join(r.dir, id, "plugin.json")
	data, err := os.ReadFile(descPath)
	if err != nil {
		return fmt.Errorf("plugin: read %s: %w", descPath, err)
	}
	var desc Descriptor
	if err := json.Unmarshal(data, &desc); err != nil {
		return fmt.Errorf("plugin: invalid plugin.json for %q: %w", id, err)
	}
	if desc.ID != id {
		return fmt.Errorf("plugin: id mismatch in %s", descPath)
	}
	r.mu.Lock()
	for _, c := range desc.Capabilities {
		r.attemptedCapabilities[c] = true
	}
	r.mu.Unlock()
	startupCtx, cancel := context.WithTimeout(serverCtx, 30*time.Second)
	defer cancel()
	return r.startEntry(serverCtx, startupCtx, filepath.Join(r.dir, id), desc, hooks)
}

// StopOne stops and deregisters a single plugin by id. No-op if not running.
func (r *Registry) StopOne(id string) error {
	r.mu.Lock()
	var target *Entry
	for i := range r.plugins {
		if r.plugins[i].Descriptor.ID == id {
			e := r.plugins[i]
			target = &e
			break
		}
	}
	r.mu.Unlock()
	if target == nil {
		return nil
	}
	r.setIntentionalStop(id)
	if target.cmd != nil {
		gracefulStop(target.cmd, target.cmdDone)
	}
	r.removeByID(id)
	r.revokeModuleCredential(id)
	return nil
}

// revokeModuleCredential retires the token the module was started with. A
// stopped module must not keep a usable credential: the process is gone, so
// anything still presenting its token is not it.
func (r *Registry) revokeModuleCredential(id string) {
	if r.credentials == nil {
		return
	}
	ctx := r.serverCtx
	if ctx == nil {
		ctx = context.Background()
	}
	if err := r.credentials.Revoke(ctx, id); err != nil {
		slog.Warn("plugin: callback credential not revoked", "id", id, "err", err)
	}
}

// setIntentionalStop marks the entry's pending exit as deliberate so watchPlugin
// will not respawn it.
func (r *Registry) setIntentionalStop(id string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for i := range r.plugins {
		if r.plugins[i].Descriptor.ID == id {
			r.plugins[i].intentionalStop = true
			return
		}
	}
}

// isRunning reports whether the plugin with the given id has an entry AND is healthy.
func (r *Registry) isRunning(id string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	for i := range r.plugins {
		if r.plugins[i].Descriptor.ID == id {
			return r.plugins[i].healthy
		}
	}
	return false
}

// isIntentionalStop reports whether the watcher should refrain from restarting.
// An absent entry counts as intentional: by the time the watcher checks, StopOne
// may already have deregistered it, and a removed plugin must never respawn.
func (r *Registry) isIntentionalStop(id string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	for i := range r.plugins {
		if r.plugins[i].Descriptor.ID == id {
			return r.plugins[i].intentionalStop
		}
	}
	return true
}

// isStaleGeneration reports whether the registry no longer holds an entry for id
// at the given generation. An absent entry or a newer generation both return true,
// meaning the watcher that owns myGen must not touch the registry for this id.
func (r *Registry) isStaleGeneration(id string, myGen int) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	for i := range r.plugins {
		if r.plugins[i].Descriptor.ID == id {
			return r.plugins[i].generation != myGen
		}
	}
	return true // absent = stale
}

// gracefulStopTimeout bounds how long a SIGTERMed plugin process group is
// given to exit before SIGKILL is sent. Shared by gracefulStop (per-plugin,
// fire-and-forget) and Shutdown (all plugins, one deadline for the whole batch).
const gracefulStopTimeout = 5 * time.Second

// shutdownKillGrace bounds how long Shutdown waits, after SIGKILLing a
// straggler's process group, for the OS to actually reap it before returning.
// SIGKILL cannot be ignored, so this only covers reap latency — it stays short.
const shutdownKillGrace = 2 * time.Second

// killBackstopDelay bounds how long a plugin may outlive a cancelled server
// context on a path that never reaches Shutdown. It has to clear Shutdown's
// own worst case, which does not even begin until the run loop has drained:
// the HTTP server alone may spend shutdown.timeoutSeconds (a user-configurable
// setting, default 10s) before cleanup() runs, and Shutdown then spends up to
// gracefulStopTimeout + shutdownKillGrace on top. A backstop sized to
// Shutdown's budget alone would fire first and SIGKILL a plugin that had not
// yet been asked to stop — the exact failure this file exists to prevent.
const killBackstopDelay = 60 * time.Second

// disableContextKill stops os/exec from killing the plugin the moment the
// server context is done. exec.CommandContext's default Cancel is
// Process.Kill(), and on the ordinary quit path the context is cancelled well
// before cleanup() calls Shutdown, so the plugin would never see a SIGTERM.
// Shutdown owns the stop sequence; armKillBackstop covers what never reaches it.
func disableContextKill(cmd *exec.Cmd) {
	cmd.Cancel = func() error { return nil }
}

// armKillBackstop SIGKILLs the plugin's process group if the server context is
// cancelled and the process is still alive killBackstopDelay later. os/exec's
// own WaitDelay backstop is deliberately not used for this: it kills the leader
// pid only, orphaning exactly the descendants Setpgid exists to catch, and its
// clock starts at cancellation rather than at the stop sequence.
// Like any in-process backstop this one dies with the dashboard: a caller that
// cancels and exits immediately orphans the plugin regardless.
func armKillBackstop(ctx context.Context, cmd *exec.Cmd, done <-chan struct{}) {
	context.AfterFunc(ctx, func() {
		timer := time.NewTimer(killBackstopDelay)
		defer timer.Stop()
		select {
		case <-done:
		case <-timer.C:
			if cmd.Process != nil {
				signalGroup(cmd.Process.Pid, syscall.SIGKILL)
			}
		}
	})
}

// pendingStop pairs a signalled plugin's process-group pid with the channel
// that closes once its process has exited, so Shutdown can wait on many at once.
type pendingStop struct {
	pid  int
	done <-chan struct{}
}

// Shutdown stops all plugin processes that were started by Load and blocks
// until they have actually exited, so the caller (serverapp calls this as
// cleanup after g.Wait()) can safely exit the process right after it returns.
// Every plugin is signalled first, then all are waited on against one shared
// deadline — gracefulStopTimeout total for the whole batch, not
// gracefulStopTimeout per plugin. Stragglers still alive past the deadline are
// SIGKILLed synchronously and given a short bounded grace period to actually
// exit before Shutdown returns. This differs from gracefulStop, whose hot-path
// callers need signal-and-forget, not a guarantee that the process is dead.
func (r *Registry) Shutdown() {
	r.mu.Lock()
	plugins := make([]Entry, len(r.plugins))
	copy(plugins, r.plugins)
	// Mark every exit as deliberate before signalling: watchPlugin only skips a
	// restart on ctx.Err() != nil, and serverCtx is still live here whenever
	// g.Wait() returned because a run-loop member failed rather than because the
	// server context was cancelled.
	for i := range r.plugins {
		r.plugins[i].intentionalStop = true
	}
	r.mu.Unlock()

	var pending []pendingStop
	for _, p := range plugins {
		if p.cmd == nil || p.cmd.Process == nil {
			continue
		}
		pending = append(pending, pendingStop{
			pid:  p.cmd.Process.Pid,
			done: beginGracefulStop(p.cmd, p.cmdDone),
		})
	}
	if len(pending) == 0 {
		return
	}

	stragglers := waitPendingStops(pending, time.Now().Add(gracefulStopTimeout))
	if len(stragglers) == 0 {
		return
	}
	for _, s := range stragglers {
		signalGroup(s.pid, syscall.SIGKILL)
	}
	waitPendingStops(stragglers, time.Now().Add(shutdownKillGrace))
}

// waitPendingStops waits, for each pending stop, until its done channel closes
// or deadline passes — whichever is first — then returns the ones that missed
// the deadline. deadline is one shared point in time, not a per-item duration,
// so len(items) stragglers cost one wait to the deadline, not one each.
func waitPendingStops(items []pendingStop, deadline time.Time) []pendingStop {
	var mu sync.Mutex
	var missed []pendingStop
	var wg sync.WaitGroup
	for _, it := range items {
		wg.Add(1)
		go func(it pendingStop) {
			defer wg.Done()
			select {
			case <-it.done:
			case <-time.After(time.Until(deadline)):
				mu.Lock()
				missed = append(missed, it)
				mu.Unlock()
			}
		}(it)
	}
	wg.Wait()
	return missed
}

// beginGracefulStop sends SIGTERM to the process group led by cmd's PID —
// Setpgid on spawn makes the plugin the group leader (pgid == pid), so the
// negative-pid kill reaches the plugin and all its descendants — and returns
// the channel that closes once the process has exited. Caller must ensure
// cmd.Process is non-nil.
// If watcherDone is non-nil it is the channel closed by the watchPlugin
// goroutine that owns cmd.Wait(); it is returned as-is rather than calling
// cmd.Wait() again, because calling Wait() from two goroutines on the same
// *exec.Cmd is undefined behavior.
func beginGracefulStop(cmd *exec.Cmd, watcherDone <-chan struct{}) <-chan struct{} {
	signalGroup(cmd.Process.Pid, syscall.SIGTERM)
	if watcherDone != nil {
		return watcherDone
	}
	done := make(chan struct{})
	go func() {
		_ = cmd.Wait()
		close(done)
	}()
	return done
}

// gracefulStop signals cmd's process group to stop and returns immediately —
// it does not wait for the process to exit. This is deliberate: its call
// sites (plugin disable, and rollback inside the reload/health-check flow)
// are hot paths, one of which is reachable from an HTTP handler, and waiting
// up to gracefulStopTimeout there would be a much larger latency change than
// this function is meant to make. Escalation to SIGKILL after
// gracefulStopTimeout runs in a goroutine detached from the caller, so it is
// best-effort: it does not survive the dashboard process exiting first.
// Callers that must know the process is actually dead before proceeding
// (server shutdown) need Shutdown instead, which drives the same signal step
// but waits on it. See beginGracefulStop for the watcherDone contract.
func gracefulStop(cmd *exec.Cmd, watcherDone <-chan struct{}) {
	if cmd.Process == nil {
		return
	}
	done := beginGracefulStop(cmd, watcherDone)
	pid := cmd.Process.Pid
	go func() {
		select {
		case <-done:
			// process exited — nothing to do
		case <-time.After(gracefulStopTimeout):
			signalGroup(pid, syscall.SIGKILL)
		}
	}()
}

// signalGroup sends sig to the process group led by pid (Setpgid makes the child
// its own group leader, so its pgid == pid). The negative target reaches the
// leader and all descendants, so a plugin's child processes die with it.
// Falls back to the single process if the group send fails.
func signalGroup(pid int, sig syscall.Signal) {
	if err := syscall.Kill(-pid, sig); err != nil {
		_ = syscall.Kill(pid, sig)
	}
}

// FindByCapability returns the first plugin with the given capability, or nil.
func (r *Registry) FindByCapability(capability string) *Entry {
	r.mu.RLock()
	defer r.mu.RUnlock()
	for i := range r.plugins {
		if r.plugins[i].Descriptor.HasCapability(capability) {
			return &r.plugins[i]
		}
	}
	return nil
}

// HasAttemptedCapability reports whether any plugin.json in the directory
// declared the given capability, regardless of whether that plugin passed
// the health-check and ended up in the registry.
func (r *Registry) HasAttemptedCapability(capability string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.attemptedCapabilities[capability]
}

// HasDir reports whether the registry was constructed with a non-empty plugin directory.
func (r *Registry) HasDir() bool {
	return r.dir != ""
}

// Dir is the directory the module was loaded from. Its own files — provider
// descriptors, routine definitions — are resolved relative to it.
func (e Entry) Dir() string { return e.pluginDir }

// Healthy reports whether this entry's process is currently considered healthy.
func (e Entry) Healthy() bool { return e.healthy }

// Lookup returns a copy of the entry for id and whether it is present. The copy
// avoids handing out a pointer into the value-backed plugins slice (which
// reallocates on append/remove). Plugin count is single-digit, so the linear
// scan under RLock matches the existing All/FindByCapability patterns.
func (r *Registry) Lookup(id string) (Entry, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	for i := range r.plugins {
		if r.plugins[i].Descriptor.ID == id {
			return r.plugins[i], true
		}
	}
	return Entry{}, false
}

// InjectEntryForTest registers a synthetic entry without starting a process.
// Test-only seam; production code paths go through startEntry.
func (r *Registry) InjectEntryForTest(d Descriptor, healthy bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.plugins = append(r.plugins, Entry{Descriptor: d, BaseURL: "http://" + d.Addr, healthy: healthy})
}

// InjectEntryWithDirForTest is InjectEntryForTest for a module whose own
// directory matters — the files it ships are resolved relative to it.
func (r *Registry) InjectEntryWithDirForTest(d Descriptor, dir string, healthy bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.plugins = append(r.plugins, Entry{Descriptor: d, BaseURL: "http://" + d.Addr, pluginDir: dir, healthy: healthy})
}

// NewHealthyEntryForTest builds a healthy Entry for tests in other packages.
func NewHealthyEntryForTest(d Descriptor) Entry {
	return Entry{Descriptor: d, BaseURL: "http://" + d.Addr, healthy: true}
}

// NewEntryForTest builds an Entry with configurable health for tests in other packages.
func NewEntryForTest(d Descriptor, healthy bool) Entry {
	return Entry{Descriptor: d, BaseURL: "http://" + d.Addr, healthy: healthy}
}

// All returns a snapshot of all loaded plugin entries.
func (r *Registry) All() []Entry {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]Entry, len(r.plugins))
	copy(out, r.plugins)
	return out
}

// Info is a safe, read-only snapshot of a loaded plugin. It intentionally
// excludes internal state (cmd, restartCount) so consumers cannot mutate
// live plugin processes.
type Info struct {
	ID           string
	Capabilities []string
	BaseURL      string
}

// Infos returns a snapshot of all loaded plugins as Info values.
// Use this instead of All() when only metadata is needed.
func (r *Registry) Infos() []Info {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]Info, 0, len(r.plugins))
	for _, e := range r.plugins {
		out = append(out, Info{
			ID:           e.Descriptor.ID,
			Capabilities: e.Descriptor.Capabilities,
			BaseURL:      e.BaseURL,
		})
	}
	return out
}

// watchPlugin waits for cmd to exit, then attempts to restart it with
// exponential backoff. It gives up after maxPluginRestarts attempts and
// removes the entry from the registry.
const maxPluginRestarts = 3

var restartBackoff = []time.Duration{1 * time.Second, 5 * time.Second, 30 * time.Second}

func (r *Registry) watchPlugin(ctx context.Context, pluginDir string, desc Descriptor, cmd *exec.Cmd, done chan<- struct{}, myGen int) {
	restartCount := 0
	current := cmd
	// currentDone is the channel we close when the current process exits.
	// Starts as the initial done from startEntry; updated to a fresh channel on
	// each restart so Shutdown always waits on the live process (fix P3).
	currentDone := done

	for {
		err := current.Wait()
		// Close the done channel for this process so Shutdown/StopOne can unblock.
		// On the first iteration this closes the original done from startEntry;
		// on subsequent iterations it closes the per-restart channel stored on the entry.
		close(currentDone)

		if r.isIntentionalStop(desc.ID) {
			return
		}
		// A nil error means the plugin exited cleanly (e.g. during Shutdown).
		// Only attempt restarts on non-nil errors.
		if err == nil {
			return
		}
		// If the server is shutting down, the SIGTERM we sent caused this exit —
		// not an unexpected crash. Return silently to avoid a spurious error log.
		if ctx.Err() != nil {
			return
		}
		slog.Error("plugin: process exited unexpectedly", "id", desc.ID, "err", err)

		if restartCount >= maxPluginRestarts {
			slog.Error("plugin: exceeded restart limit — marking unhealthy", "id", desc.ID, "restarts", restartCount)
			r.markUnhealthy(desc.ID)
			return
		}

		delay := restartBackoff[min(restartCount, len(restartBackoff)-1)]
		slog.Info("plugin: restarting after backoff", "id", desc.ID, "attempt", restartCount+1, "delay", delay)

		select {
		case <-ctx.Done():
			return
		case <-time.After(delay):
		}

		// Re-check after backoff: a concurrent deactivate+reactivate (fix P2) or a
		// plain StopOne may have raced while we slept.
		if r.isStaleGeneration(desc.ID, myGen) {
			return
		}
		if r.isIntentionalStop(desc.ID) {
			return
		}

		// #nosec G204 -- same descriptor as the initial start: desc.Command comes from the on-disk plugin.json, not from any request.
		newCmd := exec.CommandContext(ctx, desc.Command[0], desc.Command[1:]...)
		disableContextKill(newCmd)
		newCmd.Dir = pluginDir
		newCmd.Env = r.appendModuleDataDirEnv(r.appendModuleTokenEnv(ctx, r.appendSettingsEnv(ctx, buildPluginEnv(desc.Env), desc.ID), desc), desc.ID)
		newCmd.Stdout = os.Stdout
		newCmd.Stderr = os.Stderr
		newCmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

		if startErr := newCmd.Start(); startErr != nil {
			slog.Error("plugin: restart failed — could not start process", "id", desc.ID, "err", startErr)
			r.markUnhealthy(desc.ID)
			return
		}

		baseURL := "http://" + desc.Addr
		if healthErr := r.waitHealthy(ctx, baseURL); healthErr != nil {
			slog.Error("plugin: restart failed — health check did not pass", "id", desc.ID, "err", healthErr)
			// newCmd has no watcher goroutine; pass nil so gracefulStop owns Wait().
			gracefulStop(newCmd, nil)
			r.markUnhealthy(desc.ID)
			return
		}

		restartCount++
		slog.Info("plugin: restarted successfully", "id", desc.ID, "restartCount", restartCount)

		// Fresh done channel for the restarted process (fix P3): Shutdown must
		// wait on the live channel, not the already-closed original one.
		rawNewDone := make(chan struct{})

		r.mu.Lock()
		recordedNewCmd := false
		for i := range r.plugins {
			if r.plugins[i].Descriptor.ID == desc.ID {
				// Bail if a fresh lifecycle has taken over (fix P2) or if StopOne
				// was called during the backoff window.
				if r.plugins[i].generation != myGen || r.plugins[i].intentionalStop {
					r.mu.Unlock()
					gracefulStop(newCmd, nil)
					return
				}
				r.plugins[i].cmd = newCmd
				r.plugins[i].restartCount = restartCount
				r.plugins[i].healthy = true
				r.plugins[i].cmdDone = rawNewDone
				recordedNewCmd = true
				break
			}
		}
		r.mu.Unlock()

		if !recordedNewCmd {
			// Entry was evicted by StopOne during backoff — clean up to avoid a leak.
			gracefulStop(newCmd, nil)
			return
		}

		armKillBackstop(ctx, newCmd, rawNewDone)
		current = newCmd
		currentDone = rawNewDone
	}
}

// markUnhealthy flips the entry to unhealthy and notifies the OnUnhealthy hook.
// The entry is kept so the dispatcher serves a knowing 503 rather than the
// ambiguous 503 of a missing plugin.
func (r *Registry) markUnhealthy(id string) {
	r.mu.Lock()
	for i := range r.plugins {
		if r.plugins[i].Descriptor.ID == id {
			r.plugins[i].healthy = false
			break
		}
	}
	hook := r.hooks.OnUnhealthy
	r.mu.Unlock()
	if hook != nil {
		hook(id)
	}
}

// WithTransient ensures the plugin is running for the duration of fn. If it was
// not already up, WithTransient starts it, runs fn, then stops it; if it was
// already running, fn runs and the process is left untouched. Used to deliver
// lifecycle hooks to an installed-but-stopped plugin.
func (r *Registry) WithTransient(ctx context.Context, id string, fn func() error) error {
	if r.isRunning(id) {
		return fn()
	}
	if err := r.StartOne(ctx, id); err != nil {
		return fmt.Errorf("transient start %s: %w", id, err)
	}
	defer func() { _ = r.StopOne(id) }()
	return fn()
}

// removeByID removes a plugin entry from the registry by plugin ID.
func (r *Registry) removeByID(id string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for i, p := range r.plugins {
		if p.Descriptor.ID == id {
			r.plugins = append(r.plugins[:i], r.plugins[i+1:]...)
			return
		}
	}
}

// dashboardSecretEnv names env vars that carry dashboard secrets.
// These are never forwarded to plugins even if listed in desc.Env.
// Built from the canonical envsec.DeniedSecretEnvKeys base plus
// DASHBOARD_MCP_TOKEN: plugins have no legitimate use for it, unlike
// spawned Claude agents, which need it to reach the channel bridge.
var dashboardSecretEnv = buildDashboardSecretEnv()

func buildDashboardSecretEnv() map[string]bool {
	m := make(map[string]bool, len(envsec.DeniedSecretEnvKeys)+1)
	for k := range envsec.DeniedSecretEnvKeys {
		m[k] = true
	}
	m["DASHBOARD_MCP_TOKEN"] = true
	m["KONTOR_MCP_TOKEN"] = true
	return m
}

// sanitizeSettingKey uppercases key and replaces every character that is not
// A-Z, 0-9, or _ with '_', producing a valid env var suffix.
func sanitizeSettingKey(key string) string {
	upper := strings.ToUpper(key)
	var b strings.Builder
	b.Grow(len(upper))
	for _, c := range upper {
		if (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '_' {
			b.WriteRune(c)
		} else {
			b.WriteRune('_')
		}
	}
	return b.String()
}

// buildPluginEnv constructs a minimal environment for a plugin process.
// It exposes only a safe base set of env vars plus any keys explicitly
// allow-listed in the plugin's descriptor (desc.Env).
func buildPluginEnv(allowedKeys []string) []string {
	base := []string{"PATH", "HOME", "TMPDIR", "TEMP", "USER", "LANG", "LC_ALL"}
	allowed := make(map[string]bool, len(base)+len(allowedKeys))
	for _, k := range base {
		allowed[k] = true
	}
	for _, k := range allowedKeys {
		if !dashboardSecretEnv[k] { // blocklist wins over allow-list
			allowed[k] = true
		}
	}
	var env []string
	for _, kv := range os.Environ() {
		if idx := strings.Index(kv, "="); idx > 0 {
			if allowed[kv[:idx]] {
				env = append(env, kv)
			}
		}
	}
	return env
}

func (r *Registry) waitHealthy(ctx context.Context, baseURL string) error {
	healthClient := &http.Client{Timeout: 1 * time.Second}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/health", nil)
		if err != nil {
			return err
		}
		resp, err := healthClient.Do(req)
		if err == nil && resp.StatusCode == http.StatusOK {
			_, _ = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()
			return nil
		}
		if resp != nil {
			_, _ = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(200 * time.Millisecond):
		}
	}
	return fmt.Errorf("plugin at %s did not become healthy within 5s", baseURL)
}
