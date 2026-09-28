import type { Component } from 'vue'
import type { WidgetId, WidgetSpec } from './widgetSpecs'
import { defineAsyncComponent, reactive } from 'vue'
import PageLoadError from '@/components/shell/PageLoadError.vue'
import { WIDGET_IDS, WIDGET_SPECS } from './widgetSpecs'

export type WidgetDef = WidgetSpec & { component: Component }

// One chunk per feature barrel, not per widget — a static import here would put every feature into the entry chunk App.vue loads first.
const LOADERS: Record<WidgetId, () => Promise<Component>> = {
  'kontor': () => import('@/features/mission').then(m => m.KontorWidget),
  'hub': () => import('@/features/hub').then(m => m.HubWidget),
  'live-work': () => import('@/features/mission').then(m => m.LiveWorkWidget),
  'agents': () => import('@/features/cockpit').then(m => m.AgentsPanel),
  'pipeline': () => import('@/features/cockpit').then(m => m.PipelinePanel),
  'routines': () => import('@/features/cockpit').then(m => m.RoutinesPanel),
  'github': () => import('@/features/cockpit').then(m => m.GitHubPanel),
  'memory': () => import('@/features/cockpit').then(m => m.MemoryPanel),
  'cost-today': () => import('@/features/analytics').then(m => m.CostTodayWidget),
}

// Read by App.vue's pageHasHub: a chunk that 404s (e.g. the server was rebuilt
// while a tab stayed open) must not leave the needs-you strip hidden behind a
// hub tile that never rendered. A remounted tile retries its loader, so a later
// success clears the entry again.
export const failedWidgets: Set<WidgetId> = reactive(new Set<WidgetId>())

function widgetComponent(id: WidgetId): Component {
  return defineAsyncComponent({
    loader: () => LOADERS[id]().then((component) => {
      failedWidgets.delete(id)
      return component
    }, (err) => {
      failedWidgets.add(id)
      throw err
    }),
    errorComponent: PageLoadError,
  })
}

export const WIDGETS: Record<WidgetId, WidgetDef> = Object.fromEntries(
  WIDGET_IDS.map(id => [id, { ...WIDGET_SPECS[id], component: widgetComponent(id) }]),
) as Record<WidgetId, WidgetDef>

export function widgetIds(): WidgetId[] {
  return [...WIDGET_IDS]
}
