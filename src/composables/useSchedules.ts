import { onUnmounted, ref, shallowRef } from 'vue'
import { SSE_RETRY_DELAY_MS } from '../utils/sse'

export type RunMode = 'job' | 'pipeline'

export interface ScheduleView {
  id: string
  name: string
  enabled: boolean
  nlText?: string | null
  cronExpr: string
  human: string
  timezone: string
  catchup: 'none' | 'once'
  runMode: RunMode
  slugPrefix: string
  title: string
  description?: string | null
  cwd: string
  priority: string
  silverBullet: boolean
  maxIterations: number
  projectId?: string | null
  spawnerId?: string | null
  permissionTemplate?: string | null
  nextRunAt?: string | null
  lastRunAt?: string | null
  lastTaskId?: string | null
  lastSkippedAt?: string | null
  skippedCount: number
  createdAt: string
  updatedAt: string
  applications: string[]
}

export interface SchedulePreview {
  cronExpr: string
  human: string
  timezone: string
  nextRuns: string[]
}

export interface CreateScheduleBody {
  name: string
  nlText?: string
  cronExpr?: string
  timezone?: string
  catchup?: 'none' | 'once'
  runMode?: RunMode
  slugPrefix: string
  title: string
  description?: string
  cwd: string
  priority?: string
  maxIterations?: number
  permissionTemplate?: string
  projectId?: string
  spawnerId?: string
  sourceBranch?: string
  targetBranch?: string
  silverBullet?: boolean
  enabled?: boolean
  applications?: string[]
}

export type UpdateScheduleBody = Partial<CreateScheduleBody>

export interface ScheduleEvent {
  type: 'schedule_changed'
  taskId?: string
  payload?: unknown
}

const schedules = shallowRef<ScheduleView[]>([])
const isLoading = ref(true)
const error = ref<string | null>(null)

let eventSource: EventSource | null = null
let pollTimer: ReturnType<typeof setInterval> | null = null
let sseRetryTimer: ReturnType<typeof setTimeout> | null = null
let subscriberCount = 0

const FALLBACK_POLL_MS = 60_000

async function fetchSchedules(): Promise<void> {
  try {
    const res = await fetch('/api/schedules')
    if (!res.ok)
      throw new Error(`HTTP ${res.status}`)
    schedules.value = await res.json() as ScheduleView[]
    isLoading.value = false
    error.value = null
  }
  catch (err) {
    error.value = (err as Error).message
    isLoading.value = false
  }
}

function applyEvent(event: ScheduleEvent): void {
  if (event.type === 'schedule_changed') {
    const payload = event.payload as ScheduleView | undefined
    // The stream reaches every user; only the user-scoped list may add a row.
    if (payload?.id && schedules.value.some(s => s.id === payload.id))
      schedules.value = schedules.value.map(s => s.id === payload.id ? payload : s)
    else
      void fetchSchedules()
  }
}

function startSSE(): void {
  if (eventSource)
    return
  // Subscribe to the tasks stream for schedule_changed events
  eventSource = new EventSource('/api/tasks/stream')
  eventSource.onmessage = (e) => {
    try {
      const event = JSON.parse(e.data) as { type: string }
      if (event.type === 'schedule_changed')
        applyEvent(event as ScheduleEvent)
    }
    catch {
      // ignore malformed messages
    }
  }
  eventSource.onerror = () => {
    if (eventSource?.readyState === EventSource.CLOSED) {
      stopSSE()
      startPolling()
      sseRetryTimer = setTimeout(() => {
        stopPolling()
        startSSE()
      }, SSE_RETRY_DELAY_MS)
    }
  }
}

function stopSSE(): void {
  if (eventSource) {
    eventSource.close()
    eventSource = null
  }
}

function startPolling(): void {
  if (pollTimer)
    return
  pollTimer = setInterval(() => {
    void fetchSchedules()
  }, FALLBACK_POLL_MS)
}

function stopPolling(): void {
  if (pollTimer) {
    clearInterval(pollTimer)
    pollTimer = null
  }
}

function startStream(): void {
  subscriberCount++
  if (subscriberCount === 1) {
    void fetchSchedules()
    startSSE()
  }
}

export async function createSchedule(body: CreateScheduleBody): Promise<ScheduleView> {
  const res = await fetch('/api/schedules', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(body),
  })
  if (!res.ok) {
    const err = await res.json().catch(() => ({ error: `HTTP ${res.status}` }))
    throw new Error((err as { error: string }).error || 'Failed to create schedule')
  }
  const created = await res.json() as ScheduleView
  schedules.value = [created, ...schedules.value]
  return created
}

export async function updateSchedule(id: string, body: UpdateScheduleBody): Promise<ScheduleView> {
  const res = await fetch(`/api/schedules/${id}`, {
    method: 'PATCH',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(body),
  })
  if (!res.ok) {
    const err = await res.json().catch(() => ({ error: `HTTP ${res.status}` }))
    throw new Error((err as { error: string }).error || 'Failed to update schedule')
  }
  const updated = await res.json() as ScheduleView
  schedules.value = schedules.value.map(s => s.id === id ? updated : s)
  return updated
}

export async function deleteSchedule(id: string): Promise<void> {
  const res = await fetch(`/api/schedules/${id}`, { method: 'DELETE' })
  if (!res.ok) {
    const err = await res.json().catch(() => ({ error: `HTTP ${res.status}` }))
    throw new Error((err as { error: string }).error || 'Failed to delete schedule')
  }
  schedules.value = schedules.value.filter(s => s.id !== id)
}

export async function runScheduleNow(id: string): Promise<{ taskId: string }> {
  const res = await fetch(`/api/schedules/${id}/run-now`, { method: 'POST' })
  if (!res.ok) {
    const err = await res.json().catch(() => ({ error: `HTTP ${res.status}` }))
    throw new Error((err as { error: string }).error || 'Failed to trigger schedule')
  }
  return await res.json() as { taskId: string }
}

export async function previewSchedule(body: { nlText?: string, cronExpr?: string, timezone?: string }): Promise<SchedulePreview> {
  const res = await fetch('/api/schedules/preview', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(body),
  })
  if (!res.ok) {
    const err = await res.json().catch(() => ({ error: `HTTP ${res.status}` }))
    throw new Error((err as { error: string }).error || 'Unparseable phrase')
  }
  return await res.json() as SchedulePreview
}

export function useSchedules(options?: { autoStart?: boolean }) {
  let started = false
  if (options?.autoStart !== false) {
    startStream()
    started = true
  }

  onUnmounted(() => {
    if (!started)
      return
    subscriberCount--
    if (subscriberCount === 0) {
      stopSSE()
      stopPolling()
      if (sseRetryTimer) {
        clearTimeout(sseRetryTimer)
        sseRetryTimer = null
      }
    }
  })

  return {
    schedules,
    isLoading,
    error,
    refetch: fetchSchedules,
    startStream,
  }
}
