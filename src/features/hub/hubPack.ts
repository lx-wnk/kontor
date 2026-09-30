import type { Leaf, Sector } from './hubGeometry'
import { hierarchy, pack } from 'd3-hierarchy'
import { OTHER_SECTOR_KEY, R0, R_MAX } from './hubGeometry'

export interface Circle { x: number, y: number, r: number }

export const AGENT_WEIGHT = 4
const PACK_PADDING = 2
const AGENT_RIM_GAP = 6
// Clearance between the core disc and a category circle, and half of it between neighbouring categories.
export const CATEGORY_GAP = 16
const RADIUS_SEARCH_ITERATIONS = 40
// NUL-prefixed keys never collide with real vault paths, which can't contain a NUL byte.
const FILLER_SUFFIX = '\u0000filler'

interface PackDatum { key: string, value?: number, children?: PackDatum[] }

export interface PackResult {
  categories: Map<string, Circle>
  projects: Map<string, Circle>
  notes: Map<string, [number, number]>
  agentSlots: Map<string, Array<[number, number, number]>>
  extent: number
}

function sumValue(nodes: readonly PackDatum[]): number {
  return nodes.reduce((sum, d) => sum + (d.value ?? 0) + sumValue(d.children ?? []), 0)
}

interface CategoryLayout { key: string, cx: number, cy: number, r: number }

// Half-angle a circle of radius (r + gap/2) subtends from the origin at distance d; the gap/2 margin
// keeps two angularly adjacent categories a full CATEGORY_GAP apart, not just their bare radii.
function footprintDeg(r: number, d: number): number {
  return 2 * Math.asin((r + CATEGORY_GAP / 2) / d) * 180 / Math.PI
}

function layoutCategories(categoryData: readonly PackDatum[]): CategoryLayout[] {
  const sorted = [...categoryData].sort((a, b) => a.key.localeCompare(b.key))
  const weights = sorted.map(c => sumValue(c.children ?? []))
  const capR = (R_MAX - R0 - CATEGORY_GAP) / 2
  const maxScale = capR / Math.sqrt(Math.max(...weights))

  const footprintSumDeg = (s: number) => weights.reduce((sum, w) => {
    const r = s * Math.sqrt(w)
    return sum + footprintDeg(r, R0 + CATEGORY_GAP + r)
  }, 0)

  let lo = 0
  let hi = maxScale
  for (let i = 0; i < RADIUS_SEARCH_ITERATIONS; i++) {
    const mid = (lo + hi) / 2
    if (footprintSumDeg(mid) <= 360)
      lo = mid
    else hi = mid
  }
  const scale = lo

  const radii = weights.map(w => scale * Math.sqrt(w))
  const footprints = radii.map(r => footprintDeg(r, R0 + CATEGORY_GAP + r))
  const gap = (360 - footprints.reduce((sum, f) => sum + f, 0)) / sorted.length

  let at = -90
  return sorted.map((c, i) => {
    const r = radii[i]
    const centerDeg = at + footprints[i] / 2
    at += footprints[i] + gap
    const rad = centerDeg * Math.PI / 180
    const d = R0 + CATEGORY_GAP + r
    return { key: c.key, cx: d * Math.cos(rad), cy: d * Math.sin(rad), r }
  })
}

// Slot order alternates sides of the outward-facing point (0, +1, -1, +2, -2, ...) while the angles
// themselves stay evenly spaced around the full rim.
function slotOffset(i: number): number {
  if (i === 0)
    return 0
  const step = Math.ceil(i / 2)
  return i % 2 === 1 ? step : -step
}

function agentSlotsForProject(project: Circle, agents: number): Array<[number, number, number]> {
  const baseAngle = Math.atan2(project.y, project.x) * 180 / Math.PI
  const angleStep = 360 / agents
  const slotR = project.r + AGENT_RIM_GAP
  return Array.from({ length: agents }, (_, i) => {
    const angleDeg = baseAngle + slotOffset(i) * angleStep
    const rad = angleDeg * Math.PI / 180
    return [project.x + slotR * Math.cos(rad), project.y + slotR * Math.sin(rad), angleDeg]
  })
}

export function packHub(
  sectors: readonly Sector[],
  leaves: readonly Leaf[],
  notesByLeaf: ReadonlyMap<string, readonly string[]>,
  agentsByLeaf: ReadonlyMap<string, number> = new Map(),
): PackResult {
  const categories = new Map<string, Circle>()
  const projects = new Map<string, Circle>()
  const notes = new Map<string, [number, number]>()
  const agentSlots = new Map<string, Array<[number, number, number]>>()

  const categoryData: PackDatum[] = sectors
    .filter(sector => sector.key !== OTHER_SECTOR_KEY)
    .map((sector) => {
      const children = leaves
        .filter(leaf => leaf.parent === sector.key)
        .map((leaf) => {
          const noteChildren: PackDatum[] = [...(notesByLeaf.get(leaf.key) ?? [])]
            .sort()
            .map(path => ({ key: path, value: 1 }))
          const agents = agentsByLeaf.get(leaf.key) ?? 0
          if (agents > 0)
            noteChildren.push({ key: `${leaf.key}${FILLER_SUFFIX}`, value: AGENT_WEIGHT * agents })
          return { key: leaf.key, children: noteChildren }
        })
        .filter(leafDatum => leafDatum.children.length > 0)
        .sort((a, b) => a.key.localeCompare(b.key))
      return { key: sector.key, children }
    })
    .filter(categoryDatum => categoryDatum.children.length > 0)

  if (categoryData.length === 0)
    return { categories, projects, notes, agentSlots, extent: R0 }

  const categoryLayout = layoutCategories(categoryData)
  const byKey = new Map(categoryData.map(c => [c.key, c]))
  let extent = R0

  for (const { key, cx, cy, r } of categoryLayout) {
    extent = Math.max(extent, Math.hypot(cx, cy) + r)
    const root = hierarchy(byKey.get(key)!).sum(d => d.value ?? 0)
    const packed = pack<PackDatum>().size([2 * r, 2 * r]).padding(PACK_PADDING)(root)
    for (const node of packed.descendants()) {
      const x = node.x - r + cx
      const y = node.y - r + cy
      if (node.depth === 0)
        categories.set(node.data.key, { x, y, r: node.r })
      else if (node.depth === 1)
        projects.set(node.data.key, { x, y, r: node.r })
      else if (node.depth === 2 && !node.data.key.endsWith(FILLER_SUFFIX))
        notes.set(node.data.key, [x, y])
    }
  }

  for (const leaf of leaves) {
    const agents = agentsByLeaf.get(leaf.key) ?? 0
    const project = projects.get(leaf.key)
    if (agents > 0 && project)
      agentSlots.set(leaf.key, agentSlotsForProject(project, agents))
  }

  return { categories, projects, notes, agentSlots, extent }
}
