import type { BundleRoute, Point } from './hubLinks'
import { describe, expect, it } from 'vitest'
import { SESSIONS_KIND } from './hubGeometry'
import { bundlePoints, isKnowHowLink, linksOf, traceBundle } from './hubLinks'

const from: Point = [10, 20]
const to: Point = [30, 40]
const fromProject: Point = [12, 22]
const toProject: Point = [28, 38]
const fromCategory: Point = [50, 60]
const toCategory: Point = [70, 80]

describe('bundlePoints', () => {
  it('returns 3 points through the project centre for a same-project link', () => {
    const route: BundleRoute = { fromProject, toProject: fromProject, fromCategory, toCategory: fromCategory, sameProject: true, sameCategory: true }
    const pts = bundlePoints(from, to, route, 1)
    expect(pts).toHaveLength(3)
    expect(pts[1]).toEqual(fromProject)
  })

  it('returns 5 points through the category centre for a same-category, different-project link', () => {
    const route: BundleRoute = { fromProject, toProject, fromCategory, toCategory: fromCategory, sameProject: false, sameCategory: true }
    const pts = bundlePoints(from, to, route, 1)
    expect(pts).toHaveLength(5)
    expect(pts[1]).toEqual(fromProject)
    expect(pts[2]).toEqual(fromCategory)
    expect(pts[3]).toEqual(toProject)
  })

  it('returns 6 points through both category centres for a different-category link', () => {
    const route: BundleRoute = { fromProject, toProject, fromCategory, toCategory, sameProject: false, sameCategory: false }
    const pts = bundlePoints(from, to, route, 1)
    expect(pts).toHaveLength(6)
    expect(pts[1]).toEqual(fromProject)
    expect(pts[2]).toEqual(fromCategory)
    expect(pts[3]).toEqual(toCategory)
    expect(pts[4]).toEqual(toProject)
  })

  it('puts inner points on the straight line when beta is 0', () => {
    const route: BundleRoute = { fromProject, toProject, fromCategory, toCategory: fromCategory, sameProject: false, sameCategory: true }
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
