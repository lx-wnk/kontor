import type { Ref } from 'vue'
import type { PermissionRequest, PipelineTask } from '../types'
import type { PermissionDecision } from '@/features/pipeline/composables/useTasks'
import { computed, ref, watch } from 'vue'
import { bulkResolvePermissionRequests, fetchPendingPermissionRequests } from '@/features/pipeline/composables/useTasks'
import { friendlyProjectName } from '../utils/friendlyProjectName'

export interface PermissionItem {
  taskId: string
  title: string
  projectName: string
  routineId: string | null
  requests: PermissionRequest[]
}

// Owes-an-answer and requests-are-stranded are separate server flags; the second
// implies the first, but only the first covers the agent that is still alive.
function awaitsDecision(task: PipelineTask): boolean {
  return Boolean(task.hasPendingPermissions || task.blockedByPendingPermissions)
}

function projectNameFromTask(task: PipelineTask): string {
  const lastSegment = task.cwd.split('/').filter(Boolean).pop() ?? task.cwd
  return friendlyProjectName(lastSegment)
}

export function usePendingPermissions(tasks: Ref<PipelineTask[]>) {
  // Map of taskId → fetched requests (only for tasks awaiting a decision)
  const cache = ref<Map<string, PermissionRequest[]>>(new Map())
  // Track which task IDs are currently being fetched to avoid duplicate requests
  const fetching = new Set<string>()

  async function fetchForTask(task: PipelineTask): Promise<void> {
    if (fetching.has(task.id))
      return
    fetching.add(task.id)
    try {
      const requests = await fetchPendingPermissionRequests(task.id)
      cache.value = new Map(cache.value).set(task.id, requests)
    }
    finally {
      fetching.delete(task.id)
    }
  }

  async function refresh(): Promise<void> {
    const blocked = tasks.value.filter(awaitsDecision)
    await Promise.all(blocked.map(fetchForTask))
    // Remove stale entries for tasks no longer blocked
    const blockedIds = new Set(blocked.map(t => t.id))
    const next = new Map(cache.value)
    for (const id of next.keys()) {
      if (!blockedIds.has(id))
        next.delete(id)
    }
    cache.value = next
  }

  watch(
    () => tasks.value.filter(awaitsDecision).map(t => t.id).join(','),
    () => { void refresh() },
    { immediate: true },
  )

  const items = computed<PermissionItem[]>(() => {
    const result: PermissionItem[] = []
    for (const task of tasks.value) {
      if (!awaitsDecision(task))
        continue
      const requests = (cache.value.get(task.id) ?? []).filter(r => r.outcome === null)
      if (requests.length === 0)
        continue
      result.push({
        taskId: task.id,
        title: task.title,
        projectName: projectNameFromTask(task),
        routineId: task.routineId ?? null,
        requests,
      })
    }
    return result
  })

  const totalRequests = computed(() => items.value.reduce((sum, item) => sum + item.requests.length, 0))

  async function decide(taskId: string, ids: string[], decision: PermissionDecision, remember = false): Promise<void> {
    await bulkResolvePermissionRequests(taskId, ids, decision, remember)
    const updated = await fetchPendingPermissionRequests(taskId)
    cache.value = new Map(cache.value).set(taskId, updated)
  }

  async function approve(taskId: string, ids: string[], remember: boolean): Promise<void> {
    await decide(taskId, ids, 'allow_once', remember)
  }

  async function deny(taskId: string, ids: string[]): Promise<void> {
    await decide(taskId, ids, 'deny_once')
  }

  return { items, totalRequests, approve, deny, decide, refresh }
}
