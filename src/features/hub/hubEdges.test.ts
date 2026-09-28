import type { HubNote } from './composables/useObsidianGraph'
import type { Agent } from '@/types'
import { describe, expect, it } from 'vitest'
import { agentNoteRows, EDGE_WINDOW_MS, edgeAlpha, liveEdges } from './hubEdges'

const NOW = Date.parse('2026-09-23T12:00:00Z')
const iso = (agoMs: number) => new Date(NOW - agoMs).toISOString()
const note = (index: number, path: string): HubNote => ({ index, path, title: path.replace(/\.md$/, ''), mtimeMs: 0, links: [], backlinks: [] })
const BY_PATH = new Map([note(0, 'a.md'), note(1, 'b.md')].map(n => [n.path, n]))
const agent = (pid: number, recentNotes: Agent['recentNotes']) => ({ pid, recentNotes }) as Agent

describe('liveEdges', () => {
  it('draws one edge per touched note on the map, from the agent to the note', () => {
    const a = agent(1, [{ path: 'a.md', kind: 'read', at: iso(0) }, { path: 'b.md', kind: 'write', at: iso(60_000) }])
    expect(liveEdges([{ agent: a, x: 10, y: 20 }], new Set([1]), BY_PATH, NOW)).toEqual([
      { from: [10, 20], to: 0, kind: 'read', alpha: 0.9 },
      { from: [10, 20], to: 1, kind: 'write', alpha: expect.closeTo(0.83, 5) },
    ])
  })

  it('draws nothing for an agent the rail hides', () => {
    const a = agent(1, [{ path: 'a.md', kind: 'read', at: iso(0) }])
    expect(liveEdges([{ agent: a, x: 0, y: 0 }], new Set(), BY_PATH, NOW)).toEqual([])
  })

  it('drops a note that left the graph, and draws nothing while the graph is still empty', () => {
    const a = agent(1, [{ path: 'gone.md', kind: 'read', at: iso(0) }, { path: 'a.md', kind: 'read', at: iso(0) }])
    expect(liveEdges([{ agent: a, x: 0, y: 0 }], new Set([1]), BY_PATH, NOW).map(e => e.to)).toEqual([0])
    expect(liveEdges([{ agent: a, x: 0, y: 0 }], new Set([1]), new Map(), NOW)).toEqual([])
  })

  it('drops an expired or unreadable timestamp', () => {
    const a = agent(1, [{ path: 'a.md', kind: 'read', at: iso(EDGE_WINDOW_MS + 1) }, { path: 'b.md', kind: 'read', at: 'not-a-date' }])
    expect(liveEdges([{ agent: a, x: 0, y: 0 }], new Set([1]), BY_PATH, NOW)).toEqual([])
  })

  it('draws a touch stamped slightly in the future as fresh', () => {
    const a = agent(1, [{ path: 'a.md', kind: 'write', at: iso(-5_000) }])
    expect(liveEdges([{ agent: a, x: 0, y: 0 }], new Set([1]), BY_PATH, NOW)[0].alpha).toBe(0.9)
  })

  it('handles an agent without recentNotes', () => {
    expect(liveEdges([{ agent: agent(1, undefined), x: 0, y: 0 }], new Set([1]), BY_PATH, NOW)).toEqual([])
  })
})

describe('edgeAlpha', () => {
  it('fades linearly from 0.9 to 0.2 across the window', () => {
    expect(edgeAlpha(0)).toBe(0.9)
    expect(edgeAlpha(EDGE_WINDOW_MS / 2)).toBeCloseTo(0.55, 5)
    expect(edgeAlpha(EDGE_WINDOW_MS)).toBeCloseTo(0.2, 5)
  })
})

describe('agentNoteRows', () => {
  it('names each touched note on the map with its title, kind and time', () => {
    const at = iso(0)
    const a = agent(1, [{ path: 'b.md', kind: 'write', at }, { path: 'gone.md', kind: 'read', at }])
    expect(agentNoteRows(a, BY_PATH, NOW)).toEqual([{ path: 'b.md', title: 'b', kind: 'write', at }])
  })
})
