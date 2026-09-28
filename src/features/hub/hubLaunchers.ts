import type { ActiveView, CoreView } from '@/composables/useViewState'

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
      .filter(p => `page:${p.id}` !== current)
      .map(p => ({ id: `page:${p.id}`, label: p.title, icon: '▣', kind: 'view' as const, view: `page:${p.id}` as ActiveView })),
  ]
  const newPage: Launcher = { id: 'new-page', label: 'New page', icon: '+', kind: 'new-page' }
  if (views.length + 1 <= max)
    return [...views, newPage]
  return [...views.slice(0, max - 1), { id: 'more', label: 'More…', icon: '…', kind: 'more' }]
}

export function launcherSlotDeg(index: number): number {
  return -67.5 + index * 45
}
