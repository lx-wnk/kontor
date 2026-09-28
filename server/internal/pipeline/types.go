package pipeline

import (
	"context"
	"time"

	"github.com/lx-wnk/kontor/server/internal/db/ent"
	"github.com/lx-wnk/kontor/server/internal/db/repo"
	"github.com/lx-wnk/kontor/server/internal/mcpapps"
	"github.com/lx-wnk/kontor/server/internal/memory"
)

// StageTransition is a sealed interface. Only types in this package satisfy it.
// applyTransition must handle every concrete type; unhandled type panics.
type StageTransition interface{ isTransition() }

type NextTransition struct {
	Stage         string
	MetadataPatch map[string]any
	MetaClear     bool
	Output        map[string]any
}

type DoneTransition struct {
	Output        map[string]any
	MetadataPatch map[string]any
}

type FailTransition struct {
	Reason string
	Output map[string]any
}

type WaitUserTransition struct {
	Reason string
	Output map[string]any
	// AgentDone signals that the agent process has already exited normally
	// (e.g. review cycle limit reached). applyTransition will clear the PID
	// so the dead-PID reaper does not immediately re-fail the run.
	AgentDone bool
}

type IterateTransition struct {
	Output map[string]any
}

type OnHoldTransition struct {
	PermissionRequestID string
	Output              map[string]any
}

type AsyncRunningTransition struct {
	PID         int
	SessionID   string
	SessionFile string // path to synthetic JSONL; empty for Claude (discovered normally)
	Output      map[string]any
}

type RequeueTransition struct {
	Reason      string
	Output      map[string]any
	Attempt     int
	NextRetryAt time.Time
}

func (NextTransition) isTransition()         {}
func (DoneTransition) isTransition()         {}
func (FailTransition) isTransition()         {}
func (WaitUserTransition) isTransition()     {}
func (IterateTransition) isTransition()      {}
func (OnHoldTransition) isTransition()       {}
func (AsyncRunningTransition) isTransition() {}
func (RequeueTransition) isTransition()      {}

// SystemPromptQuerier is a narrow interface so pipeline does not import the full repo package.
type SystemPromptQuerier interface {
	ListForStage(ctx context.Context, stage string) ([]*ent.SystemPrompt, error)
}

// SpawnerResolverFunc resolves the effective DB spawner row for a task right
// before `exec`. The pipeline only needs the resolved spawner; the actual
// resolver lives in internal/services and is wired by the composition root.
// stage is the current pipeline stage (e.g. "implementation"); "" for non-agent stages.
// Errors are propagated and abort the spawn — callers must NEVER silently
// fall back when an explicit reference fails to load.
type SpawnerResolverFunc func(ctx context.Context, taskID, stage string) (*ent.Spawner, error)

// StageContext is passed to stage handlers.
type StageContext struct {
	Ctx                  context.Context
	Task                 *ent.Task
	StageRun             *ent.StageRun
	Permissions          []*ent.TaskPermission
	AllStageRuns         []*ent.StageRun
	PreviousOutput       map[string]any
	PriorIterationOutput map[string]any
	ResumeSessionID      string
	UserAdditionalPrompt string
	MCPToken             string
	MCPUrl               string

	// StageTimeout is the global stageTimeoutSeconds the kill check enforces; 0 means none.
	StageTimeout time.Duration

	// AllowGitPush mirrors OrchestratorOptions.AllowGitPush for the current stage,
	// gating whether spawned agents may run `git push`.
	AllowGitPush bool

	// AdditionalDirs holds the paths of project folders beyond the task cwd
	// that the spawned agent should be able to access. Each path is forwarded
	// to the `claude` CLI as a --add-dir flag. Populated from
	// OrchestratorOptions.ResolveAdditionalDirs; nil means no extra dirs.
	AdditionalDirs []string

	// ResolveSpawner returns the effective DB spawner row for the current task
	// (task → project → claude-default). Stage handlers invoke this right
	// before the native Claude spawn so the resulting ent.Spawner can be
	// passed through SpawnAgentOptions. When nil, the legacy `claude` path
	// is used unchanged.
	ResolveSpawner SpawnerResolverFunc

	// SystemPromptRepo is used to fetch custom system prompt overrides for this stage.
	// May be nil if the feature is not configured.
	SystemPromptRepo SystemPromptQuerier

	// StageModelFn returns the effective per-stage model for the current stage and
	// project, applying: coded Balanced default → global DB row → project DB row.
	// Task/spawner explicit overrides are applied on top by stage_handlers.go.
	// projectID may be nil for tasks without a project. Returns "" for non-agent stages.
	StageModelFn func(ctx context.Context, stage string, projectID *string) string

	// DispatchHTTPSpawn runs the given HTTP spawn function in the goroutine pool
	// and returns immediately. The caller should return AsyncRunningTransition{PID:0}
	// after calling this. Results are drained by the orchestrator on the next tick.
	// The context passed to spawn is NOT the caller's (HTTP request) context — it
	// outlives the request and is cancelled only when the orchestrator shuts down.
	// When nil (e.g. in tests without a live orchestrator), callers must invoke the
	// spawn function synchronously.
	DispatchHTTPSpawn func(stageRunID, taskID string, spawn func(context.Context) (string, error))

	RecordAudit       func(action string, details map[string]any)
	RequestPermission func(tool, pattern, reason string) *ent.PermissionRequest

	// PermissionStatus reads back the row a prior RequestPermission call filed,
	// by its ID. Kept acp-agnostic: it returns the ent row, not an acp-package
	// type, so this file does not need to import the acp adapter.
	PermissionStatus func(ctx context.Context, id string) (*ent.PermissionRequest, error)

	// InjectMemory retrieves ranked memory entries for the stage's spawn user
	// prompt — typically a bound (*memory.Retriever).Retrieve method value.
	// Nil disables the memory push outright: memory being unavailable must
	// never block a spawn, so "no injector wired" means "no injection" rather
	// than a best-effort partial one.
	InjectMemory memory.InjectorFunc

	// MemoryBudget bounds the injected memory block in characters. <= 0
	// disables injection — a non-positive value must never be read as
	// "unbounded", so this fails closed rather than risking an uncapped block.
	MemoryBudget int

	// RecordMemoryInjection persists what memory was offered and what fit for
	// this stage run. Nil skips recording: it is best-effort bookkeeping and
	// must not fail a spawn.
	RecordMemoryInjection func(ctx context.Context, in repo.RecordInjectionInput) (*ent.MemoryInjection, error)

	// AuthorizeMemory gates the automatic memory push behind the same
	// memory.Authorize capability check every human-facing memory route
	// (MCP tools, HTTP API) already goes through — the push is otherwise the
	// one memory read in the system with no capability gate. Nil disables
	// the push the same way a nil InjectMemory does; a denial degrades to no
	// memory block rather than failing the spawn (see injectMemoryBlock).
	//
	// routineID names the routine whose firing produced this task, so a
	// grant made against that routine is in scope for the push. Empty for
	// a task a human created.
	AuthorizeMemory func(ctx context.Context, scope repo.Scope, routineID string) error

	// IssueTaskAPIKey mints the MCP credential a spawned stage agent presents
	// to the dashboard's own /api/mcp endpoint. Nil disables issuance: the
	// spawn then gets no dashboard-tasks config entry and reaches no task API,
	// since --strict-mcp-config shuts the user-scope one out. A mint failure
	// is likewise non-fatal — see
	// stage_handlers.go's caller.
	IssueTaskAPIKey func(ctx context.Context, stageRunID string, stageTimeout time.Duration) (string, error)

	// ResolveApplications decides which MCP applications a run gets, with
	// which environment, and which of their tools are allowed or denied. nil
	// means the run gets no MCP applications.
	ResolveApplications func(ctx context.Context, task *ent.Task) (mcpapps.RunApplications, error)

	// RegisterSpawnCleanup takes the cleanup closure a spawn returns, so the
	// files it wrote — the temp --mcp-config carrying the credential minted
	// above, and the settings allow-list entries — are removed when the run
	// releases its credentials. Nil leaves those files in place until the
	// process exits.
	RegisterSpawnCleanup func(stageRunID string, cleanup func())
}

// StageHandler is implemented by each pipeline stage.
type StageHandler interface {
	Stage() string
	RequiresAgent() bool
	Execute(ctx *StageContext) (StageTransition, error)
}

// PromptBundle is the system+user prompt pair passed to the Claude CLI.
type PromptBundle struct {
	SystemPrompt string
	UserPrompt   string
}

// StageOrder defines canonical stage progression.
var StageOrder = []string{
	"backlog",
	"ready",
	"plan_review",
	"implementation",
	"self_review",
	"finalization",
	"done",
}

// NextStage returns the next stage after s, or "done" if s is the last stage.
func NextStage(s string) string {
	for i, stage := range StageOrder {
		if stage == s && i < len(StageOrder)-1 {
			return StageOrder[i+1]
		}
	}
	return "done"
}

// IsTerminalStage returns true for done and cancelled.
func IsTerminalStage(s string) bool {
	return s == "done" || s == "cancelled"
}

const (
	TaskKindPipeline = "pipeline"
	TaskKindJob      = "job"
	StageJob         = "job"
)

// StageKindViolation reports why a task of kind may not enter stage, or "".
func StageKindViolation(kind, stage string) string {
	if IsTerminalStage(stage) {
		return ""
	}
	if kind == TaskKindJob && stage != StageJob {
		return "job tasks run only the job stage, refused move to " + stage
	}
	if kind != TaskKindJob && stage == StageJob {
		return "only job tasks run the job stage"
	}
	return ""
}

// OrchestratorOptions configures the PipelineOrchestrator.
type OrchestratorOptions struct {
	PollInterval     time.Duration
	Client           *ent.Client
	TaskRepo         repo.TaskRepo
	StageRunRepo     repo.StageRunRepo
	PermissionRepo   repo.PermissionRepo
	AuditRepo        repo.AuditEventRepo
	ConfigRepo       repo.PipelineConfigRepo
	SystemPromptRepo SystemPromptQuerier
	// DepRepo enables dependency-cascade and picker-gate behaviour.
	// When nil (e.g. in older tests), dependency logic is skipped entirely.
	DepRepo repo.DependencyRepo

	// MCPToken and MCPUrl are injected into spawned stage agents via
	// DASHBOARD_MCP_TOKEN / DASHBOARD_MCP_URL so the channel bridge can
	// authenticate back-calls to the dashboard REST API.
	MCPToken string
	MCPUrl   string

	// WorktreeRoot is the base directory for auto-created git worktrees.
	// Defaults to ~/<worktree.DefaultRootDirName> when empty (see worktree.DefaultRoot).
	// Set via DASHBOARD_WORKTREE_ROOT.
	WorktreeRoot string

	// ForceWorktrees ensures every agent-driven task runs in an isolated git worktree,
	// even when task.SourceBranch is not set. The branch is derived as "feat/<slug>".
	// Set via DASHBOARD_FORCE_WORKTREES=true.
	ForceWorktrees bool

	// AllowGitPush permits spawned agents to run `git push` globally. Sourced from
	// the "git.allowPush" DB setting; per-task metadata["allowGitPush"] can still
	// opt in individually even when this is false.
	AllowGitPush bool

	// SpawnFn launches the agent process for native (claude) stages. Production
	// wires pipeline.SpawnStageAgent at the DI root; when nil the orchestrator
	// defaults to syntheticSpawn so tests can never launch a real agent.
	SpawnFn SpawnFunc

	// EnsureWorktreeFn creates (or verifies) a git worktree for a task.
	// Production wires ensureTaskWorktree; tests inject a stub to avoid real git.
	// When nil the orchestrator defaults to ensureTaskWorktree.
	EnsureWorktreeFn func(ctx context.Context, task *ent.Task, worktreeRoot string) (path, branch string, err error)

	// RemoveWorktreeFn force-removes a task's git worktree and clears its
	// WorktreePath, freeing the source branch when the task reaches a terminal
	// stage. Production wires services.WorktreeManager.RemoveWorktree; tests
	// inject a stub to avoid real git. When nil the orchestrator defaults to a
	// closure over removeTaskWorktree + TaskRepo (force-only). Errors are
	// non-fatal — callers log and continue.
	RemoveWorktreeFn func(ctx context.Context, task *ent.Task, force bool) error

	// SetupWorktreeFn, when non-nil, is called after a worktree is successfully created.
	// projectID may be nil for tasks without a project. Errors are logged as warnings;
	// they do NOT fail the task (soft-failure policy).
	SetupWorktreeFn func(ctx context.Context, projectID *string, worktreePath string) error

	// CheckpointerStartFn, when non-nil, starts the per-turn checkpoint watcher
	// once a task's worktree becomes active (stage run promoted to "running").
	// Nil-safe — no-op when absent (e.g. the no-DB/test path).
	CheckpointerStartFn func(taskID, worktreePath string)

	// CheckpointerStopFn, when non-nil, stops the checkpoint watcher and prunes
	// checkpoint refs/rows just before a terminal task's worktree is removed.
	// Nil-safe — no-op when absent.
	CheckpointerStopFn func(taskID string)

	// HasUnpushedWorkFn reports whether a task's worktree still holds unpushed
	// commits or uncommitted changes. When it returns true, terminal cleanup
	// retains the worktree instead of force-removing it, so the work is not
	// orphaned. Nil disables both this check and finalization's unpushed-work check.
	HasUnpushedWorkFn func(ctx context.Context, task *ent.Task) bool

	// PushFn pushes the task branch to origin. Called off the tick loop when
	// finalization completes and the task allows git push. Production wires
	// ProductionPushFn; tests inject a stub. When nil, push is skipped.
	PushFn func(ctx context.Context, task *ent.Task) error

	// CreateDraftPRFn creates a draft PR for the task branch against the given
	// base. Returns the PR number and URL. Production wires
	// ProductionCreateDraftPRFn; tests inject a stub. When nil, PR creation is
	// skipped.
	CreateDraftPRFn func(ctx context.Context, worktreePath, branch, base, title, prBody string) (prNumber int, prURL string, err error)

	// ResolveSpawner returns the effective DB spawner row for a task right
	// before the native Claude path is taken. When nil, stage handlers spawn
	// with the legacy `claude` CLI (current behaviour).
	ResolveSpawner SpawnerResolverFunc

	// ResolveAdditionalDirs returns the extra directory paths (beyond task.Cwd)
	// that the spawned agent should be able to reach via --add-dir. Called once
	// per task just before StageContext construction. When nil, or when the
	// function returns nil/empty, no extra directories are added. Errors should
	// be logged and treated as an empty result — never block the spawn.
	ResolveAdditionalDirs func(ctx context.Context, task *ent.Task) []string

	OnPermissionRequest func(taskID string, req *ent.PermissionRequest)
	OnStageFailed       func(taskID string, info StageFailedInfo)
	// OnTaskChanged is called after every successful state-machine transition.
	// payload carries the value returned by BuildTaskPayload (if set), which was
	// read inside the same DB transaction as the writes, guaranteeing a consistent
	// view. Callers that invoke OnTaskChanged outside a transaction pass nil;
	// the closure in di_pipeline.go falls back to a live read in that case.
	OnTaskChanged func(taskID string, transitionKind string, payload any)
	// BuildTaskPayload, when non-nil, is called inside applyTransitionWrites (before
	// tx.Commit) with tx-bound repos so that the returned snapshot reflects the
	// just-applied writes. The result is forwarded verbatim as the payload argument
	// to OnTaskChanged after the commit succeeds. Returning nil (e.g. on error)
	// tells OnTaskChanged to fall back to a live read. The pipeline package does not
	// interpret the returned value — callers own its type.
	BuildTaskPayload func(ctx context.Context, taskID string, srRepo repo.StageRunRepo, permRepo repo.PermissionRepo) any

	// InjectMemory, MemoryBudget and RecordMemoryInjection are forwarded
	// verbatim onto every StageContext this orchestrator builds — see the
	// matching fields there for what each one does. Production wires a bound
	// (*memory.Retriever).Retrieve, a positive character budget, and
	// repo.MemoryRepo.RecordInjection at DI time (serverapp/di_pipeline.go);
	// leaving InjectMemory nil or MemoryBudget <= 0 disables the push
	// entirely rather than treating it as unbounded.
	InjectMemory          memory.InjectorFunc
	MemoryBudget          int
	RecordMemoryInjection func(ctx context.Context, in repo.RecordInjectionInput) (*ent.MemoryInjection, error)

	// AuthorizeMemory is forwarded verbatim onto every StageContext this
	// orchestrator builds — see the matching StageContext field for what it
	// gates. Production wires a closure over repo.CapabilityRepo/GrantRepo
	// calling memory.Authorize at DI time (serverapp/di_pipeline.go); nil
	// disables the push the same way a nil InjectMemory does.
	AuthorizeMemory func(ctx context.Context, scope repo.Scope, routineID string) error

	// IssueTaskAPIKey is forwarded verbatim onto every StageContext this
	// orchestrator builds — see the matching StageContext field for what it
	// does. RevokeTaskAPIKeys is consumed directly by stageRunService
	// (stage_run_service.go) and by applyTransitionWrites' post-commit hooks
	// (transitions.go), not forwarded onto StageContext: revocation is not a
	// per-stage-handler concern. Production wires both to
	// mcp.StageKeyIssuer{Keys: apiKeyRepo}.Issue/Revoke at DI time
	// (serverapp/di_pipeline.go); nil disables issuance/revocation the same
	// way a nil AuthorizeMemory disables the memory push.
	IssueTaskAPIKey   func(ctx context.Context, stageRunID string, stageTimeout time.Duration) (string, error)
	RevokeTaskAPIKeys func(ctx context.Context, stageRunID string) error

	// ResolveApplications is forwarded verbatim onto every StageContext this
	// orchestrator builds — see the matching StageContext field for what it
	// does. nil disables MCP applications the same way a nil AuthorizeMemory
	// disables the memory push.
	ResolveApplications func(ctx context.Context, task *ent.Task) (mcpapps.RunApplications, error)
}

// StageFailedInfo carries failure metadata to the OnStageFailed callback.
type StageFailedInfo struct {
	StageRunID string
	Stage      string
	Iteration  int
	Error      string
}
