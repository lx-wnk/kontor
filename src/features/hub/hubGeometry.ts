export const DAY_MS = 86_400_000
// Matches the --sector-0…7 theme tokens in main.css.
export const SECTOR_PALETTE_SIZE = 8

// World units: the hub's "fit all" view shows a disc of about WORLD_RADIUS around the core.
export const R0 = 110
export const R_MAX = 410
export const MAX_AGE_DAYS = 730
export const WORLD_RADIUS = 520
// The overview map frames the whole world plus a rim, so the outermost ring is never flush against its border.
export const MINIMAP_RIM = 20
export const MINIMAP_HALF = WORLD_RADIUS + MINIMAP_RIM
export const WEDGE_INNER = 100
export const WEDGE_OUTER = 440
export const SECTOR_LABEL_RADIUS = 392
export const LAUNCHER_PX = 40 // HubLaunchers.vue's `size-10` button, centred on its slot.
export const LAUNCHER_SECTOR_CLEARANCE_PX = 56
export const SECTOR_FLOOR_DEG = 24
// All floors together never claim more than this, so the weighting always keeps a quarter of the circle.
export const SECTOR_FLOOR_BUDGET_DEG = 270
export const AGENT_FLOOR_PX = 116
export const AGENT_WAITING_FLOOR_PX = 88
export const AGENT_SPACING_PX = 112
export const AGENT_STAGE_MARGIN_PX = 90
export const AGENT_SECTOR_STAGGER_PX = 40
// Beyond three, the stagger walks agents off the stage faster than it buys them arc.
export const AGENT_SECTOR_TIERS_MAX = 3
export const SECTOR_LABEL_AGENT_CLEARANCE_PX = 40
export const RING_LABEL_GAP_PX = 16
export const RING_LABEL_AGENT_CLEARANCE_PX = 12
const AGENT_WORLD_MIN = 82
const AGENT_WAITING_WORLD_MIN = 60
export const OTHER_SECTOR_KEY = '__other__'
export const RINGS: ReadonlyArray<{ label: string, days: number }> = [
  { label: 'today', days: 1 },
  { label: 'week', days: 7 },
  { label: 'month', days: 30 },
  { label: 'year', days: 365 },
]

export function radiusForAge(ageDays: number): number {
  const age = Math.min(Math.max(ageDays, 0), MAX_AGE_DAYS)
  return R0 + (R_MAX - R0) * Math.sqrt(age / MAX_AGE_DAYS)
}

export function hash01(text: string): number {
  let h = 0x811C9DC5
  for (let i = 0; i < text.length; i++) {
    h ^= text.charCodeAt(i)
    h = Math.imul(h, 0x01000193)
  }
  return (h >>> 0) / 4294967296
}

// A sector's palette slot belongs to its key, not to its place in the sorted list: a sector that
// appears or disappears before it must never recolour it. Two keys may share a slot.
export function sectorColour(key: string): number {
  return Math.floor(hash01(key) * SECTOR_PALETTE_SIZE)
}

// Agents of one sector sit on sectorTiers(count) rings, so only every tiers-th of them shares a radius.
export function sectorTiers(agentsInSector: number): number {
  return Math.min(Math.max(agentsInSector, 1), AGENT_SECTOR_TIERS_MAX)
}

// The arc a sector owes its agents: agentAngles splits it into count+1 steps and only every
// sectorTiers-th agent shares a radius, so each same-tier pair must span AGENT_SPACING_PX on the
// base ring. Sectors with fewer than two agents have no same-tier pair and keep the plain floor.
export function sectorFloorDeg(agentsInSector: number, agentsTotal: number): number {
  if (agentsInSector < 2 || agentsTotal < agentsInSector)
    return SECTOR_FLOOR_DEG
  const needPx = AGENT_SPACING_PX * (agentsInSector + 1) / sectorTiers(agentsInSector)
  return Math.max(SECTOR_FLOOR_DEG, 360 * needPx / (2 * Math.PI * agentRingPx(agentsTotal)))
}

// `projects` counts the distinct agent projects and feeds the weight; `agents` counts the running
// instances and feeds only the floor, which is the one thing their labels really need room for.
export interface SectorInput { key: string, label: string, weight: number, projects?: number, agents?: number }
export interface Sector extends SectorInput { start: number, end: number }

export function buildSectors(inputs: SectorInput[]): Sector[] {
  const sorted = [...inputs].sort((a, b) => a.key.localeCompare(b.key))
  const n = sorted.length
  if (n === 0)
    return []
  const spans = Array.from({ length: n }).fill(360 / n) as number[]
  const projectsOf = sorted.map(s => Math.max(s.projects ?? 0, 0))
  const agentsOf = sorted.map(s => Math.max(s.agents ?? 0, 0))
  const agentsTotal = agentsOf.reduce((sum, a) => sum + a, 0)
  const roots = sorted.map((s, i) => Math.sqrt(Math.max(s.weight + projectsOf[i], 0)))
  const wanted = agentsOf.map(a => sectorFloorDeg(a, agentsTotal))
  const wantedSum = wanted.reduce((sum, f) => sum + f, 0)
  // A floor is a best-effort minimum, never a claim on the circle: unscaled, fifteen of them fill it
  // and the weighting silently stops. Shrinking them together keeps it alive, and keeps their order.
  const floors = wantedSum > SECTOR_FLOOR_BUDGET_DEG ? wanted.map(f => f * SECTOR_FLOOR_BUDGET_DEG / wantedSum) : wanted
  if (roots.some(r => r > 0)) {
    const floored = new Set<number>()
    // One sector per pass, the hungriest first: floors differ, so flooring several at once could
    // hand out more than the 360° left.
    for (;;) {
      const free = 360 - [...floored].reduce((sum, i) => sum + floors[i], 0)
      const total = roots.reduce((sum, r, i) => floored.has(i) ? sum : sum + r, 0)
      let hungriest = -1
      for (let i = 0; i < n; i++) {
        if (floored.has(i))
          continue
        spans[i] = total > 0 ? free * roots[i] / total : free / (n - floored.size)
        if (spans[i] < floors[i] && (hungriest < 0 || floors[i] - spans[i] > floors[hungriest] - spans[hungriest]))
          hungriest = i
      }
      if (hungriest < 0)
        break
      floored.add(hungriest)
    }
    for (const i of floored) spans[i] = floors[i]
  }
  let at = -90
  return sorted.map((s, i) => {
    const sector = { ...s, start: at, end: at + spans[i] }
    at += spans[i]
    return sector
  })
}

export function sectorMid(s: Sector): number {
  return (s.start + s.end) / 2
}

export function polar(radius: number, deg: number): [number, number] {
  const r = deg * Math.PI / 180
  return [radius * Math.cos(r), radius * Math.sin(r)]
}

function arcTo(radius: number, deg: number, sweep: 0 | 1): string {
  const [x, y] = polar(radius, deg)
  return `A${radius},${radius} 0 0 ${sweep} ${x},${y}`
}

// Two half-arcs per edge: a lone 360° sector's single arc would end on its own start and draw nothing.
export function wedgePath(start: number, end: number): string {
  const mid = (start + end) / 2
  const [ox, oy] = polar(WEDGE_OUTER, start)
  const [ix, iy] = polar(WEDGE_INNER, end)
  return `M${ox},${oy}${arcTo(WEDGE_OUTER, mid, 1)}${arcTo(WEDGE_OUTER, end, 1)}L${ix},${iy}${arcTo(WEDGE_INNER, mid, 0)}${arcTo(WEDGE_INNER, start, 0)}Z`
}

export function notePoint(path: string, sector: Sector, ageDays: number): [number, number] {
  const width = sector.end - sector.start
  const margin = Math.min(3, width / 4)
  return polar(radiusForAge(ageDays), sector.start + margin + hash01(path) * (width - 2 * margin))
}

export function agentAngles(count: number, sector: Sector): number[] {
  return Array.from({ length: count }, (_, i) => sector.start + (sector.end - sector.start) * (i + 1) / (count + 1))
}

// On-screen agent ring: widens with the agent count, capped by the stage; the floor wins over the cap.
export function agentRingPx(count: number, stagePx = Infinity): number {
  const grownPx = count * AGENT_SPACING_PX / (2 * Math.PI)
  return Math.max(AGENT_FLOOR_PX, Math.min(grownPx, stagePx / 2 - AGENT_STAGE_MARGIN_PX))
}

// Same-sector agents share one base ring; sector-local indices walk round the sector's tiers so
// neighbouring labels land on different radii instead of stacking.
export function agentSectorRingPx(baseRingPx: number, indexInSector: number, countInSector: number, stagePx = Infinity): number {
  const staggered = baseRingPx + (indexInSector % sectorTiers(countInSector)) * AGENT_SECTOR_STAGGER_PX
  return Math.max(AGENT_FLOOR_PX, Math.min(staggered, stagePx / 2 - AGENT_STAGE_MARGIN_PX))
}

// World radius that puts the agent on its on-screen ring; a waiting agent sits the floor gap further in.
export function agentRadius(scale: number, waiting: boolean, ringPx = AGENT_FLOOR_PX): number {
  return waiting
    ? Math.max(AGENT_WAITING_WORLD_MIN, (ringPx - (AGENT_FLOOR_PX - AGENT_WAITING_FLOOR_PX)) / scale)
    : Math.max(AGENT_WORLD_MIN, ringPx / scale)
}

export function sectorLabelRadius(scale: number, agentRingPx: number): number {
  return Math.max(SECTOR_LABEL_RADIUS, (agentRingPx + SECTOR_LABEL_AGENT_CLEARANCE_PX) / scale)
}

// The launchers ring outside the legend, never in its band: whatever pushes the sector names out
// pushes the launchers the same way, keeping a fixed gap on screen.
export function launcherRingRadius(scale: number, agentRingPx: number): number {
  return sectorLabelRadius(scale, agentRingPx) + LAUNCHER_SECTOR_CLEARANCE_PX / scale
}

export function visibleRingLabels(scale: number, agentRingPx: number): typeof RINGS {
  let last = -Infinity
  return RINGS.filter((ring) => {
    const px = radiusForAge(ring.days) * scale
    if (px < agentRingPx + RING_LABEL_AGENT_CLEARANCE_PX || px - last < RING_LABEL_GAP_PX)
      return false
    last = px
    return true
  })
}

function sectorKeyForSet(path: string, wanted: ReadonlySet<string>): { key: string, label: string } {
  const folders = path.split('/').slice(0, -1)
  const hit = folders.findIndex(f => wanted.has(f.toLowerCase()))
  if (hit >= 0)
    return { key: folders.slice(0, hit + 1).join('/'), label: folders[hit] }
  return folders.length ? { key: folders[0], label: folders[0] } : { key: '', label: 'Notes' }
}

export function sectorKeyFor(path: string, projects: readonly string[]): { key: string, label: string } {
  return sectorKeyForSet(path, new Set(projects.map(p => p.toLowerCase())))
}

export interface SectorPlan { sectors: Sector[], sectorOfNote: Map<string, string>, sectorOfProject: Map<string, string> }

// `agentProjects` holds one entry per running agent, repeats included: a sector's weight counts the
// distinct projects among them, its floor the instances.
export function planSectors(notePaths: readonly string[], agentProjects: readonly string[]): SectorPlan {
  const distinct = [...new Set(agentProjects)]
  const sectorOfNote = new Map<string, string>()
  const sectorOfProject = new Map<string, string>()
  const agentsOfProject = new Map<string, number>()
  for (const p of agentProjects) agentsOfProject.set(p, (agentsOfProject.get(p) ?? 0) + 1)
  if (notePaths.length === 0) {
    for (const p of distinct) sectorOfProject.set(p, p)
    return { sectors: buildSectors(distinct.map(p => ({ key: p, label: p, weight: 1, projects: 1, agents: agentsOfProject.get(p) }))), sectorOfNote, sectorOfProject }
  }
  const wanted = new Set(distinct.map(p => p.toLowerCase()))
  const inputs = new Map<string, SectorInput>()
  for (const path of notePaths) {
    const { key, label } = sectorKeyForSet(path, wanted)
    sectorOfNote.set(path, key)
    const input = inputs.get(key)
    if (input)
      input.weight++
    else inputs.set(key, { key, label, weight: 1 })
  }
  for (const p of distinct) {
    const match = [...inputs.values()].find(s => s.label.toLowerCase() === p.toLowerCase())
    sectorOfProject.set(p, match?.key ?? OTHER_SECTOR_KEY)
  }
  if ([...sectorOfProject.values()].includes(OTHER_SECTOR_KEY))
    inputs.set(OTHER_SECTOR_KEY, { key: OTHER_SECTOR_KEY, label: 'Other', weight: 1 })
  for (const [project, count] of agentsOfProject) {
    const sector = inputs.get(sectorOfProject.get(project)!)!
    sector.projects = (sector.projects ?? 0) + 1
    sector.agents = (sector.agents ?? 0) + count
  }
  return { sectors: buildSectors([...inputs.values()]), sectorOfNote, sectorOfProject }
}
