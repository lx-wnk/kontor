import type { MockInstance } from 'vitest'
import type { WorkspaceLayout } from './layout'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { DEFAULT_LAYOUT, serializeLayout } from './layout'

function settingsResponse(value: string) {
  return new Response(JSON.stringify([{ key: 'workspace.layout', value }]), { status: 200 })
}

function deferred() {
  let resolve!: (res: Response) => void
  const promise = new Promise<Response>((r) => {
    resolve = r
  })
  return { promise, resolve }
}

function withoutFirst(n: number): WorkspaceLayout {
  return { ...DEFAULT_LAYOUT, pages: [{ ...DEFAULT_LAYOUT.pages[0], tiles: DEFAULT_LAYOUT.pages[0].tiles.slice(n) }] }
}

function patchedValues(fetch: MockInstance<typeof globalThis.fetch>): string[] {
  return fetch.mock.calls.filter(([, init]) => init?.method === 'PATCH').map(([, init]) => JSON.parse(init!.body as string).value)
}

const settle = () => new Promise(resolve => setTimeout(resolve, 0))

async function fresh() {
  vi.resetModules()
  return (await import('./useWorkspace')).useWorkspace()
}

describe('useWorkspace', () => {
  beforeEach(() => vi.restoreAllMocks())

  it('shows the built-in layout when nothing is stored', async () => {
    vi.spyOn(globalThis, 'fetch').mockResolvedValueOnce(settingsResponse(''))
    const ws = await fresh()
    await ws.load()
    expect(ws.layout.value).toEqual(DEFAULT_LAYOUT)
    expect(ws.locked.value).toBeNull()
  })

  // A parse bug must not destroy what the operator built: editing locks, so
  // nothing writes over the stored value until they choose Reset.
  it('locks editing when the stored layout is unreadable', async () => {
    vi.spyOn(globalThis, 'fetch').mockResolvedValueOnce(settingsResponse('{broken'))
    const ws = await fresh()
    await ws.load()
    expect(ws.layout.value).toEqual(DEFAULT_LAYOUT)
    expect(ws.locked.value).toMatchObject({ kind: 'unreadable', message: expect.stringMatching(/could not be read/i) })
  })

  it('saves with a PATCH and keeps the change on screen when the save fails', async () => {
    const fetch = vi.spyOn(globalThis, 'fetch')
      .mockResolvedValueOnce(settingsResponse(''))
      .mockResolvedValueOnce(new Response('{"error":"bad request"}', { status: 400 }))
    const ws = await fresh()
    await ws.load()
    const next = { ...DEFAULT_LAYOUT, pages: [{ ...DEFAULT_LAYOUT.pages[0], tiles: [] }] }
    expect(await ws.save(next)).toBe(true)
    await vi.waitFor(() => expect(ws.saveError.value).toMatch(/not saved/i))
    expect(fetch).toHaveBeenLastCalledWith('/api/settings/workspace.layout', expect.objectContaining({
      method: 'PATCH',
      body: JSON.stringify({ value: serializeLayout(next) }),
    }))
    expect(ws.layout.value).toEqual(next)
  })

  it('refuses to save while locked', async () => {
    const fetch = vi.spyOn(globalThis, 'fetch').mockResolvedValueOnce(settingsResponse('{broken'))
    const ws = await fresh()
    await ws.load()
    expect(await ws.save(DEFAULT_LAYOUT)).toBe(false)
    expect(fetch).toHaveBeenCalledTimes(1)
  })

  // An edit on the built-in layout before the stored one arrives would write over the stored one.
  it('refuses to save before the stored layout has loaded', async () => {
    let answer!: (res: Response) => void
    const pending = new Promise<Response>((resolve) => {
      answer = resolve
    })
    const fetch = vi.spyOn(globalThis, 'fetch').mockReturnValueOnce(pending)
    const ws = await fresh()
    const loading = ws.load()
    const next = { ...DEFAULT_LAYOUT, pages: [{ ...DEFAULT_LAYOUT.pages[0], tiles: [] }] }
    expect(await ws.save(next)).toBe(false)
    expect(ws.layout.value).toEqual(DEFAULT_LAYOUT)
    answer(settingsResponse(''))
    await loading
    expect(fetch).toHaveBeenCalledTimes(1)
    expect(fetch).not.toHaveBeenCalledWith('/api/settings/workspace.layout', expect.anything())
  })

  it('locks editing when the settings endpoint returns a non-ok response', async () => {
    vi.spyOn(globalThis, 'fetch').mockResolvedValueOnce(new Response('', { status: 500 }))
    const ws = await fresh()
    await ws.load()
    expect(ws.layout.value).toEqual(DEFAULT_LAYOUT)
    expect(ws.locked.value).toMatchObject({ kind: 'unloaded', message: expect.stringMatching(/could not be loaded/i) })
  })

  it('locks editing when the settings endpoint rejects (network error)', async () => {
    vi.spyOn(globalThis, 'fetch').mockRejectedValueOnce(new Error('network error'))
    const ws = await fresh()
    await ws.load()
    expect(ws.layout.value).toEqual(DEFAULT_LAYOUT)
    expect(ws.locked.value).toMatchObject({ kind: 'unloaded', message: expect.stringMatching(/could not be loaded/i) })
  })

  it('retries a 429 with Retry-After and shows the stored layout once it succeeds', async () => {
    vi.useFakeTimers()
    const stored = { ...DEFAULT_LAYOUT, pages: [{ ...DEFAULT_LAYOUT.pages[0], tiles: [] }] }
    const fetch = vi.spyOn(globalThis, 'fetch')
      .mockResolvedValueOnce(new Response('', { status: 429, headers: { 'Retry-After': '1' } }))
      .mockResolvedValueOnce(settingsResponse(serializeLayout(stored)))
    const ws = await fresh()
    const loadPromise = ws.load()
    await vi.advanceTimersByTimeAsync(1000)
    await loadPromise
    expect(fetch).toHaveBeenCalledTimes(2)
    expect(ws.layout.value).toEqual(stored)
    expect(ws.locked.value).toBeNull()
    vi.useRealTimers()
  })

  it('locks editing after a 4th consecutive 429', async () => {
    vi.useFakeTimers()
    const fetch = vi.spyOn(globalThis, 'fetch').mockResolvedValue(new Response('', { status: 429 }))
    const ws = await fresh()
    const loadPromise = ws.load()
    await vi.advanceTimersByTimeAsync(3000)
    await loadPromise
    expect(fetch).toHaveBeenCalledTimes(4)
    expect(ws.layout.value).toEqual(DEFAULT_LAYOUT)
    expect(ws.locked.value).toMatchObject({ kind: 'unloaded', message: expect.stringMatching(/could not be loaded/i) })
    vi.useRealTimers()
  })

  it('does not retry a 500', async () => {
    const fetch = vi.spyOn(globalThis, 'fetch').mockResolvedValueOnce(new Response('', { status: 500 }))
    const ws = await fresh()
    await ws.load()
    expect(fetch).toHaveBeenCalledTimes(1)
    expect(ws.locked.value).toMatchObject({ kind: 'unloaded', message: expect.stringMatching(/could not be loaded/i) })
  })

  it('retries a 429 save and clears saveError once it succeeds', async () => {
    vi.useFakeTimers()
    const fetch = vi.spyOn(globalThis, 'fetch')
      .mockResolvedValueOnce(settingsResponse(''))
      .mockResolvedValueOnce(new Response('', { status: 429, headers: { 'Retry-After': '1' } }))
      .mockResolvedValueOnce(new Response('', { status: 200 }))
    const ws = await fresh()
    await ws.load()
    const next = { ...DEFAULT_LAYOUT, pages: [{ ...DEFAULT_LAYOUT.pages[0], tiles: [] }] }
    expect(await ws.save(next)).toBe(true)
    await vi.advanceTimersByTimeAsync(1000)
    await vi.waitFor(() => expect(fetch).toHaveBeenCalledTimes(3))
    expect(ws.saveError.value).toBeNull()
    vi.useRealTimers()
  })

  it('reports a save failure after a 4th consecutive 429', async () => {
    vi.useFakeTimers()
    const fetch = vi.spyOn(globalThis, 'fetch')
      .mockResolvedValueOnce(settingsResponse(''))
      .mockResolvedValue(new Response('', { status: 429 }))
    const ws = await fresh()
    await ws.load()
    expect(await ws.save(DEFAULT_LAYOUT)).toBe(true)
    await vi.advanceTimersByTimeAsync(3000)
    await vi.waitFor(() => expect(ws.saveError.value).toMatch(/not saved/i))
    expect(fetch).toHaveBeenCalledTimes(5)
    vi.useRealTimers()
  })

  it('retries a failed load and unlocks once the stored layout arrives', async () => {
    vi.spyOn(globalThis, 'fetch')
      .mockResolvedValueOnce(new Response('', { status: 500 }))
      .mockResolvedValueOnce(settingsResponse(serializeLayout(withoutFirst(1))))
    const ws = await fresh()
    await ws.load()
    expect(ws.locked.value?.kind).toBe('unloaded')
    await ws.retry()
    expect(ws.locked.value).toBeNull()
    expect(ws.layout.value).toEqual(withoutFirst(1))
  })

  it('reset clears the lock and writes the built-in layout', async () => {
    const fetch = vi.spyOn(globalThis, 'fetch')
      .mockResolvedValueOnce(settingsResponse('{broken'))
      .mockResolvedValueOnce(new Response('', { status: 200 }))
    const ws = await fresh()
    await ws.load()
    await ws.reset()
    expect(ws.locked.value).toBeNull()
    expect(patchedValues(fetch)).toEqual([serializeLayout(DEFAULT_LAYOUT)])
  })

  // Each edit is its own save; sending every one would spend the shared per-IP rate limit.
  it('sends one write at a time and collapses the edits made meanwhile into the latest', async () => {
    const first = deferred()
    const fetch = vi.spyOn(globalThis, 'fetch')
      .mockResolvedValueOnce(settingsResponse(''))
      .mockReturnValueOnce(first.promise)
      .mockResolvedValue(new Response('', { status: 200 }))
    const ws = await fresh()
    await ws.load()
    for (const n of [1, 2, 3])
      expect(await ws.save(withoutFirst(n))).toBe(true)
    first.resolve(new Response('', { status: 200 }))
    await vi.waitFor(() => expect(patchedValues(fetch)).toHaveLength(2))
    await settle()
    expect(patchedValues(fetch)).toEqual([serializeLayout(withoutFirst(1)), serializeLayout(withoutFirst(3))])
  })

  it('clears the save error once a later write lands', async () => {
    const first = deferred()
    const fetch = vi.spyOn(globalThis, 'fetch')
      .mockResolvedValueOnce(settingsResponse(''))
      .mockReturnValueOnce(first.promise)
      .mockResolvedValueOnce(new Response('', { status: 200 }))
    const ws = await fresh()
    await ws.load()
    await ws.save(withoutFirst(1))
    await ws.save(withoutFirst(2))
    first.resolve(new Response('{"error":"bad request"}', { status: 400 }))
    await vi.waitFor(() => expect(fetch).toHaveBeenCalledTimes(3))
    await settle()
    expect(ws.saveError.value).toBeNull()
  })

  it('clears the save error once a retry refetch replaces the layout', async () => {
    const fetch = vi.spyOn(globalThis, 'fetch')
      .mockResolvedValueOnce(settingsResponse(''))
      .mockResolvedValueOnce(new Response('{"error":"bad request"}', { status: 400 }))
      .mockResolvedValueOnce(settingsResponse(serializeLayout(withoutFirst(1))))
    const ws = await fresh()
    await ws.load()
    expect(await ws.save(withoutFirst(2))).toBe(true)
    await vi.waitFor(() => expect(ws.saveError.value).toMatch(/not saved/i))
    await ws.retry()
    expect(ws.layout.value).toEqual(withoutFirst(1))
    expect(ws.saveError.value).toBeNull()
    expect(fetch).toHaveBeenCalledTimes(3)
  })

  // wide is per-window UI state, not layout: it must never reach the PATCH body or survive a load.
  it('keeps wide out of the saved layout and null after load', async () => {
    const fetch = vi.spyOn(globalThis, 'fetch').mockResolvedValueOnce(settingsResponse(''))
    const ws = await fresh()
    await ws.load()
    expect(ws.wide.value).toBeNull()
    ws.wide.value = 'hub'
    expect(await ws.save(DEFAULT_LAYOUT)).toBe(true)
    await vi.waitFor(() => expect(fetch).toHaveBeenCalledWith('/api/settings/workspace.layout', expect.anything()))
    expect(patchedValues(fetch)).toEqual([serializeLayout(DEFAULT_LAYOUT)])
  })

  // Window events reach every module instance earlier tests imported, so these
  // stay last and answer every request instead of queueing one-shot responses.
  it('reads the layout again when the window regains focus or becomes visible', async () => {
    let stored = ''
    vi.spyOn(globalThis, 'fetch').mockImplementation(async () => settingsResponse(stored))
    const ws = await fresh()
    await ws.load()
    stored = serializeLayout(withoutFirst(1))
    window.dispatchEvent(new Event('focus'))
    expect(await ws.save(withoutFirst(4))).toBe(false)
    await vi.waitFor(() => expect(ws.layout.value).toEqual(withoutFirst(1)))
    stored = serializeLayout(withoutFirst(2))
    vi.spyOn(document, 'visibilityState', 'get').mockReturnValue('visible')
    document.dispatchEvent(new Event('visibilitychange'))
    await vi.waitFor(() => expect(ws.layout.value).toEqual(withoutFirst(2)))
  })

  it('does not read the layout again while a write is pending', async () => {
    const pending = deferred()
    let stored = ''
    vi.spyOn(globalThis, 'fetch').mockImplementation(async (_input, init) => init?.method === 'PATCH' ? pending.promise : settingsResponse(stored))
    const ws = await fresh()
    await ws.load()
    expect(await ws.save(withoutFirst(1))).toBe(true)
    stored = serializeLayout(withoutFirst(2))
    window.dispatchEvent(new Event('focus'))
    await settle()
    expect(ws.layout.value).toEqual(withoutFirst(1))
    pending.resolve(new Response('', { status: 200 }))
  })
})
