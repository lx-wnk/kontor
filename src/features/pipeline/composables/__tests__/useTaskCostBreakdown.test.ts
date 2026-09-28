import type { PipelineTask } from '@/types'
import { flushPromises } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { ref } from 'vue'
import { useTaskCostBreakdown } from '@/features/pipeline/composables/useTaskCostBreakdown'

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

describe('useTaskCostBreakdown', () => {
  it('loads the breakdown immediately for a set task', async () => {
    vi.mocked(fetch).mockResolvedValue({
      ok: true,
      json: async () => [{ stage: 'implementation', costCents: 120 }],
    } as Response)

    const task = ref<PipelineTask | null>(makeTask())
    const { costBreakdown, costLoading, costError } = useTaskCostBreakdown(task)
    await flushPromises()

    expect(fetch).toHaveBeenCalledWith('/api/tasks/task-1/cost-breakdown')
    expect(costBreakdown.value).toEqual([{ stage: 'implementation', costCents: 120 }])
    expect(costLoading.value).toBe(false)
    expect(costError.value).toBe('')
  })

  it('does not fetch when there is no task', async () => {
    const task = ref<PipelineTask | null>(null)
    useTaskCostBreakdown(task)
    await flushPromises()

    expect(fetch).not.toHaveBeenCalled()
  })

  it('sets a status-coded error message when the response is not ok', async () => {
    vi.mocked(fetch).mockResolvedValue({ ok: false, status: 503 } as Response)

    const task = ref<PipelineTask | null>(makeTask())
    const { costBreakdown, costError } = useTaskCostBreakdown(task)
    await flushPromises()

    expect(costError.value).toBe('Failed to load cost breakdown (503)')
    expect(costBreakdown.value).toEqual([])
  })

  it('sets a generic error message when fetch rejects (network failure)', async () => {
    vi.mocked(fetch).mockRejectedValue(new Error('offline'))

    const task = ref<PipelineTask | null>(makeTask())
    const { costError } = useTaskCostBreakdown(task)
    await flushPromises()

    expect(costError.value).toBe('Failed to load cost breakdown')
  })

  it('reloads when the task switches to a different stage run (currentIteration changes)', async () => {
    vi.mocked(fetch).mockResolvedValue({ ok: true, json: async () => [] } as Response)

    const task = ref<PipelineTask | null>(makeTask({ currentIteration: 1 }))
    useTaskCostBreakdown(task)
    await flushPromises()
    expect(fetch).toHaveBeenCalledTimes(1)

    task.value = makeTask({ currentIteration: 2 })
    await flushPromises()

    expect(fetch).toHaveBeenCalledTimes(2)
  })

  it('clears a previous error and reflects fresh data on the next successful load', async () => {
    vi.mocked(fetch).mockResolvedValueOnce({ ok: false, status: 500 } as Response)

    const task = ref<PipelineTask | null>(makeTask({ id: 'task-1' }))
    const { costBreakdown, costError } = useTaskCostBreakdown(task)
    await flushPromises()
    expect(costError.value).toContain('500')

    vi.mocked(fetch).mockResolvedValueOnce({ ok: true, json: async () => [{ stage: 'qa', costCents: 5 }] } as Response)
    task.value = makeTask({ id: 'task-2' })
    await flushPromises()

    expect(costError.value).toBe('')
    expect(costBreakdown.value).toEqual([{ stage: 'qa', costCents: 5 }])
  })
})
