import type { PipelineTask, TaskDependency } from '@/types'
import { flushPromises } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { ref } from 'vue'
import { useTaskDependencies } from '@/features/pipeline/composables/useTaskDependencies'
import {
  addTaskDependency,
  fetchDependencies,
  fetchDependents,
  removeTaskDependency,
} from '@/features/pipeline/composables/useTasks'

vi.mock('@/features/pipeline/composables/useTasks', () => ({
  fetchDependencies: vi.fn(),
  fetchDependents: vi.fn(),
  addTaskDependency: vi.fn(),
  removeTaskDependency: vi.fn(),
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

function makeDependency(overrides: Partial<TaskDependency> = {}): TaskDependency {
  return {
    id: 'dep-1',
    taskId: 'task-1',
    taskTitle: 'Task',
    dependsOnId: 'dep-99',
    dependsOnTitle: 'Other Task',
    dependsOnStage: 'done',
    requiredStage: 'done',
    onCancelAction: 'on_hold',
    ...overrides,
  }
}

beforeEach(() => {
  vi.clearAllMocks()
  vi.mocked(fetchDependencies).mockResolvedValue([])
  vi.mocked(fetchDependents).mockResolvedValue([])
  vi.mocked(addTaskDependency).mockResolvedValue(makeDependency())
  vi.mocked(removeTaskDependency).mockResolvedValue(undefined)
})

afterEach(() => {
  vi.restoreAllMocks()
})

describe('useTaskDependencies — load', () => {
  it('loads dependencies and dependents immediately for a set task', async () => {
    vi.mocked(fetchDependencies).mockResolvedValue([makeDependency({ id: 'd1' })])
    vi.mocked(fetchDependents).mockResolvedValue([makeDependency({ id: 'd2' })])

    const task = ref<PipelineTask | null>(makeTask())
    const deps = useTaskDependencies(task)
    await flushPromises()

    expect(fetchDependencies).toHaveBeenCalledWith('task-1')
    expect(fetchDependents).toHaveBeenCalledWith('task-1')
    expect(deps.dependencies.value).toEqual([expect.objectContaining({ id: 'd1' })])
    expect(deps.dependents.value).toEqual([expect.objectContaining({ id: 'd2' })])
  })

  it('does not fetch when there is no task', async () => {
    const task = ref<PipelineTask | null>(null)
    useTaskDependencies(task)
    await flushPromises()

    expect(fetchDependencies).not.toHaveBeenCalled()
    expect(fetchDependents).not.toHaveBeenCalled()
  })

  it('reloads when the task id changes', async () => {
    const task = ref<PipelineTask | null>(makeTask({ id: 'task-1' }))
    useTaskDependencies(task)
    await flushPromises()
    expect(fetchDependencies).toHaveBeenCalledTimes(1)

    task.value = makeTask({ id: 'task-2' })
    await flushPromises()

    expect(fetchDependencies).toHaveBeenCalledTimes(2)
    expect(fetchDependencies).toHaveBeenLastCalledWith('task-2')
  })

  it('surfaces a load error without throwing', async () => {
    vi.mocked(fetchDependencies).mockRejectedValue(new Error('network down'))

    const task = ref<PipelineTask | null>(makeTask())
    const deps = useTaskDependencies(task)
    await flushPromises()

    expect(deps.depError.value).toBe('Failed to load dependencies')
  })
})

describe('useTaskDependencies — handleAddDependency', () => {
  it('is a no-op when newDepId is blank', async () => {
    const task = ref<PipelineTask | null>(makeTask())
    const deps = useTaskDependencies(task)
    await flushPromises()

    deps.newDepId.value = '   '
    await deps.handleAddDependency()

    expect(addTaskDependency).not.toHaveBeenCalled()
  })

  it('is a no-op when there is no task', async () => {
    const task = ref<PipelineTask | null>(null)
    const deps = useTaskDependencies(task)
    deps.newDepId.value = 'dep-9'
    await deps.handleAddDependency()

    expect(addTaskDependency).not.toHaveBeenCalled()
  })

  it('trims the id, adds the dependency with the selected stage/action, clears the field and reloads', async () => {
    const task = ref<PipelineTask | null>(makeTask())
    const deps = useTaskDependencies(task)
    await flushPromises()

    deps.newDepId.value = '  dep-99  '
    deps.newDepStage.value = 'cancelled'
    deps.newDepCancelAction.value = 'cancel'
    vi.mocked(fetchDependencies).mockResolvedValue([makeDependency({ id: 'dep-99' })])

    await deps.handleAddDependency()

    expect(addTaskDependency).toHaveBeenCalledWith('task-1', 'dep-99', 'cancelled', 'cancel')
    expect(deps.newDepId.value).toBe('')
    expect(deps.isAddingDep.value).toBe(false)
    expect(deps.depError.value).toBe('')
    expect(deps.dependencies.value).toEqual([expect.objectContaining({ id: 'dep-99' })])
  })

  it('surfaces the server error message and keeps the entered id on failure', async () => {
    vi.mocked(addTaskDependency).mockRejectedValue(new Error('circular dependency'))

    const task = ref<PipelineTask | null>(makeTask())
    const deps = useTaskDependencies(task)
    await flushPromises()
    deps.newDepId.value = 'dep-1'

    await deps.handleAddDependency()

    expect(deps.depError.value).toBe('circular dependency')
    expect(deps.newDepId.value).toBe('dep-1')
    expect(deps.isAddingDep.value).toBe(false)
  })
})

describe('useTaskDependencies — handleRemoveDependency', () => {
  it('removes the dependency and reloads the lists', async () => {
    const task = ref<PipelineTask | null>(makeTask())
    const deps = useTaskDependencies(task)
    await flushPromises()
    vi.mocked(fetchDependencies).mockResolvedValue([])

    await deps.handleRemoveDependency('dep-1')

    expect(removeTaskDependency).toHaveBeenCalledWith('task-1', 'dep-1')
    expect(fetchDependencies).toHaveBeenCalled()
  })

  it('surfaces the server error message on failure', async () => {
    vi.mocked(removeTaskDependency).mockRejectedValue(new Error('cannot remove'))

    const task = ref<PipelineTask | null>(makeTask())
    const deps = useTaskDependencies(task)
    await flushPromises()

    await deps.handleRemoveDependency('dep-1')

    expect(deps.depError.value).toBe('cannot remove')
  })

  it('is a no-op when there is no task', async () => {
    const task = ref<PipelineTask | null>(null)
    const deps = useTaskDependencies(task)

    await deps.handleRemoveDependency('dep-1')

    expect(removeTaskDependency).not.toHaveBeenCalled()
  })
})
