import type { Sector } from './hubGeometry'
import { polar, R0, R_MAX, sectorMid } from './hubGeometry'

export type Point = [number, number]

// Anchor radii for hierarchical edge bundling (Holten 2006): links pull toward their shared
// ancestor's radius before fanning back out to the note, so siblings' links visually merge.
export const LINK_LEAF_ANCHOR_R = R0 + 0.3 * (R_MAX - R0)
export const LINK_CATEGORY_ANCHOR_R = R0 + 0.12 * (R_MAX - R0)
export const LINK_BUNDLE_BETA = 0.82

export interface BundleRoute { fromLeaf: Sector, toLeaf: Sector, fromCategory: Sector, toCategory: Sector }

function leafAnchor(sector: Sector): Point {
  return polar(LINK_LEAF_ANCHOR_R, sectorMid(sector))
}

function categoryAnchor(sector: Sector): Point {
  return polar(LINK_CATEGORY_ANCHOR_R, sectorMid(sector))
}

function midpoint(p: Point, q: Point): Point {
  return [(p[0] + q[0]) / 2, (p[1] + q[1]) / 2]
}

export function bundlePoints(from: Point, to: Point, route: BundleRoute, beta = LINK_BUNDLE_BETA): Point[] {
  const { fromLeaf, toLeaf, fromCategory, toCategory } = route
  const leavesDiffer = fromLeaf.key !== toLeaf.key
  const categoriesDiffer = fromCategory.key !== toCategory.key
  const pts: Point[] = [from, leafAnchor(fromLeaf)]
  if (categoriesDiffer)
    pts.push(categoryAnchor(fromCategory), categoryAnchor(toCategory))
  else if (leavesDiffer)
    pts.push(categoryAnchor(fromCategory))
  if (leavesDiffer)
    pts.push(leafAnchor(toLeaf))
  pts.push(to)

  const last = pts.length - 1
  return pts.map(([x, y], i) => {
    if (i === 0 || i === last)
      return [x, y]
    const t = i / last
    const sx = from[0] + (to[0] - from[0]) * t
    const sy = from[1] + (to[1] - from[1]) * t
    return [beta * x + (1 - beta) * sx, beta * y + (1 - beta) * sy]
  })
}

type TraceCtx = Pick<CanvasRenderingContext2D, 'moveTo' | 'lineTo' | 'quadraticCurveTo'>

export function traceBundle(ctx: TraceCtx, pts: readonly Point[]): void {
  if (pts.length === 0)
    return
  ctx.moveTo(pts[0][0], pts[0][1])
  if (pts.length === 2) {
    ctx.lineTo(pts[1][0], pts[1][1])
    return
  }
  const last = pts.length - 1
  for (let i = 1; i <= last - 1; i++) {
    const [cx, cy] = pts[i]
    const [ex, ey] = i === last - 1 ? pts[last] : midpoint(pts[i], pts[i + 1])
    ctx.quadraticCurveTo(cx, cy, ex, ey)
  }
}

export interface AggregatedLink { a: string, b: string, count: number, sample: [number, number] }

export function aggregateLinks(links: ReadonlyArray<[number, number]>, groupOf: (note: number) => string): AggregatedLink[] {
  const byPair = new Map<string, AggregatedLink>()
  for (const link of links) {
    const [from, to] = link
    const ga = groupOf(from)
    const gb = groupOf(to)
    if (ga === gb)
      continue
    const [a, b] = ga.localeCompare(gb) <= 0 ? [ga, gb] : [gb, ga]
    const existing = byPair.get(`${a}\u0000${b}`)
    if (existing)
      existing.count++
    else byPair.set(`${a}\u0000${b}`, { a, b, count: 1, sample: link })
  }
  return [...byPair.values()].sort((x, y) => x.a.localeCompare(y.a) || x.b.localeCompare(y.b))
}

export function linksOf(links: ReadonlyArray<[number, number]>, note: number): number[] {
  const result: number[] = []
  links.forEach(([from, to], i) => {
    if (from === note || to === note)
      result.push(i)
  })
  return result
}
