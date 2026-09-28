import type { Component } from 'vue'
import type { WidgetId, WidgetSpec } from './widgetSpecs'
import { defineAsyncComponent } from 'vue'
import { WIDGET_IDS, WIDGET_SPECS } from './widgetSpecs'

export type WidgetDef = WidgetSpec & { component: Component }

// Each widget is its own chunk: a static import here would put every widget, and
// every feature it imports, into the index chunk that App.vue loads first.
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

export const WIDGETS: Record<WidgetId, WidgetDef> = Object.fromEntries(
  WIDGET_IDS.map(id => [id, { ...WIDGET_SPECS[id], component: defineAsyncComponent(LOADERS[id]) }]),
) as Record<WidgetId, WidgetDef>

export function widgetIds(): WidgetId[] {
  return [...WIDGET_IDS]
}
