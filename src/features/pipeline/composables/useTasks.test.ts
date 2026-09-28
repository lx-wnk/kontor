import type { PipelineTask } from '@/types'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { byActivityDesc } from '@/features/pipeline/composables/useTasks'

function makeTask(id: string, updatedAt: string): PipelineTask {
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
    updatedAt,
    metadata: null,
    silverBullet: false,
    planMode: false,
    priority: 'medium',
    userId: null,
    rank: null,
  }
}

describe('byActivityDesc', () => {
  it('places the task with the newer updatedAt first', () => {
    const older = makeTask('a', '2026-01-01T00:00:00Z')
    const newer = makeTask('b', '2026-06-01T00:00:00Z')
    expect([older, newer].sort(byActivityDesc).map(t => t.id)).toEqual(['b', 'a'])
  })

  it('returns 0 for equal timestamps', () => {
    const ts = '2026-06-01T12:00:00Z'
    expect(byActivityDesc(makeTask('x', ts), makeTask('y', ts))).toBe(0)
  })

  it('sorts three tasks newest-first', () => {
    const tasks = [
      makeTask('mid', '2026-03-01T00:00:00Z'),
      makeTask('newest', '2026-06-01T00:00:00Z'),
      makeTask('oldest', '2026-01-01T00:00:00Z'),
    ]
    expect([...tasks].sort(byActivityDesc).map(t => t.id)).toEqual(['newest', 'mid', 'oldest'])
  })

  it('does not throw on a missing updatedAt and orders the valid timestamp first', () => {
    const valid = makeTask('valid', '2026-06-01T00:00:00Z')
    const missing = makeTask('missing', undefined as unknown as string)
    expect(() => [missing, valid].sort(byActivityDesc)).not.toThrow()
    expect([missing, valid].sort(byActivityDesc).map(t => t.id)).toEqual(['valid', 'missing'])
  })
})

function makeUpsertTask(id: string, title: string): PipelineTask {
  return { ...makeTask(id, '2026-01-01T00:00:00Z'), title }
}

describe('refreshTask', () => {
  let mod: typeof import('@/features/pipeline/composables/useTasks')

  beforeEach(async () => {
    vi.resetModules()
    mod = await import('@/features/pipeline/composables/useTasks')
  })

  afterEach(() => {
    vi.unstubAllGlobals()
  })

  it('adds the task when its id is unknown', async () => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue({
      ok: true,
      json: () => Promise.resolve(makeUpsertTask('new-1', 'New Task')),
    }))

    await mod.refreshTask('new-1')

    const { tasks } = mod.useTasks({ autoStart: false })
    expect(tasks.value.map(t => t.id)).toContain('new-1')
  })

  it('replaces the task when its id is already known', async () => {
    const { tasks } = mod.useTasks({ autoStart: false })
    tasks.value = [makeUpsertTask('existing-1', 'Old')]
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue({
      ok: true,
      json: () => Promise.resolve(makeUpsertTask('existing-1', 'Updated')),
    }))

    await mod.refreshTask('existing-1')

    expect(tasks.value).toHaveLength(1)
    expect(tasks.value[0].id).toBe('existing-1')
    expect(tasks.value[0].title).toBe('Updated')
  })
})

describe('findOrFetchTask', () => {
  let mod: typeof import('@/features/pipeline/composables/useTasks')

  beforeEach(async () => {
    vi.resetModules()
    mod = await import('@/features/pipeline/composables/useTasks')
  })

  afterEach(() => {
    vi.unstubAllGlobals()
  })

  it('returns the task from the store without fetching when already present', async () => {
    const { tasks } = mod.useTasks({ autoStart: false })
    tasks.value = [makeUpsertTask('t1', 'Cached')]
    const fetchSpy = vi.fn()
    vi.stubGlobal('fetch', fetchSpy)

    const result = await mod.findOrFetchTask('t1')

    expect(fetchSpy).not.toHaveBeenCalled()
    expect(result?.id).toBe('t1')
    expect(result?.title).toBe('Cached')
  })

  it('fetches and returns the task when not in the store', async () => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue({
      ok: true,
      json: () => Promise.resolve(makeUpsertTask('t2', 'Fetched')),
    }))

    const result = await mod.findOrFetchTask('t2')

    expect(result?.id).toBe('t2')
    expect(result?.title).toBe('Fetched')
  })

  it('returns null when the task cannot be found even after a fetch', async () => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue({ ok: false }))

    const result = await mod.findOrFetchTask('missing')

    expect(result).toBeNull()
  })

  it('propagates fetch network errors to the caller', async () => {
    vi.stubGlobal('fetch', vi.fn().mockRejectedValue(new Error('network down')))

    await expect(mod.findOrFetchTask('err')).rejects.toThrow('network down')
  })
})

describe('applyEvent', () => {
  let mod: typeof import('@/features/pipeline/composables/useTasks')

  beforeEach(async () => {
    vi.resetModules()
    mod = await import('@/features/pipeline/composables/useTasks')
  })

  afterEach(() => {
    vi.unstubAllGlobals()
    vi.restoreAllMocks()
  })

  it('logs a warning for unknown SSE event types', () => {
    const warnSpy = vi.spyOn(console, 'warn').mockImplementation(() => {})
    mod.applyEvent({ type: 'unexpected_future_event' as any, taskId: 'x' })
    expect(warnSpy).toHaveBeenCalledWith('[useTasks] unknown SSE event type:', 'unexpected_future_event')
  })

  it('stays silent for events other composables own on the shared stream', () => {
    const warnSpy = vi.spyOn(console, 'warn').mockImplementation(() => {})
    for (const type of ['schedule_changed', 'applications_changed', 'eval_drift'] as const)
      mod.applyEvent({ type, taskId: '' })
    expect(warnSpy).not.toHaveBeenCalled()
  })
})
