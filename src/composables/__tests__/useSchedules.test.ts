import type { ScheduleView } from '../useSchedules'
import { mount } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { defineComponent, nextTick } from 'vue'

class MockEventSource {
  static instances: MockEventSource[] = []
  onmessage: ((e: MessageEvent) => void) | null = null
  onerror: ((e: Event) => void) | null = null
  readyState = 0
  static CONNECTING = 0
  static OPEN = 1
  static CLOSED = 2

  constructor(public url: string) {
    MockEventSource.instances.push(this)
  }

  close() {
    this.readyState = 2
  }
}

let mod: typeof import('../useSchedules')

function makeSchedule(id: string, name: string, overrides: Partial<ScheduleView> = {}): ScheduleView {
  return {
    id,
    name,
    enabled: true,
    cronExpr: '0 9 * * 1-5',
    human: 'At 09:00 AM, Monday through Friday',
    timezone: 'UTC',
    catchup: 'none',
    runMode: 'job',
    skippedCount: 0,
    slugPrefix: 'sched',
    title: 'Test Task',
    cwd: '/tmp',
    priority: 'medium',
    silverBullet: false,
    maxIterations: 20,
    createdAt: '2026-01-01T00:00:00Z',
    updatedAt: '2026-01-01T00:00:00Z',
    applications: [],
    ...overrides,
  }
}

beforeEach(async () => {
  MockEventSource.instances = []
  vi.stubGlobal('EventSource', MockEventSource)
  vi.stubGlobal('fetch', vi.fn().mockResolvedValue({
    ok: true,
    json: () => Promise.resolve([makeSchedule('s1', 'Daily')]),
  }))
  vi.resetModules()
  mod = await import('../useSchedules')
})

afterEach(() => {
  vi.unstubAllGlobals()
})

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

describe('useSchedules', () => {
  it('fetches /api/schedules on first subscribe and populates list', async () => {
    const { result, wrapper } = withSetup(() => mod.useSchedules())
    await Promise.resolve()
    await Promise.resolve()
    await nextTick()

    expect(fetch).toHaveBeenCalledWith('/api/schedules')
    expect(result.schedules.value).toHaveLength(1)
    expect(result.schedules.value[0].name).toBe('Daily')
    wrapper.unmount()
  })

  it('opens an EventSource on /api/tasks/stream', async () => {
    const { wrapper } = withSetup(() => mod.useSchedules())
    await nextTick()
    expect(MockEventSource.instances).toHaveLength(1)
    expect(MockEventSource.instances[0].url).toBe('/api/tasks/stream')
    wrapper.unmount()
  })

  it('re-fetches on schedule_changed SSE event', async () => {
    const fetchMock = vi.fn()
      .mockResolvedValueOnce({ ok: true, json: () => Promise.resolve([makeSchedule('s1', 'Daily')]) })
      .mockResolvedValueOnce({ ok: true, json: () => Promise.resolve([makeSchedule('s1', 'Daily'), makeSchedule('s2', 'Weekly')]) })
    vi.stubGlobal('fetch', fetchMock)

    const { result, wrapper } = withSetup(() => mod.useSchedules())
    await Promise.resolve()
    await Promise.resolve()
    await nextTick()

    expect(result.schedules.value).toHaveLength(1)

    const es = MockEventSource.instances[0]
    es.onmessage?.(new MessageEvent('message', {
      data: JSON.stringify({ type: 'schedule_changed' }),
    }))

    await Promise.resolve()
    await Promise.resolve()
    await nextTick()

    expect(result.schedules.value).toHaveLength(2)
    wrapper.unmount()
  })

  it('createSchedule POSTs to /api/schedules and prepends result', async () => {
    const { result, wrapper } = withSetup(() => mod.useSchedules())
    await Promise.resolve()
    await Promise.resolve()
    await nextTick()

    const newSchedule = makeSchedule('s2', 'Weekly')
    const fetchMock = vi.fn().mockResolvedValue({
      ok: true,
      json: () => Promise.resolve(newSchedule),
    })
    vi.stubGlobal('fetch', fetchMock)

    await mod.createSchedule({
      name: 'Weekly',
      nlText: 'every monday at 9am',
      slugPrefix: 'sched-weekly',
      title: 'Weekly Task',
      cwd: '/tmp',
    })

    expect(fetchMock).toHaveBeenCalledWith('/api/schedules', expect.objectContaining({
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
    }))
    expect(result.schedules.value[0].id).toBe('s2')
    wrapper.unmount()
  })

  it('updateSchedule PATCHes /api/schedules/:id', async () => {
    const { result, wrapper } = withSetup(() => mod.useSchedules())
    await Promise.resolve()
    await Promise.resolve()
    await nextTick()

    const updated = makeSchedule('s1', 'Daily Updated')
    const fetchMock = vi.fn().mockResolvedValue({
      ok: true,
      json: () => Promise.resolve(updated),
    })
    vi.stubGlobal('fetch', fetchMock)

    await mod.updateSchedule('s1', { name: 'Daily Updated' })

    expect(fetchMock).toHaveBeenCalledWith('/api/schedules/s1', expect.objectContaining({
      method: 'PATCH',
    }))
    expect(result.schedules.value[0].name).toBe('Daily Updated')
    wrapper.unmount()
  })

  it('deleteSchedule issues DELETE /api/schedules/:id and removes entry', async () => {
    const { result, wrapper } = withSetup(() => mod.useSchedules())
    await Promise.resolve()
    await Promise.resolve()
    await nextTick()

    expect(result.schedules.value).toHaveLength(1)

    const fetchMock = vi.fn().mockResolvedValue({ ok: true, json: () => Promise.resolve({}) })
    vi.stubGlobal('fetch', fetchMock)

    await mod.deleteSchedule('s1')

    expect(fetchMock).toHaveBeenCalledWith('/api/schedules/s1', { method: 'DELETE' })
    expect(result.schedules.value).toHaveLength(0)
    wrapper.unmount()
  })

  it('runScheduleNow POSTs to /api/schedules/:id/run-now', async () => {
    const fetchMock = vi.fn().mockResolvedValue({
      ok: true,
      json: () => Promise.resolve({ taskId: 'task-123' }),
    })
    vi.stubGlobal('fetch', fetchMock)

    const res = await mod.runScheduleNow('s1')
    expect(fetchMock).toHaveBeenCalledWith('/api/schedules/s1/run-now', { method: 'POST' })
    expect(res.taskId).toBe('task-123')
  })

  it('previewSchedule POSTs to /api/schedules/preview', async () => {
    const fetchMock = vi.fn().mockResolvedValue({
      ok: true,
      json: () => Promise.resolve({
        cronExpr: '0 9 * * 1-5',
        human: 'At 09:00 AM, Monday through Friday',
        timezone: 'UTC',
        nextRuns: ['2026-01-05T09:00:00Z'],
      }),
    })
    vi.stubGlobal('fetch', fetchMock)

    const preview = await mod.previewSchedule({ nlText: 'every weekday at 9am' })

    expect(fetchMock).toHaveBeenCalledWith('/api/schedules/preview', expect.objectContaining({
      method: 'POST',
    }))
    expect(preview.cronExpr).toBe('0 9 * * 1-5')
    expect(preview.nextRuns).toHaveLength(1)
  })

  it('previewSchedule throws on 422 response', async () => {
    const fetchMock = vi.fn().mockResolvedValue({
      ok: false,
      status: 422,
      json: () => Promise.resolve({ error: 'unparseable phrase' }),
    })
    vi.stubGlobal('fetch', fetchMock)

    await expect(mod.previewSchedule({ nlText: 'gibberish' })).rejects.toThrow('unparseable phrase')
  })

  it('autoStart:false consumer does not decrement subscriberCount on unmount', async () => {
    // Active consumer starts the stream
    const { result: active, wrapper: activeWrapper } = withSetup(() => mod.useSchedules())
    await Promise.resolve()
    await Promise.resolve()
    await nextTick()

    expect(MockEventSource.instances).toHaveLength(1)
    expect(active.schedules.value).toHaveLength(1)

    // Passive consumer — never starts the stream
    const { wrapper: passiveWrapper } = withSetup(() => mod.useSchedules({ autoStart: false }))
    await nextTick()

    // No second EventSource created — shared stream reused
    expect(MockEventSource.instances).toHaveLength(1)

    // Unmount the passive consumer
    passiveWrapper.unmount()
    await nextTick()

    // Stream must still be alive — EventSource not closed
    const es = MockEventSource.instances[0]
    expect(es.readyState).not.toBe(2)

    // Active consumer still works: SSE event still triggers re-fetch
    const fetchMock = vi.fn().mockResolvedValue({
      ok: true,
      json: () => Promise.resolve([makeSchedule('s1', 'Daily'), makeSchedule('s2', 'Weekly')]),
    })
    vi.stubGlobal('fetch', fetchMock)

    es.onmessage?.(new MessageEvent('message', {
      data: JSON.stringify({ type: 'schedule_changed' }),
    }))
    await Promise.resolve()
    await Promise.resolve()
    await nextTick()

    expect(active.schedules.value).toHaveLength(2)
    activeWrapper.unmount()
  })

  it('createSchedule throws on non-ok response', async () => {
    const fetchMock = vi.fn().mockResolvedValue({
      ok: false,
      status: 400,
      json: () => Promise.resolve({ error: 'invalid body' }),
    })
    vi.stubGlobal('fetch', fetchMock)

    await expect(mod.createSchedule({ name: 'x', slugPrefix: 'x', title: 'x', cwd: '/tmp' })).rejects.toThrow('invalid body')
  })

  it('schedule_changed with a payload for a listed schedule updates it without fetching', async () => {
    const { result, wrapper } = withSetup(() => mod.useSchedules())
    await Promise.resolve()
    await Promise.resolve()
    await nextTick()

    expect(result.schedules.value).toHaveLength(1)

    const fetchSpy = vi.fn()
    vi.stubGlobal('fetch', fetchSpy)

    const updated = makeSchedule('s1', 'Daily Updated', { updatedAt: '2026-06-01T00:00:00Z' })
    const es = MockEventSource.instances[0]
    es.onmessage?.(new MessageEvent('message', {
      data: JSON.stringify({ type: 'schedule_changed', taskId: 's1', payload: updated }),
    }))

    await nextTick()

    expect(fetchSpy).not.toHaveBeenCalled()
    expect(result.schedules.value).toHaveLength(1)
    expect(result.schedules.value[0].name).toBe('Daily Updated')
    wrapper.unmount()
  })

  it('schedule_changed with a payload for an unlisted schedule re-fetches instead of inserting it', async () => {
    const { result, wrapper } = withSetup(() => mod.useSchedules())
    await Promise.resolve()
    await Promise.resolve()
    await nextTick()

    const fetchRefetch = vi.fn().mockResolvedValue({
      ok: true,
      json: () => Promise.resolve([makeSchedule('s1', 'Daily')]),
    })
    vi.stubGlobal('fetch', fetchRefetch)

    const es = MockEventSource.instances[0]
    es.onmessage?.(new MessageEvent('message', {
      data: JSON.stringify({ type: 'schedule_changed', taskId: 'foreign', payload: makeSchedule('foreign', 'Not mine') }),
    }))

    await Promise.resolve()
    await Promise.resolve()
    await nextTick()

    expect(fetchRefetch).toHaveBeenCalledWith('/api/schedules')
    expect(result.schedules.value.map(s => s.id)).toEqual(['s1'])
    wrapper.unmount()
  })
})
