import type { Sector } from './hubGeometry'
import type { Point } from './hubLinks'
import { describe, expect, it } from 'vitest'
import { polar, sectorMid, SESSIONS_KIND } from './hubGeometry'
import { bundlePoints, isKnowHowLink, LINK_CATEGORY_ANCHOR_R, LINK_LEAF_ANCHOR_R, linksOf, traceBundle } from './hubLinks'

const catA: Sector = { key: 'catA', label: 'A', weight: 1, start: 0, end: 90 }
const catB: Sector = { key: 'catB', label: 'B', weight: 1, start: 90, end: 180 }
const leafA1: Sector = { key: 'catA/leaf1', label: 'leaf1', weight: 1, start: 0, end: 45 }
const leafA2: Sector = { key: 'catA/leaf2', label: 'leaf2', weight: 1, start: 45, end: 90 }
const leafB1: Sector = { key: 'catB/leaf1', label: 'leaf1', weight: 1, start: 90, end: 180 }

const from: Point = [10, 20]
const to: Point = [30, 40]

describe('bundlePoints', () => {
  it('returns 3 points for a same-leaf link', () => {
    const route = { fromLeaf: leafA1, toLeaf: leafA1, fromCategory: catA, toCategory: catA }
    expect(bundlePoints(from, to, route)).toHaveLength(3)
  })

  it('returns 5 points for a same-category, different-leaf link', () => {
    const route = { fromLeaf: leafA1, toLeaf: leafA2, fromCategory: catA, toCategory: catA }
    expect(bundlePoints(from, to, route)).toHaveLength(5)
  })

  it('returns 6 points for a different-category link', () => {
    const route = { fromLeaf: leafA1, toLeaf: leafB1, fromCategory: catA, toCategory: catB }
    expect(bundlePoints(from, to, route)).toHaveLength(6)
  })

  it('keeps anchors exactly at their polar position when beta is 1', () => {
    const route = { fromLeaf: leafA1, toLeaf: leafB1, fromCategory: catA, toCategory: catB }
    const pts = bundlePoints(from, to, route, 1)
    expect(pts[1]).toEqual(polar(LINK_LEAF_ANCHOR_R, sectorMid(leafA1)))
    expect(pts[2]).toEqual(polar(LINK_CATEGORY_ANCHOR_R, sectorMid(catA)))
    expect(pts[3]).toEqual(polar(LINK_CATEGORY_ANCHOR_R, sectorMid(catB)))
    expect(pts[4]).toEqual(polar(LINK_LEAF_ANCHOR_R, sectorMid(leafB1)))
  })

  it('puts inner points on the straight line when beta is 0', () => {
    const route = { fromLeaf: leafA1, toLeaf: leafA2, fromCategory: catA, toCategory: catA }
    const pts = bundlePoints(from, to, route, 0)
    const last = pts.length - 1
    pts.forEach((p, i) => {
      if (i === 0 || i === last)
        return
      const t = i / last
      expect(p[0]).toBeCloseTo(from[0] + (to[0] - from[0]) * t)
      expect(p[1]).toBeCloseTo(from[1] + (to[1] - from[1]) * t)
    })
  })
})

function recorder() {
  const calls: unknown[][] = []
  return {
    calls,
    moveTo: (x: number, y: number) => calls.push(['moveTo', x, y]),
    lineTo: (x: number, y: number) => calls.push(['lineTo', x, y]),
    quadraticCurveTo: (cx: number, cy: number, x: number, y: number) => calls.push(['quadraticCurveTo', cx, cy, x, y]),
  }
}

describe('traceBundle', () => {
  it('draws a straight line for 2 points', () => {
    const rec = recorder()
    traceBundle(rec, [[0, 0], [10, 10]])
    expect(rec.calls).toEqual([['moveTo', 0, 0], ['lineTo', 10, 10]])
  })

  it('draws a quadratic B-spline through midpoints for 4 points', () => {
    const rec = recorder()
    traceBundle(rec, [[0, 0], [1, 1], [2, 2], [3, 3]])
    expect(rec.calls).toEqual([
      ['moveTo', 0, 0],
      ['quadraticCurveTo', 1, 1, 1.5, 1.5],
      ['quadraticCurveTo', 2, 2, 3, 3],
    ])
  })
})

describe('linksOf', () => {
  it('returns indices of links touching a note', () => {
    const links: Array<[number, number]> = [[0, 1], [1, 2], [3, 4]]
    expect(linksOf(links, 1)).toEqual([0, 1])
    expect(linksOf(links, 5)).toEqual([])
  })
})

describe('isKnowHowLink', () => {
  it('is false when both ends are sessions', () => {
    expect(isKnowHowLink(SESSIONS_KIND, SESSIONS_KIND)).toBe(false)
  })

  it('is true when either end is not sessions', () => {
    expect(isKnowHowLink('knowhow', SESSIONS_KIND)).toBe(true)
    expect(isKnowHowLink(SESSIONS_KIND, 'knowhow')).toBe(true)
    expect(isKnowHowLink('knowhow', 'knowhow')).toBe(true)
  })
})
