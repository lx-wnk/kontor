package merger

import (
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/lx-wnk/kontor/sdk"
	"github.com/lx-wnk/kontor/server/internal/parser"
)

// liveSnapshot caches the minimum a finished agent card needs to be
// reconstructed from JSONL after its process has exited.
type liveSnapshot struct {
	sessionID   string
	path        string
	projectPath string
	configDir   string
	// configDirKnown mirrors sdk.Agent.ClaudeConfigDirKnown: it was read while
	// the process was alive, and stays true after it exits — the answer does not
	// become unknown just because the process is gone.
	configDirKnown bool
	provider       sdk.Provider
}

// staleTracker is an in-memory, process-scoped registry of controllable agents
// (channel/MCP-connected) recorded while they were live, so their cards can stay
// visible as finished after the process exits. Emission no longer depends on the
// discovery file still existing — the bridge deletes it on exit — so the
// recorded fact is the source of truth. parseFn is injectable for tests; the
// default hits the real JSONL parser.
type staleTracker struct {
	mu      sync.Mutex
	seen    map[int]liveSnapshot
	parseFn func(path string) (*parser.SessionData, error)
}

func newStaleTracker() *staleTracker {
	return &staleTracker{
		seen:    make(map[int]liveSnapshot),
		parseFn: parseSessionByPath,
	}
}

// parseSessionByPath parses a JSONL session file and fills SessionID/Path from
// the path. ParseSessionFile only parses the body; the live resolver derives
// these from the filename, so the stale path must do the same to match.
func parseSessionByPath(path string) (*parser.SessionData, error) {
	data, err := parser.ParseSessionFile(path)
	if err != nil {
		return nil, err
	}
	data.SessionID = strings.TrimSuffix(filepath.Base(path), ".jsonl")
	data.Path = path
	return data, nil
}

// record stores a snapshot for a live agent. Snapshots without a session id or
// path are useless for later reconstruction and are dropped. Non-Claude
// providers (Codex/Gemini) never resolve a session path, so they are dropped
// here and never get a finished card — the feature is Claude-only by design.
func (t *staleTracker) record(pid int, snap liveSnapshot) {
	if snap.sessionID == "" || snap.path == "" {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.seen[pid] = snap
}

// buildStale emits a finished sdk.Agent for each tracked pid whose process is no
// longer live. Tracked pids are forgotten only via dismiss (the DELETE
// endpoint), not by inspecting the discovery file — the bridge deletes that file
// on exit, so its absence no longer means "dismissed".
func (t *staleTracker) buildStale(livePIDs map[int]bool, baselineCost float64) []sdk.Agent {
	t.mu.Lock()
	defer t.mu.Unlock()

	var out []sdk.Agent
	for pid, snap := range t.seen {
		if livePIDs[pid] {
			continue
		}
		session, err := t.parseFn(snap.path)
		if err != nil || session == nil || session.SessionID == "" {
			// Transient parse miss; retry next tick. A permanently unparseable
			// dead pid stays in seen for the process lifetime — accepted bounded
			// leak for this process-scoped registry; no age-out by design.
			continue
		}
		out = append(out, buildFinishedAgent(pid, snap, session, baselineCost))
	}
	return out
}

// dismiss forgets a tracked pid so its finished card stops appearing.
func (t *staleTracker) dismiss(pid int) {
	t.mu.Lock()
	defer t.mu.Unlock()
	delete(t.seen, pid)
}

// buildFinishedAgent mirrors buildAgent but reconstructs a finished, controllable
// card from a cached snapshot plus freshly parsed session data. Uptime is left
// zero because the process is gone.
// Keep sdk.Agent field population in parity with buildAgent in merger.go — a new sdk.Agent field must be added to both.
func buildFinishedAgent(pid int, snap liveSnapshot, session *parser.SessionData, baselineCost float64) sdk.Agent {
	provider := snap.provider
	if provider == "" {
		provider = sdk.ProviderClaude
	}
	c := EstimateCostForProvider(provider, session.TokenUsage, session.Model)
	health := ComputeHealthScore(session, c.Total, c.Unknown, baselineCost)

	return sdk.Agent{
		PID:                       pid,
		SessionID:                 session.SessionID,
		Provider:                  provider,
		ProjectPath:               snap.projectPath,
		ProjectName:               filepath.Base(snap.projectPath),
		CWD:                       snap.projectPath,
		ClaudeConfigDir:           snap.configDir,
		ClaudeConfigDirKnown:      snap.configDirKnown,
		Entrypoint:                session.Entrypoint,
		Status:                    sdk.AgentStatusFinished,
		Working:                   false,
		ChannelAvailable:          true,
		LiveInjectable:            false,
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
