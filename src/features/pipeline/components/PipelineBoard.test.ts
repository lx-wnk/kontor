import type { PipelineStage, PipelineTask } from '@/types'
import { shallowMount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { ref } from 'vue'
import PipelineBoard from '@/features/pipeline/components/PipelineBoard.vue'
import SortableTaskList from '@/features/pipeline/components/SortableTaskList.vue'

// Mutable state shared between vi.mock factory and test bodies.
// The factory's useTasks() function closes over this variable and reads
// its current value each time it is called (at component mount time),
// so setting it before shallowMount works correctly.
let mockStageMap: Partial<Record<PipelineStage, PipelineTask[]>> = {}

vi.mock('@/features/pipeline/composables/useTasks', () => ({
  useTasks: () => ({
    tasks: { value: [] as PipelineTask[] },
    pipelineTasks: { value: [] as PipelineTask[] },
    tasksByStageMap: {
      // Plain object — not reactive. Tests rely on initial render only.
      get value() { return mockStageMap },
    },
    isLoading: ref(false),
    error: ref(null),
    selectTask: vi.fn(),
    startStream: vi.fn(),
  }),
  reorderTask: vi.fn(),
  byActivityDesc: (a: PipelineTask, b: PipelineTask) =>
    new Date(b.updatedAt).getTime() - new Date(a.updatedAt).getTime(),
  byRank: (a: PipelineTask, b: PipelineTask) =>
    (a.rank ?? new Date(a.createdAt).getTime() * 1000) - (b.rank ?? new Date(b.createdAt).getTime() * 1000),
}))

vi.mock('@/composables/useProjects', () => ({
  useProjects: () => ({ projects: ref([]) }),
}))

// Prevent SortableTaskList (shallow stub) from calling real useSortable
vi.mock('@vueuse/integrations/useSortable', () => ({
  useSortable: vi.fn(),
}))

beforeEach(() => {
  mockStageMap = {}
})

function makeTask(id: string, overrides: Partial<PipelineTask> = {}): PipelineTask {
  return {
    id,
    slug: `slug-${id}`,
    title: `Task ${id}`,
    description: null,
    cwd: '/repo',
    worktreePath: null,
    sourceBranch: null,
    targetBranch: null,
    currentStage: 'ready',
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
    rank: null,
    needsUser: false,
    ...overrides,
  }
}

// COLUMNS order (from PipelineBoard.vue):
// 0 needs-you, 1 backlog, 2 ready, 3 plan_review, 4 implementation,
// 5 finalization, 6 done, 7 cancelled
describe('pipelineBoard — sortable prop threading', () => {
  it('passes sortable=true to needs-you, backlog, and ready SortableTaskLists', () => {
    const wrapper = shallowMount(PipelineBoard)
    const lists = wrapper.findAllComponents(SortableTaskList)
    expect(lists[0].props('sortable')).toBe(true) // needs-you
    expect(lists[1].props('sortable')).toBe(true) // backlog
    expect(lists[2].props('sortable')).toBe(true) // ready
    wrapper.unmount()
  })

  it('passes sortable=false to plan_review, implementation, finalization, done, cancelled', () => {
    const wrapper = shallowMount(PipelineBoard)
    const lists = wrapper.findAllComponents(SortableTaskList)
    expect(lists[3].props('sortable')).toBe(false) // plan_review
    expect(lists[4].props('sortable')).toBe(false) // implementation
    expect(lists[5].props('sortable')).toBe(false) // finalization
    expect(lists[6].props('sortable')).toBe(false) // done
    expect(lists[7].props('sortable')).toBe(false) // cancelled
    wrapper.unmount()
  })
})

describe('pipelineBoard — column task ordering', () => {
  it('sorts non-sortable column tasks by updatedAt descending', () => {
    const older = makeTask('older', { currentStage: 'implementation', updatedAt: '2026-01-01T00:00:00Z' })
    const newer = makeTask('newer', { currentStage: 'implementation', updatedAt: '2026-06-01T00:00:00Z' })
    // tasksByStageMap delivers them in rank order (both rank=null → creation order)
    mockStageMap = { implementation: [older, newer] }

    const wrapper = shallowMount(PipelineBoard)
    // implementation column is at index 4
    const tasks = wrapper.findAllComponents(SortableTaskList)[4].props('tasks') as PipelineTask[]
    expect(tasks[0].id).toBe('newer')
    expect(tasks[1].id).toBe('older')
    wrapper.unmount()
  })

  it('preserves rank order for sortable columns (does not re-sort by activity)', () => {
    // rank-first has earlier updatedAt but lower rank → should stay first in ready
    const rankFirst = makeTask('rank-first', {
      currentStage: 'ready',
      rank: 100,
      updatedAt: '2026-01-01T00:00:00Z',
    })
    const rankSecond = makeTask('rank-second', {
      currentStage: 'ready',
      rank: 200,
      updatedAt: '2026-06-01T00:00:00Z',
    })
    // tasksByStageMap delivers them already rank-sorted
    mockStageMap = { ready: [rankFirst, rankSecond] }

    const wrapper = shallowMount(PipelineBoard)
    // ready column is at index 2
    const tasks = wrapper.findAllComponents(SortableTaskList)[2].props('tasks') as PipelineTask[]
    expect(tasks[0].id).toBe('rank-first')
    expect(tasks[1].id).toBe('rank-second')
    wrapper.unmount()
  })

  it('orders needs-you tasks from different stages by rank, not stage grouping', () => {
    // needsUser tasks aggregated from two stages; ranks interleave the stages
    const backlogHigh = makeTask('backlog-high', { currentStage: 'backlog', needsUser: true, rank: 300 })
    const readyLow = makeTask('ready-low', { currentStage: 'ready', needsUser: true, rank: 100 })
    const backlogLow = makeTask('backlog-low', { currentStage: 'backlog', needsUser: true, rank: 200 })
    mockStageMap = {
      backlog: [backlogLow, backlogHigh],
      ready: [readyLow],
    }

    const wrapper = shallowMount(PipelineBoard)
    // needs-you column is at index 0
    const tasks = wrapper.findAllComponents(SortableTaskList)[0].props('tasks') as PipelineTask[]
    expect(tasks.map((t: PipelineTask) => t.id)).toEqual(['ready-low', 'backlog-low', 'backlog-high'])
    wrapper.unmount()
  })

  it('sorts implementation column tasks with three tasks newest-first', () => {
    const t1 = makeTask('t1', { currentStage: 'implementation', updatedAt: '2026-03-01T00:00:00Z' })
    const t2 = makeTask('t2', { currentStage: 'implementation', updatedAt: '2026-06-01T00:00:00Z' })
    const t3 = makeTask('t3', { currentStage: 'implementation', updatedAt: '2026-01-01T00:00:00Z' })
    mockStageMap = { implementation: [t1, t2, t3] }

    const wrapper = shallowMount(PipelineBoard)
    const tasks = wrapper.findAllComponents(SortableTaskList)[4].props('tasks') as PipelineTask[]
    expect(tasks.map((t: PipelineTask) => t.id)).toEqual(['t2', 't1', 't3'])
    wrapper.unmount()
  })
})
