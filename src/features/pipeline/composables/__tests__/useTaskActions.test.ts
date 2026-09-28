import type { PendingGroup, UseTaskDetails } from '@/features/pipeline/composables/useTaskDetails'
import type { PermissionRequest, PipelineTask, TaskPermission } from '@/types'
import { mount } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { defineComponent, ref } from 'vue'
import { TASK_SLASH_COMMANDS, useTaskActions } from '@/features/pipeline/composables/useTaskActions'
import {
  analyzeTask,
  bulkResolvePermissionRequests,
  cancelTask,
  fetchTaskPermissions,
  grantTaskPermission,
  resolvePermissionRequest,
  retryTask,
} from '@/features/pipeline/composables/useTasks'

vi.mock('@/features/pipeline/composables/useTasks', () => ({
  analyzeTask: vi.fn(),
  bulkResolvePermissionRequests: vi.fn(),
  cancelTask: vi.fn(),
  fetchTaskPermissions: vi.fn(),
  grantTaskPermission: vi.fn(),
  resolvePermissionRequest: vi.fn(),
  retryTask: vi.fn(),
}))

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

function makePermissionRequest(overrides: Partial<PermissionRequest> = {}): PermissionRequest {
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

function makeDetails(pendingGroups: PendingGroup[] = []): UseTaskDetails {
  return {
    permissions: ref<TaskPermission[]>([]),
    pendingByStageRun: ref<PendingGroup[]>(pendingGroups),
    handleAction: vi.fn(),
  } as unknown as UseTaskDetails
}

function withSetup<T>(composable: () => T) {
  let result!: T
  const Wrapper = defineComponent({
    setup() {
      result = composable()
      return {}
    },
    template: '<div />',
  })
  const wrapper = mount(Wrapper, { attachTo: document.body })
  return { result, wrapper }
}

beforeEach(() => {
  vi.clearAllMocks()
  vi.mocked(cancelTask).mockResolvedValue(undefined)
  vi.mocked(retryTask).mockResolvedValue(undefined)
  vi.mocked(analyzeTask).mockResolvedValue({ pid: 123, cwd: '/repo' })
  vi.mocked(resolvePermissionRequest).mockResolvedValue(undefined)
  vi.mocked(bulkResolvePermissionRequests).mockResolvedValue({ resolved: 0, errors: [] })
  vi.mocked(grantTaskPermission).mockResolvedValue({} as TaskPermission)
  vi.mocked(fetchTaskPermissions).mockResolvedValue([])
})

afterEach(() => {
  vi.useRealTimers()
  vi.restoreAllMocks()
})

describe('slash command list', () => {
  it('exposes the expected slash commands', () => {
    expect(TASK_SLASH_COMMANDS.map(c => c.name)).toEqual(['/retry', '/grant', '/cancel', '/status', '/help'])
  })
})

describe('useTaskActions — onCancelClick (two-step confirm)', () => {
  it('arms the confirmation on the first click without cancelling', () => {
    const task = ref<PipelineTask | null>(makeTask())
    const details = makeDetails()
    const { result, wrapper } = withSetup(() => useTaskActions(task, details))

    result.onCancelClick()

    expect(result.cancelConfirm.value).toBe(true)
    expect(details.handleAction).not.toHaveBeenCalled()
    wrapper.unmount()
  })

  it('cancels on the second click and resets the confirm flag', () => {
    const task = ref<PipelineTask | null>(makeTask())
    const details = makeDetails()
    const { result, wrapper } = withSetup(() => useTaskActions(task, details))

    result.onCancelClick()
    result.onCancelClick()

    expect(result.cancelConfirm.value).toBe(false)
    expect(details.handleAction).toHaveBeenCalledTimes(1)
    wrapper.unmount()
  })

  it('the confirmed action calls cancelTask with the task id', async () => {
    const task = ref<PipelineTask | null>(makeTask({ id: 'task-9' }))
    const details = makeDetails()
    const { result, wrapper } = withSetup(() => useTaskActions(task, details))

    result.onCancelClick()
    result.onCancelClick()
    const passedAction = vi.mocked(details.handleAction).mock.calls[0][0]
    await passedAction()

    expect(cancelTask).toHaveBeenCalledWith('task-9')
    wrapper.unmount()
  })

  it('auto-resets the confirmation after 5 seconds', () => {
    vi.useFakeTimers()
    const task = ref<PipelineTask | null>(makeTask())
    const details = makeDetails()
    const { result, wrapper } = withSetup(() => useTaskActions(task, details))

    result.onCancelClick()
    expect(result.cancelConfirm.value).toBe(true)

    vi.advanceTimersByTime(5000)
    expect(result.cancelConfirm.value).toBe(false)
    wrapper.unmount()
  })
})

describe('useTaskActions — permission resolution', () => {
  it('onResolve calls resolvePermissionRequest with the task, request id and outcome', async () => {
    const task = ref<PipelineTask | null>(makeTask())
    const details = makeDetails()
    const { result, wrapper } = withSetup(() => useTaskActions(task, details))

    const req = makePermissionRequest({ id: 'req-7' })
    await result.onResolve(req, 'granted')
    const passedAction = vi.mocked(details.handleAction).mock.calls[0][0]
    await passedAction()

    expect(resolvePermissionRequest).toHaveBeenCalledWith('task-1', 'req-7', 'allow_once')
    wrapper.unmount()
  })

  it('onResolve maps a refusal to deny_once', async () => {
    const task = ref<PipelineTask | null>(makeTask())
    const details = makeDetails()
    const { result, wrapper } = withSetup(() => useTaskActions(task, details))

    const req = makePermissionRequest({ id: 'req-8' })
    await result.onResolve(req, 'denied')
    const passedAction = vi.mocked(details.handleAction).mock.calls[0][0]
    await passedAction()

    expect(resolvePermissionRequest).toHaveBeenCalledWith('task-1', 'req-8', 'deny_once')
    wrapper.unmount()
  })

  it('onResolveAll bulk-resolves the matching group and throws a combined error on failures', async () => {
    const details = makeDetails([
      { stageRunId: 'run-1', requests: [makePermissionRequest({ id: 'r1' }), makePermissionRequest({ id: 'r2' })] },
    ])
    vi.mocked(bulkResolvePermissionRequests).mockResolvedValue({ resolved: 1, errors: ['r2 failed'] })
    const task = ref<PipelineTask | null>(makeTask())
    const { result, wrapper } = withSetup(() => useTaskActions(task, details))

    await result.onResolveAll('run-1', 'granted')
    const passedAction = vi.mocked(details.handleAction).mock.calls[0][0]

    await expect(passedAction()).rejects.toThrow('1 request(s) failed: r2 failed')
    expect(bulkResolvePermissionRequests).toHaveBeenCalledWith('task-1', ['r1', 'r2'], 'allow_once')
    wrapper.unmount()
  })

  it('onResolveAll resolves silently when there are no errors', async () => {
    const details = makeDetails([
      { stageRunId: 'run-1', requests: [makePermissionRequest({ id: 'r1' })] },
    ])
    vi.mocked(bulkResolvePermissionRequests).mockResolvedValue({ resolved: 1, errors: [] })
    const task = ref<PipelineTask | null>(makeTask())
    const { result, wrapper } = withSetup(() => useTaskActions(task, details))

    await result.onResolveAll('run-1', 'granted')
    const passedAction = vi.mocked(details.handleAction).mock.calls[0][0]

    await expect(passedAction()).resolves.toBeUndefined()
    wrapper.unmount()
  })

  it('onResolveAll sends an empty id list when the stageRunId has no matching group', async () => {
    const details = makeDetails([])
    const task = ref<PipelineTask | null>(makeTask())
    const { result, wrapper } = withSetup(() => useTaskActions(task, details))

    await result.onResolveAll('missing-run', 'denied')
    const passedAction = vi.mocked(details.handleAction).mock.calls[0][0]
    await passedAction()

    expect(bulkResolvePermissionRequests).toHaveBeenCalledWith('task-1', [], 'deny_once')
    wrapper.unmount()
  })
})

describe('useTaskActions — onGrantPermission', () => {
  it('rejects a blank tool name without calling the API', async () => {
    const task = ref<PipelineTask | null>(makeTask())
    const details = makeDetails()
    const { result, wrapper } = withSetup(() => useTaskActions(task, details))

    const ok = await result.onGrantPermission('   ', null)

    expect(ok).toBe(false)
    expect(result.permError.value).toBe('Tool name is required')
    expect(grantTaskPermission).not.toHaveBeenCalled()
    wrapper.unmount()
  })

  it('grants the permission, refreshes the list and returns true', async () => {
    vi.mocked(fetchTaskPermissions).mockResolvedValue([{ id: 'p1' } as TaskPermission])
    const task = ref<PipelineTask | null>(makeTask())
    const details = makeDetails()
    const { result, wrapper } = withSetup(() => useTaskActions(task, details))

    const ok = await result.onGrantPermission(' Bash ', ' rm -rf * ')

    expect(ok).toBe(true)
    expect(grantTaskPermission).toHaveBeenCalledWith('task-1', 'Bash', 'rm -rf *')
    expect(details.permissions.value).toHaveLength(1)
    expect(result.isGranting.value).toBe(false)
    wrapper.unmount()
  })

  it('normalizes a blank pattern to null', async () => {
    const task = ref<PipelineTask | null>(makeTask())
    const details = makeDetails()
    const { result, wrapper } = withSetup(() => useTaskActions(task, details))

    await result.onGrantPermission('Bash', '   ')

    expect(grantTaskPermission).toHaveBeenCalledWith('task-1', 'Bash', null)
    wrapper.unmount()
  })

  it('surfaces the server error and returns false on failure', async () => {
    vi.mocked(grantTaskPermission).mockRejectedValue(new Error('duplicate grant'))
    const task = ref<PipelineTask | null>(makeTask())
    const details = makeDetails()
    const { result, wrapper } = withSetup(() => useTaskActions(task, details))

    const ok = await result.onGrantPermission('Bash', null)

    expect(ok).toBe(false)
    expect(result.permError.value).toBe('duplicate grant')
    expect(result.isGranting.value).toBe(false)
    wrapper.unmount()
  })
})

describe('useTaskActions — onAnalyze / onRetry', () => {
  it('onAnalyze stores the pid/cwd returned by analyzeTask', async () => {
    const task = ref<PipelineTask | null>(makeTask())
    const details = makeDetails()
    const { result, wrapper } = withSetup(() => useTaskActions(task, details))

    await result.onAnalyze()
    const passedAction = vi.mocked(details.handleAction).mock.calls[0][0]
    await passedAction()

    expect(analyzeTask).toHaveBeenCalledWith('task-1')
    expect(result.analysisInfo.value).toEqual({ pid: 123, cwd: '/repo' })
    wrapper.unmount()
  })

  it('onAnalyze is a no-op without a task', async () => {
    const task = ref<PipelineTask | null>(null)
    const details = makeDetails()
    const { result, wrapper } = withSetup(() => useTaskActions(task, details))

    await result.onAnalyze()

    expect(details.handleAction).not.toHaveBeenCalled()
    wrapper.unmount()
  })

  it('onRetry passes the additional prompt through to retryTask with a success message', async () => {
    const task = ref<PipelineTask | null>(makeTask())
    const details = makeDetails()
    const { result, wrapper } = withSetup(() => useTaskActions(task, details))

    result.additionalPrompt.value = 'try again with more context'
    await result.onRetry()

    expect(details.handleAction).toHaveBeenCalledWith(expect.any(Function), 'Stage re-queued — will run when a slot is free')
    const passedAction = vi.mocked(details.handleAction).mock.calls[0][0]
    await passedAction()

    expect(retryTask).toHaveBeenCalledWith('task-1', 'try again with more context')
    wrapper.unmount()
  })
})

describe('useTaskActions — onSlashSelect', () => {
  it('always clears the additional prompt field', async () => {
    const task = ref<PipelineTask | null>(makeTask())
    const details = makeDetails()
    const { result, wrapper } = withSetup(() => useTaskActions(task, details))
    result.additionalPrompt.value = 'leftover text'

    await result.onSlashSelect({ name: '/status' })

    expect(result.additionalPrompt.value).toBe('')
    wrapper.unmount()
  })

  it('/retry triggers onRetry', async () => {
    const task = ref<PipelineTask | null>(makeTask())
    const details = makeDetails()
    const { result, wrapper } = withSetup(() => useTaskActions(task, details))

    await result.onSlashSelect({ name: '/retry' })
    const passedAction = vi.mocked(details.handleAction).mock.calls.at(-1)?.[0]
    await passedAction!()

    expect(retryTask).toHaveBeenCalledWith('task-1', undefined)
    wrapper.unmount()
  })

  it('/grant bulk-grants every pending group in order when all succeed', async () => {
    const details = makeDetails([
      { stageRunId: 'run-1', requests: [makePermissionRequest({ id: 'r1' })] },
      { stageRunId: 'run-2', requests: [makePermissionRequest({ id: 'r2' })] },
    ])
    vi.mocked(bulkResolvePermissionRequests).mockResolvedValue({ resolved: 1, errors: [] })
    const task = ref<PipelineTask | null>(makeTask())
    const { result, wrapper } = withSetup(() => useTaskActions(task, details))

    await result.onSlashSelect({ name: '/grant' })
    const passedAction = vi.mocked(details.handleAction).mock.calls[0][0]

    await expect(passedAction()).resolves.toBeUndefined()
    expect(bulkResolvePermissionRequests).toHaveBeenNthCalledWith(1, 'task-1', ['r1'], 'allow_once')
    expect(bulkResolvePermissionRequests).toHaveBeenNthCalledWith(2, 'task-1', ['r2'], 'allow_once')
    wrapper.unmount()
  })

  it('/grant stops at the first failing group and never reaches the next one', async () => {
    const details = makeDetails([
      { stageRunId: 'run-1', requests: [makePermissionRequest({ id: 'r1' })] },
      { stageRunId: 'run-2', requests: [makePermissionRequest({ id: 'r2' })] },
    ])
    vi.mocked(bulkResolvePermissionRequests).mockResolvedValueOnce({ resolved: 0, errors: ['r1 denied by policy'] })
    const task = ref<PipelineTask | null>(makeTask())
    const { result, wrapper } = withSetup(() => useTaskActions(task, details))

    await result.onSlashSelect({ name: '/grant' })
    const passedAction = vi.mocked(details.handleAction).mock.calls[0][0]

    await expect(passedAction()).rejects.toThrow('1 request(s) failed: r1 denied by policy')
    expect(bulkResolvePermissionRequests).toHaveBeenCalledTimes(1)
    expect(bulkResolvePermissionRequests).toHaveBeenCalledWith('task-1', ['r1'], 'allow_once')
    wrapper.unmount()
  })

  it('/cancel cancels the task', async () => {
    const task = ref<PipelineTask | null>(makeTask({ id: 'task-5' }))
    const details = makeDetails()
    const { result, wrapper } = withSetup(() => useTaskActions(task, details))

    await result.onSlashSelect({ name: '/cancel' })
    const passedAction = vi.mocked(details.handleAction).mock.calls[0][0]
    await passedAction()

    expect(cancelTask).toHaveBeenCalledWith('task-5')
    wrapper.unmount()
  })

  it('unknown commands only reset the prompt without invoking an action', async () => {
    const task = ref<PipelineTask | null>(makeTask())
    const details = makeDetails()
    const { result, wrapper } = withSetup(() => useTaskActions(task, details))

    await result.onSlashSelect({ name: '/help' })

    expect(details.handleAction).not.toHaveBeenCalled()
    wrapper.unmount()
  })

  it('is a no-op without a task', async () => {
    const task = ref<PipelineTask | null>(null)
    const details = makeDetails()
    const { result, wrapper } = withSetup(() => useTaskActions(task, details))

    await result.onSlashSelect({ name: '/cancel' })

    expect(details.handleAction).not.toHaveBeenCalled()
    wrapper.unmount()
  })
})
