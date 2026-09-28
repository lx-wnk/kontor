export const WIDGET_IDS = ['kontor', 'hub', 'live-work', 'agents', 'pipeline', 'routines', 'github', 'memory', 'cost-today'] as const
export type WidgetId = typeof WIDGET_IDS[number]

export const HUB_WIDGET: WidgetId = 'hub'

export function isWidgetId(id: string): id is WidgetId {
  return (WIDGET_IDS as readonly string[]).includes(id)
}

export interface WidgetSpec {
  id: WidgetId
  title: string
  defaultColSpan: number
  defaultRowSpan: number
  minColSpan: number
  minRowSpan: number
}

function spec(id: WidgetId, title: string, def: [number, number], min: [number, number]): WidgetSpec {
  return { id, title, defaultColSpan: def[0], defaultRowSpan: def[1], minColSpan: min[0], minRowSpan: min[1] }
}

// Spans from the Zentrale spec's widget table. Plain data on purpose: the layout
// rules import this without pulling in a single component.
export const WIDGET_SPECS: Record<WidgetId, WidgetSpec> = {
  'kontor': spec('kontor', 'Kontor', [6, 1], [4, 1]),
  'hub': spec('hub', 'Zentrale', [6, 11], [6, 6]),
  'live-work': spec('live-work', 'Live work', [3, 5], [3, 3]),
  'agents': spec('agents', 'Agents', [3, 3], [3, 2]),
  'pipeline': spec('pipeline', 'Pipeline', [3, 3], [3, 2]),
  'routines': spec('routines', 'Routines', [3, 4], [3, 2]),
  'github': spec('github', 'GitHub', [3, 3], [3, 2]),
  'memory': spec('memory', 'Memory', [3, 3], [3, 2]),
  'cost-today': spec('cost-today', 'Today', [3, 3], [2, 2]),
}
