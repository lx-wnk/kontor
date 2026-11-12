export const INSIGHTS_TABS = ['overview', 'sessions', 'cost', 'quality', 'workflows'] as const
export type InsightsTab = (typeof INSIGHTS_TABS)[number]

export const INSIGHTS_TAB_KEY = 'insights-tab'

let pendingTab: InsightsTab | null = null

export function readInsightsTab(): InsightsTab {
  if (pendingTab)
    return pendingTab
  try {
    const v = localStorage.getItem(INSIGHTS_TAB_KEY)
    return (INSIGHTS_TABS as readonly string[]).includes(v ?? '') ? (v as InsightsTab) : 'overview'
  }
  catch {
    return 'overview'
  }
}

export function writeInsightsTab(t: InsightsTab): void {
  try {
    localStorage.setItem(INSIGHTS_TAB_KEY, t)
    pendingTab = null
  }
  catch {
    pendingTab = t
  }
}
