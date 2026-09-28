// Package sdk provides shared types for the agent-dashboard modules.
package sdk

//go:generate tygo generate --config ../tygo.yaml

// TokenUsage mirrors the Claude Code session token counters.
type TokenUsage struct {
	InputTokens         int `json:"inputTokens"`
	OutputTokens        int `json:"outputTokens"`
	CacheCreationTokens int `json:"cacheCreationTokens"`
	CacheReadTokens     int `json:"cacheReadTokens"`
}

// SessionMeta is read from ~/.claude/usage-data/session-meta/{sessionId}.json
type SessionMeta struct {
	InputTokens   int    `json:"inputTokens"`
	OutputTokens  int    `json:"outputTokens"`
	LinesAdded    int    `json:"linesAdded"`
	LinesRemoved  int    `json:"linesRemoved"`
	FilesModified int    `json:"filesModified"`
	GitCommits    int    `json:"gitCommits"`
	ToolErrors    int    `json:"toolErrors"`
	UsesMCP       bool   `json:"usesMcp"`
	FirstPrompt   string `json:"firstPrompt"`
}

// SubAgentStatus is the lifecycle state of a spawned sub-agent.
type SubAgentStatus string

const (
	SubAgentStatusActive    SubAgentStatus = "active"
	SubAgentStatusCompleted SubAgentStatus = "completed"
)

// SubAgent represents a sub-agent spawned by a parent Claude session.
type SubAgent struct {
	ID              string         `json:"id"`
	Type            string         `json:"type"`
	Status          SubAgentStatus `json:"status"`
	CurrentAction   string         `json:"currentAction"`
	SessionFile     string         `json:"sessionFile"`
	TokensUsed      int            `json:"tokensUsed"`
	DurationSeconds int            `json:"durationSeconds"`
	LatestOutput    string         `json:"latestOutput"`
}

// TaskInfoStatus is the state of a TodoWrite-tracked task.
type TaskInfoStatus string

const (
	TaskInfoStatusPending    TaskInfoStatus = "pending"
	TaskInfoStatusInProgress TaskInfoStatus = "in_progress"
	TaskInfoStatusCompleted  TaskInfoStatus = "completed"
)

// TaskInfo is a task tracked by Claude Code's internal task list.
type TaskInfo struct {
	ID      string         `json:"id"`
	Subject string         `json:"subject"`
	Status  TaskInfoStatus `json:"status"`
}

// AgentStatus is the computed activity state of an agent process.
type AgentStatus string

const (
	AgentStatusActive   AgentStatus = "active"
	AgentStatusWaiting  AgentStatus = "waiting"
	AgentStatusIdle     AgentStatus = "idle"
	AgentStatusFinished AgentStatus = "finished"
)

// Entrypoint describes how the Claude Code process was launched.
type Entrypoint string

const (
	EntrypointCLI     Entrypoint = "cli"
	EntrypointDesktop Entrypoint = "desktop"
	EntrypointUnknown Entrypoint = "unknown"
)

// Provider identifies which AI coding CLI an agent process belongs to.
type Provider string

const (
	ProviderClaude Provider = "claude"
	ProviderCodex  Provider = "codex"
	ProviderGemini Provider = "gemini"
	ProviderJunie  Provider = "junie"
)

// ErrorState describes a recognisable error condition seen in the session log.
type ErrorState string

const (
	ErrorStateQuotaExhausted ErrorState = "quota_exhausted"
	ErrorStateRateLimited    ErrorState = "rate_limited"
	ErrorStateAuthFailed     ErrorState = "auth_failed"
)

// SankeyNode is one node in the tool-call Sankey diagram. ID equals Name
// today but is kept separate so collisions across distinct sources can
// later be disambiguated.
type SankeyNode struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// SankeyLink is one directed source→target flow with an aggregated count.
type SankeyLink struct {
	Source string `json:"source"`
	Target string `json:"target"`
	Value  int    `json:"value"`
}

// SankeyMeta carries summary counters for the Sankey response.
type SankeyMeta struct {
	SessionCount int `json:"sessionCount"`
	CallCount    int `json:"callCount"`
}

// SankeyData is the response payload for GET /api/visualizations/sankey.
type SankeyData struct {
	Nodes []SankeyNode `json:"nodes"`
	Links []SankeyLink `json:"links"`
	Meta  SankeyMeta   `json:"meta"`
}

// DAGNode is one node in the session DAG (tool call, assistant turn, or
// user message). The DAG is per-session so IDs are line-local.
type DAGNode struct {
	ID    string `json:"id"`
	Type  string `json:"type"` // "tool" | "assistant" | "user"
	Label string `json:"label"`
	Ts    string `json:"ts"` // RFC3339 timestamp, empty if absent
}

// DAGLink is one edge in the session DAG. Kind is "chrono" for time-
// ordered succession or "result" for a tool_use → tool_result match.
type DAGLink struct {
	Source string `json:"source"`
	Target string `json:"target"`
	Kind   string `json:"kind"`
}

// DAGData is the response payload for GET /api/visualizations/dag.
type DAGData struct {
	Nodes []DAGNode `json:"nodes"`
	Links []DAGLink `json:"links"`
}

// SpawnTreeNode is one session node in the spawn tree. Depth is computed
// from the root via BFS. ToolCount and CostCents reflect the session,
// not the cumulative subtree.
type SpawnTreeNode struct {
	ID          string `json:"id"`
	Label       string `json:"label"`
	Depth       int    `json:"depth"`
	ToolCount   int    `json:"toolCount"`
	CostCents   int    `json:"costCents"`
	Project     string `json:"project"`
	Model       string `json:"model"`
	FirstPrompt string `json:"firstPrompt"`
}

// SpawnTreeLink is a parent→child spawn relationship.
type SpawnTreeLink struct {
	Source string `json:"source"`
	Target string `json:"target"`
}

// SpawnTreeData is the response payload for GET /api/visualizations/spawn-tree.
type SpawnTreeData struct {
	Roots []string        `json:"roots"`
	Nodes []SpawnTreeNode `json:"nodes"`
	Links []SpawnTreeLink `json:"links"`
}

// CoOccurrenceMeta carries summary counters for the co-occurrence response.
type CoOccurrenceMeta struct {
	SessionCount int  `json:"sessionCount"`
	Truncated    bool `json:"truncated"`
}

// CoOccurrenceData is the response payload for
// GET /api/visualizations/co-occurrence. Matrix is square with side equal
// to len(Tools); Matrix[i][j] = sessions containing both Tools[i] and
// Tools[j] (diagonal = sessions containing the tool at all).
// Lift is the same-dimension normalized lift score: Lift[i][j] =
// (Matrix[i][j] × N) / (Matrix[i][i] × Matrix[j][j]), where N is the
// total session count. Diagonal is 0 (self-lift is meaningless).
type CoOccurrenceData struct {
	Tools  []string         `json:"tools"`
	Matrix [][]int          `json:"matrix"`
	Lift   [][]float64      `json:"lift"`
	Meta   CoOccurrenceMeta `json:"meta"`
}

// BtwMessage is the last assistant text that appeared alongside tool calls.
// Message is the text content; Response is reserved for future use.
type BtwMessage struct {
	Message  string  `json:"message"`
	Response *string `json:"response"`
}

// WorktreeStatusDTO describes the live git state of a task's worktree.
// Ahead and Behind are pointers so JSON null is preserved when the base
// branch cannot be resolved on `origin` (e.g. local-only base).
type WorktreeStatusDTO struct {
	Branch    string `json:"branch"`
	Ahead     *int   `json:"ahead"`
	Behind    *int   `json:"behind"`
	Dirty     bool   `json:"dirty"`
	FileCount int    `json:"fileCount"`
}

// PendingPermission is a permission request an orchestrated agent is currently
// blocked on, surfaced on the agent so it can be resolved from the roster.
type PendingPermission struct {
	ID      string  `json:"id"`
	Tool    string  `json:"tool"`
	Pattern *string `json:"pattern"`
	// PatternElided carries Pattern's cut count as its own field, mirroring
	// RecentTool.Elided, so a truncated Pattern cannot forge its own "…" next
	// to the Allow button.
	PatternElided int `json:"patternElided,omitempty"`
	// DeniedBy is the user's own permissions.deny rule forbidding this call,
	// verbatim, or nil when no rule covers it. A hook "allow" short-circuits
	// Claude Code's own evaluation, so a dashboard approval would otherwise
	// release a restriction the user believes is absolute. The client offers no
	// Allow when this is set; the server refuses one regardless.
	DeniedBy *string `json:"deniedBy,omitempty"`
	// DeniedByElided mirrors PatternElided, for DeniedBy.
	DeniedByElided int     `json:"deniedByElided,omitempty"`
	Reason         *string `json:"reason"`
	RequestedAt    string  `json:"requestedAt"`
	// DeniedByDefault is true when a grant already denies this capability in
	// this run's contexts — the same resolution the run's allow list uses. The
	// client offers no decision for such a request; the answer is to change the
	// grant, not to answer the prompt.
	DeniedByDefault bool `json:"deniedByDefault,omitempty"`
}

// PendingCapabilityDecision is a capability decision waiting for a human at a
// server enforcement point; unlike PendingPermission it names a capability
// and scope, not a tool, and belongs to no Claude Code session.
type PendingCapabilityDecision struct {
	ID         string `json:"id"`
	Capability string `json:"capability"`
	Value      string `json:"value"`
	// ValueElided/ContextElided carry the cut count as their own field, mirroring
	// RecentTool.Elided, so a truncated Value/Context cannot forge its own "…".
	ValueElided   int    `json:"valueElided,omitempty"`
	Context       string `json:"context"`
	ContextElided int    `json:"contextElided,omitempty"`
	Reason        string `json:"reason"`
	RequestedAt   string `json:"requestedAt"`
}

// RecentTool is one entry of the recent-tool trail. Detail is the tool's own
// human-readable argument -- a Bash command, an Edit/Write path -- and is
// agent-authored text on its way to a UI: never interpolate it into a shell
// command, a query or HTML without escaping at the point of use.
//
// Elided is how many characters Detail was cut by, 0 when it was not cut. It is
// a separate field rather than a suffix inside Detail so the payload cannot
// forge a cut that never happened: the client renders it as its own element.
type RecentTool struct {
	Name   string `json:"name"`
	Detail string `json:"detail,omitempty"`
	Elided int    `json:"elided,omitempty"`
}

// NoteTouchKind says whether an agent read or wrote a vault note.
type NoteTouchKind string

const (
	NoteTouchKindRead  NoteTouchKind = "read"
	NoteTouchKindWrite NoteTouchKind = "write"
)

// NoteTouch is one vault note an agent read or wrote recently.
type NoteTouch struct {
	// Path is relative to obsidian.vaultRoot, the form the vault graph lists.
	Path string        `json:"path"`
	Kind NoteTouchKind `json:"kind"`
	// At is RFC 3339, like Agent.LastActivity.
	At string `json:"at"`
}

// PendingToolUse is the last assistant tool_use block that has no matching
// tool_result yet. It indicates the agent is currently executing or blocked on
// that tool call.
//
// Pattern is the command string (Bash), file path (Edit/Write), or empty for
// other tools. It is the grant identity: a permission preset is matched by
// exact equality, so it is carried verbatim and must never be normalised.
// PatternDisplay is the same value made safe to render -- deceptive runes
// removed, whitespace collapsed, never truncated. Show PatternDisplay to a
// human; send Pattern to the permission API.
type PendingToolUse struct {
	ID             string `json:"id"`
	Tool           string `json:"tool"`
	Pattern        string `json:"pattern"`
	PatternDisplay string `json:"patternDisplay"`
}

// HookEvent is one lifecycle-hook event recorded for a session when the opt-in
// hook receiver is installed (POST /api/hooks/event). It adds per-event
// granularity on top of the process/JSONL scan. Tool and Summary are truncated,
// secret-safe projections of the raw hook payload — never the full tool_input or
// tool_response.
type HookEvent struct {
	Type    string `json:"type"`    // hook type, e.g. "PreToolUse" | "PostToolUse" | "Stop"
	Tool    string `json:"tool"`    // tool name, empty for non-tool hooks
	At      string `json:"at"`      // RFC3339 timestamp the event was received
	Summary string `json:"summary"` // truncated, secret-safe payload preview
}

// DetectedOption is a single numbered option row of a parsed AskUserQuestion
// modal.
type DetectedOption struct {
	Index       int    `json:"index"`
	Label       string `json:"label"`
	Description string `json:"description,omitempty"`
}

// DetectedQuestion is a parsed AskUserQuestion modal, detected from a live
// terminal's rendered rows by askq.DetectQuestion.
//
// Invariant (ENFORCED, not merely typical): the real option rows are numbered
// contiguously 1..n, and the two UI-injected meta-rows follow immediately, so
// TypeSomethingIndex == len(Options)+1 and ChatAboutIndex == TypeSomethingIndex+1.
type DetectedQuestion struct {
	// Header is a best-effort title line above the question. Bordered renders
	// carry a real one; borderless renders leave whatever scrolled above the
	// modal here (a prompt echo, the welcome box). Not UI-facing.
	Header             string           `json:"header"`
	Question           string           `json:"question"`
	MultiSelect        bool             `json:"multiSelect"`
	Options            []DetectedOption `json:"options"`
	TypeSomethingIndex int              `json:"typeSomethingIndex"`
	ChatAboutIndex     int              `json:"chatAboutIndex"`
}

// DetectedConfirm is the AskUserQuestion review/submit screen, detected from a
// live terminal's rendered rows by askq.DetectConfirmScreen.
//
// It is the screen a multi-question AskUserQuestion flow lands on after the
// last question has been answered ("Ready to submit your answers?" plus a
// Submit/Cancel choice). It carries no meta-rows, so it is not a
// DetectedQuestion: there is nothing to type and nothing to chat about, only
// two numbered options to pick from.
type DetectedConfirm struct {
	Question string           `json:"question"`
	Options  []DetectedOption `json:"options"`
}

// PendingScreen is whichever interactive AskUserQuestion screen is currently
// open on a session's terminal. At most one field is non-nil; both are nil when
// no such screen is open. Probing for both in one round-trip keeps the scan hot
// path to a single capture per tick.
type PendingScreen struct {
	Question *DetectedQuestion `json:"question,omitempty"`
	Confirm  *DetectedConfirm  `json:"confirm,omitempty"`
}

// How an agent's spawner attribution was established: recorded from the pipeline
// task the agent runs, or read back from the live process, which is how sessions
// started outside the dashboard are placed.
//
// These are untyped consts, so tygo emits no counterpart; the client mirrors
// them by hand in src/types.ts (SPAWNER_SOURCE_TASK / SPAWNER_SOURCE_ENV).
// Changing a value here means changing it there.
const (
	SpawnerSourceTask = "task"
	SpawnerSourceEnv  = "env"
)

// Agent is the unified view of a running Claude Code process.
type Agent struct {
	PID         int      `json:"pid"`
	SessionID   string   `json:"sessionId"`
	Provider    Provider `json:"provider"`
	ProjectPath string   `json:"projectPath"`
	ProjectName string   `json:"projectName"`
	CWD         string   `json:"cwd"`
	// ClaudeConfigDir is the value of CLAUDE_CONFIG_DIR detected in the running
	// session's process env (empty when the session uses the default ~/.claude).
	// Lets the dashboard resolve which config root a session's slash commands /
	// skills / plugins are loaded from when enumerating per-session scope.
	ClaudeConfigDir string `json:"claudeConfigDir,omitempty"`
	// ClaudeConfigDirKnown says whether the process environment was actually
	// read, which is what separates "this session sets no CLAUDE_CONFIG_DIR"
	// from "its environment could not be read" — indistinguishable in
	// ClaudeConfigDir alone, and the difference between attributing a session
	// to a profile and having no idea which profile it runs on. Server-side
	// only (the client has no use for it), hence no JSON field.
	ClaudeConfigDirKnown bool         `json:"-"`
	Entrypoint           Entrypoint   `json:"entrypoint"`
	Status               AgentStatus  `json:"status"`
	Uptime               int64        `json:"uptime"`
	LastActivity         string       `json:"lastActivity"`
	CurrentAction        *string      `json:"currentAction"`
	LastTools            []RecentTool `json:"lastTools"`
	// SessionTitle is the newest /rename custom title, else the newest
	// Claude-generated title, else empty.
	SessionTitle string `json:"sessionTitle,omitempty"`
	// RecentNotes are the vault notes this agent read or wrote in the last ten minutes, newest first.
	RecentNotes               []NoteTouch    `json:"recentNotes,omitempty"`
	Tasks                     []TaskInfo     `json:"tasks"`
	Subagents                 []SubAgent     `json:"subagents"`
	TokenUsage                TokenUsage     `json:"tokenUsage"`
	CostEstimate              float64        `json:"costEstimate"`
	CacheCreationCostEstimate float64        `json:"cacheCreationCostEstimate"`
	CacheReadCostEstimate     float64        `json:"cacheReadCostEstimate"`
	HealthScore               int            `json:"healthScore"`
	Model                     *string        `json:"model"`
	CodeVersion               *string        `json:"codeVersion"`
	ConversationTurns         int            `json:"conversationTurns"`
	ToolCounts                map[string]int `json:"toolCounts"`
	Meta                      *SessionMeta   `json:"meta"`
	ChannelAvailable          bool           `json:"channelAvailable"`
	// Working is true when the agent is actively generating: it owes the next
	// step (open turn — a trailing user message or unmatched tool_use, via
	// parser.SessionData.TurnOpen) OR a live session emitted output within the
	// last few seconds (pty lastOutputAt / tmux window_activity).
	Working bool `json:"working"`
	// PermissionsBypassed is true when the process runs with
	// --dangerously-skip-permissions or --permission-mode bypassPermissions, so it
	// never stops for a tool-permission prompt. An unresolved tool_use in such a
	// session means the tool is running, not that someone has to approve it.
	PermissionsBypassed bool `json:"permissionsBypassed"`
	// LiveInjectable is true when the dashboard can deliver a prompt to this
	// running interactive session as real keyboard input — either via the pty
	// broker (`agent-dashboard ptyhost`) or `tmux send-keys`. When false, sending
	// resumes the session as a new one (MCP log delivery does not drive it).
	LiveInjectable bool `json:"liveInjectable,omitempty"`
	// InternalProcess is true when this is Claude Code's own internal
	// daemon/spare-pool machinery (a grandchild of a real session, matched by
	// scanner.IsInternalProcess), not a session the user can address —
	// LiveInjectable is meaningless for it and no prompt can be sent to it.
	InternalProcess bool `json:"internalProcess,omitempty"`
	// PendingQuestion is the AskUserQuestion modal currently detected on this
	// session's live terminal (render-sourced, via the pty broker's /question
	// endpoint or a tmux capture-pane snapshot), or nil when no modal is open.
	// Only ever set on injectable sessions.
	PendingQuestion *DetectedQuestion `json:"pendingQuestion,omitempty"`
	// PendingConfirm is the AskUserQuestion review/submit screen currently
	// detected on this session's live terminal, or nil. Mutually exclusive with
	// PendingQuestion: the TUI shows one or the other, never both. Only ever set
	// on injectable sessions.
	PendingConfirm      *DetectedConfirm `json:"pendingConfirm,omitempty"`
	LastOutput          *string          `json:"lastOutput"`
	ConvergenceAlert    bool             `json:"convergenceAlert"`
	ConvergenceToolName *string          `json:"convergenceToolName"`
	ErrorState          *ErrorState      `json:"errorState"`
	PipelineTaskID      string           `json:"pipelineTaskId,omitempty"`
	PipelineTaskTitle   string           `json:"pipelineTaskTitle,omitempty"`
	// SpawnerID/SpawnerName name the configured spawner this session belongs to,
	// and SpawnerSource says how that was established (see SpawnerSource*).
	// Empty when no spawner could be attributed.
	SpawnerID     string `json:"spawnerId,omitempty"`
	SpawnerName   string `json:"spawnerName,omitempty"`
	SpawnerSource string `json:"spawnerSource,omitempty"`
	// PendingPermissions are the approval requests this session is blocked on and
	// that can be answered from the dashboard: either a pipeline stage run's
	// stored requests, or a PreToolUse hook call the permission bridge is holding
	// open. Both are answerable; the client tells them apart by PipelineTaskID.
	PendingPermissions []PendingPermission `json:"pendingPermissions,omitempty"`
	// HeldPermissions are PreToolUse hook calls the permission bridge is holding
	// open for this session, in arrival order. Deliberately NOT merged into
	// PendingPermissions: those are database-backed pipeline stage-run rows that
	// resolve through a different endpoint, and sharing one slice let a hook id
	// ride along in the pipeline's bulk-resolve payload.
	HeldPermissions []PendingPermission `json:"heldPermissions,omitempty"`
	// PermissionBridgeArmed means the dashboard intercepts this session's
	// permission prompts. Off by default: PreToolUse fires before Claude Code
	// decides whether to prompt at all, so holding every call would stall every
	// session. A session is intercepted only after someone asked for it.
	PermissionBridgeArmed bool `json:"permissionBridgeArmed,omitempty"`
	// AwaitingTerminalPermission means the session is showing its own permission
	// prompt, which the dashboard can report but not answer -- the bridge either
	// lapsed before anyone decided, or the session is not armed. Set from the
	// Notification hook's typed notification_type, never from its prose.
	AwaitingTerminalPermission bool `json:"awaitingTerminalPermission,omitempty"`
	// TerminalPermissionToolUseID names the tool call that terminal prompt is
	// about, when the bridge held that call and the hold lapsed. Empty for a
	// session the bridge never held, because the Notification payload carries no
	// tool id of its own. Without it a standing grant can only be offered for
	// PendingToolUse below, which is derived independently from the transcript
	// and can name a different call entirely -- so a notice that outlives its
	// prompt would put a grant button next to a tool nobody asked about.
	TerminalPermissionToolUseID string          `json:"terminalPermissionToolUseId,omitempty"`
	PendingToolUse              *PendingToolUse `json:"pendingToolUse,omitempty"`
	Machine                     string          `json:"machine,omitempty"`
	LastBtw                     *BtwMessage     `json:"lastBtw"`
	// CostUnknown is true when the provider does not expose token counts and
	// cost cannot be estimated. CostEstimate will be 0 in this case.
	CostUnknown bool `json:"costUnknown,omitempty"`
	// CostLocal is true when the session runs a locally-served (Ollama) model,
	// whose cost is $0 rather than unknown. CostEstimate is 0 in this case.
	CostLocal bool `json:"costLocal,omitempty"`
	// RecentHookEvents carries per-event hook granularity for sessions where the
	// opt-in hook receiver holds events. Omitted entirely when no hook is
	// installed, so clients without hooks receive byte-identical payloads.
	RecentHookEvents []HookEvent `json:"recentHookEvents,omitempty"`
}
