import type { Leaf, Sector } from './hubGeometry'
import type { Circle } from './hubPack'
import { describe, expect, it } from 'vitest'
import { OTHER_SECTOR_KEY, R0, R_MAX } from './hubGeometry'
import { CATEGORY_GAP, packHub } from './hubPack'

function leaf(key: string, parent: string): Leaf {
  return { key, label: key, weight: 1, start: 0, end: 1, parent }
}

function dist(ax: number, ay: number, bx: number, by: number): number {
  return Math.hypot(ax - bx, ay - by)
}

const sectors: Sector[] = [
  { key: 'alpha', label: 'Alpha', weight: 1, start: -90, end: 0 },
  { key: 'beta', label: 'Beta', weight: 1, start: 0, end: 200 },
  { key: OTHER_SECTOR_KEY, label: 'Other', weight: 1, start: 200, end: 270 },
]

const leaves: Leaf[] = [
  leaf('alpha/proj1', 'alpha'),
  leaf('alpha/proj2', 'alpha'),
  leaf('beta/proj1', 'beta'),
  leaf(OTHER_SECTOR_KEY, OTHER_SECTOR_KEY),
]

const notesByLeaf = new Map<string, readonly string[]>([
  ['alpha/proj1', ['alpha/proj1/note1.md', 'alpha/proj1/note2.md']],
  ['alpha/proj2', ['alpha/proj2/note1.md']],
  ['beta/proj1', ['beta/proj1/note1.md', 'beta/proj1/note2.md', 'beta/proj1/note3.md']],
  [OTHER_SECTOR_KEY, ['other/note1.md']],
])

describe('packHub', () => {
  const result = packHub(sectors, leaves, notesByLeaf)

  it('skips the OTHER sector entirely', () => {
    expect(result.categories.has(OTHER_SECTOR_KEY)).toBe(false)
    expect(result.projects.has(OTHER_SECTOR_KEY)).toBe(false)
    expect(result.notes.has('other/note1.md')).toBe(false)
  })

  it('places every note inside its project circle', () => {
    for (const [leafKey, paths] of notesByLeaf) {
      const project = result.projects.get(leafKey)
      if (!project)
        continue
      for (const path of paths) {
        const [nx, ny] = result.notes.get(path)!
        expect(dist(nx, ny, project.x, project.y)).toBeLessThanOrEqual(project.r + 1e-6)
      }
    }
  })

  it('places every project circle inside its category circle', () => {
    for (const l of leaves) {
      const category = result.categories.get(l.parent)
      const project = result.projects.get(l.key)
      if (!category || !project)
        continue
      expect(dist(project.x, project.y, category.x, category.y) + project.r).toBeLessThanOrEqual(category.r + 1e-6)
    }
  })

  it('sits each category tangent-ish to the core, apart from its neighbours, within R_MAX', () => {
    const cats = [...result.categories.values()]
    for (const c of cats) {
      const centreDist = Math.hypot(c.x, c.y)
      expect(centreDist - c.r).toBeCloseTo(R0 + CATEGORY_GAP, 0)
      expect(centreDist + c.r).toBeLessThanOrEqual(R_MAX + 1e-6)
    }
    for (let i = 0; i < cats.length; i++) {
      for (let j = i + 1; j < cats.length; j++)
        expect(dist(cats[i].x, cats[i].y, cats[j].x, cats[j].y)).toBeGreaterThanOrEqual(cats[i].r + cats[j].r - 1e-6)
    }
  })

  it('spreads categories evenly, with angular footprints summing to at most 360°', () => {
    const cats = [...result.categories.values()]
    const footprintOf = (c: Circle) => 2 * Math.asin((c.r + CATEGORY_GAP / 2) / Math.hypot(c.x, c.y)) * 180 / Math.PI
    const withAngle = cats
      .map(c => ({ angle: Math.atan2(c.y, c.x) * 180 / Math.PI, footprint: footprintOf(c) }))
      .sort((a, b) => a.angle - b.angle)
    const totalFootprint = withAngle.reduce((sum, c) => sum + c.footprint, 0)
    expect(totalFootprint).toBeLessThanOrEqual(360 + 1e-6)

    const evenGap = (360 - totalFootprint) / cats.length
    const emptyGaps = withAngle.map((c, i) => {
      const next = withAngle[(i + 1) % withAngle.length]
      const delta = ((next.angle - c.angle) + 360) % 360 || 360
      return delta - c.footprint / 2 - next.footprint / 2
    })
    expect(Math.max(...emptyGaps)).toBeLessThanOrEqual(evenGap + 1e-6)
  })

  it('reports an extent close to R_MAX', () => {
    expect(result.extent).toBeGreaterThanOrEqual(R_MAX * 0.95)
    expect(result.extent).toBeLessThanOrEqual(R_MAX * 1.05)
  })

  it('is deterministic for the same input', () => {
    const again = packHub(sectors, leaves, notesByLeaf)
    expect([...again.categories]).toEqual([...result.categories])
    expect([...again.projects]).toEqual([...result.projects])
    expect([...again.notes]).toEqual([...result.notes])
  })

  it('grows a category radius with its note count', () => {
    const heavySectors: Sector[] = [
      { key: 'small', label: 'Small', weight: 1, start: -90, end: 90 },
      { key: 'big', label: 'Big', weight: 1, start: 90, end: 270 },
    ]
    const heavyLeaves: Leaf[] = [leaf('small/proj1', 'small'), leaf('big/proj1', 'big')]
    const smallNotes = ['small/proj1/n1.md']
    const bigNotes = Array.from({ length: 4 * smallNotes.length }, (_, i) => `big/proj1/n${i}.md`)
    const heavyNotes = new Map<string, readonly string[]>([
      ['small/proj1', smallNotes],
      ['big/proj1', bigNotes],
    ])
    const packed = packHub(heavySectors, heavyLeaves, heavyNotes)
    expect(packed.categories.get('big')!.r).toBeGreaterThan(packed.categories.get('small')!.r * 1.5)
  })

  it('gives a project with agents a bigger circle than an equal-note project without', () => {
    const withAgents = packHub(sectors, leaves, notesByLeaf, new Map([['alpha/proj1', 3]]))
    const noAgents = result
    expect(withAgents.projects.get('alpha/proj1')!.r).toBeGreaterThan(noAgents.projects.get('alpha/proj1')!.r)
  })

  it('places agent slots on the project rim, first one facing away from the core', () => {
    const withAgents = packHub(sectors, leaves, notesByLeaf, new Map([['alpha/proj1', 3]]))
    const project = withAgents.projects.get('alpha/proj1')!
    const slots = withAgents.agentSlots.get('alpha/proj1')!
    expect(slots).toHaveLength(3)
    for (const [sx, sy] of slots)
      expect(dist(sx, sy, project.x, project.y)).toBeGreaterThan(project.r)
    const expectedAngle = Math.atan2(project.y, project.x) * 180 / Math.PI
    expect(slots[0][2]).toBeCloseTo(expectedAngle)
  })

  it('works for a vault with a single category', () => {
    const only: Sector[] = [{ key: 'solo', label: 'Solo', weight: 1, start: -90, end: 270 }]
    const packed = packHub(only, [leaf('solo/proj1', 'solo')], new Map([['solo/proj1', ['solo/proj1/note1.md']]]))
    expect(packed.categories.get('solo')!.r).toBeGreaterThan(0)
    expect(dist(0, 0, packed.categories.get('solo')!.x, packed.categories.get('solo')!.y)).toBeGreaterThanOrEqual(0)
  })
})
