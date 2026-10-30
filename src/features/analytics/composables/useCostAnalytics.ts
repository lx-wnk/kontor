import { onUnmounted, ref, shallowRef } from 'vue'
import { errorMessage } from '@/utils/errorMessage'

const POLL_INTERVAL_MS = 60_000

export interface ModelBreakdown {
  model: string
  costUsd: number
  inputTokens: number
  outputTokens: number
  sessions: number
}

export interface ProjectBreakdown {
  projectPath: string
  projectName: string
  costUsd: number
  inputTokens: number
  outputTokens: number
  sessions: number
}

export interface DayPoint {
  day: string
  model: string
  costUsd: number
}

export interface WeekPoint {
  week: string
  costUsd: number
}

export interface CostSummary {
  byModel: ModelBreakdown[]
  byProject: ProjectBreakdown[]
  byDay: DayPoint[]
  byWeek: WeekPoint[]
  totalUsd: number
  totalInputTokens: number
  totalOutputTokens: number
  from: string
  to: string
  updatedAt: number
}

function emptySummary(): CostSummary {
  return {
    byModel: [],
    byProject: [],
    byDay: [],
    byWeek: [],
    totalUsd: 0,
    totalInputTokens: 0,
    totalOutputTokens: 0,
    from: '',
    to: '',
    updatedAt: 0,
  }
}

function toDateString(d: Date): string {
  return d.toISOString().slice(0, 10)
}

/**
 * Fetches /api/cost/summary and re-polls every 60s while the composable is
 * active. Returns a shared reactive view; safe to call from multiple sites
 * — only one fetch loop runs per `useCostAnalytics()` invocation.
 */
export function useCostAnalytics() {
  const summary = shallowRef<CostSummary>(emptySummary())
  const isLoading = ref(true)
  const error = ref<string | null>(null)
  const from = ref<string>('')
  const to = ref<string>('')
  const project = ref<string>('')

  let intervalId: ReturnType<typeof setInterval> | null = null
  let aborter: AbortController | null = null
  let active = true

  async function fetchOnce() {
    if (!active)
      return
    aborter?.abort()
    aborter = new AbortController()
    try {
      const params = new URLSearchParams()
      if (from.value)
        params.set('from', from.value)
      if (to.value)
        params.set('to', to.value)
      if (project.value)
        params.set('project', project.value)
      const qs = params.toString()
      const url = qs ? `/api/cost/summary?${qs}` : '/api/cost/summary'
      const res = await fetch(url, { signal: aborter.signal })
      if (!res.ok)
        throw new Error(`HTTP ${res.status}`)
      const data = await res.json() as CostSummary
      summary.value = data
      error.value = null
    }
    catch (e: unknown) {
      if ((e as { name?: string })?.name === 'AbortError')
        return
      error.value = errorMessage(e, 'Failed to load cost summary')
    }
    finally {
      isLoading.value = false
    }
  }

  function setRange(newFrom: string, newTo: string, newProject = '') {
    from.value = newFrom
    to.value = newTo
    project.value = newProject
    void fetchOnce()
  }

  function start() {
    if (intervalId !== null)
      return
    active = true
    // Default range: last 30 days, computed lazily inside start() to avoid module-load date issues
    if (!from.value && !to.value) {
      const now = new Date()
      const past = new Date(now)
      past.setDate(past.getDate() - 30)
      to.value = toDateString(now)
      from.value = toDateString(past)
    }
    void fetchOnce()
    intervalId = setInterval(fetchOnce, POLL_INTERVAL_MS)
  }

  function stop() {
    active = false
    if (intervalId !== null) {
      clearInterval(intervalId)
      intervalId = null
    }
    aborter?.abort()
    aborter = null
  }

  onUnmounted(stop)

  return {
    summary,
    isLoading,
    error,
    from,
    to,
    setRange,
    refresh: fetchOnce,
    start,
    stop,
  }
}
