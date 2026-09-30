import { SESSIONS_KIND } from './hubGeometry'

export type Point = [number, number]

export const LINK_BUNDLE_BETA = 0.82

// Anchor points for hierarchical edge bundling (Holten 2006): links pull toward the shared project
// and category circle centres before fanning back out to the note, so siblings' links visually merge.
export interface BundleRoute {
  fromProject: Point
  toProject: Point
  fromCategory: Point
  toCategory: Point
  sameProject: boolean
  sameCategory: boolean
}

function midpoint(p: Point, q: Point): Point {
  return [(p[0] + q[0]) / 2, (p[1] + q[1]) / 2]
}

export function bundlePoints(from: Point, to: Point, route: BundleRoute, beta = LINK_BUNDLE_BETA): Point[] {
  const { fromProject, toProject, fromCategory, toCategory, sameProject, sameCategory } = route
  const pts: Point[] = [from, fromProject]
  if (!sameCategory)
    pts.push(fromCategory, toCategory)
  else if (!sameProject)
    pts.push(fromCategory)
  if (!sameProject)
    pts.push(toProject)
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

export function linksOf(links: ReadonlyArray<[number, number]>, note: number): number[] {
  const result: number[] = []
  links.forEach(([from, to], i) => {
    if (from === note || to === note)
      result.push(i)
  })
  return result
}

export function isKnowHowLink(kindA: string, kindB: string): boolean {
  return kindA !== SESSIONS_KIND || kindB !== SESSIONS_KIND
}
