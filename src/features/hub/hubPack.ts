import type { Leaf, Sector } from './hubGeometry'
import { hierarchy, pack } from 'd3-hierarchy'
import { OTHER_SECTOR_KEY, R0, R_MAX } from './hubGeometry'

export interface Circle { x: number, y: number, r: number }

export const AGENT_WEIGHT = 4
const PACK_PADDING = 2
const WORK_SIZE = 2 * R_MAX
const CORE_VALUE_ITERATIONS = 6
const AGENT_RIM_GAP = 6
// NUL-prefixed keys never collide with real vault paths, which can't contain a NUL byte.
const CORE_KEY = '\u0000core'
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

function packAt(categoryData: readonly PackDatum[], coreValue: number) {
  const data: PackDatum = { key: 'root', children: [{ key: CORE_KEY, value: coreValue }, ...categoryData] }
  const root = hierarchy(data).sum(d => d.value ?? 0)
  return pack<PackDatum>().size([WORK_SIZE, WORK_SIZE]).padding(PACK_PADDING)(root)
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

  let coreValue = sumValue(categoryData)
  let packed = packAt(categoryData, coreValue)
  // The core's packed radius is only a fraction of the whole (CORE + categories) sibling group, set by
  // coreValue relative to the categories' total value; nudge it towards R0/R_MAX and re-pack until it holds.
  for (let i = 0; i < CORE_VALUE_ITERATIONS; i++) {
    const coreNode = packed.children!.find(c => c.data.key === CORE_KEY)!
    const catNodes = packed.children!.filter(c => c.data.key !== CORE_KEY)
    const extentRaw = Math.max(...catNodes.map(c => Math.hypot(c.x - coreNode.x, c.y - coreNode.y) + c.r))
    const ratio = coreNode.r / extentRaw
    coreValue *= (R0 / R_MAX / ratio) ** 2
    packed = packAt(categoryData, coreValue)
  }

  const coreNode = packed.children!.find(c => c.data.key === CORE_KEY)!
  const catNodes = packed.children!.filter(c => c.data.key !== CORE_KEY)
  const extentRaw = Math.max(...catNodes.map(c => Math.hypot(c.x - coreNode.x, c.y - coreNode.y) + c.r))
  const scale = R_MAX / extentRaw

  for (const node of packed.descendants()) {
    if (node.depth === 0 || node.data.key === CORE_KEY)
      continue
    const x = (node.x - coreNode.x) * scale
    const y = (node.y - coreNode.y) * scale
    const r = node.r * scale
    if (node.depth === 1)
      categories.set(node.data.key, { x, y, r })
    else if (node.depth === 2)
      projects.set(node.data.key, { x, y, r })
    else if (node.depth === 3 && !node.data.key.endsWith(FILLER_SUFFIX))
      notes.set(node.data.key, [x, y])
  }

  for (const leaf of leaves) {
    const agents = agentsByLeaf.get(leaf.key) ?? 0
    const project = projects.get(leaf.key)
    if (agents > 0 && project)
      agentSlots.set(leaf.key, agentSlotsForProject(project, agents))
  }

  return { categories, projects, notes, agentSlots, extent: R_MAX }
}
