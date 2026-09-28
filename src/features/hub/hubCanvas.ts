import type { HubNote } from './composables/useObsidianGraph'
import type { Camera } from './hubCamera'
import type { AgentDisplayStatus } from '@/utils/statusColors'
import { friendlyProjectName } from '@/utils/friendlyProjectName'
import { statusLabel } from '@/utils/statusColors'
import { toScreen } from './hubCamera'

export interface LabelCandidate { index: number, sx: number, sy: number, text: string, priority: number }

export interface LabelBox { x: number, y: number, w: number, h: number }

// What a rendered label measures on screen. HubOrbit measures it from the DOM once per distinct
// text; the size depends on the text and the font only, never on the camera.
export interface LabelSize { w: number, h: number }

const UNMEASURED: LabelSize = { w: 0, h: 0 }

// Where HubBrainCanvas.vue's drawLabels puts a note's label: to the right of its point, and
// vertically centred on it (textBaseline 'middle').
export const NOTE_LABEL_OFFSET_PX = 8.8
const NOTE_LABEL_H = 16

// The width is measured with the canvas' own measureText, never counted off the characters.
export function noteLabelBox(c: LabelCandidate, width: number): LabelBox {
  return { x: c.sx + NOTE_LABEL_OFFSET_PX, y: c.sy - NOTE_LABEL_H / 2, w: width, h: NOTE_LABEL_H }
}

export function boxesOverlap(a: LabelBox, b: LabelBox): boolean {
  // An empty box is an unmeasured label: it covers nothing, so it collides with nothing.
  if (a.w <= 0 || a.h <= 0 || b.w <= 0 || b.h <= 0)
    return false
  return a.x < b.x + b.w && a.x + a.w > b.x && a.y < b.y + b.h && a.y + a.h > b.y
}

export function notePriority(n: { hub: boolean, touched: boolean, fresh: boolean, linkCount: number }): number {
  return (n.hub ? 100 : 0) + (n.touched ? 80 : 0) + (n.fresh ? 40 : 0) + n.linkCount
}

// A permanently-occupied area a label must not land on, e.g. another agent's dot or a sector name.
// ownerIndex exempts one candidate's own obstacle (its own dot) from blocking its own label.
export interface LabelObstacle { box: LabelBox, ownerIndex?: number }

// Greedy placement: highest priority first, skip a candidate whose box overlaps one already placed
// or a pre-seeded obstacle (own obstacle, if any, excluded).
export function cullLabels(candidates: LabelCandidate[], boxOf: (c: LabelCandidate) => LabelBox, obstacles: readonly LabelObstacle[] = []): Set<number> {
  const kept = new Set<number>()
  const placed: LabelBox[] = []
  for (const c of [...candidates].sort((a, b) => b.priority - a.priority)) {
    const box = boxOf(c)
    const blocked = placed.some(p => boxesOverlap(box, p))
      || obstacles.some(o => o.ownerIndex !== c.index && boxesOverlap(box, o.box))
    if (blocked)
      continue
    placed.push(box)
    kept.add(c.index)
  }
  return kept
}

// The key a label's measurement is cached under: the text HubOrbit renders, name and status word
// side by side. Both HubOrbit (measuring) and HubWidget (culling) name a label through this.
export function agentLabelKey(projectName: string, state: AgentDisplayStatus): string {
  return `${friendlyProjectName(projectName)} ${statusLabel(state)}`
}

// A sector name reads "<label><weight badge>"; its key spaces the two apart.
export function sectorLabelKey(label: string, weight: number): string {
  return `${label} ${weight}`
}

const AGENT_DOT_PX = 18 // HubOrbit.vue's `size-[18px]` dot, centred on the agent's screen point.
const AGENT_DOT_SIZE: LabelSize = { w: AGENT_DOT_PX, h: AGENT_DOT_PX }
const AGENT_LABEL_GAP_PX = 3
const INWARD_DOWN: readonly [number, number] = [0, 1]

// The direction from an agent back to the core, which sits at the world origin.
export function inwardUnit(x: number, y: number): [number, number] {
  const d = Math.hypot(x, y)
  // An agent on the core has no inward direction; below the dot is where a label hangs anyway.
  return d === 0 ? [...INWARD_DOWN] : [-x / d, -y / d]
}

// How far a box reaches from its centre along a direction.
function reach(size: LabelSize, ux: number, uy: number): number {
  return Math.abs(ux) * size.w / 2 + Math.abs(uy) * size.h / 2
}

// An agent's label hangs inward, toward the core: in the southern half "below the dot" points into
// the sector-name ring, where the label always loses. The push is long enough that the label's own
// box clears the dot's box at any angle — the two reaches plus the gap separate them on one axis.
export function agentLabelOffset(inward: readonly [number, number], size: LabelSize = UNMEASURED): [number, number] {
  const [ux, uy] = inward
  const d = AGENT_LABEL_GAP_PX + reach(AGENT_DOT_SIZE, ux, uy) + reach(size, ux, uy)
  return [ux * d, uy * d]
}

// The box a label occupies. HubOrbit.vue translates the rendered span by the same offset, so this is
// the box the browser draws and not a second guess at it.
export function agentLabelBox(c: LabelCandidate, size: LabelSize = UNMEASURED, inward: readonly [number, number] = INWARD_DOWN): LabelBox {
  const [dx, dy] = agentLabelOffset(inward, size)
  return { x: c.sx + dx - size.w / 2, y: c.sy + dy - size.h / 2, w: size.w, h: size.h }
}

// Inward is the first choice, outward the fallback: same-sector agents sit on radial tiers, so
// inward is exactly where the tier below has its dots. A label with nowhere to go keeps its inward
// place and the culler drops it.
export function agentLabelDirection(c: LabelCandidate, size: LabelSize | undefined, inward: readonly [number, number], obstacles: readonly LabelObstacle[]): [number, number] {
  const outward: [number, number] = [-inward[0], -inward[1]]
  const blocked = (d: readonly [number, number]) =>
    obstacles.some(o => o.ownerIndex !== c.index && boxesOverlap(agentLabelBox(c, size, d), o.box))
  return blocked(inward) && !blocked(outward) ? outward : [inward[0], inward[1]]
}

// An agent's own dot: an obstacle a neighbour's label must not cover, or that agent's dot becomes
// invisible (its label sits on a `bg-card/85` background) and unclickable underneath it.
export function agentDotBox(sx: number, sy: number): LabelBox {
  const r = AGENT_DOT_PX / 2
  return { x: sx - r, y: sy - r, w: AGENT_DOT_PX, h: AGENT_DOT_PX }
}

// A sector name centres on its point (HubOrbit.vue's `-translate-1/2`). It is an obstacle for an
// agent label and never yields to one; it yields only to the docked launcher rail, which is fixed to
// the screen while the map pans under it (HubWidget.vue's `namedSectors`).
export function sectorLabelBox(sx: number, sy: number, size: LabelSize = UNMEASURED): LabelBox {
  return { x: sx - size.w / 2, y: sy - size.h / 2, w: size.w, h: size.h }
}

// needs-the-operator outranks working, which outranks everything else (Ruling R23).
export function agentPriority(needsOperator: boolean, working: boolean): number {
  return (needsOperator ? 2 : 0) + (working ? 1 : 0)
}

export function hitNote(points: ReadonlyArray<[number, number]>, cam: Camera, sx: number, sy: number, maxPx = 8): number {
  let best = -1
  let bestDist = maxPx
  points.forEach(([x, y], i) => {
    const [px, py] = toScreen(cam, x, y)
    const d = Math.hypot(px - sx, py - sy)
    if (d <= bestDist) {
      bestDist = d
      best = i
    }
  })
  return best
}

export function isToday(mtimeMs: number, nowMs: number): boolean {
  const a = new Date(mtimeMs)
  const b = new Date(nowMs)
  return a.getFullYear() === b.getFullYear() && a.getMonth() === b.getMonth() && a.getDate() === b.getDate()
}

export function hubNoteSet(notes: ReadonlyArray<HubNote>, sectorOf: (n: HubNote) => string): Set<number> {
  const bySector = new Map<string, HubNote[]>()
  for (const n of notes) {
    if (n.backlinks.length < 2)
      continue
    const key = sectorOf(n)
    const list = bySector.get(key)
    if (list)
      list.push(n)
    else
      bySector.set(key, [n])
  }
  const result = new Set<number>()
  for (const list of bySector.values()) {
    list.sort((a, b) => b.backlinks.length - a.backlinks.length)
    for (const n of list.slice(0, 3))
      result.add(n.index)
  }
  return result
}
