import type { NextKind, NextThing } from '../composables/useNextThing'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { isPutAway, markSeen, useAttentionMarks, visibleNextThings } from '../composables/useAttentionMarks'

const toastError = vi.hoisted(() => vi.fn())
vi.mock('@/composables/useToast', () => ({ toast: { error: toastError } }))

function item(kind: NextKind, key: string, since: string): NextThing {
  return { kind, key, since } as NextThing
}

class MockEventSource {
  static instances: MockEventSource[] = []
  onmessage: ((e: MessageEvent) => void) | null = null
  onerror: ((e: Event) => void) | null = null
  readyState = 1
  constructor(public url: string) {
    MockEventSource.instances.push(this)
  }

  close() {}
}

const T = '2026-11-19T10:00:00Z'

describe('isPutAway', () => {
  const marks = new Map([['permission:a', T]])

  it('hides when the mark covers since', () => {
    expect(isPutAway(item('permission', 'permission:a', T), marks, null)).toBe(true)
  })

  it('shows when since is newer than the mark', () => {
    expect(isPutAway(item('permission', 'permission:a', '2026-11-19T10:00:01Z'), marks, null)).toBe(false)
  })

  it('shows without a mark', () => {
    expect(isPutAway(item('permission', 'permission:b', T), marks, null)).toBe(false)
  })

  it('compares a nanosecond mark by instant', () => {
    const nanos = new Map([['permission:a', '2026-11-19T10:00:00.000000000Z']])
    expect(isPutAway(item('permission', 'permission:a', T), nanos, null)).toBe(true)
  })

  it('does not hide an item whose since is unparseable', () => {
    expect(isPutAway(item('permission', 'permission:a', 'garbage'), marks, null)).toBe(false)
  })

  it('hides failed and finished at or before the baseline', () => {
    const none = new Map<string, string>()
    expect(isPutAway(item('finished', 'finished:1', '2026-11-19T09:59:59Z'), none, T)).toBe(true)
    expect(isPutAway(item('finished', 'finished:2', '2026-11-19T10:00:01Z'), none, T)).toBe(false)
    expect(isPutAway(item('failed', 'failed:1', T), none, T)).toBe(true)
    expect(isPutAway(item('permission', 'permission:1', '2026-11-19T09:00:00Z'), none, T)).toBe(false)
  })

  it('shows failed and finished with an unparseable since once the baseline is loaded', () => {
    expect(isPutAway(item('failed', 'failed:1', 'garbage'), new Map(), T)).toBe(false)
  })

  it('hides failed and finished while the baseline is not loaded', () => {
    const none = new Map<string, string>()
    expect(isPutAway(item('finished', 'finished:1', T), none, null)).toBe(true)
    expect(isPutAway(item('failed', 'failed:1', T), none, null)).toBe(true)
    expect(isPutAway(item('permission', 'permission:1', T), none, null)).toBe(false)
  })
})

describe('visibleNextThings', () => {
  it('drops only the marked item', () => {
    const list = [item('permission', 'permission:a', T), item('plan', 'plan:b', T), item('question', 'question:c', T)]
    const out = visibleNextThings(list, new Map([['plan:b', T]]), null)
    expect(out.map(t => t.key)).toEqual(['permission:a', 'question:c'])
  })
})

describe('markSeen', () => {
  const { marks } = useAttentionMarks()

  beforeEach(() => {
    toastError.mockClear()
  })

  afterEach(() => {
    vi.unstubAllGlobals()
  })

  it('sets the mark before the request resolves and PUTs it', async () => {
    const fetchMock = vi.fn().mockResolvedValue({ ok: true })
    vi.stubGlobal('fetch', fetchMock)
    const p = markSeen('failed:s1', T)
    expect(marks.value.get('failed:s1')).toBe(T)
    await p
    expect(fetchMock).toHaveBeenCalledTimes(1)
    const [url, init] = fetchMock.mock.calls[0]
    expect(url).toBe('/api/attention/marks')
    expect(init.method).toBe('PUT')
    expect(init.body).toBe('{"key":"failed:s1","seenThrough":"2026-11-19T10:00:00Z"}')
  })

  it('restores the previous map and toasts on a server error', async () => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue({ ok: false, status: 500 }))
    await markSeen('failed:s2', T)
    expect(marks.value.has('failed:s2')).toBe(false)
    expect(toastError).toHaveBeenCalledTimes(1)
    expect(toastError).toHaveBeenCalledWith('Could not put that away')
  })

  it('restores the previous map and toasts when fetch throws', async () => {
    vi.stubGlobal('fetch', vi.fn().mockRejectedValue(new Error('offline')))
    await markSeen('failed:s3', T)
    expect(marks.value.has('failed:s3')).toBe(false)
    expect(toastError).toHaveBeenCalledTimes(1)
  })
})

describe('stream', () => {
  afterEach(() => {
    vi.unstubAllGlobals()
  })

  it('applies an attention_marked event and loads the baseline', async () => {
    MockEventSource.instances = []
    vi.stubGlobal('EventSource', MockEventSource)
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue({
      ok: true,
      json: () => Promise.resolve({ baseline: T, marks: [{ key: 'seed:1', seenThrough: T }] }),
    }))
    const { marks, baseline, start } = useAttentionMarks()
    start()
    await vi.waitFor(() => expect(baseline.value).toBe(T))
    expect(marks.value.get('seed:1')).toBe(T)
    MockEventSource.instances.at(-1)!.onmessage?.({
      data: JSON.stringify({ type: 'attention_marked', mark: { key: 'k:1', seenThrough: '2026-11-19T11:00:00Z' } }),
    } as MessageEvent)
    expect(marks.value.get('k:1')).toBe('2026-11-19T11:00:00Z')
  })
})
