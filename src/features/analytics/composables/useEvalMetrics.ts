import type { DriftAlert, EvalMetricSnapshot, MetricKey } from '@/types'
import { onUnmounted, ref, shallowRef } from 'vue'
import { MAX_LOOKBACK_HOURS, METRIC_KEYS } from '@/utils/evalMetrics'

const POLL_INTERVAL_MS = 60_000

export function useEvalMetrics() {
  // snapshots grouped by metricKey for easy charting
  const snapshots = shallowRef<Record<MetricKey, EvalMetricSnapshot[]>>(emptySnapshots())
  const openAlerts = shallowRef<DriftAlert[]>([])
  const isLoading = ref(true)
  const error = ref<string | null>(null)
  const hours = ref(168)
  // Most recent snapshot found outside the current window, or null when the
  // window has data of its own (no hint needed) or none was ever recorded.
  const lastDataAt = ref<string | null>(null)

  let intervalId: ReturnType<typeof setInterval> | null = null
  let aborter: AbortController | null = null
  let driftEventSource: EventSource | null = null
  let active = true

  function emptySnapshots(): Record<MetricKey, EvalMetricSnapshot[]> {
    return Object.fromEntries(METRIC_KEYS.map(k => [k, [] as EvalMetricSnapshot[]])) as Record<MetricKey, EvalMetricSnapshot[]>
  }

  // One request, not one per metric. The endpoint returns every metric when no
  // `metric` filter is given, and nine parallel calls put a single page load
  // over the server's own per-IP burst of 20 — the app answered itself 429 and
  // rendered "Failed to load eval data" on a cold start.
  async function fetchMetrics(signal: AbortSignal): Promise<void> {
    const res = await fetch(`/api/eval/metrics?hours=${hours.value}`, { signal })
    if (!res.ok)
      throw new Error(`HTTP ${res.status}`)
    const all = await res.json() as EvalMetricSnapshot[]

    const grouped = emptySnapshots()
    const wanted = new Set<string>(METRIC_KEYS)
    for (const row of all) {
      if (wanted.has(row.metricKey))
        grouped[row.metricKey as MetricKey].push(row)
    }
    snapshots.value = grouped

    const isEmpty = METRIC_KEYS.every(key => grouped[key].length === 0)
    if (!isEmpty || hours.value >= MAX_LOOKBACK_HOURS) {
      lastDataAt.value = null
      return
    }
    // Best-effort hint only, probed off a single metric (all metrics are
    // written by the same scan) — never lets a probe failure fail the load.
    try {
      const res = await fetch(`/api/eval/metrics?metric=${METRIC_KEYS[0]}&hours=${MAX_LOOKBACK_HOURS}`, { signal })
      const data = res.ok ? await res.json() as EvalMetricSnapshot[] : []
      lastDataAt.value = data.reduce<string | null>((max, d) => (!max || d.recordedAt > max ? d.recordedAt : max), null)
    }
    catch {
      lastDataAt.value = null
    }
  }

  async function fetchAlerts(signal: AbortSignal): Promise<void> {
    const res = await fetch('/api/eval/drift?status=open', { signal })
    if (!res.ok)
      throw new Error(`HTTP ${res.status}`)
    openAlerts.value = await res.json() as DriftAlert[]
  }

  async function fetchAll(): Promise<void> {
    if (!active)
      return
    aborter?.abort()
    aborter = new AbortController()
    const { signal } = aborter
    try {
      await Promise.all([fetchMetrics(signal), fetchAlerts(signal)])
      error.value = null
    }
    catch (e: unknown) {
      if ((e as { name?: string })?.name === 'AbortError')
        return
      error.value = e instanceof Error ? e.message : 'Failed to load eval data'
    }
    finally {
      isLoading.value = false
    }
  }

  async function fetchAlertsOnly(): Promise<void> {
    if (!active)
      return
    const ctrl = new AbortController()
    try {
      await fetchAlerts(ctrl.signal)
      error.value = null
    }
    catch (e: unknown) {
      if ((e as { name?: string })?.name !== 'AbortError')
        error.value = e instanceof Error ? e.message : 'Failed to load alerts'
    }
  }

  function startDriftStream(): void {
    if (driftEventSource)
      return
    // EventSource is absent in non-browser environments (jsdom/SSR); the 60s poll covers data freshness there.
    if (typeof EventSource === 'undefined')
      return
    driftEventSource = new EventSource('/api/tasks/stream')
    driftEventSource.onmessage = (e) => {
      try {
        const event = JSON.parse(e.data as string) as { type: string }
        if (event.type === 'eval_drift')
          void fetchAlertsOnly()
      }
      catch { /* ignore malformed frames */ }
    }
    driftEventSource.onerror = () => {
      if (driftEventSource?.readyState === EventSource.CLOSED) {
        driftEventSource = null
        // SSE dropped — the 60 s setInterval poll in start() keeps data fresh
      }
    }
  }

  function stopDriftStream(): void {
    driftEventSource?.close()
    driftEventSource = null
  }

  async function acknowledge(id: string): Promise<void> {
    const res = await fetch(`/api/eval/drift/${id}/ack`, { method: 'POST' })
    if (!res.ok)
      throw new Error(`HTTP ${res.status}`)
    await fetchAlertsOnly()
  }

  async function runScan(): Promise<void> {
    const res = await fetch('/api/eval/scan', { method: 'POST' })
    if (!res.ok)
      throw new Error(`HTTP ${res.status}`)
    await fetchAll()
  }

  async function setHours(newHours: number): Promise<void> {
    if (newHours === hours.value)
      return
    hours.value = newHours
    await fetchAll()
  }

  function start(): void {
    if (intervalId !== null)
      return
    active = true
    void fetchAll()
    intervalId = setInterval(fetchAll, POLL_INTERVAL_MS)
    startDriftStream()
  }

  function stop(): void {
    active = false
    if (intervalId !== null) {
      clearInterval(intervalId)
      intervalId = null
    }
    aborter?.abort()
    aborter = null
    stopDriftStream()
  }

  onUnmounted(stop)

  return {
    snapshots,
    openAlerts,
    isLoading,
    error,
    hours,
    lastDataAt,
    acknowledge,
    runScan,
    setHours,
    refresh: fetchAll,
    start,
    stop,
  }
}
