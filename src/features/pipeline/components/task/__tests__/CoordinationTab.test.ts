import { flushPromises, mount } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { ref } from 'vue'
import { toast } from '@/composables/useToast'
import CoordinationTab from '@/features/pipeline/components/task/CoordinationTab.vue'
import { TaskRefKey } from '@/features/pipeline/composables/taskModalContext'

function makeTask(overrides: Record<string, unknown> = {}) {
  return {
    id: 'task-abc',
    slug: 'my-task',
    title: 'Test Task',
    description: null,
    cwd: '/home/user',
    worktreePath: null,
    sourceBranch: null,
    targetBranch: null,
    currentStage: 'ready',
    currentIteration: 0,
    maxIterations: 10,
    tokenBudget: null,
    costBudgetCents: null,
    createdAt: new Date().toISOString(),
    updatedAt: new Date().toISOString(),
    metadata: null,
    silverBullet: false,
    priority: 'medium',
    userId: null,
    projectId: null,
    spawnerId: null,
    parentTaskId: null,
    activeSessionId: null,
    activePid: null,
    latestStageRunStatus: null,
    blockedByPendingPermissions: false,
    refineStatus: null,
    refineError: null,
    isBlocked: false,
    ...overrides,
  }
}

function mountTab(taskOverrides: Record<string, unknown> = {}) {
  const task = ref(makeTask(taskOverrides))
  return mount(CoordinationTab, {
    global: {
      provide: {
        [TaskRefKey as symbol]: task,
      },
    },
  })
}

describe('coordinationTab', () => {
  beforeEach(() => {
    vi.stubGlobal('fetch', vi.fn())
  })

  afterEach(() => {
    vi.unstubAllGlobals()
  })

  it('renders scratchpad key/value and updated_by after loading', async () => {
    vi.mocked(fetch)
      .mockResolvedValueOnce({
        ok: true,
        json: async () => ({
          entries: [
            {
              namespace: 'task-abc',
              key: 'progress',
              value: 'step-3',
              updated_at: '2026-06-25T10:00:00Z',
              updated_by_task_id: 'task-xyz',
            },
          ],
        }),
      } as Response)
      .mockResolvedValueOnce({
        ok: true,
        json: async () => ({
          locks: [
            {
              namespace: 'task-abc',
              key: 'deploy-lock',
              owner_task_id: 'task-xyz',
              acquired_at: '2026-06-25T09:00:00Z',
              expires_at: '2026-06-25T11:00:00Z',
            },
          ],
        }),
      } as Response)

    const wrapper = mountTab()
    await flushPromises()

    expect(wrapper.text()).toContain('progress')
    expect(wrapper.text()).toContain('step-3')
    expect(wrapper.text()).toContain('task-xyz')
  })

  it('renders lock key and owner after loading', async () => {
    vi.mocked(fetch)
      .mockResolvedValueOnce({
        ok: true,
        json: async () => ({ entries: [] }),
      } as Response)
      .mockResolvedValueOnce({
        ok: true,
        json: async () => ({
          locks: [
            {
              namespace: 'task-abc',
              key: 'deploy-lock',
              owner_task_id: 'task-owner',
              acquired_at: '2026-06-25T09:00:00Z',
              expires_at: '2026-06-25T11:00:00Z',
            },
          ],
        }),
      } as Response)

    const wrapper = mountTab()
    await flushPromises()

    expect(wrapper.text()).toContain('deploy-lock')
    expect(wrapper.text()).toContain('task-owner')
  })

  it('uses parentTaskId as namespace when present', async () => {
    vi.mocked(fetch)
      .mockResolvedValue({
        ok: true,
        json: async () => ({ entries: [], locks: [] }),
      } as Response)

    mountTab({ parentTaskId: 'parent-task-id' })
    await flushPromises()

    expect(vi.mocked(fetch)).toHaveBeenCalledWith(
      expect.stringContaining('parent-task-id'),
    )
  })

  it('uses task id as namespace when parentTaskId is null', async () => {
    vi.mocked(fetch)
      .mockResolvedValue({
        ok: true,
        json: async () => ({ entries: [], locks: [] }),
      } as Response)

    mountTab({ parentTaskId: null })
    await flushPromises()

    expect(vi.mocked(fetch)).toHaveBeenCalledWith(
      expect.stringContaining('task-abc'),
    )
  })

  it('has no write controls — no inputs or mutating buttons', async () => {
    vi.mocked(fetch)
      .mockResolvedValue({
        ok: true,
        json: async () => ({ entries: [], locks: [] }),
      } as Response)

    const wrapper = mountTab()
    await flushPromises()

    expect(wrapper.findAll('input')).toHaveLength(0)
    expect(wrapper.findAll('textarea')).toHaveLength(0)
    expect(wrapper.findAll('button')).toHaveLength(0)
  })

  it('shows loading state initially', () => {
    vi.mocked(fetch).mockReturnValue(new Promise(() => {}))
    const wrapper = mountTab()
    expect(wrapper.text()).toContain('Loading')
  })

  it('surfaces a toast when fetch fails', async () => {
    const errorSpy = vi.spyOn(toast, 'error')
    vi.mocked(fetch).mockRejectedValue(new Error('network error'))
    const wrapper = mountTab()
    await flushPromises()
    expect(errorSpy).toHaveBeenCalledWith(expect.stringContaining('Failed'))
    expect(wrapper.find('.text-danger-text').exists()).toBe(false)
  })

  it('shows empty state when no scratchpads or locks', async () => {
    vi.mocked(fetch)
      .mockResolvedValueOnce({
        ok: true,
        json: async () => ({ entries: [] }),
      } as Response)
      .mockResolvedValueOnce({
        ok: true,
        json: async () => ({ locks: [] }),
      } as Response)

    const wrapper = mountTab()
    await flushPromises()

    expect(wrapper.text()).toContain('No scratchpad entries')
    expect(wrapper.text()).toContain('No active locks')
  })
})
