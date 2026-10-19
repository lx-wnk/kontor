import type { PipelineTask } from '@/types'
import { mount } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { ref } from 'vue'
import LiveWorkWidget from '../components/LiveWorkWidget.vue'

function makeTask(id: string, overrides: Partial<PipelineTask> = {}): PipelineTask {
  return {
    id,
    slug: id,
    title: id,
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
    createdAt: '2026-09-25T10:00:00Z',
    updatedAt: '2026-09-25T10:00:00Z',
    metadata: null,
    silverBullet: false,
    planMode: false,
    priority: 'medium',
    userId: null,
    ...overrides,
  } as PipelineTask
}

const tasksList = ref<PipelineTask[]>([])
const agentsList = ref<any[]>([])

vi.mock('@/features/pipeline', async importOriginal => ({
  ...await importOriginal<typeof import('@/features/pipeline')>(),
  useTasks: () => ({
    tasks: tasksList,
    isLoading: ref(false),
  }),
}))

vi.mock('@/features/agents', () => ({
  useAgents: () => ({
    agents: agentsList,
  }),
}))

// The count figure shares the "live-" prefix with the rail rows.
function railOrder(wrapper: ReturnType<typeof mount>): Array<string | undefined> {
  return wrapper
    .findAll('[data-testid^="live-"]:not([data-testid="live-work-count"])')
    .map(el => el.attributes('data-testid'))
}

describe('liveWorkWidget', () => {
  beforeEach(() => {
    vi.useFakeTimers()
    tasksList.value = []
    agentsList.value = []
  })

  afterEach(() => {
    vi.useRealTimers()
  })

  it('renders the icon and counts only the running tasks in the figure slot', () => {
    tasksList.value = [
      makeTask('a', { currentStage: 'implementation' }),
      makeTask('b', { currentStage: 'backlog' }),
    ]
    const wrapper = mount(LiveWorkWidget)
    expect(wrapper.get('header').text()).toContain('▶')
    expect(wrapper.get('[data-testid="live-work-count"]').text()).toBe('1')
    wrapper.unmount()
  })

  it('excludes blocked tasks from the count and list', () => {
    tasksList.value = [
      makeTask('running', { currentStage: 'implementation' }),
      makeTask('blocked', { currentStage: 'plan_review', isBlocked: true }),
    ]
    const wrapper = mount(LiveWorkWidget)
    expect(wrapper.get('[data-testid="live-work-count"]').text()).toBe('1')
    expect(wrapper.text()).not.toContain('blocked')
    wrapper.unmount()
  })

  it('excludes unsatisfiable tasks from the count and list', () => {
    tasksList.value = [
      makeTask('running', { currentStage: 'implementation' }),
      makeTask('unsat', { currentStage: 'plan_review', isUnsatisfiable: true }),
    ]
    const wrapper = mount(LiveWorkWidget)
    expect(wrapper.get('[data-testid="live-work-count"]').text()).toBe('1')
    wrapper.unmount()
  })

  it('sorts tasks by most recent activity (descending)', () => {
    tasksList.value = [
      makeTask('old', { currentStage: 'implementation', updatedAt: '2026-09-25T09:00:00Z' }),
      makeTask('new', { currentStage: 'implementation', updatedAt: '2026-09-25T11:00:00Z' }),
      makeTask('mid', { currentStage: 'implementation', updatedAt: '2026-09-25T10:00:00Z' }),
    ]
    const wrapper = mount(LiveWorkWidget)
    const slugs = railOrder(wrapper)
    expect(slugs).toEqual(['live-new', 'live-mid', 'live-old'])
    wrapper.unmount()
  })

  it('does not reorder within the 30s throttle window on activity-only change', async () => {
    tasksList.value = [
      makeTask('a', { currentStage: 'implementation', updatedAt: '2026-09-25T10:00:00Z' }),
      makeTask('b', { currentStage: 'implementation', updatedAt: '2026-09-25T11:00:00Z' }),
    ]
    const wrapper = mount(LiveWorkWidget)
    const initialOrder = railOrder(wrapper)
    expect(initialOrder).toEqual(['live-b', 'live-a'])

    // Update a's updatedAt to be newer, but don't change stage/status (no fingerprint change)
    tasksList.value = [
      makeTask('a', { currentStage: 'implementation', updatedAt: '2026-09-25T12:00:00Z' }),
      makeTask('b', { currentStage: 'implementation', updatedAt: '2026-09-25T11:00:00Z' }),
    ]
    await wrapper.vm.$nextTick()

    const afterUpdate = railOrder(wrapper)
    // Order should NOT change yet — sort keys were snapshotted
    expect(afterUpdate).toEqual(['live-b', 'live-a'])
    wrapper.unmount()
  })

  it('reorders after advancing past LIVE_WORK_RESORT_MS', async () => {
    tasksList.value = [
      makeTask('a', { currentStage: 'implementation', updatedAt: '2026-09-25T10:00:00Z' }),
      makeTask('b', { currentStage: 'implementation', updatedAt: '2026-09-25T11:00:00Z' }),
    ]
    const wrapper = mount(LiveWorkWidget)

    // Change a's updatedAt to be newer
    tasksList.value = [
      makeTask('a', { currentStage: 'implementation', updatedAt: '2026-09-25T12:00:00Z' }),
      makeTask('b', { currentStage: 'implementation', updatedAt: '2026-09-25T11:00:00Z' }),
    ]

    // Advance past the throttle window
    vi.advanceTimersByTime(31_000)
    await wrapper.vm.$nextTick()

    const afterThrottle = railOrder(wrapper)
    expect(afterThrottle).toEqual(['live-a', 'live-b'])
    wrapper.unmount()
  })

  it('reorders immediately on stage change (fingerprint change)', async () => {
    tasksList.value = [
      makeTask('a', { currentStage: 'implementation', updatedAt: '2026-09-25T10:00:00Z' }),
      makeTask('b', { currentStage: 'implementation', updatedAt: '2026-09-25T11:00:00Z' }),
    ]
    const wrapper = mount(LiveWorkWidget)

    // a changes stage — fingerprint changes — triggers immediate reorder
    tasksList.value = [
      makeTask('a', { currentStage: 'self_review', updatedAt: '2026-09-25T12:00:00Z' }),
      makeTask('b', { currentStage: 'implementation', updatedAt: '2026-09-25T11:00:00Z' }),
    ]
    await wrapper.vm.$nextTick()

    const afterStageChange = railOrder(wrapper)
    expect(afterStageChange).toEqual(['live-a', 'live-b'])
    wrapper.unmount()
  })
})
