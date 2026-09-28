import type {
  Agent as _AgentBase,
  SessionMeta as _SessionMetaBase,
  SubAgent as _SubAgentBase,
  TaskInfo as _TaskInfoBase,
  AgentStatus,
  HookEvent,
  PendingPermission,
  TokenUsage,
} from './sdk.generated'
import type { MetricKey } from './utils/evalMetrics'
// Types generated from sdk/types.go via tygo — do not edit these directly.
// Run `task generate` to regenerate after changing sdk/types.go.
import {
  AgentStatusActive,
  AgentStatusFinished,
  AgentStatusIdle,
  AgentStatusWaiting,
} from './sdk.generated'

export type { AgentStatus, HookEvent, PendingPermission, TokenUsage }

export interface SessionMeta extends Omit<_SessionMetaBase, 'firstPrompt'> {
  firstPrompt: string | null
}

export interface SubAgent extends Omit<_SubAgentBase, 'status' | 'currentAction'> {
  status: 'active' | 'completed'
  currentAction: string | null
}

export interface TaskInfo extends Omit<_TaskInfoBase, 'status'> {
  status: 'pending' | 'in_progress' | 'completed'
}

// Agent re-exported from sdk.generated with narrowed tasks/subagents/meta types.
// The generated base has tasks: TaskInfo[] and subagents: SubAgent[] using the broad
// generated types; here we override them with the narrower local types.
export type Agent = Omit<_AgentBase, 'tasks' | 'subagents' | 'meta'> & {
  tasks: TaskInfo[]
  subagents: SubAgent[]
  meta: SessionMeta | null
}

// Derived from sdk.generated consts — automatically stays in sync with sdk/types.go.
export const AGENT_STATUSES = [AgentStatusActive, AgentStatusWaiting, AgentStatusIdle, AgentStatusFinished] as const

// Mirrors sdk.SpawnerSourceTask / sdk.SpawnerSourceEnv. Those are untyped Go
// consts, so tygo emits `spawnerSource?: string` and no constant — the parity
// is hand-held and this is the one place the client names it.
export const SPAWNER_SOURCE_TASK = 'task'
export const SPAWNER_SOURCE_ENV = 'env'
export type SpawnerSource = typeof SPAWNER_SOURCE_TASK | typeof SPAWNER_SOURCE_ENV

export interface ChannelReply {
  message: string
  timestamp: string
}

export interface GitStatusLastCommit {
  hash: string
  shortHash: string
  message: string
  author: string
  date: string
}

export interface GitStatus {
  branch: string
  aheadCount: number
  behindCount: number
  staged: string[]
  unstaged: string[]
  untracked: string[]
  lastCommit: GitStatusLastCommit | null
  remoteUrl: string | null
}

export interface OutputMessage {
  role: 'assistant' | 'tool_call' | 'tool_result' | 'human' | 'channel_reply' | 'task' | 'subagent'
  content: string
  timestamp?: string
  toolName?: string
  filePath?: string
  taskStatus?: 'pending' | 'in_progress' | 'completed'
  taskId?: string
  subagentType?: string
  queued?: boolean
}

// Task Pipeline Types
export type PipelineStage
  = | 'backlog'
    | 'ready'
    | 'plan_review'
    | 'implementation'
    | 'self_review'
    | 'finalization'
    | 'done'
    | 'on_hold'
    | 'cancelled'

// `failed` is NOT a pipeline stage — it lives only on stage_run.status.
// When a stage run fails, the task stays on its current stage; the UI
// derives "needs user" from latestStageRunStatus === 'failed' and offers
// Retry / Analyze actions via the task modal.

export type StageRunStatus
  = | 'pending'
    | 'running'
    | 'awaiting_user'
    | 'on_hold'
    | 'done'
    | 'failed'
    | 'requeued'

// Mirrors of server-side contracts, kept in parity by hand (Go cannot import TS):
// autonomy is server/internal/taskcontrol/autonomy.go's ValidAutonomyValues,
// priority is the set the task write path and scheduler accept. The dropdown
// lists in utils/taskOptions.ts are checked against these with `satisfies`, so
// no list can offer a value the server would reject. The reverse is deliberately
// not enforced: a list may cover fewer values than the union, which is what
// retiring an option from the UI looks like while the server still accepts it.
export type TaskPriority = 'high' | 'medium' | 'low'
export type TaskAutonomy = 'manual' | 'spec_gated' | 'full'

export interface TaskDependency {
  id: string
  taskId: string
  taskTitle: string
  dependsOnId: string
  dependsOnTitle: string
  dependsOnStage: PipelineStage
  requiredStage: 'done' | 'cancelled'
  onCancelAction: 'cancel' | 'start' | 'on_hold'
}

export interface PipelineTask {
  id: string
  slug: string
  title: string
  description: string | null
  cwd: string
  worktreePath: string | null
  sourceBranch: string | null
  targetBranch: string | null
  currentStage: PipelineStage
  parentTaskId: string | null
  maxIterations: number
  tokenBudget: number | null
  costBudgetCents: number | null
  stageTimeoutSeconds: number
  createdAt: string
  updatedAt: string
  metadata: Record<string, unknown> | null
  /**
   * Jump-the-queue flag — silver-bullet tasks win the picker against all
   *  other ordering criteria. Set at creation, editable later.
   */
  silverBullet: boolean
  planMode: boolean
  /** Soft priority used after silver-bullet and stage-furthest-first. */
  priority: TaskPriority
  /**
   * Manual drag-and-drop order within a stage column. Gap-based float; lower
   * sorts first. Tiebreaker between priority and createdAt in runner pickup.
   * Seeded from createdAt on the server, so it is effectively always present.
   */
  rank?: number | null
  // Owning user (multi-user mode). Null for legacy/system tasks created
  // before multi-user was introduced; only admins see those.
  userId: string | null
  // Computed at read time by the API — not stored in DB.
  // True when the latest stage_run is paused waiting for user input,
  // regardless of what currentStage is.
  needsUser?: boolean
  latestStageRunStatus?: StageRunStatus | null
  autoRetryCount?: number | null
  nextRetryAt?: string | null
  refineStatus?: 'none' | 'refining' | 'draft_ready' | 'failed' | null
  refineError?: string | null
  currentIteration?: number
  // Session id of the most relevant stage_run (running > most recent with session).
  // Used by the task modal to mount a live "follow along" pane against the
  // same JSONL transcript + channel the session is streaming to.
  activeSessionId?: string | null
  // PID of the stage_run when it is currently running. Null between runs.
  activePid?: number | null
  // True when this task is blocked by unfulfilled dependencies.
  isBlocked?: boolean
  // True when blocked AND every blocking prereq is terminal (done/cancelled)
  // but reached the wrong stage — dependency can never be satisfied.
  isUnsatisfiable?: boolean
  // True when the latest stage_run on the current stage is terminal
  // (done/failed) OR a zombie awaiting_user (dead PID), AND it still has
  // unresolved permission_requests. The orchestrator's lingering-pending
  // gate refuses to spawn a new run while this is true; surface in UI so
  // the user sees WHY their task is parked.
  blockedByPendingPermissions?: boolean
  // True whenever the task has unresolved permission_requests and is not
  // cancelled — regardless of whether the agent that asked is still alive.
  // The agent files its request as it parks on awaiting_user and its process
  // usually dies seconds later, so blockedByPendingPermissions alone misses
  // the window in between. The orchestrator is gated on that narrower flag,
  // not on this one; this is the "someone owes an answer" signal.
  hasPendingPermissions?: boolean
  // Project and spawner associations (Projects/Folders/Spawners feature).
  projectId?: string | null
  spawnerId?: string | null
  // Distinguishes routine-owned background jobs from pipeline tasks. Jobs are
  // excluded from board/stage groupings but still flow through needs-you.
  kind?: 'pipeline' | 'job'
  routineId?: string | null
  autonomy?: TaskAutonomy
  availableActions?: AvailableAction[]
  childCount?: number
  activeChildCount?: number
  activeChild?: {
    tokensUsed: number
    costCents: number
    durationSeconds: number
    currentStage: string
    latestOutput: string
  } | null
}

export interface AvailableAction {
  action: string
  enabled: boolean
  reason?: string
  primary: boolean
}

export interface StageRun {
  id: string
  taskId: string
  stage: PipelineStage
  sessionId: string | null
  sessionName: string | null
  pid: number | null
  status: StageRunStatus
  startedAt: string | null
  endedAt: string | null
  iteration: number
  output: Record<string, unknown> | null
  tokensUsed: number
  costCents: number
  // Timestamp of the most recent user resolution of a permission_request
  // tied to this run. Used by `sweepAwaitingUserRuns` to anchor the
  // wallclock budget to user activity rather than spawn time, so a slow-
  // responding user does not get the agent killed at the 4h timeout.
  // Null until at least one permission has been resolved on this run.
  lastGrantAt: string | null
}

export interface TaskPermission {
  id: string
  taskId: string
  tool: string
  pattern: string | null
  granted: boolean
  requestedAt: string
  decidedAt: string | null
  /** Identity of whoever decided the grant (JWT sub or API key id); null = no decision recorded. */
  decidedBy: string | null
  /** ISO timestamp; null = never expires. */
  expiresAt: string | null
}

export interface PermissionRequest {
  id: string
  stageRunId: string
  tool: string
  pattern: string | null
  reason: string | null
  requestedAt: string
  resolvedAt: string | null
  outcome: 'granted' | 'denied' | 'timeout' | null
  /** Total requests for this (tool, pattern) across all stage_runs of this task. Computed at read time. */
  reRequestCount?: number
  /** True when the Bash pattern is outside the server's safe allow-list; granting is a conscious human override. */
  outsideSafeList?: boolean
  /** True when a grant already denies this capability in the task's contexts. An answer here would not reach the agent; the grant has to change instead. */
  deniedByDefault?: boolean
}

// `FeedbackStage` is the subset of stages on which user-authored feedback
// can be recorded — currently the two approval-gated artifact stages.
// Distinct from `PipelineStage` so the type matches the `task_feedback.stage`
// CHECK constraint exactly: any caller passing a non-feedback PipelineStage
// is rejected at compile time instead of failing the CHECK at INSERT time.
export type FeedbackStage = 'planning' | 'implementation_plan'

export interface TaskFeedback {
  id: string
  taskId: string
  stage: FeedbackStage
  stageRunId: string | null
  iteration: number
  feedback: string
  createdAt: string
  resolvedAt: string | null
  resolvedByStageRunId: string | null
}

export interface AuditEntry {
  id: string
  taskId: string | null
  userId: string | null
  /** Derived server-side: the metadata actor tag, else 'user' when a user id is present, else 'system'. */
  actor: string
  action: string
  target: string
  timestamp: string
  details: Record<string, unknown> | null
}

export type NotificationEventType
  = | 'on_hold'
    | 'approval_needed'
    | 'completed'
    | 'failed'
    | 'budget_exceeded'
    | 'iteration_warning'

export type NotificationChannel = 'browser' | 'email' | 'system' | 'webpush' | 'webhook'

export interface NotificationPreference {
  eventType: NotificationEventType
  channels: NotificationChannel[]
  enabled: boolean
}

// Projects, Folders & Spawners
export interface Project {
  id: string
  slug: string
  name: string
  description?: string
  color?: string
  defaultSpawnerId?: string | null
  setupCommand?: string | null
  folderCount?: number
  folders?: ProjectFolder[]
  createdAt: string
  updatedAt: string
}

export interface ProjectFolder {
  id: string
  projectId: string
  path: string
  label?: string
  isDefault: boolean
  createdAt: string
}

export type SpawnerAdapterType = 'claude' | 'ollama' | 'openai' | 'custom' | 'acp'

export interface AdapterConfigKey {
  key: string
  type: string
  required: boolean
  note?: string
}

export interface AdapterMeta {
  name: SpawnerAdapterType
  description: string
  configKeys: AdapterConfigKey[]
}

export interface Spawner {
  id: string
  name: string
  slug: string
  command: string
  args: string[]
  env: Record<string, string>
  adapterType: SpawnerAdapterType
  adapterConfig: Record<string, string>
  modelOverride?: string
  description?: string
  builtIn: boolean
  isDefault: boolean
  createdAt: string
  updatedAt: string
}

// Eval / drift-detection types.
// MetricKey is derived from the canonical key list in utils/evalMetrics.
export type { MetricKey } from './utils/evalMetrics'

export interface EvalMetricSnapshot {
  id: string
  spawnerId: string
  model: string
  stage: string
  metricKey: MetricKey
  value: number
  sampleCount: number
  windowStart: string
  windowEnd: string
  recordedAt: string
}

export interface DriftAlert {
  id: string
  spawnerId: string
  model: string
  stage: string
  metricKey: MetricKey
  status: string
  direction: 'up' | 'down'
  baselineValue: number
  recentValue: number
  delta: number
  threshold: number
  sampleCount: number
  detectedAt: string
  acknowledgedAt: string | null
}

// MCP API Key types
export type McpScope = 'tasks:read' | 'tasks:write' | 'pipeline:control' | 'keys:manage'

export interface ApiKey {
  id: string
  name: string
  // key_hash is intentionally absent — never send the hash over the wire
  scopes: McpScope[]
  active: boolean
  userId: string | null
  createdAt: string
  lastUsedAt: string | null
}
