import type { PipelineTask, Project } from '@/types'
import { mount, shallowMount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { nextTick } from 'vue'
import SortableTaskList from '@/features/pipeline/components/SortableTaskList.vue'
import TaskCard from '@/features/pipeline/components/TaskCard.vue'
import { reorderTask } from '@/features/pipeline/composables/useTasks'

// Capture useSortable callbacks so tests can trigger them manually.
let capturedOnStart: (() => void) | undefined
let capturedOnEnd: ((evt: { oldIndex?: number, newIndex?: number }) => void) | undefined
let capturedOptions: Record<string, unknown> = {}

vi.mock('@vueuse/integrations/useSortable', () => ({
  useSortable: (_el: unknown, _list: unknown, opts: Record<string, unknown>) => {
    capturedOnStart = opts?.onStart as () => void
    capturedOnEnd = opts?.onEnd as (evt: { oldIndex?: number, newIndex?: number }) => void
    capturedOptions = { ...opts }
    return undefined
  },
}))

// SortableTaskList only imports reorderTask — mock the whole module to avoid SSE side-effects.
vi.mock('@/features/pipeline/composables/useTasks', () => ({
  reorderTask: vi.fn(),
}))

beforeEach(() => {
  capturedOnStart = undefined
  capturedOnEnd = undefined
  capturedOptions = {}
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
    ...overrides,
  }
}

const emptyProjectById = new Map<string, Project>()

describe('sortableTaskList — drag guard', () => {
  it('resets list normally when a prop update arrives outside a drag', async () => {
    const taskA = makeTask('a')
    const taskB = makeTask('b')
    const wrapper = shallowMount(SortableTaskList, {
      props: { tasks: [taskA, taskB], projectById: emptyProjectById },
    })

    await wrapper.setProps({ tasks: [taskB, taskA] })
    await nextTick()

    const cards = wrapper.findAllComponents(TaskCard)
    expect(cards[0].props('task').id).toBe('b')
    expect(cards[1].props('task').id).toBe('a')

    wrapper.unmount()
  })

  it('does NOT reset list while a drag is in progress (onStart fired, onEnd not yet)', async () => {
    const taskA = makeTask('a')
    const taskB = makeTask('b')
    const wrapper = shallowMount(SortableTaskList, {
      props: { tasks: [taskA, taskB], projectById: emptyProjectById },
    })

    capturedOnStart?.()

    // SSE delivers new prop order during the drag
    await wrapper.setProps({ tasks: [taskB, taskA] })
    await nextTick()

    // Guard must have fired — list should still show original order
    const cards = wrapper.findAllComponents(TaskCard)
    expect(cards[0].props('task').id).toBe('a')
    expect(cards[1].props('task').id).toBe('b')

    wrapper.unmount()
  })

  it('resets list after drag ends (onEnd clears isDragging before returning)', async () => {
    const taskA = makeTask('a')
    const taskB = makeTask('b')
    const wrapper = shallowMount(SortableTaskList, {
      props: { tasks: [taskA, taskB], projectById: emptyProjectById },
    })

    capturedOnStart?.()
    // End with same position — still clears isDragging
    capturedOnEnd?.({ oldIndex: 0, newIndex: 0 })

    await wrapper.setProps({ tasks: [taskB, taskA] })
    await nextTick()

    const cards = wrapper.findAllComponents(TaskCard)
    expect(cards[0].props('task').id).toBe('b')
    expect(cards[1].props('task').id).toBe('a')

    wrapper.unmount()
  })

  it('skips reset when incoming order matches current (same IDs, same positions)', async () => {
    const taskA = makeTask('a')
    const taskB = makeTask('b')
    const wrapper = shallowMount(SortableTaskList, {
      props: { tasks: [taskA, taskB], projectById: emptyProjectById },
    })

    // New objects, same IDs, same order — simulates an SSE field-update with no rerank
    await wrapper.setProps({ tasks: [{ ...taskA, updatedAt: '2026-06-01T00:00:00Z' }, { ...taskB }] })
    await nextTick()

    const cards = wrapper.findAllComponents(TaskCard)
    expect(cards[0].props('task').id).toBe('a')
    expect(cards[1].props('task').id).toBe('b')

    wrapper.unmount()
  })
})

describe('sortableTaskList — sortable prop', () => {
  it('passes disabled:true to useSortable when sortable is false', () => {
    shallowMount(SortableTaskList, {
      props: { tasks: [], projectById: emptyProjectById, sortable: false },
    }).unmount()
    expect(capturedOptions.disabled).toBe(true)
  })

  it('passes disabled:false to useSortable when sortable is true (default)', () => {
    shallowMount(SortableTaskList, {
      props: { tasks: [], projectById: emptyProjectById },
    }).unmount()
    expect(capturedOptions.disabled).toBe(false)
  })

  it('forwards sortable prop to each TaskCard', () => {
    const wrapper = shallowMount(SortableTaskList, {
      props: { tasks: [makeTask('x')], projectById: emptyProjectById, sortable: false },
    })
    const card = wrapper.findAllComponents(TaskCard)[0]
    expect(card.props('sortable')).toBe(false)
    wrapper.unmount()
  })
})

describe('sortableTaskList — keyboard reorder (A11Y-2)', () => {
  it('marks only the first and last TaskCard as boundary rows', () => {
    const wrapper = shallowMount(SortableTaskList, {
      props: { tasks: [makeTask('a'), makeTask('b'), makeTask('c')], projectById: emptyProjectById },
    })
    const cards = wrapper.findAllComponents(TaskCard)
    expect(cards[0].props('isFirst')).toBe(true)
    expect(cards[0].props('isLast')).toBe(false)
    expect(cards[1].props('isFirst')).toBe(false)
    expect(cards[1].props('isLast')).toBe(false)
    expect(cards[2].props('isFirst')).toBe(false)
    expect(cards[2].props('isLast')).toBe(true)
    wrapper.unmount()
  })

  it('clicking "Move down" on the middle TaskCard reorders it after its next neighbor', async () => {
    const wrapper = mount(SortableTaskList, {
      props: { tasks: [makeTask('a'), makeTask('b'), makeTask('c')], projectById: emptyProjectById },
    })

    await wrapper.findAll('[data-testid="task-move-down"]')[1].trigger('click')

    expect(reorderTask).toHaveBeenCalledWith('b', 'c', null)
    const cards = wrapper.findAllComponents(TaskCard)
    expect(cards.map(c => c.props('task').id)).toEqual(['a', 'c', 'b'])
    wrapper.unmount()
  })

  it('clicking "Move up" on the last TaskCard reorders it before its previous neighbor', async () => {
    const wrapper = mount(SortableTaskList, {
      props: { tasks: [makeTask('a'), makeTask('b'), makeTask('c')], projectById: emptyProjectById },
    })

    await wrapper.findAll('[data-testid="task-move-up"]')[2].trigger('click')

    expect(reorderTask).toHaveBeenCalledWith('c', 'a', 'b')
    const cards = wrapper.findAllComponents(TaskCard)
    expect(cards.map(c => c.props('task').id)).toEqual(['a', 'c', 'b'])
    wrapper.unmount()
  })

  it('the first row\'s "Move up" button is disabled and the last row\'s "Move down" is disabled', () => {
    const wrapper = mount(SortableTaskList, {
      props: { tasks: [makeTask('a'), makeTask('b')], projectById: emptyProjectById },
    })

    const moveUpButtons = wrapper.findAll('[data-testid="task-move-up"]')
    const moveDownButtons = wrapper.findAll('[data-testid="task-move-down"]')
    expect(moveUpButtons[0].attributes('disabled')).toBeDefined()
    expect(moveDownButtons[1].attributes('disabled')).toBeDefined()
    expect(moveUpButtons[1].attributes('disabled')).toBeUndefined()
    expect(moveDownButtons[0].attributes('disabled')).toBeUndefined()
    wrapper.unmount()
  })
})
