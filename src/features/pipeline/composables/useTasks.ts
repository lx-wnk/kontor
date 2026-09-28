import type { Checkpoint } from '@/composables/useCheckpoints'
import type { PermissionRequest, PipelineStage, PipelineTask, StageRun, TaskDependency, TaskFeedback, TaskPermission } from '@/types'
import { computed, onUnmounted, ref, shallowRef } from 'vue'
import { emitCheckpointAdded } from '@/composables/useCheckpoints'
import { runAction } from '@/composables/useRunAction'
import { createSseResource } from '@/composables/useSseResource'
import { errorMessage } from '@/utils/errorMessage'

const tasks = shallowRef<PipelineTask[]>([])
// Routine-owned jobs never appear on the pipeline board — filter once here so
// every stage grouping (tasksByStage, tasksByStageMap) inherits the exclusion.
const pipelineTasks = computed(() => tasks.value.filter(t => t.kind !== 'job'))
const selectedTask = ref<PipelineTask | null>(null)
const isLoading = ref(true)
const error = ref<string | null>(null)

export interface TaskEvent {
  type: 'task_created' | 'task_updated' | 'task_deleted' | 'stage_run_updated' | 'permission_request' | 'checkpoint_added'
    | 'schedule_changed' | 'applications_changed' | 'eval_drift'
  taskId: string
  payload?: unknown
}

// Gap applied when a card is dropped at a column edge (no neighbor on one side).
// Mirrors rankGap in server/internal/db/repo/task_repo.go.
const RANK_GAP = 1 << 20

/**
 * Effective sort key for a task: its manual drag rank, falling back to creation
 * time (microseconds) so unranked rows still order deterministically. Mirrors
 * effectiveRank() on the server.
 */
export function effectiveRank(t: PipelineTask): number {
  return t.rank ?? new Date(t.createdAt).getTime() * 1000
}

export function byRank(a: PipelineTask, b: PipelineTask): number {
  return effectiveRank(a) - effectiveRank(b)
}

export function byActivityDesc(a: PipelineTask, b: PipelineTask): number {
  return (new Date(b.updatedAt).getTime() || 0) - (new Date(a.updatedAt).getTime() || 0)
}

/**
 * Reposition a task between two neighbors via drag-and-drop. Applies the
 * server-computed midpoint optimistically (so the card stays put on drop) and
 * rolls back to the previous rank if the request fails. beforeId/afterId are the
 * cards immediately above/below the drop target; null at a column edge.
 */
export async function reorderTask(
  taskId: string,
  beforeId: string | null,
  afterId: string | null,
): Promise<void> {
  const moved = tasks.value.find(t => t.id === taskId)
  if (!moved)
    return
  const before = beforeId ? tasks.value.find(t => t.id === beforeId) : undefined
  const after = afterId ? tasks.value.find(t => t.id === afterId) : undefined

  const prevRank = moved.rank
  moved.rank = midpointRank(before, after)
  tasks.value = [...tasks.value]

  try {
    const res = await fetch(`/api/tasks/${taskId}/rank`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ before: beforeId ?? '', after: afterId ?? '' }),
    })
    if (!res.ok) {
      const err = await res.json().catch(() => ({ error: `HTTP ${res.status}` }))
      throw new Error(err.error || 'Failed to reorder task')
    }
    const updated = await res.json() as PipelineTask
    moved.rank = updated.rank
    tasks.value = [...tasks.value]
  }
  catch (err) {
    moved.rank = prevRank
    tasks.value = [...tasks.value]
    throw err
  }
}

function midpointRank(before?: PipelineTask, after?: PipelineTask): number {
  if (before && after)
    return (effectiveRank(before) + effectiveRank(after)) / 2
  if (before)
    return effectiveRank(before) + RANK_GAP
  if (after)
    return effectiveRank(after) - RANK_GAP
  return Date.now() * 1000
}

async function fetchTasks() {
  try {
    const res = await fetch('/api/tasks?kind=all')
    if (!res.ok)
      throw new Error(`HTTP ${res.status}`)
    tasks.value = await res.json() as PipelineTask[]
    isLoading.value = false
    error.value = null
  }
  catch (err) {
    error.value = errorMessage(err)
    isLoading.value = false
  }
}

export async function refreshTask(taskId: string): Promise<void> {
  const res = await fetch(`/api/tasks/${taskId}`)
  if (!res.ok)
    return
  const task = await res.json() as PipelineTask
  if (!task?.id)
    return
  const exists = tasks.value.some(t => t.id === task.id)
  if (exists) {
    tasks.value = tasks.value.map(t => t.id === task.id ? task : t)
  }
  else {
    tasks.value = [task, ...tasks.value]
  }
  if (selectedTask.value?.id === task.id)
    selectedTask.value = task
}

export async function findOrFetchTask(taskId: string): Promise<PipelineTask | null> {
  const cached = tasks.value.find(t => t.id === taskId)
  if (cached)
    return cached
  await refreshTask(taskId)
  return tasks.value.find(t => t.id === taskId) ?? null
}

function handleSseMessage(data: string) {
  try {
    const event: TaskEvent = JSON.parse(data)
    applyEvent(event)
  }
  catch {
    // ignore malformed messages
  }
}

const sse = createSseResource({
  streamUrl: '/api/tasks/stream',
  fetchInitial: fetchTasks,
  onMessage: handleSseMessage,
})

export function applyEvent(event: TaskEvent) {
  switch (event.type) {
    case 'task_created': {
      const task = event.payload as PipelineTask
      if (task && !tasks.value.some(t => t.id === task.id))
        tasks.value = [task, ...tasks.value]
      break
    }
    case 'task_updated': {
      const task = event.payload as PipelineTask
      if (task) {
        tasks.value = tasks.value.map(t => t.id === task.id ? task : t)
        if (selectedTask.value?.id === task.id)
          selectedTask.value = task
      }
      break
    }
    case 'task_deleted': {
      tasks.value = tasks.value.filter(t => t.id !== event.taskId)
      if (selectedTask.value?.id === event.taskId)
        selectedTask.value = null
      break
    }
    case 'stage_run_updated':
    case 'permission_request': {
      // Server now includes the enriched task as payload (F-PERF-013). Apply
      // it directly when present to avoid a round-trip refetch. Fall back to
      // refetch for legacy events that carry no payload (e.g. in-flight during
      // a rolling restart).
      const task = event.payload as PipelineTask | undefined
      if (task?.id) {
        tasks.value = tasks.value.map(t => t.id === task.id ? task : t)
        if (selectedTask.value?.id === task.id)
          selectedTask.value = task
      }
      else {
        void refreshTask(event.taskId)
      }
      break
    }
    case 'checkpoint_added': {
      emitCheckpointAdded(event.payload as Checkpoint)
      break
    }
    // Shared stream: useSchedules, useApplications and useEvalMetrics own these.
    case 'schedule_changed':
    case 'applications_changed':
    case 'eval_drift':
      break
    default:
      console.warn('[useTasks] unknown SSE event type:', event.type)
  }
}

export interface CreateTaskInput {
  slug: string
  title: string
  description?: string
  cwd: string
  sourceBranch?: string
  targetBranch?: string
  useWorktree?: boolean
  maxIterations?: number
  tokenBudget?: number
  parentTaskId?: string
  silverBullet?: boolean
  priority?: 'high' | 'medium' | 'low'
  stage?: string
  template?: string
  projectId?: string
  spawnerId?: string
  autonomy?: 'manual' | 'spec_gated' | 'full'
}

export async function createTask(input: CreateTaskInput): Promise<PipelineTask> {
  const res = await fetch('/api/tasks', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({
      slug: input.slug,
      title: input.title,
      description: input.description,
      cwd: input.cwd,
      sourceBranch: input.sourceBranch,
      targetBranch: input.targetBranch,
      useWorktree: input.useWorktree,
      maxIterations: input.maxIterations,
      tokenBudget: input.tokenBudget,
      parentTaskId: input.parentTaskId,
      silverBullet: input.silverBullet,
      priority: input.priority,
      stage: input.stage,
      template: input.template,
      projectId: input.projectId,
      spawnerId: input.spawnerId,
      autonomy: input.autonomy,
    }),
  })
  if (!res.ok) {
    const err = await res.json().catch(() => ({ error: `HTTP ${res.status}` }))
    throw new Error(err.error || 'Failed to create task')
  }
  return await res.json() as PipelineTask
}

export async function progressTask(taskId: string): Promise<void> {
  const res = await fetch(`/api/tasks/${taskId}/progress`, { method: 'POST' })
  if (!res.ok) {
    const err = await res.json().catch(() => ({ error: `HTTP ${res.status}` }))
    throw new Error(err.error || 'Failed to progress task')
  }
}

// Endpoint defined in ACTION_META (useRunAction.ts — canonical source).
export async function cancelTask(taskId: string): Promise<void> {
  return runAction(taskId, 'cancel')
}

/**
 * Re-queue the task's current stage after a failed stage_run. Returns 202
 * Accepted — the stage_run is pending and will start when a runner slot is
 * free. Only valid when latestStageRunStatus === 'failed'.
 * Endpoint defined in ACTION_META (useRunAction.ts — canonical source).
 */
export async function retryTask(taskId: string, additionalPrompt?: string): Promise<void> {
  return runAction(taskId, 'retry', additionalPrompt?.trim() ? { additionalPrompt: additionalPrompt.trim() } : undefined)
}

/**
 * Re-queue a resume of the task's last stage_run (continues the agent's
 * previous Claude session via `--resume`). Returns 202 Accepted — the
 * stage_run is pending and will start when a runner slot is free.
 */
export async function resumeStageTask(taskId: string, additionalPrompt?: string): Promise<void> {
  const res = await fetch(`/api/tasks/${taskId}/resume-stage`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ additionalPrompt: additionalPrompt?.trim() || undefined }),
  })
  if (!res.ok) {
    const err = await res.json().catch(() => ({ error: `HTTP ${res.status}` }))
    throw new Error(err.error || 'Failed to resume task')
  }
}

/**
 * Spawn an independent analysis side-session for the task. The session
 * is a normal `claude` CLI process launched in the task's worktree with
 * a rich prompt containing task identity + failure details + pointers to
 * the last session JSONLs. Returns the PID so the UI can link to it in
 * the agent-monitoring view.
 */
export async function analyzeTask(taskId: string): Promise<{ pid: number, cwd: string }> {
  const res = await fetch(`/api/tasks/${taskId}/analyze`, { method: 'POST' })
  if (!res.ok) {
    const err = await res.json().catch(() => ({ error: `HTTP ${res.status}` }))
    throw new Error(err.error || 'Failed to start analysis session')
  }
  return await res.json() as { pid: number, cwd: string }
}

export async function fetchTaskFeedback(taskId: string): Promise<TaskFeedback[]> {
  const res = await fetch(`/api/tasks/${taskId}/feedback`)
  if (!res.ok)
    return []
  return await res.json() as TaskFeedback[]
}

export async function fetchStageRuns(taskId: string): Promise<StageRun[]> {
  const res = await fetch(`/api/tasks/${taskId}/stage-runs`)
  if (!res.ok)
    return []
  return await res.json() as StageRun[]
}

export async function fetchStageRunAgentOutput(taskId: string, runId: string): Promise<string | null> {
  const res = await fetch(`/api/tasks/${taskId}/stage-runs/${runId}/agent-output`)
  if (!res.ok)
    return null
  const data = await res.json() as { text: string | null }
  return data.text
}

export async function fetchTaskPermissions(taskId: string): Promise<TaskPermission[]> {
  const res = await fetch(`/api/tasks/${taskId}/permissions`)
  if (!res.ok)
    return []
  return await res.json() as TaskPermission[]
}

export async function grantTaskPermission(
  taskId: string,
  tool: string,
  pattern: string | null,
): Promise<TaskPermission> {
  const res = await fetch(`/api/tasks/${taskId}/permissions`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ tool, pattern, granted: true }),
  })
  if (!res.ok) {
    const err = await res.json().catch(() => ({ error: `HTTP ${res.status}` }))
    throw new Error(err.error || 'Failed to grant permission')
  }
  return await res.json() as TaskPermission
}

export async function fetchPendingPermissionRequests(taskId: string): Promise<PermissionRequest[]> {
  const res = await fetch(`/api/tasks/${taskId}/permission-requests`)
  if (!res.ok)
    return []
  return await res.json() as PermissionRequest[]
}

export type PermissionDecision = 'allow_once' | 'allow_routine' | 'deny_routine' | 'deny_once'

export async function resolvePermissionRequest(taskId: string, requestId: string, decision: PermissionDecision): Promise<void> {
  const res = await fetch(`/api/tasks/${taskId}/permission-requests/${requestId}/resolve`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ decision }),
  })
  if (!res.ok) {
    const err = await res.json().catch(() => ({ error: `HTTP ${res.status}` }))
    throw new Error(err.error || 'Failed to resolve')
  }
}

export interface BulkResolveResponse {
  resolved: number
  errors: string[]
}

export async function bulkResolvePermissionRequests(
  taskId: string,
  permissionIds: string[],
  decision: PermissionDecision,
  remember = false,
): Promise<BulkResolveResponse> {
  const res = await fetch(`/api/permission-requests/bulk-resolve`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ taskId, decision, permissionIds, remember }),
  })
  if (!res.ok) {
    const err = await res.json().catch(() => ({ error: `HTTP ${res.status}` }))
    throw new Error(err.error || 'Failed to bulk-resolve')
  }
  return await res.json() as BulkResolveResponse
}

export async function fetchDependencies(taskId: string): Promise<TaskDependency[]> {
  const res = await fetch(`/api/tasks/${taskId}/dependencies`)
  if (!res.ok)
    throw new Error(await res.text())
  return res.json() as Promise<TaskDependency[]>
}

export async function fetchDependents(taskId: string): Promise<TaskDependency[]> {
  const res = await fetch(`/api/tasks/${taskId}/dependents`)
  if (!res.ok)
    throw new Error(await res.text())
  return res.json() as Promise<TaskDependency[]>
}

export async function addTaskDependency(
  taskId: string,
  dependsOnId: string,
  requiredStage: 'done' | 'cancelled' = 'done',
  onCancelAction: 'cancel' | 'start' | 'on_hold' = 'on_hold',
): Promise<TaskDependency> {
  const res = await fetch(`/api/tasks/${taskId}/dependencies`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ dependsOnId, requiredStage, onCancelAction }),
  })
  if (!res.ok)
    throw new Error(await res.text())
  return res.json() as Promise<TaskDependency>
}

export async function removeTaskDependency(taskId: string, depId: string): Promise<void> {
  const res = await fetch(`/api/tasks/${taskId}/dependencies/${depId}`, { method: 'DELETE' })
  if (!res.ok)
    throw new Error(await res.text())
}

export function useTasks(options?: { autoStart?: boolean }) {
  const owns = options?.autoStart !== false
  if (owns)
    sse.startStream()
  // Only a caller that started the stream may stop it. A consumer passing
  // autoStart: false never incremented the shared subscriber count, so an
  // unconditional teardown would decrement a count it never raised and, at
  // zero, close the connection for every other consumer.
  if (owns)
    onUnmounted(sse.stopStream)

  function selectTask(task: PipelineTask | null) {
    selectedTask.value = task
  }

  function tasksByStage(stage: PipelineStage): PipelineTask[] {
    return pipelineTasks.value.filter(t => t.currentStage === stage).sort(byRank)
  }

  const tasksByStageMap = computed(() => {
    const map: Partial<Record<PipelineStage, PipelineTask[]>> = {}
    for (const task of pipelineTasks.value) {
      if (!map[task.currentStage])
        map[task.currentStage] = []
      map[task.currentStage]!.push(task)
    }
    for (const stage of Object.keys(map) as PipelineStage[])
      map[stage]!.sort(byRank)
    return map
  })

  return {
    tasks,
    pipelineTasks,
    selectedTask,
    isLoading,
    error,
    tasksByStage,
    tasksByStageMap,
    selectTask,
    refetch: fetchTasks,
    startStream: sse.startStream,
  }
}
