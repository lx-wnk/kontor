import type { Sector } from './hubGeometry'
import type { Point } from './hubLinks'
import { describe, expect, it } from 'vitest'
import { polar, sectorMid, SESSIONS_KIND } from './hubGeometry'
import { aggregateLinks, bundlePoints, distanceToPolyline, isKnowHowLink, LINK_CATEGORY_ANCHOR_R, LINK_LEAF_ANCHOR_R, linksOf, nearestArc, sampleBundle, traceBundle } from './hubLinks'

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

describe('aggregateLinks', () => {
  const groupOf = (n: number) => n < 3 ? 'g1' : n < 6 ? 'g2' : 'g3'

  it('drops same-group links, merges reversed pairs, and counts', () => {
    const links: Array<[number, number]> = [[0, 4], [4, 0], [1, 5], [7, 8]]
    expect(aggregateLinks(links, groupOf)).toEqual([
      { a: 'g1', b: 'g2', count: 3, sample: [0, 4] },
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

describe('sampleBundle', () => {
  it('starts and ends at the input endpoints', () => {
    const pts: Point[] = [[0, 0], [1, 1], [2, 2], [3, 3]]
    const sampled = sampleBundle(pts)
    expect(sampled[0]).toEqual(pts[0])
    expect(sampled[sampled.length - 1]).toEqual(pts[pts.length - 1])
  })

  it('is a straight line for 2 points', () => {
    const sampled = sampleBundle([[0, 0], [10, 10]], 4)
    sampled.forEach(([x, y]) => expect(x).toBeCloseTo(y))
  })

  it('passes through the quadratic t=0.5 point for a 3-point curve', () => {
    const p0: Point = [0, 0]
    const c: Point = [10, 0]
    const p2: Point = [10, 10]
    const sampled = sampleBundle([p0, c, p2], 8)
    const mid = sampled[4]
    expect(mid[0]).toBeCloseTo((p0[0] + 2 * c[0] + p2[0]) / 4)
    expect(mid[1]).toBeCloseTo((p0[1] + 2 * c[1] + p2[1]) / 4)
  })
})

describe('distanceToPolyline', () => {
  const line: Point[] = [[0, 0], [10, 0]]

  it('is 0 for a point on the line', () => {
    expect(distanceToPolyline([5, 0], line)).toBeCloseTo(0)
  })

  it('is the perpendicular distance for a nearby point', () => {
    expect(distanceToPolyline([5, 1], line)).toBeCloseTo(1)
  })

  it('is large for a far point', () => {
    expect(distanceToPolyline([100, 100], line)).toBeGreaterThan(50)
  })
})

describe('nearestArc', () => {
  const arcA = { id: 'a', line: [[0, 0], [10, 0]] as Point[] }
  const arcB = { id: 'b', line: [[0, 10], [10, 10]] as Point[] }

  it('picks the closest arc within tolerance', () => {
    expect(nearestArc([arcA, arcB], [5, 0.5], 1)?.id).toBe('a')
  })

  it('returns null beyond tolerance', () => {
    expect(nearestArc([arcA, arcB], [5, 5], 1)).toBeNull()
  })

  it('breaks ties by picking the first arc', () => {
    const arcC = { id: 'c', line: [[0, 0], [10, 0]] as Point[] }
    expect(nearestArc([arcA, arcC], [5, 5], 10)?.id).toBe('a')
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
