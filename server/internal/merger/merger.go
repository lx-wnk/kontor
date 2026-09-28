// Package merger combines process scan results with JSONL session data into Agent values.
package merger

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/lx-wnk/agent-dashboard/sdk"
	"github.com/lx-wnk/agent-dashboard/server/internal/channelconfig"
	"github.com/lx-wnk/agent-dashboard/server/internal/parser"
	"github.com/lx-wnk/agent-dashboard/server/internal/provider"
	"github.com/lx-wnk/agent-dashboard/server/internal/scanner"
)

const (
	activeThreshold  = 30 * time.Second
	waitingThreshold = 5 * time.Minute
	outputThreshold  = 5 * time.Second
)

// tmuxActivityFn returns a tmux pane's last-activity time. Seam for tests.
var tmuxActivityFn = realTmuxActivity

func realTmuxActivity(pane string) (time.Time, bool) {
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	out, err := exec.CommandContext(ctx, "tmux", "display-message", "-p", "-t", pane, "#{window_activity}").Output()
	if err != nil {
		return time.Time{}, false
	}
	sec, err := strconv.ParseInt(strings.TrimSpace(string(out)), 10, 64)
	if err != nil {
		return time.Time{}, false
	}
	return time.Unix(sec, 0), true
}

// readDiscoveryFileFn is a seam over os.ReadFile so tests can count or fail
// discovery-file reads. Production always uses os.ReadFile.
var readDiscoveryFileFn = os.ReadFile

// discoveryState is the combined result of reading an agent's two channel
// discovery files exactly once each.
type discoveryState struct {
	channelAvailable bool
	liveInjectable   bool
	recentOutput     bool
}

// readChannelDiscovery reads {pid}.json (channel bridge) and {pid}.pty.json
// (pty broker) exactly once each and derives channelAvailable, liveInjectable,
// and recentOutput from those two decodes — the combined replacement for what
// were previously two independent double-reads (channelDiscovery +
// recentChannelOutput each read both files).
//
// Two files are consulted independently (a missing or unreadable file simply
// contributes nothing — no error):
//
//   - {pid}.json     — channel bridge: written by bridge.go. Presence implies
//     channelAvailable. Contains tmuxPane/tmuxSocket for tmux-based injection.
//   - {pid}.pty.json — pty broker: written by ptyhost.go. Presence also implies
//     channelAvailable. Contains ptyInject:true for loopback-HTTP injection and
//     lastOutputAt for output-recency detection.
//
// This two-file model avoids the collision that occurred on the no-tmux path:
// previously ptyhost.go and bridge.go both wrote to {pid}.json, and the bridge
// (booting ~1s after ptyhost) overwrote the ptyInject field, breaking
// liveInjectable detection.
//
// channelAvailable is true when either file exists. liveInjectable is true
// when the bridge file's tmuxPane is non-empty OR the pty file's ptyInject
// field is true. recentOutput is true when the pty file's lastOutputAt is
// within outputThreshold — checked first — OR, only when that check does not
// already satisfy recency, the bridge file's tmuxPane has recent
// window_activity (tmuxActivityFn), preserving the original short-circuit that
// avoids shelling out to tmux when the pty signal already answered it.
func readChannelDiscovery(home string, pid int) discoveryState {
	var s discoveryState
	var tmuxPane string

	if data, err := readDiscoveryFileFn(channelconfig.DiscoveryFile(home, pid)); err == nil {
		s.channelAvailable = true
		var bridge struct {
			TmuxPane string `json:"tmuxPane"`
		}
		if json.Unmarshal(data, &bridge) == nil {
			tmuxPane = bridge.TmuxPane
			if tmuxPane != "" {
				s.liveInjectable = true
			}
		}
	}

	if data, err := readDiscoveryFileFn(channelconfig.DiscoveryPtyFile(home, pid)); err == nil {
		s.channelAvailable = true
		var pty struct {
			PtyInject    bool   `json:"ptyInject"`
			LastOutputAt string `json:"lastOutputAt"`
		}
		if json.Unmarshal(data, &pty) == nil {
			if pty.PtyInject {
				s.liveInjectable = true
			}
			if pty.LastOutputAt != "" {
				if ts, perr := time.Parse(time.RFC3339, pty.LastOutputAt); perr == nil && time.Since(ts) < outputThreshold {
					s.recentOutput = true
				}
			}
		}
	}

	if !s.recentOutput && tmuxPane != "" {
		if ts, ok := tmuxActivityFn(tmuxPane); ok && time.Since(ts) < outputThreshold {
			s.recentOutput = true
		}
	}

	return s
}

// readAgentChannelState resolves the home directory once and reads both
// discovery files once via readChannelDiscovery — the single call buildAgent
// makes per agent per tick.
func readAgentChannelState(pid int) discoveryState {
	home, err := os.UserHomeDir()
	if err != nil {
		return discoveryState{}
	}
	return readChannelDiscovery(home, pid)
}

// recentChannelOutput reports whether a live session emitted output within
// outputThreshold: a pty broker's lastOutputAt, or a tmux pane's window_activity.
func recentChannelOutput(pid int) bool {
	return readAgentChannelState(pid).recentOutput
}

// CalculateStatus returns the agent status based on time since last activity.
func CalculateStatus(lastActivity time.Time) sdk.AgentStatus {
	age := time.Since(lastActivity)
	switch {
	case age < activeThreshold:
		return sdk.AgentStatusActive
	case age < waitingThreshold:
		return sdk.AgentStatusWaiting
	default:
		return sdk.AgentStatusIdle
	}
}

// channelDiscovery reads the dashboard-channel discovery files for the given PID
// and returns both availability and live-injectability. See readChannelDiscovery
// for the two-file model this derives from.
func channelDiscovery(pid int) (channelAvailable, liveInjectable bool) {
	s := readAgentChannelState(pid)
	return s.channelAvailable, s.liveInjectable
}

// strPtr returns nil if s is empty, otherwise a pointer to s.
func strPtr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// errorStatePtr returns nil if s is empty, otherwise a pointer to s.
func errorStatePtr(s sdk.ErrorState) *sdk.ErrorState {
	if s == "" {
		return nil
	}
	return &s
}

// CostBreakdown holds estimated costs plus the "unknown" sentinel.
type CostBreakdown struct {
	Total       float64
	CacheCreate float64
	CacheRead   float64
	Unknown     bool
}

// EstimateCostForProvider returns the cost breakdown for a session, gated by
// provider awareness: non-Claude providers without a known pricing entry are
// flagged as Unknown=true with all costs zeroed. Claude sessions always use
// the pricing table (with default-model fallback) — unchanged from prior behaviour.
func EstimateCostForProvider(provider sdk.Provider, usage sdk.TokenUsage, model string) CostBreakdown {
	if provider != "" && provider != sdk.ProviderClaude && !parser.HasPricing(model) {
		return CostBreakdown{Unknown: true}
	}
	return CostBreakdown{
		Total:       parser.EstimateCost(usage, model),
		CacheCreate: parser.EstimateCacheCreationCost(usage, model),
		CacheRead:   parser.EstimateCacheReadCost(usage, model),
	}
}

// Enricher optionally annotates the freshly-built agent slice with data that
// lives outside the filesystem scan (e.g. pipeline task links from SQLite). It
// mutates agents in place by index and MUST be fail-soft: any lookup error
// leaves the affected agent untouched. A nil Enricher disables enrichment.
//
// Defined here (not in db/repo) so merger stays a leaf package with no db
// dependency; the composition root injects a db-backed implementation, mirroring
// the BaselineProvider injection.
type Enricher func(ctx context.Context, agents []sdk.Agent)

// ChainEnrichers composes enrichers into one that applies each in order. Nil
// elements are skipped; if none remain it returns nil, preserving the
// "nil Enricher disables enrichment" contract so the no-crossing path stays
// byte-identical. A single active enricher is returned directly (no wrapper).
func ChainEnrichers(enrichers ...Enricher) Enricher {
	active := make([]Enricher, 0, len(enrichers))
	for _, e := range enrichers {
		if e != nil {
			active = append(active, e)
		}
	}
	switch len(active) {
	case 0:
		return nil
	case 1:
		return active[0]
	default:
		return func(ctx context.Context, agents []sdk.Agent) {
			for _, e := range active {
				e(ctx, agents)
			}
		}
	}
}

// GetAgentsOpts carries optional settings for a single GetAgents call.
type GetAgentsOpts struct {
	// BaselinePerSessionCostUSD is the average per-session cost over the past 7 days,
	// pre-computed by the caller. Zero means "no baseline available" and disables
	// the cost-spike component of the health score (no penalty).
	BaselinePerSessionCostUSD float64
	// Enricher, when non-nil, is invoked once after the agent slice is built and
	// filtered, before return. See the Enricher type doc.
	Enricher Enricher
}

// Merger builds the agent roster by combining a process scan with session data.
// It owns the stale tracker (cross-tick finished-agent state), so exactly one
// Merger should exist per process; the composition root builds it and shares it
// with every read path (broadcast loop, HTTP accessors, search).
type Merger struct {
	scan        func(ctx context.Context) ([]scanner.ProcessInfo, error)
	tracker     *staleTracker
	registry    *provider.Registry
	screenProbe ScreenProbeFn
}

// ScreenProbeFn resolves whichever AskUserQuestion screen is currently open on
// an injectable session's live terminal — the modal itself or its review/submit
// screen — or nil when none is. Implementations must be fail-soft: any lookup
// error yields nil rather than propagating.
type ScreenProbeFn func(pid int) *sdk.PendingScreen

// Option configures a Merger.
type Option func(*Merger)

// WithScanFn overrides the process scanner (tests inject a synthetic list).
func WithScanFn(fn func(ctx context.Context) ([]scanner.ProcessInfo, error)) Option {
	return func(m *Merger) { m.scan = fn }
}

// WithRegistry injects the provider registry used to resolve and cost
// non-Claude sessions. When nil, only Claude sessions are handled (legacy path).
func WithRegistry(r *provider.Registry) Option {
	return func(m *Merger) { m.registry = r }
}

// WithScreenProbe injects the probe used to attach a live-detected
// AskUserQuestion screen to injectable agents. When nil (the default), no agent
// ever carries PendingQuestion or PendingConfirm.
func WithScreenProbe(fn ScreenProbeFn) Option {
	return func(m *Merger) { m.screenProbe = fn }
}

// New builds a Merger. Defaults: the real scanner.ScanProcesses and a fresh
// stale tracker.
func New(opts ...Option) *Merger {
	m := &Merger{scan: scanner.ScanProcesses, tracker: newStaleTracker()}
	for _, o := range opts {
		o(m)
	}
	return m
}

// GetAgents scans running Claude processes and merges them with session data.
// Processes with no matching active session are silently skipped.
func (m *Merger) GetAgents(ctx context.Context, opts GetAgentsOpts) ([]sdk.Agent, error) {
	processes, err := m.scan(ctx)
	if err != nil {
		return nil, err
	}

	// Pre-allocate the result slice so each goroutine writes to its own index,
	// avoiding a mutex and producing deterministic ordering (same as processes).
	agents := make([]sdk.Agent, len(processes))

	// Resolved session paths, indexed by process index so each goroutine writes
	// its own disjoint slot (same race-safety as the agents slice).
	sessionPaths := make([]string, len(processes))

	// Group process indices by project directory (encoded cwd + config dir).
	// Resolution must be sequential WITHIN a group so the shared `claimed` set
	// keeps every same-folder agent on a distinct session — the core fix for
	// "all sessions in one folder show the same content". Groups are independent
	// (different directories cannot share a session file), so they run in
	// parallel to preserve throughput on the per-session tail-reads.
	type groupKey struct{ encoded, configDir string }
	groups := make(map[groupKey][]int)
	for i, proc := range processes {
		k := groupKey{parser.EncodePath(proc.CWD), proc.ClaudeConfigDir}
		groups[k] = append(groups[k], i)
	}

	// One session-listing cache for the whole tick so non-Claude providers walk
	// each config tree once, not once per process. Shared read-mostly across the
	// parallel groups; SessionScan guards its own state.
	var scan *provider.SessionScan
	if m.registry != nil {
		scan = m.registry.NewSessionScan()
	}

	var wg sync.WaitGroup
	for _, idxs := range groups {
		idxs := idxs
		wg.Add(1)
		go func() {
			defer wg.Done()
			// Resolve youngest process first so a freshly-started agent claims
			// the freshest session in the fallback path; deterministic ordering
			// keeps the UI stable across ticks.
			sort.SliceStable(idxs, func(a, b int) bool {
				return processes[idxs[a]].Uptime < processes[idxs[b]].Uptime
			})
			claimed := make(map[string]bool)
			for _, i := range idxs {
				proc := processes[i]
				session, extra, err := m.resolveSession(proc, claimed, scan)
				if err != nil {
					session = provisionalSession(proc, claimed)
					if session == nil {
						continue // no matching session; zero value left at agents[i]
					}
					extra = resolveExtra{}
				}
				agents[i] = m.buildAgent(proc, session, extra, opts.BaselinePerSessionCostUSD)
				sessionPaths[i] = session.Path
			}
		}()
	}
	wg.Wait()
	// Filter out zero-value entries (processes with no matching session) and
	// record each live controllable agent's snapshot. Only channel-available
	// agents are recorded, so only they can later surface as a finished card.
	livePIDs := make(map[int]bool, len(processes))
	liveSessions := make(map[string]bool)
	result := agents[:0]
	for i, a := range agents {
		if a.SessionID == "" {
			continue
		}
		result = append(result, a)
		livePIDs[a.PID] = true
		liveSessions[a.SessionID] = true
		if a.ChannelAvailable {
			m.tracker.record(a.PID, liveSnapshot{
				sessionID:      a.SessionID,
				path:           sessionPaths[i],
				projectPath:    a.ProjectPath,
				configDir:      a.ClaudeConfigDir,
				configDirKnown: a.ClaudeConfigDirKnown,
				provider:       a.Provider,
			})
		}
	}

	// Append finished (stale) controllable agents. Dedup guards the PID-reuse
	// edge: a session re-launched under a new live PID must not also show a
	// stale card from the old PID's snapshot.
	for _, s := range m.tracker.buildStale(livePIDs, opts.BaselinePerSessionCostUSD) {
		if liveSessions[s.SessionID] {
			continue
		}
		result = append(result, s)
	}

	if opts.Enricher != nil {
		opts.Enricher(ctx, result)
	}
	return result, nil
}

// DismissAgent removes a finished agent from the in-memory tracker so its card
// stops appearing. Dismissal is in-memory (not discovery-file deletion) because
// the channel bridge already deletes that file when the agent exits.
func (m *Merger) DismissAgent(pid int) { m.tracker.dismiss(pid) }

// resolveExtra carries provider-supplied cost signals from non-Claude resolution.
type resolveExtra struct {
	inFileProvider string
	inFileCost     float64
}

// resolveSession dispatches session resolution by provider. Claude (and empty)
// use the pid-session-aware path; other providers resolve via the registry.
func (m *Merger) resolveSession(proc scanner.ProcessInfo, claimed map[string]bool, scan *provider.SessionScan) (*parser.SessionData, resolveExtra, error) {
	if proc.Provider == "" || proc.Provider == sdk.ProviderClaude {
		s, err := parser.ResolveSessionForProcess(parser.SessionRequest{
			CWD:             proc.CWD,
			PID:             proc.PID,
			Command:         proc.Command,
			UptimeSeconds:   proc.Uptime,
			ClaudeConfigDir: proc.ClaudeConfigDir,
		}, claimed)
		return s, resolveExtra{}, err
	}
	if m.registry == nil {
		return nil, resolveExtra{}, fmt.Errorf("no registry to resolve provider %s", proc.Provider)
	}
	s, inFileProvider, inFileCost, err := m.registry.ResolveSessionScan(proc.Provider, proc.CWD, claimed, scan)
	return s, resolveExtra{inFileProvider: inFileProvider, inFileCost: inFileCost}, err
}

// buildAgent assembles an sdk.Agent from a scanned process and its resolved
// session data.
func (m *Merger) buildAgent(proc scanner.ProcessInfo, session *parser.SessionData, extra resolveExtra, baselineCost float64) sdk.Agent {
	prov := proc.Provider
	if prov == "" {
		prov = sdk.ProviderClaude
	}
	var c CostBreakdown
	var costLocal bool
	if prov == sdk.ProviderClaude || m.registry == nil {
		c = EstimateCostForProvider(prov, session.TokenUsage, session.Model)
	} else {
		rc := m.registry.Cost(prov, session.TokenUsage, session.Model, extra.inFileCost, extra.inFileProvider)
		c = CostBreakdown{Total: rc.Total, CacheCreate: rc.CacheCreate, CacheRead: rc.CacheRead, Unknown: rc.Unknown}
		costLocal = rc.Local
	}
	discovery := readAgentChannelState(proc.PID)
	var pendingQuestion *sdk.DetectedQuestion
	var pendingConfirm *sdk.DetectedConfirm
	if discovery.liveInjectable && m.screenProbe != nil {
		if screen := m.screenProbe(proc.PID); screen != nil {
			pendingQuestion, pendingConfirm = screen.Question, screen.Confirm
		}
	}
	health := ComputeHealthScore(session, c.Total, c.Unknown, baselineCost)

	return sdk.Agent{
		PID:                       proc.PID,
		SessionID:                 session.SessionID,
		Provider:                  prov,
		ProjectPath:               proc.CWD,
		ProjectName:               filepath.Base(proc.CWD),
		CWD:                       proc.CWD,
		ClaudeConfigDir:           proc.ClaudeConfigDir,
		ClaudeConfigDirKnown:      proc.ClaudeConfigDirKnown,
		Entrypoint:                session.Entrypoint,
		Status:                    CalculateStatus(session.LastActivity),
		Working:                   session.TurnOpen || discovery.recentOutput,
		ChannelAvailable:          discovery.channelAvailable,
		LiveInjectable:            discovery.liveInjectable,
		InternalProcess:           proc.InternalProcess,
		PermissionsBypassed:       parser.PermissionsBypassedFromArgs(proc.Command),
		PendingQuestion:           pendingQuestion,
		PendingConfirm:            pendingConfirm,
		Uptime:                    proc.Uptime,
		LastActivity:              session.LastActivity.Format(time.RFC3339),
		CurrentAction:             strPtr(session.CurrentAction),
		LastTools:                 append(make([]sdk.RecentTool, 0), session.LastTools...),
		Tasks:                     append(make([]sdk.TaskInfo, 0), session.Tasks...),
		Subagents:                 buildSubagents(session),
		TokenUsage:                session.TokenUsage,
		CostEstimate:              c.Total,
		CacheCreationCostEstimate: c.CacheCreate,
		CacheReadCostEstimate:     c.CacheRead,
		CostUnknown:               c.Unknown,
		CostLocal:                 costLocal,
		HealthScore:               health,
		Model:                     strPtr(session.Model),
		ConversationTurns:         session.ConversationTurns,
		ToolCounts:                session.ToolCounts,
		Meta:                      session.Meta,
		ConvergenceAlert:          session.ConvergenceAlert,
		ConvergenceToolName:       strPtr(session.ConvergenceToolName),
		ErrorState:                errorStatePtr(session.ErrorState),
		LastOutput:                strPtr(session.LastOutput),
		LastBtw:                   session.LastBtw,
		PendingToolUse:            session.PendingToolUse,
	}
}

// buildSubagents reads <sessionDir>/subagents/*.jsonl and returns a populated
// SubAgent slice, sorted active-first then by most recent activity descending.
// os.ReadDir sorts by filename (a content hash), which carries no relation to
// activity, so callers cannot rely on directory order. Returns an empty slice
// when the directory does not exist or session.Path is unset.
func buildSubagents(session *parser.SessionData) []sdk.SubAgent {
	out := []sdk.SubAgent{}
	if session.Path == "" {
		return out
	}
	subDir := filepath.Join(filepath.Dir(session.Path), session.SessionID, "subagents")
	entries, err := os.ReadDir(subDir)
	if err != nil {
		return out
	}
	// Collect live paths first so we can evict stale cache entries in one pass.
	livePaths := make(map[string]bool, len(entries))
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".jsonl") {
			livePaths[filepath.Join(subDir, e.Name())] = true
		}
	}
	parser.PruneSubagentCache(livePaths)

	now := time.Now()
	lastActivity := make(map[string]time.Time, len(entries))
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".jsonl") {
			continue
		}
		p := filepath.Join(subDir, name)
		parsed, err := parser.ParseSubagentFileCached(p)
		if err != nil {
			continue
		}
		status := sdk.SubAgentStatusCompleted
		if !parsed.LastActivity.IsZero() && now.Sub(parsed.LastActivity) < activeThreshold {
			status = sdk.SubAgentStatusActive
		}
		id := strings.TrimSuffix(name, ".jsonl")
		lastActivity[id] = parsed.LastActivity
		out = append(out, sdk.SubAgent{
			ID:              id,
			Type:            "subagent",
			Status:          status,
			CurrentAction:   parsed.CurrentAction,
			SessionFile:     p,
			TokensUsed:      parsed.TokensUsed,
			DurationSeconds: parsed.DurationSeconds,
			LatestOutput:    parsed.LatestOutput,
		})
	}
	sort.SliceStable(out, func(i, j int) bool {
		ai, aj := out[i], out[j]
		if (ai.Status == sdk.SubAgentStatusActive) != (aj.Status == sdk.SubAgentStatusActive) {
			return ai.Status == sdk.SubAgentStatusActive
		}
		return lastActivity[ai.ID].After(lastActivity[aj.ID])
	})
	return out
}

// provisionalSession answers for a process that names its own session on its
// command line but whose transcript is not on disk yet. Claude writes nothing
// until a turn closes, so an agent whose opening move is a question has no file
// — and dropping it hides the one session actually waiting for an answer. The
// pty broker already holds that question; this is what gives it an agent to
// hang on.
//
// Returns nil for a process that names no session, which keeps the list as it
// was for everything else: an unnamed `claude`, an internal daemon, or a second
// process claiming a session another one already took.
func provisionalSession(proc scanner.ProcessInfo, claimed map[string]bool) *parser.SessionData {
	// --session-id is a Claude CLI flag; another provider's id is not named there.
	if proc.Provider != "" && proc.Provider != sdk.ProviderClaude {
		return nil
	}
	id := parser.SessionIDFromArgs(proc.Command)
	if id == "" || claimed[id] {
		return nil
	}
	if claimed != nil {
		claimed[id] = true
	}
	return &parser.SessionData{
		SessionID:  id,
		Entrypoint: sdk.EntrypointCLI,
		// Process start, not zero: a status derived from the zero time reads as
		// long idle for a session that began seconds ago.
		LastActivity: time.Now().Add(-time.Duration(proc.Uptime) * time.Second),
		// It is mid-turn by construction — the turn is what has not closed.
		TurnOpen: true,
	}
}
