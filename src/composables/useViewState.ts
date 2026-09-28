import type { AgentGroup, AgentSort } from '../utils/agentGroup'
import { ref, watch } from 'vue'
import { PAGE_ID_PATTERN, ZENTRALE_PAGE_ID } from '@/features/workspace/layout'
import { AGENT_GROUP_OPTIONS, AGENT_SORT_OPTIONS, resolveGroup } from '../utils/agentGroup'

export type CoreView = 'zentrale' | 'dashboard' | 'workflows' | 'pipeline' | 'cost' | 'schedules' | 'eval'
// A page the operator created. The prefix keeps "is this one of mine?"
// answerable in one line everywhere a view is compared.
export type ActiveView = CoreView | `page:${string}`
export type DashboardLayout = 'cards' | 'list'

// Exported: the command palette derives one navigation command per view, so
// a view added here shows up there without a second list to keep in step.
export const ACTIVE_VIEWS: CoreView[] = ['zentrale', 'dashboard', 'workflows', 'pipeline', 'cost', 'schedules', 'eval']

export function isCoreView(v: string): v is CoreView {
  return (ACTIVE_VIEWS as string[]).includes(v)
}

export function pageIdOf(v: ActiveView): string | null {
  return v.startsWith('page:') ? v.slice(5) : null
}

export function resolveView(v: ActiveView, pageIds: string[]): ActiveView {
  const id = pageIdOf(v)
  if (id === null)
    return v
  if (id === ZENTRALE_PAGE_ID || !PAGE_ID_PATTERN.test(id) || !pageIds.includes(id))
    return 'zentrale'
  return v
}
const AGENT_SORT_VALUES: AgentSort[] = AGENT_SORT_OPTIONS.map(o => o.value)
const AGENT_GROUP_VALUES: AgentGroup[] = AGENT_GROUP_OPTIONS.map(o => o.value)

function readInitial(): { view: ActiveView, layout: DashboardLayout } {
  const ls = typeof localStorage !== 'undefined' ? localStorage : null
  const stored = ls?.getItem('agent-active-view')
  const storedLayout = ls?.getItem('agent-dashboard-layout')

  const legacy = ls?.getItem('agent-view-mode')
  let view: ActiveView
  let layout: DashboardLayout = storedLayout === 'list' ? 'list' : 'cards'

  // A page view is kept unchecked: the layout loads after this runs, and
  // resolveView falls back once it has.
  if (stored && (isCoreView(stored) || stored.startsWith('page:'))) {
    view = stored as ActiveView
  }
  else {
    view = 'zentrale'
    if (stored) {
      // stored value is no longer valid — overwrite to stop the guard firing every load
      ls?.setItem('agent-active-view', 'zentrale')
    }

    if (!stored && legacy) {
      switch (legacy) {
        case 'pipeline':
          view = 'pipeline'
          break
        case 'workflows':
          view = 'workflows'
          break
        case 'config-explorer':
          // Config view was folded into Settings → Spawners → Details; send
          // legacy deep-links to the dashboard rather than a dead view.
          view = 'dashboard'
          break
        case 'cost-analytics':
          view = 'cost'
          break
        case 'list':
          view = 'dashboard'
          layout = 'list'
          break
        case 'cards':
          view = 'dashboard'
          layout = 'cards'
          break
      }
      ls?.removeItem('agent-view-mode')
    }
  }
  return { view, layout }
}

function readStoredSort(): AgentSort {
  const ls = typeof localStorage !== 'undefined' ? localStorage : null
  const v = ls?.getItem('agent-dashboard-sort')
  return v && AGENT_SORT_VALUES.includes(v as AgentSort) ? (v as AgentSort) : 'latest'
}

function readStoredGroup(): AgentGroup {
  const ls = typeof localStorage !== 'undefined' ? localStorage : null
  const v = ls?.getItem('agent-dashboard-group')
  return v && AGENT_GROUP_VALUES.includes(v as AgentGroup) ? (v as AgentGroup) : 'none'
}

// The grouping parked while a filter hides it. Empty string = nothing parked.
function readParkedGroup(): AgentGroup | null {
  const ls = typeof localStorage !== 'undefined' ? localStorage : null
  const v = ls?.getItem('agent-dashboard-group-parked')
  return v && AGENT_GROUP_VALUES.includes(v as AgentGroup) ? (v as AgentGroup) : null
}

function readStoredProject(): string {
  const ls = typeof localStorage !== 'undefined' ? localStorage : null
  return ls?.getItem('agent-dashboard-project') ?? 'all'
}

function readStoredSpawner(): string {
  const ls = typeof localStorage !== 'undefined' ? localStorage : null
  return ls?.getItem('agent-dashboard-spawner') ?? 'all'
}

const initial = readInitial()
const activeView = ref<ActiveView>(initial.view)
const dashboardLayout = ref<DashboardLayout>(initial.layout)
const dashboardSort = ref<AgentSort>(readStoredSort())
const dashboardGroup = ref<AgentGroup>(readStoredGroup())
const dashboardProject = ref<string>(readStoredProject())
const dashboardSpawner = ref<string>(readStoredSpawner())
const parkedGroup = ref<AgentGroup | null>(readParkedGroup())

// Filtering to one spawner takes "Spawner" out of the grouping options. Parking
// the choice keeps dashboardGroup a value the control can actually show — so
// picking "No grouping" while filtered sticks — and restores it when the filter
// clears, which is what the disappearing option looked like it promised.
watch(dashboardSpawner, (spawner) => {
  if (resolveGroup(dashboardGroup.value, spawner) !== dashboardGroup.value) {
    parkedGroup.value = dashboardGroup.value
    dashboardGroup.value = 'none'
  }
  else if (parkedGroup.value && resolveGroup(parkedGroup.value, spawner) === parkedGroup.value) {
    dashboardGroup.value = parkedGroup.value
    parkedGroup.value = null
  }
}, { flush: 'sync' })

/** Records an explicit grouping choice, which supersedes any parked one. */
function setDashboardGroup(value: AgentGroup): void {
  parkedGroup.value = null
  dashboardGroup.value = value
}

watch(activeView, (v) => {
  if (typeof localStorage !== 'undefined')
    localStorage.setItem('agent-active-view', v)
}, { flush: 'sync' })
watch(dashboardLayout, (l) => {
  if (typeof localStorage !== 'undefined')
    localStorage.setItem('agent-dashboard-layout', l)
}, { flush: 'sync' })
watch(dashboardSort, (v) => {
  if (typeof localStorage !== 'undefined')
    localStorage.setItem('agent-dashboard-sort', v)
}, { flush: 'sync' })
watch(dashboardGroup, (v) => {
  if (typeof localStorage !== 'undefined')
    localStorage.setItem('agent-dashboard-group', v)
}, { flush: 'sync' })
watch(parkedGroup, (v) => {
  if (typeof localStorage !== 'undefined')
    localStorage.setItem('agent-dashboard-group-parked', v ?? '')
}, { flush: 'sync' })
watch(dashboardProject, (v) => {
  if (typeof localStorage !== 'undefined')
    localStorage.setItem('agent-dashboard-project', v)
}, { flush: 'sync' })
watch(dashboardSpawner, (v) => {
  if (typeof localStorage !== 'undefined')
    localStorage.setItem('agent-dashboard-spawner', v)
}, { flush: 'sync' })

export function useViewState() {
  return { activeView, dashboardLayout, dashboardSort, dashboardGroup, setDashboardGroup, dashboardProject, dashboardSpawner }
}
