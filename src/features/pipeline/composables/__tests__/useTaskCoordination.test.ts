import type { PipelineTask } from '@/types'
import { flushPromises } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { ref } from 'vue'
import { useTaskCoordination } from '@/features/pipeline/composables/useTaskCoordination'

function makeTask(overrides: Partial<PipelineTask> = {}): PipelineTask {
  return {
    id: 'task-1',
    slug: 'my-task',
    title: 'Task',
    description: null,
    cwd: '/repo',
    worktreePath: null,
    sourceBranch: null,
    targetBranch: null,
    currentStage: 'implementation',
    parentTaskId: null,
    maxIterations: 10,
    tokenBudget: null,
    costBudgetCents: null,
    createdAt: '2026-01-01T00:00:00Z',
    updatedAt: '2026-01-01T00:00:00Z',
    metadata: null,
    silverBullet: false,
    planMode: false,
    priority: 'medium',
    userId: null,
    ...overrides,
  }
}

beforeEach(() => {
  vi.stubGlobal('fetch', vi.fn())
})

afterEach(() => {
  vi.unstubAllGlobals()
})

describe('useTaskCoordination — namespace resolution', () => {
  it('uses the task id as namespace when parentTaskId is null', async () => {
    vi.mocked(fetch).mockResolvedValue({ ok: true, json: async () => ({ entries: [], locks: [] }) } as Response)

    const task = ref<PipelineTask | null>(makeTask({ id: 'task-1', parentTaskId: null }))
    useTaskCoordination(task)
    await flushPromises()

    expect(fetch).toHaveBeenCalledWith('/api/coord/task-1/scratchpads')
    expect(fetch).toHaveBeenCalledWith('/api/coord/task-1/locks')
  })

  it('uses parentTaskId as namespace when present', async () => {
    vi.mocked(fetch).mockResolvedValue({ ok: true, json: async () => ({ entries: [], locks: [] }) } as Response)

    const task = ref<PipelineTask | null>(makeTask({ id: 'task-1', parentTaskId: 'parent-1' }))
    useTaskCoordination(task)
    await flushPromises()

    expect(fetch).toHaveBeenCalledWith('/api/coord/parent-1/scratchpads')
    expect(fetch).toHaveBeenCalledWith('/api/coord/parent-1/locks')
  })

  it('does not fetch when there is no task', async () => {
    const task = ref<PipelineTask | null>(null)
    useTaskCoordination(task)
    await flushPromises()

    expect(fetch).not.toHaveBeenCalled()
  })

  it('reloads when the namespace changes for the same task reference', async () => {
    vi.mocked(fetch).mockResolvedValue({ ok: true, json: async () => ({ entries: [], locks: [] }) } as Response)

    const task = ref<PipelineTask | null>(makeTask({ id: 'task-1', parentTaskId: null }))
    useTaskCoordination(task)
    await flushPromises()
    expect(fetch).toHaveBeenCalledTimes(2)

    task.value = makeTask({ id: 'task-1', parentTaskId: 'parent-9' })
    await flushPromises()

    expect(fetch).toHaveBeenCalledTimes(4)
    expect(fetch).toHaveBeenCalledWith('/api/coord/parent-9/scratchpads')
  })
})

describe('useTaskCoordination — load results', () => {
  it('populates scratchpads and locks from a successful load', async () => {
    vi.mocked(fetch)
      .mockResolvedValueOnce({
        ok: true,
        json: async () => ({ entries: [{ namespace: 'task-1', key: 'progress', value: 'step-3', updated_at: 't', updated_by_task_id: 'x' }] }),
      } as Response)
      .mockResolvedValueOnce({
        ok: true,
        json: async () => ({ locks: [{ namespace: 'task-1', key: 'lock-a', owner_task_id: 'x', acquired_at: 't', expires_at: 't2' }] }),
      } as Response)

    const task = ref<PipelineTask | null>(makeTask())
    const { scratchpads, locks, loading, error } = useTaskCoordination(task)
    await flushPromises()

    expect(scratchpads.value).toHaveLength(1)
    expect(locks.value).toHaveLength(1)
    expect(loading.value).toBe(false)
    expect(error.value).toBe('')
  })

  it('defaults entries/locks to an empty array when the payload omits them', async () => {
    vi.mocked(fetch).mockResolvedValue({ ok: true, json: async () => ({}) } as Response)

    const task = ref<PipelineTask | null>(makeTask())
    const { scratchpads, locks } = useTaskCoordination(task)
    await flushPromises()

    expect(scratchpads.value).toEqual([])
    expect(locks.value).toEqual([])
  })

  it('surfaces a status-coded error when either request fails', async () => {
    vi.mocked(fetch)
      .mockResolvedValueOnce({ ok: false, status: 404 } as Response)
      .mockResolvedValueOnce({ ok: true, json: async () => ({ locks: [] }) } as Response)

    const task = ref<PipelineTask | null>(makeTask())
    const { error, scratchpads } = useTaskCoordination(task)
    await flushPromises()

    expect(error.value).toBe('Failed to load coordination data (404)')
    expect(scratchpads.value).toEqual([])
  })

  it('surfaces a generic error when the fetch call rejects', async () => {
    vi.mocked(fetch).mockRejectedValue(new Error('offline'))

    const task = ref<PipelineTask | null>(makeTask())
    const { error } = useTaskCoordination(task)
    await flushPromises()

    expect(error.value).toBe('Failed to load coordination data')
  })
})
