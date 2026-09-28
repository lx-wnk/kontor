import type { ActiveView, CoreView } from '../composables/useViewState'
import { pageIdOf } from '../composables/useViewState'

export type NavGroup = 'Monitor' | 'Build' | 'Insights'

export interface NavItemConfig {
  view: CoreView
  label: string
  icon: string
  group: NavGroup
}

export const NAV_GROUPS: NavGroup[] = ['Monitor', 'Build', 'Insights']

export const NAV_ITEMS: NavItemConfig[] = [
  { view: 'zentrale', label: 'Zentrale', icon: '◎', group: 'Monitor' },
  { view: 'dashboard', label: 'Dashboard', icon: '▦', group: 'Monitor' },
  { view: 'pipeline', label: 'Pipeline', icon: '▤', group: 'Build' },
  { view: 'schedules', label: 'Schedules', icon: '⏱', group: 'Build' },
  { view: 'workflows', label: 'Workflows', icon: '⤳', group: 'Insights' },
  { view: 'cost', label: 'Cost', icon: '◷', group: 'Insights' },
  { view: 'eval', label: 'Eval', icon: '⬡', group: 'Insights' },
]

export function viewTitle(view: ActiveView, pages: ReadonlyArray<{ id: string, title: string }> = []): string {
  const id = pageIdOf(view)
  if (id !== null)
    return pages.find(p => p.id === id)?.title ?? ''
  return NAV_ITEMS.find(i => i.view === view)?.label ?? 'Dashboard'
}

export function navItemTestId(view: ActiveView): string {
  const id = pageIdOf(view)
  return id !== null ? `nav-page-${id}` : `nav-item-${view}`
}

export function navItemSelector(view: ActiveView): string {
  return `[data-testid="${navItemTestId(view)}"]`
}
