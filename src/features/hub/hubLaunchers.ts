import type { Camera } from './hubCamera'
import type { LabelBox } from './hubCanvas'
import type { ActiveView, CoreView } from '@/composables/useViewState'
import { pageView } from '@/composables/useViewState'
import { toScreen } from './hubCamera'
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
export function launcherPoint(index: number, docked: boolean, cam: Camera, agentRingPx: number): [number, number] {
  return docked
    ? [DOCK_X, DOCK_TOP + index * DOCK_STEP]
    : toScreen(cam, ...polar(launcherRingRadius(cam.k, agentRingPx), launcherSlotDeg(index)))
}

export function launcherBox(index: number, docked: boolean, cam: Camera, agentRingPx: number): LabelBox {
  const [sx, sy] = launcherPoint(index, docked, cam, agentRingPx)
  return { x: sx - LAUNCHER_PX / 2, y: sy - LAUNCHER_PX / 2, w: LAUNCHER_PX, h: LAUNCHER_PX }
}
