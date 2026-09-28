import type { PipelineTask, Project, Spawner } from '@/types'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { ref } from 'vue'
import { useProjects } from '@/composables/useProjects'
import { useSpawners } from '@/composables/useSpawners'
import { useTaskAssignment } from '@/features/pipeline/composables/useTaskAssignment'

vi.mock('@/composables/useProjects', () => ({ useProjects: vi.fn() }))
vi.mock('@/composables/useSpawners', () => ({ useSpawners: vi.fn() }))

function makeProject(id: string, overrides: Partial<Project> = {}): Project {
  return { id, slug: id, name: id, createdAt: '2026-01-01T00:00:00Z', updatedAt: '2026-01-01T00:00:00Z', ...overrides }
}

function makeSpawner(id: string, overrides: Partial<Spawner> = {}): Spawner {
  return {
    id,
    name: id,
    slug: id,
    command: 'claude',
    args: [],
    env: {},
    adapterType: 'claude',
    adapterConfig: {},
    builtIn: false,
    isDefault: false,
    createdAt: '2026-01-01T00:00:00Z',
    updatedAt: '2026-01-01T00:00:00Z',
    ...overrides,
  }
}

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
  vi.clearAllMocks()
  vi.mocked(useProjects).mockReturnValue({ projects: ref([]) } as unknown as ReturnType<typeof useProjects>)
  vi.mocked(useSpawners).mockReturnValue({ spawners: ref([]) } as unknown as ReturnType<typeof useSpawners>)
  vi.stubGlobal('fetch', vi.fn())
})

afterEach(() => {
  vi.unstubAllGlobals()
  vi.restoreAllMocks()
})

describe('useTaskAssignment — currentProject', () => {
  it('is null when the task has no projectId', () => {
    const task = ref(makeTask({ projectId: null }))
    const { currentProject } = useTaskAssignment(task)

    expect(currentProject.value).toBeNull()
  })

  it('resolves the matching project by id', () => {
    vi.mocked(useProjects).mockReturnValue({ projects: ref([makeProject('p1'), makeProject('p2')]) } as unknown as ReturnType<typeof useProjects>)
    const task = ref(makeTask({ projectId: 'p2' }))
    const { currentProject } = useTaskAssignment(task)

    expect(currentProject.value?.id).toBe('p2')
  })

  it('is null when projectId does not match any known project', () => {
    vi.mocked(useProjects).mockReturnValue({ projects: ref([makeProject('p1')]) } as unknown as ReturnType<typeof useProjects>)
    const task = ref(makeTask({ projectId: 'missing' }))
    const { currentProject } = useTaskAssignment(task)

    expect(currentProject.value).toBeNull()
  })
})

describe('useTaskAssignment — effectiveSpawner', () => {
  it('prefers the task\'s explicit spawnerId over the project default', () => {
    vi.mocked(useProjects).mockReturnValue({ projects: ref([makeProject('p1', { defaultSpawnerId: 's-default' })]) } as unknown as ReturnType<typeof useProjects>)
    vi.mocked(useSpawners).mockReturnValue({ spawners: ref([makeSpawner('s-default'), makeSpawner('s-explicit')]) } as unknown as ReturnType<typeof useSpawners>)
    const task = ref(makeTask({ projectId: 'p1', spawnerId: 's-explicit' }))
    const { effectiveSpawner } = useTaskAssignment(task)

    expect(effectiveSpawner.value?.id).toBe('s-explicit')
  })

  it('falls back to the project default when the task has none', () => {
    vi.mocked(useProjects).mockReturnValue({ projects: ref([makeProject('p1', { defaultSpawnerId: 's-default' })]) } as unknown as ReturnType<typeof useProjects>)
    vi.mocked(useSpawners).mockReturnValue({ spawners: ref([makeSpawner('s-default')]) } as unknown as ReturnType<typeof useSpawners>)
    const task = ref(makeTask({ projectId: 'p1', spawnerId: null }))
    const { effectiveSpawner } = useTaskAssignment(task)

    expect(effectiveSpawner.value?.id).toBe('s-default')
  })

  it('is null when neither the task nor the project specify a spawner', () => {
    const task = ref(makeTask({ projectId: null, spawnerId: null }))
    const { effectiveSpawner } = useTaskAssignment(task)

    expect(effectiveSpawner.value).toBeNull()
  })

  it('is null when the resolved id has no matching spawner', () => {
    const task = ref(makeTask({ spawnerId: 'ghost' }))
    const { effectiveSpawner } = useTaskAssignment(task)

    expect(effectiveSpawner.value).toBeNull()
  })
})

describe('useTaskAssignment — onProjectChange / onSpawnerChange', () => {
  it('sends a PATCH for projectId, treating an empty selection as null', async () => {
    vi.mocked(fetch).mockResolvedValue({ ok: true } as Response)
    const task = ref(makeTask({ id: 'task-1' }))
    const { onProjectChange, isAssigningProject } = useTaskAssignment(task)

    const pending = onProjectChange('')
    expect(isAssigningProject.value).toBe(true)
    await pending

    expect(fetch).toHaveBeenCalledWith('/api/tasks/task-1', expect.objectContaining({
      method: 'PATCH',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ projectId: null }),
    }))
    expect(isAssigningProject.value).toBe(false)
  })

  it('sends a PATCH for spawnerId with the selected value', async () => {
    vi.mocked(fetch).mockResolvedValue({ ok: true } as Response)
    const task = ref(makeTask({ id: 'task-1' }))
    const { onSpawnerChange, isAssigningSpawner } = useTaskAssignment(task)

    await onSpawnerChange('s-2')

    expect(fetch).toHaveBeenCalledWith('/api/tasks/task-1', expect.objectContaining({
      method: 'PATCH',
      body: JSON.stringify({ spawnerId: 's-2' }),
    }))
    expect(isAssigningSpawner.value).toBe(false)
  })

  it('surfaces the server error message on a failed PATCH', async () => {
    vi.mocked(fetch).mockResolvedValue({ ok: false, status: 400, json: async () => ({ error: 'project archived' }) } as Response)
    const task = ref(makeTask())
    const { onProjectChange, assignError } = useTaskAssignment(task)

    await onProjectChange('p1')

    expect(assignError.value).toBe('project archived')
  })

  it('falls back to an HTTP-status message when the error body cannot be parsed', async () => {
    vi.mocked(fetch).mockResolvedValue({ ok: false, status: 500, json: () => Promise.reject(new Error('bad json')) } as unknown as Response)
    const task = ref(makeTask())
    const { onProjectChange, assignError } = useTaskAssignment(task)

    await onProjectChange('p1')

    expect(assignError.value).toBe('HTTP 500')
  })

  it('clears a previous assignError before issuing a new request', async () => {
    vi.mocked(fetch)
      .mockResolvedValueOnce({ ok: false, status: 400, json: async () => ({ error: 'nope' }) } as Response)
      .mockResolvedValueOnce({ ok: true } as Response)
    const task = ref(makeTask())
    const { onProjectChange, assignError } = useTaskAssignment(task)

    await onProjectChange('p1')
    expect(assignError.value).toBe('nope')

    await onProjectChange('p2')
    expect(assignError.value).toBeNull()
  })

  it('is a no-op when there is no task', async () => {
    const task = ref<PipelineTask | null>(null)
    const { onProjectChange } = useTaskAssignment(task)

    await onProjectChange('p1')

    expect(fetch).not.toHaveBeenCalled()
  })
})
