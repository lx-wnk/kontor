import { beforeEach, describe, expect, it, vi } from 'vitest'

type Mod = typeof import('../composables/useObsidianGraph')
let useObsidianGraph: Mod['useObsidianGraph']
const fetchMock = vi.fn()

function jsonResponse(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), { status })
}

beforeEach(async () => {
  // The state is module-level on purpose; a fresh module per test keeps it from leaking.
  vi.resetModules()
  fetchMock.mockReset()
  vi.stubGlobal('fetch', fetchMock)
  ;({ useObsidianGraph } = await import('../composables/useObsidianGraph'))
})

describe('useObsidianGraph', () => {
  it('maps an unconfigured vault', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse({ configured: false }))
    const g = useObsidianGraph()
    await g.refresh()
    expect(g.status.value).toBe('unconfigured')
  })

  it('maps a 403 to denied with the server message', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse({ error: 'memory.read denied' }, 403))
    const g = useObsidianGraph()
    await g.refresh()
    expect(g.status.value).toBe('denied')
    expect(g.message.value).toBe('memory.read denied')
  })

  it('builds titles, links and backlinks from a ready graph', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse({
      configured: true,
      notes: [['a/One.md', 1000], ['Two.md', 2000]],
      links: [[0, 1]],
    }))
    const g = useObsidianGraph()
    await g.refresh()
    expect(g.status.value).toBe('ready')
    expect(g.notes.value).toEqual([
      { index: 0, path: 'a/One.md', title: 'One', mtimeMs: 1000, links: [1], backlinks: [] },
      { index: 1, path: 'Two.md', title: 'Two', mtimeMs: 2000, links: [], backlinks: [0] },
    ])
  })

  it('skips a second refresh within 60s and fetches again on force', async () => {
    fetchMock.mockResolvedValue(jsonResponse({ configured: true, notes: [], links: [] }))
    const g = useObsidianGraph()
    await g.refresh()
    await g.refresh()
    expect(fetchMock).toHaveBeenCalledTimes(1)
    await g.refresh(true)
    expect(fetchMock).toHaveBeenCalledTimes(2)
  })

  it('keeps the notes and reports failed when a later refresh fails', async () => {
    fetchMock
      .mockResolvedValueOnce(jsonResponse({ configured: true, notes: [['a.md', 1]], links: [] }))
      .mockResolvedValueOnce(new Response('', { status: 500 }))
    const g = useObsidianGraph()
    await g.refresh()
    const before = g.notes.value
    await g.refresh(true)
    expect(g.status.value).toBe('failed')
    expect(g.notes.value).toBe(before)
  })

  it('fetches again on the next refresh within 60s after a failed fetch', async () => {
    fetchMock
      .mockRejectedValueOnce(new TypeError('network down'))
      .mockResolvedValueOnce(jsonResponse({ configured: true, notes: [['a.md', 1]], links: [] }))
    const g = useObsidianGraph()
    await g.refresh()
    expect(g.status.value).toBe('failed')
    await g.refresh()
    expect(fetchMock).toHaveBeenCalledTimes(2)
    expect(g.status.value).toBe('ready')
  })

  it('drops links whose note indices are out of range', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse({
      configured: true,
      notes: [['One.md', 1], ['Two.md', 2]],
      links: [[0, 1], [0, 5], [-1, 0], [7, 1], [1, 0.5]],
    }))
    const g = useObsidianGraph()
    await g.refresh()
    expect(g.notes.value.map(n => [n.links, n.backlinks])).toEqual([[[1], []], [[], [0]]])
  })

  it('recentNotes returns the newest notes first', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse({
      configured: true,
      notes: [['a.md', 1000], ['b.md', 3000], ['c.md', 2000]],
      links: [],
    }))
    const g = useObsidianGraph()
    await g.refresh()
    expect(g.recentNotes(2).map(n => n.path)).toEqual(['b.md', 'c.md'])
  })

  it('opens a note and reports the server error text on failure', async () => {
    fetchMock.mockResolvedValueOnce(new Response(null, { status: 204 }))
    const g = useObsidianGraph()
    expect(await g.openInObsidian('a.md')).toBeNull()
    expect(fetchMock).toHaveBeenCalledWith('/api/obsidian/open', expect.objectContaining({
      method: 'POST',
      body: JSON.stringify({ path: 'a.md' }),
    }))

    fetchMock.mockResolvedValueOnce(jsonResponse({ error: 'note is not in the vault graph' }, 404))
    expect(await g.openInObsidian('missing.md')).toBe('note is not in the vault graph')
  })
})
