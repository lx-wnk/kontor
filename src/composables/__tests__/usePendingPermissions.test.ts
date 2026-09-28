import type { Agent, PermissionRequest, PipelineTask } from '@/types'
import { flushPromises } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { ref } from 'vue'
import { usePendingPermissions } from '../usePendingPermissions'

const { fetchPendingMock, bulkResolveMock } = vi.hoisted(() => ({
  fetchPendingMock: vi.fn(),
  bulkResolveMock: vi.fn(),
}))

vi.mock('@/features/pipeline/composables/useTasks', () => ({
  fetchPendingPermissionRequests: fetchPendingMock,
  bulkResolvePermissionRequests: bulkResolveMock,
}))

function makeTask(overrides: Partial<PipelineTask> = {}): PipelineTask {
  return {
    id: 'task-1',
    slug: 'my-task',
    title: 'Task',
    description: null,
    cwd: '/repo/my-project',
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

function makeRequest(overrides: Partial<PermissionRequest> = {}): PermissionRequest {
  return {
    id: 'req-1',
    stageRunId: 'run-1',
    tool: 'Bash',
    pattern: null,
    reason: null,
    requestedAt: '2026-01-01T00:00:00Z',
    resolvedAt: null,
    outcome: null,
    ...overrides,
  }
}

beforeEach(() => {
  fetchPendingMock.mockReset()
  bulkResolveMock.mockReset()
})

describe('usePendingPermissions', () => {
  it('fetches requests only for tasks blocked by pending permissions', async () => {
    fetchPendingMock.mockResolvedValue([makeRequest()])
    const tasks = ref<PipelineTask[]>([
      makeTask({ id: 'task-1', blockedByPendingPermissions: true }),
      makeTask({ id: 'task-2', blockedByPendingPermissions: false }),
    ])

    usePendingPermissions(tasks)
    await flushPromises()

    expect(fetchPendingMock).toHaveBeenCalledTimes(1)
    expect(fetchPendingMock).toHaveBeenCalledWith('task-1')
  })

  it('surfaces a task that owes a decision but is not yet blocked', async () => {
    fetchPendingMock.mockResolvedValue([makeRequest()])
    const tasks = ref<PipelineTask[]>([
      makeTask({ id: 'task-1', hasPendingPermissions: true, blockedByPendingPermissions: false }),
    ])
    const { items } = usePendingPermissions(tasks)
    await flushPromises()

    expect(fetchPendingMock).toHaveBeenCalledWith('task-1')
    expect(items.value.map(i => i.taskId)).toEqual(['task-1'])
  })

  it('ignores a task with neither flag set', async () => {
    fetchPendingMock.mockResolvedValue([makeRequest()])
    const tasks = ref<PipelineTask[]>([
      makeTask({ id: 'task-1', hasPendingPermissions: false, blockedByPendingPermissions: false }),
    ])
    const { items } = usePendingPermissions(tasks)
    await flushPromises()

    expect(fetchPendingMock).not.toHaveBeenCalled()
    expect(items.value).toEqual([])
  })

  it('derives projectName from the last cwd segment', async () => {
    fetchPendingMock.mockResolvedValue([makeRequest()])
    const tasks = ref<PipelineTask[]>([
      makeTask({ id: 'task-1', cwd: '/home/user/my-cool-project', blockedByPendingPermissions: true, title: 'Fix bug' }),
    ])

    const { items } = usePendingPermissions(tasks)
    await flushPromises()

    expect(items.value).toHaveLength(1)
    expect(items.value[0]).toMatchObject({ taskId: 'task-1', title: 'Fix bug', projectName: 'My Cool Project', routineId: null })
    expect(items.value[0].requests).toHaveLength(1)
  })

  it('takes projectName from the agent working the task, like its card', async () => {
    fetchPendingMock.mockResolvedValue([makeRequest()])
    const tasks = ref<PipelineTask[]>([
      makeTask({ id: 'task-1', cwd: '/home/user/agent-dashboard', blockedByPendingPermissions: true }),
    ])
    const agents = ref([{ pipelineTaskId: 'task-1', projectName: 'kontor' } as Agent])

    const { items } = usePendingPermissions(tasks, agents)
    await flushPromises()

    expect(items.value[0]).toMatchObject({ projectName: 'Kontor', cwd: '/home/user/agent-dashboard' })
  })

  it('carries the task routineId through to the item', async () => {
    fetchPendingMock.mockResolvedValue([makeRequest()])
    const tasks = ref<PipelineTask[]>([
      makeTask({ id: 'task-1', blockedByPendingPermissions: true, routineId: 'routine-1' }),
    ])

    const { items } = usePendingPermissions(tasks)
    await flushPromises()

    expect(items.value[0].routineId).toBe('routine-1')
  })

  it('excludes items whose cached requests are all already resolved', async () => {
    fetchPendingMock.mockResolvedValue([makeRequest({ outcome: 'granted' })])
    const tasks = ref<PipelineTask[]>([
      makeTask({ id: 'task-1', blockedByPendingPermissions: true }),
    ])

    const { items, totalRequests } = usePendingPermissions(tasks)
    await flushPromises()

    expect(items.value).toHaveLength(0)
    expect(totalRequests.value).toBe(0)
  })

  it('sums requests across multiple blocked tasks', async () => {
    fetchPendingMock.mockImplementation(async (taskId: string) =>
      taskId === 'task-1' ? [makeRequest({ id: 'a' }), makeRequest({ id: 'b' })] : [makeRequest({ id: 'c' })])
    const tasks = ref<PipelineTask[]>([
      makeTask({ id: 'task-1', blockedByPendingPermissions: true }),
      makeTask({ id: 'task-2', blockedByPendingPermissions: true }),
    ])

    const { totalRequests } = usePendingPermissions(tasks)
    await flushPromises()

    expect(totalRequests.value).toBe(3)
  })

  it('drops stale cache entries once a task is no longer blocked', async () => {
    fetchPendingMock.mockResolvedValue([makeRequest()])
    const tasks = ref<PipelineTask[]>([
      makeTask({ id: 'task-1', blockedByPendingPermissions: true }),
    ])

    const { items, refresh } = usePendingPermissions(tasks)
    await flushPromises()
    expect(items.value).toHaveLength(1)

    tasks.value = [makeTask({ id: 'task-1', blockedByPendingPermissions: false })]
    await refresh()

    expect(items.value).toHaveLength(0)
  })

  it('approve resolves the given ids and refreshes the cache for that task', async () => {
    fetchPendingMock
      .mockResolvedValueOnce([makeRequest({ id: 'req-1' })])
      .mockResolvedValueOnce([])
    bulkResolveMock.mockResolvedValue({ resolved: 1, errors: [] })
    const tasks = ref<PipelineTask[]>([
      makeTask({ id: 'task-1', blockedByPendingPermissions: true }),
    ])

    const { items, approve } = usePendingPermissions(tasks)
    await flushPromises()
    expect(items.value).toHaveLength(1)

    await approve('task-1', ['req-1'], true)

    expect(bulkResolveMock).toHaveBeenCalledWith('task-1', ['req-1'], 'allow_once', true)
    expect(items.value).toHaveLength(0)
  })

  it('deny resolves the given ids without remembering', async () => {
    fetchPendingMock
      .mockResolvedValueOnce([makeRequest({ id: 'req-1' })])
      .mockResolvedValueOnce([])
    bulkResolveMock.mockResolvedValue({ resolved: 1, errors: [] })
    const tasks = ref<PipelineTask[]>([
      makeTask({ id: 'task-1', blockedByPendingPermissions: true }),
    ])

    const { deny } = usePendingPermissions(tasks)
    await flushPromises()

    await deny('task-1', ['req-1'])

    expect(bulkResolveMock).toHaveBeenCalledWith('task-1', ['req-1'], 'deny_once', false)
  })

  it('does not issue duplicate fetches for a task already being fetched', async () => {
    let resolveFirst!: (v: PermissionRequest[]) => void
    fetchPendingMock.mockImplementation(() => new Promise((resolve) => {
      resolveFirst = resolve
    }))
    const tasks = ref<PipelineTask[]>([
      makeTask({ id: 'task-1', blockedByPendingPermissions: true }),
    ])

    const { refresh } = usePendingPermissions(tasks)
    // watch(immediate) already kicked off the first fetch; refresh() here
    // races a second call for the same task while it is still in flight.
    const second = refresh()
    resolveFirst([makeRequest()])
    await second
    await flushPromises()

    expect(fetchPendingMock).toHaveBeenCalledTimes(1)
  })
})
