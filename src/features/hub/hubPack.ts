import type { Leaf, Sector } from './hubGeometry'
import { hierarchy, pack } from 'd3-hierarchy'
import { OTHER_SECTOR_KEY, polar, R0, R_MAX, sectorMid } from './hubGeometry'

export interface Circle { x: number, y: number, r: number }

export const CATEGORY_RING_R = (R0 + R_MAX) / 2
export const CATEGORY_GAP_FACTOR = 0.94
const PACK_PADDING = 2

interface PackDatum { key: string, value?: number, children?: PackDatum[] }

export interface PackResult {
  categories: Map<string, Circle>
  projects: Map<string, Circle>
  notes: Map<string, [number, number]>
}

export function packHub(
  sectors: readonly Sector[],
  leaves: readonly Leaf[],
  notesByLeaf: ReadonlyMap<string, readonly string[]>,
): PackResult {
  const categories = new Map<string, Circle>()
  const projects = new Map<string, Circle>()
  const notes = new Map<string, [number, number]>()

  for (const sector of sectors) {
    if (sector.key === OTHER_SECTOR_KEY)
      continue
    const categoryLeaves = leaves.filter(leaf => leaf.parent === sector.key)
    const noteCount = categoryLeaves.reduce((sum, leaf) => sum + (notesByLeaf.get(leaf.key)?.length ?? 0), 0)
    if (noteCount === 0)
      continue

    const spanRad = (sector.end - sector.start) * Math.PI / 180
    // The category circle must fit the ring band and its own wedge, whichever is tighter; a wedge of
    // 180° or more no longer constrains it (sin would fall back towards 0 at a full turn).
    const wedgeR = spanRad >= Math.PI ? Infinity : CATEGORY_RING_R * Math.sin(spanRad / 2)
    const catR = Math.min((R_MAX - R0) / 2, wedgeR) * CATEGORY_GAP_FACTOR
    const [cx, cy] = polar(CATEGORY_RING_R, sectorMid(sector))
    categories.set(sector.key, { x: cx, y: cy, r: catR })

    const data: PackDatum = {
      key: sector.key,
      children: [...categoryLeaves]
        .sort((a, b) => a.key.localeCompare(b.key))
        .map(leaf => ({
          key: leaf.key,
          children: [...(notesByLeaf.get(leaf.key) ?? [])].sort().map(path => ({ key: path, value: 1 })),
        })),
    }
    const root = hierarchy(data).sum(d => d.value ?? 0)
    // pack() sizes root to exactly fill [0, 2r] and centres it at (r, r), so the offset to world
    // coordinates is constant for every descendant.
    const packed = pack<PackDatum>().size([2 * catR, 2 * catR]).padding(PACK_PADDING)(root)
    const dx = cx - catR
    const dy = cy - catR

    for (const leafNode of packed.children ?? []) {
      projects.set(leafNode.data.key, { x: leafNode.x + dx, y: leafNode.y + dy, r: leafNode.r })
      for (const noteNode of leafNode.children ?? [])
        notes.set(noteNode.data.key, [noteNode.x + dx, noteNode.y + dy])
    }
  }

  return { categories, projects, notes }
}
