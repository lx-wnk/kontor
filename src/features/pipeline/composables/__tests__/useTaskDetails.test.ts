import type { Agent, PermissionRequest, PipelineTask, StageRun, TaskFeedback, TaskPermission } from '@/types'
import { flushPromises, mount } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { defineComponent, ref, shallowRef } from 'vue'
import { useAgents } from '@/features/agents/composables/useAgents'
import { useTaskDetails } from '@/features/pipeline/composables/useTaskDetails'
import {
  fetchPendingPermissionRequests,
  fetchStageRunAgentOutput,
  fetchStageRuns,
  fetchTaskFeedback,
  fetchTaskPermissions,
} from '@/features/pipeline/composables/useTasks'

vi.mock('@/features/agents/composables/useAgents', () => ({
  useAgents: vi.fn(),
}))

vi.mock('@/features/pipeline/composables/useTasks', () => ({
  fetchStageRuns: vi.fn(),
  fetchTaskPermissions: vi.fn(),
  fetchPendingPermissionRequests: vi.fn(),
  fetchTaskFeedback: vi.fn(),
  fetchStageRunAgentOutput: vi.fn(),
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

function makeStageRun(overrides: Partial<StageRun> = {}): StageRun {
  return {
    id: 'run-1',
    taskId: 'task-1',
    stage: 'implementation',
    sessionId: null,
    sessionName: null,
    pid: null,
    status: 'done',
    startedAt: '2026-01-01T00:00:00Z',
    endedAt: '2026-01-01T00:05:00Z',
    iteration: 1,
    output: null,
    tokensUsed: 100,
    costCents: 50,
    lastGrantAt: null,
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
  vi.mocked(useAgents).mockReturnValue({ agents: shallowRef([]) } as unknown as ReturnType<typeof useAgents>)
  vi.mocked(fetchStageRuns).mockResolvedValue([])
  vi.mocked(fetchTaskPermissions).mockResolvedValue([])
  vi.mocked(fetchPendingPermissionRequests).mockResolvedValue([])
  vi.mocked(fetchTaskFeedback).mockResolvedValue([])
  vi.mocked(fetchStageRunAgentOutput).mockResolvedValue(null)
})

afterEach(() => {
  vi.useRealTimers()
  vi.restoreAllMocks()
})

describe('useTaskDetails — loadDetails', () => {
  it('loads stage runs, permissions, pending requests and feedback on mount', async () => {
    vi.mocked(fetchStageRuns).mockResolvedValue([makeStageRun()])
    vi.mocked(fetchTaskPermissions).mockResolvedValue([{ id: 'p1' } as TaskPermission])
    vi.mocked(fetchPendingPermissionRequests).mockResolvedValue([makePermissionRequest()])
    vi.mocked(fetchTaskFeedback).mockResolvedValue([{ id: 'f1' } as TaskFeedback])

    const task = ref<PipelineTask | null>(makeTask())
    const { result, wrapper } = withSetup(() => useTaskDetails(task))
    await flushPromises()

    expect(result.stageRuns.value).toHaveLength(1)
    expect(result.permissions.value).toHaveLength(1)
    expect(result.pendingRequests.value).toHaveLength(1)
    expect(result.feedbackHistory.value).toHaveLength(1)
    wrapper.unmount()
  })

  it('does not fetch when the task is null', async () => {
    const task = ref<PipelineTask | null>(null)
    const { wrapper } = withSetup(() => useTaskDetails(task))
    await flushPromises()

    expect(fetchStageRuns).not.toHaveBeenCalled()
    wrapper.unmount()
  })
})

describe('useTaskDetails — pipelineAgent', () => {
  it('matches by sessionId when set', async () => {
    const agent = { pid: 111, sessionId: 'sess-1' } as Agent
    vi.mocked(useAgents).mockReturnValue({ agents: shallowRef([agent]) } as unknown as ReturnType<typeof useAgents>)

    const task = ref<PipelineTask | null>(makeTask({ activeSessionId: 'sess-1', activePid: 999 }))
    const { result, wrapper } = withSetup(() => useTaskDetails(task))
    await flushPromises()

    expect(result.pipelineAgent.value).toBe(agent)
    wrapper.unmount()
  })

  it('falls back to activePid when no sessionId is set', async () => {
    const agent = { pid: 222, sessionId: 'sess-2' } as Agent
    vi.mocked(useAgents).mockReturnValue({ agents: shallowRef([agent]) } as unknown as ReturnType<typeof useAgents>)

    const task = ref<PipelineTask | null>(makeTask({ activeSessionId: null, activePid: 222 }))
    const { result, wrapper } = withSetup(() => useTaskDetails(task))
    await flushPromises()

    expect(result.pipelineAgent.value).toBe(agent)
    wrapper.unmount()
  })

  it('is null when neither sessionId nor pid match a live agent', async () => {
    const task = ref<PipelineTask | null>(makeTask())
    const { result, wrapper } = withSetup(() => useTaskDetails(task))
    await flushPromises()

    expect(result.pipelineAgent.value).toBeNull()
    wrapper.unmount()
  })
})

describe('useTaskDetails — latest run computeds', () => {
  it('exposes the last stage run and its agentMessage/error from output', async () => {
    const runs = [
      makeStageRun({ id: 'run-1', output: null }),
      makeStageRun({ id: 'run-2', status: 'failed', output: { agentMessage: 'hi', error: 'boom' } }),
    ]
    vi.mocked(fetchStageRuns).mockResolvedValue(runs)

    const task = ref<PipelineTask | null>(makeTask({ latestStageRunStatus: 'failed' }))
    const { result, wrapper } = withSetup(() => useTaskDetails(task))
    await flushPromises()

    expect(result.latestStageRun.value?.id).toBe('run-2')
    expect(result.latestRunAgentMessage.value).toBe('hi')
    expect(result.latestRunError.value).toBe('boom')
    wrapper.unmount()
  })

  it('is null when there are no stage runs', async () => {
    const task = ref<PipelineTask | null>(makeTask())
    const { result, wrapper } = withSetup(() => useTaskDetails(task))
    await flushPromises()

    expect(result.latestStageRun.value).toBeNull()
    expect(result.latestRunAgentMessage.value).toBeNull()
    expect(result.latestRunError.value).toBeNull()
    wrapper.unmount()
  })
})

describe('useTaskDetails — status flags', () => {
  it('isFailedRun reflects latestStageRunStatus === failed', async () => {
    const task = ref<PipelineTask | null>(makeTask({ latestStageRunStatus: 'failed' }))
    const { result, wrapper } = withSetup(() => useTaskDetails(task))
    await flushPromises()

    expect(result.isFailedRun.value).toBe(true)
    wrapper.unmount()
  })

  it('isTerminal is true for done/cancelled stages and false otherwise', async () => {
    const task = ref<PipelineTask | null>(makeTask({ currentStage: 'done' }))
    const { result, wrapper } = withSetup(() => useTaskDetails(task))
    await flushPromises()
    expect(result.isTerminal.value).toBe(true)

    task.value = makeTask({ currentStage: 'implementation' })
    await flushPromises()
    expect(result.isTerminal.value).toBe(false)
    wrapper.unmount()
  })

  it('a failed run is never terminal, so it stays actionable (Retry / Analyze)', async () => {
    const task = ref<PipelineTask | null>(makeTask({ currentStage: 'implementation', latestStageRunStatus: 'failed' }))
    const { result, wrapper } = withSetup(() => useTaskDetails(task))
    await flushPromises()

    expect(result.isFailedRun.value).toBe(true)
    expect(result.isTerminal.value).toBe(false)
    wrapper.unmount()
  })

  it('isOnHoldStage is true only for the on_hold stage', async () => {
    const task = ref<PipelineTask | null>(makeTask({ currentStage: 'on_hold' }))
    const { result, wrapper } = withSetup(() => useTaskDetails(task))
    await flushPromises()

    expect(result.isOnHoldStage.value).toBe(true)
    wrapper.unmount()
  })

  it('isResumableAwaitingUser is true when awaiting_user has no pending requests', async () => {
    vi.mocked(fetchPendingPermissionRequests).mockResolvedValue([])

    const task = ref<PipelineTask | null>(makeTask({ latestStageRunStatus: 'awaiting_user' }))
    const { result, wrapper } = withSetup(() => useTaskDetails(task))
    await flushPromises()

    expect(result.isResumableAwaitingUser.value).toBe(true)
    wrapper.unmount()
  })

  it('isResumableAwaitingUser is false when awaiting_user still has pending requests', async () => {
    vi.mocked(fetchPendingPermissionRequests).mockResolvedValue([makePermissionRequest()])

    const task = ref<PipelineTask | null>(makeTask({ latestStageRunStatus: 'awaiting_user' }))
    const { result, wrapper } = withSetup(() => useTaskDetails(task))
    await flushPromises()

    expect(result.isResumableAwaitingUser.value).toBe(false)
    wrapper.unmount()
  })
})

describe('useTaskDetails — pendingByStageRun / totals', () => {
  it('groups pending requests by stageRunId', async () => {
    vi.mocked(fetchPendingPermissionRequests).mockResolvedValue([
      makePermissionRequest({ id: 'r1', stageRunId: 'run-a' }),
      makePermissionRequest({ id: 'r2', stageRunId: 'run-a' }),
      makePermissionRequest({ id: 'r3', stageRunId: 'run-b' }),
    ])

    const task = ref<PipelineTask | null>(makeTask())
    const { result, wrapper } = withSetup(() => useTaskDetails(task))
    await flushPromises()

    expect(result.pendingByStageRun.value).toHaveLength(2)
    const groupA = result.pendingByStageRun.value.find(g => g.stageRunId === 'run-a')
    const groupB = result.pendingByStageRun.value.find(g => g.stageRunId === 'run-b')
    expect(groupA?.requests.map(r => r.id)).toEqual(['r1', 'r2'])
    expect(groupB?.requests.map(r => r.id)).toEqual(['r3'])
    wrapper.unmount()
  })

  it('is empty when there are no pending requests', async () => {
    const task = ref<PipelineTask | null>(makeTask())
    const { result, wrapper } = withSetup(() => useTaskDetails(task))
    await flushPromises()

    expect(result.pendingByStageRun.value).toEqual([])
    wrapper.unmount()
  })

  it('sums tokensUsed and costCents across all stage runs, treating missing values as 0', async () => {
    vi.mocked(fetchStageRuns).mockResolvedValue([
      makeStageRun({ tokensUsed: 100, costCents: 50 }),
      makeStageRun({ tokensUsed: undefined as unknown as number, costCents: undefined as unknown as number }),
      makeStageRun({ tokensUsed: 25, costCents: 10 }),
    ])

    const task = ref<PipelineTask | null>(makeTask())
    const { result, wrapper } = withSetup(() => useTaskDetails(task))
    await flushPromises()

    expect(result.totalTokensUsed.value).toBe(125)
    expect(result.totalCostCents.value).toBe(60)
    wrapper.unmount()
  })
})

describe('useTaskDetails — handleAction', () => {
  it('runs the action, reloads details and sets a success message', async () => {
    const task = ref<PipelineTask | null>(makeTask())
    const { result, wrapper } = withSetup(() => useTaskDetails(task))
    await flushPromises()
    vi.mocked(fetchStageRuns).mockClear()

    const action = vi.fn().mockResolvedValue(undefined)
    await result.handleAction(action, 'Done!')

    expect(action).toHaveBeenCalled()
    expect(fetchStageRuns).toHaveBeenCalled()
    expect(result.actionSuccess.value).toBe('Done!')
    expect(result.isActing.value).toBe(false)
    wrapper.unmount()
  })

  it('surfaces the error message and does not set a success message', async () => {
    const task = ref<PipelineTask | null>(makeTask())
    const { result, wrapper } = withSetup(() => useTaskDetails(task))
    await flushPromises()

    const action = vi.fn().mockRejectedValue(new Error('boom'))
    await result.handleAction(action, 'Done!')

    expect(result.actionError.value).toBe('boom')
    expect(result.actionSuccess.value).toBe('')
    expect(result.isActing.value).toBe(false)
    wrapper.unmount()
  })

  it('is a no-op without a task', async () => {
    const task = ref<PipelineTask | null>(null)
    const { result, wrapper } = withSetup(() => useTaskDetails(task))
    const action = vi.fn()

    await result.handleAction(action)

    expect(action).not.toHaveBeenCalled()
    wrapper.unmount()
  })

  it('resets actionError when the task id changes but keeps it across other field-only updates', async () => {
    const task = ref<PipelineTask | null>(makeTask({ id: 'task-1' }))
    const { result, wrapper } = withSetup(() => useTaskDetails(task))
    await flushPromises()
    result.actionError.value = 'previous failure'

    task.value = makeTask({ id: 'task-1', currentStage: 'implementation', currentIteration: 2 })
    await flushPromises()
    expect(result.actionError.value).toBe('previous failure')

    task.value = makeTask({ id: 'task-2' })
    await flushPromises()
    expect(result.actionError.value).toBe('')
    wrapper.unmount()
  })
})

describe('useTaskDetails — session text + running poll', () => {
  it('loads session text once for a done run without polling', async () => {
    vi.mocked(fetchStageRuns).mockResolvedValue([makeStageRun({ status: 'done', output: null })])
    vi.mocked(fetchStageRunAgentOutput).mockResolvedValue('session output')

    const task = ref<PipelineTask | null>(makeTask())
    const { result, wrapper } = withSetup(() => useTaskDetails(task))
    await flushPromises()

    expect(result.sessionAgentText.value).toBe('session output')
    expect(fetchStageRunAgentOutput).toHaveBeenCalledTimes(1)
    wrapper.unmount()
  })

  it('skips fetching session text when the latest run already carries an agentMessage', async () => {
    vi.mocked(fetchStageRuns).mockResolvedValue([makeStageRun({ status: 'done', output: { agentMessage: 'already have it' } })])

    const task = ref<PipelineTask | null>(makeTask())
    const { wrapper } = withSetup(() => useTaskDetails(task))
    await flushPromises()

    expect(fetchStageRunAgentOutput).not.toHaveBeenCalled()
    wrapper.unmount()
  })

  it('polls session text every 5s for a running stage run and stops polling on unmount', async () => {
    const task = ref<PipelineTask | null>(makeTask())
    const { result, wrapper } = withSetup(() => useTaskDetails(task))
    await flushPromises()

    vi.mocked(fetchStageRuns).mockResolvedValue([makeStageRun({ status: 'running', output: null })])
    vi.mocked(fetchStageRunAgentOutput).mockResolvedValue('partial output')
    vi.useFakeTimers()

    await result.loadDetails()
    expect(fetchStageRunAgentOutput).toHaveBeenCalledTimes(1)

    await vi.advanceTimersByTimeAsync(5000)
    expect(fetchStageRunAgentOutput).toHaveBeenCalledTimes(2)

    wrapper.unmount()
    await vi.advanceTimersByTimeAsync(10000)
    expect(fetchStageRunAgentOutput).toHaveBeenCalledTimes(2)
  })
})
