import type { Camera } from './hubCamera'
import type { LabelBox } from './hubCanvas'
import type { ActiveView, CoreView } from '@/composables/useViewState'
import { pageView } from '@/composables/useViewState'
import { toScreen } from './hubCamera'
import { boxesOverlap } from './hubCanvas'
import { LAUNCHER_PX, launcherRingRadius, polar } from './hubGeometry'

export interface Launcher { id: string, label: string, icon: string, kind: 'view' | 'new-page' | 'more', view?: ActiveView }

export const MAX_LAUNCHERS = 8

export function launchersFor(
  items: ReadonlyArray<{ view: CoreView, label: string, icon: string }>,
  pages: ReadonlyArray<{ id: string, title: string }>,
  current: ActiveView,
  max = MAX_LAUNCHERS,
): Launcher[] {
  const views: Launcher[] = [
    ...items.filter(i => i.view !== current).map(i => ({ id: i.view, label: i.label, icon: i.icon, kind: 'view' as const, view: i.view })),
    ...pages
      .filter(p => pageView(p.id) !== current)
      .map(p => ({ id: pageView(p.id), label: p.title, icon: '▣', kind: 'view' as const, view: pageView(p.id) })),
  ]
  const newPage: Launcher = { id: 'new-page', label: 'New page', icon: '+', kind: 'new-page' }
  if (views.length + 1 <= max)
    return [...views, newPage]
  return [...views.slice(0, max - 1), { id: 'more', label: 'More…', icon: '…', kind: 'more' }]
}

export function launcherSlotDeg(index: number): number {
  return -67.5 + index * 45
}

const DOCK_X = 30
const DOCK_TOP = 150
const DOCK_STEP = 46

// Where a launcher's centre lands on screen: on the left rail while docked, on its ring slot else.
export function launcherPoint(index: number, docked: boolean, cam: Camera, k0: number, agentRingPx: number, angleDeg = launcherSlotDeg(index)): [number, number] {
  return docked
    ? [DOCK_X, DOCK_TOP + index * DOCK_STEP]
    : toScreen(cam, ...polar(launcherRingRadius(k0, agentRingPx), angleDeg))
}

export function launcherBox(index: number, docked: boolean, cam: Camera, k0: number, agentRingPx: number, angleDeg = launcherSlotDeg(index)): LabelBox {
  const [sx, sy] = launcherPoint(index, docked, cam, k0, agentRingPx, angleDeg)
  return { x: sx - LAUNCHER_PX / 2, y: sy - LAUNCHER_PX / 2, w: LAUNCHER_PX, h: LAUNCHER_PX }
}

const ROTATE_STEP_DEG = 5
const MAX_ROTATE_DEG = 180

// The default slots rotated as one block by the smallest offset clearing every blocked box; null docks.
export function launcherAngles(count: number, ringPx: number, blocked: readonly LabelBox[]): number[] | null {
  const anglesAt = (rotation: number) => Array.from({ length: count }, (_, i) => launcherSlotDeg(i) + rotation)
  const boxAt = (deg: number): LabelBox => {
    const [x, y] = polar(ringPx, deg)
    return { x: x - LAUNCHER_PX / 2, y: y - LAUNCHER_PX / 2, w: LAUNCHER_PX, h: LAUNCHER_PX }
  }
  const clear = (deg: number) => blocked.every(b => !boxesOverlap(boxAt(deg), b))
  const fits = (rotation: number) => anglesAt(rotation).every(clear)
  if (blocked.length === 0 || fits(0))
    return anglesAt(0)
  for (let step = ROTATE_STEP_DEG; step <= MAX_ROTATE_DEG; step += ROTATE_STEP_DEG) {
    if (fits(step))
      return anglesAt(step)
    if (fits(-step))
      return anglesAt(-step)
  }
  return null
}

// The needs-you queue wrapper's layout; HubWidget.vue binds its style from these.
export const QUEUE_TOP_PX = 10
export const QUEUE_MAX_WIDTH_PX = 560
export const QUEUE_SIDE_INSET_PX = 120
export const QUEUE_MAX_HEIGHT_SHARE = 0.45

// The queue at its tallest, from the stage size alone.
export function queueWorstCaseBox(stageWidth: number, stageHeight: number): LabelBox {
  const w = Math.min(QUEUE_MAX_WIDTH_PX, stageWidth - QUEUE_SIDE_INSET_PX)
  return { x: (stageWidth - w) / 2, y: QUEUE_TOP_PX, w, h: stageHeight * QUEUE_MAX_HEIGHT_SHARE }
}
