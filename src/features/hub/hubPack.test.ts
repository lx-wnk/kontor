import type { Leaf, Sector } from './hubGeometry'
import { describe, expect, it } from 'vitest'
import { OTHER_SECTOR_KEY, R0, R_MAX } from './hubGeometry'
import { packHub } from './hubPack'

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

  it('keeps category circles apart and within the [R0, R_MAX] ring', () => {
    const cats = [...result.categories.values()]
    for (const c of cats) {
      const centreDist = Math.hypot(c.x, c.y)
      expect(centreDist - c.r).toBeGreaterThanOrEqual(R0 - 1e-6)
      expect(centreDist + c.r).toBeLessThanOrEqual(R_MAX + 1e-6)
    }
    for (let i = 0; i < cats.length; i++) {
      for (let j = i + 1; j < cats.length; j++)
        expect(dist(cats[i].x, cats[i].y, cats[j].x, cats[j].y)).toBeGreaterThanOrEqual(cats[i].r + cats[j].r)
    }
  })

  it('is deterministic for the same input', () => {
    const again = packHub(sectors, leaves, notesByLeaf)
    expect([...again.categories]).toEqual([...result.categories])
    expect([...again.projects]).toEqual([...result.projects])
    expect([...again.notes]).toEqual([...result.notes])
  })

  it('gives a tiny angular span category a positive radius', () => {
    const tinySectors: Sector[] = [{ key: 'sliver', label: 'Sliver', weight: 1, start: 0, end: 2 }]
    const tinyLeaves: Leaf[] = [leaf('sliver/proj1', 'sliver')]
    const tinyNotes = new Map<string, readonly string[]>([['sliver/proj1', ['sliver/proj1/note1.md']]])
    const tiny = packHub(tinySectors, tinyLeaves, tinyNotes)
    expect(tiny.categories.get('sliver')!.r).toBeGreaterThan(0)
  })

  it('gives a vault with a single category the full ring band, not a zero-width wedge', () => {
    const only: Sector[] = [{ key: 'solo', label: 'Solo', weight: 1, start: -90, end: 270 }]
    const packed = packHub(only, [leaf('solo/proj1', 'solo')], new Map([['solo/proj1', ['solo/proj1/note1.md']]]))
    expect(packed.categories.get('solo')!.r).toBeCloseTo((R_MAX - R0) / 2 * 0.94)
  })
})
