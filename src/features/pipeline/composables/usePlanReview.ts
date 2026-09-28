import type { PipelineTask } from '@/types'
import { ref } from 'vue'
import { actionEndpoint } from '@/composables/useRunAction'

const POLL_INTERVAL_MS = 1500

// gate_state mirrors stage_run.status; these values mean the gate is settled.
const TERMINAL_STATES = new Set(['awaiting_user', 'done', 'failed', 'cancelled', 'requeued', 'rate_limited'])

export function usePlanReview(taskId: () => string | null) {
  const gateState = ref<string>('unknown')
  const approvedPlan = ref<Record<string, unknown> | null>(null)
  const loading = ref(false)
  const error = ref<string | null>(null)

  let pollTimer: ReturnType<typeof setTimeout> | null = null

  function stopPolling(): void {
    if (pollTimer) {
      clearTimeout(pollTimer)
      pollTimer = null
    }
  }

  async function fetchStatus(): Promise<void> {
    const id = taskId()
    if (!id)
      return
    loading.value = true
    error.value = null
    try {
      const res = await fetch(`/api/plan/${id}/status`)
      if (!res.ok) {
        error.value = `Failed to load plan status: ${res.status}`
        return
      }
      const data = await res.json() as { gate_state: string, approved_plan?: Record<string, unknown> | null }
      // Discard a response that resolved after the panel switched to another task.
      if (taskId() !== id)
        return
      gateState.value = data.gate_state
      approvedPlan.value = data.approved_plan ?? null
    }
    catch (err) {
      error.value = String(err)
    }
    finally {
      loading.value = false
    }
  }

  // NOTE: taskId is captured at schedule time so a mid-flight task switch abandons the poll.
  async function pollUntilDone(id: string): Promise<void> {
    if (taskId() !== id)
      return
    await fetchStatus()
    if (!TERMINAL_STATES.has(gateState.value))
      pollTimer = setTimeout(() => void pollUntilDone(id), POLL_INTERVAL_MS)
  }

  /** Resets state, fetches, and begins polling if the gate is not yet settled. */
  async function start(): Promise<void> {
    const id = taskId()
    if (!id)
      return
    stopPolling()
    gateState.value = 'unknown'
    approvedPlan.value = null
    error.value = null
    await fetchStatus()
    if (!TERMINAL_STATES.has(gateState.value))
      pollTimer = setTimeout(() => void pollUntilDone(id), POLL_INTERVAL_MS)
  }

  /** Cancels any pending poll. */
  function stop(): void {
    stopPolling()
  }

  async function approve(): Promise<PipelineTask | null> {
    const id = taskId()
    if (!id)
      return null
    error.value = null
    try {
      const endpoint = actionEndpoint('approve_plan', id)!
      const res = await fetch(endpoint, { method: 'POST' })
      if (!res.ok) {
        const body = await res.json().catch(() => null)
        error.value = (body as { error?: string } | null)?.error ?? `Approve failed: ${res.status}`
        return null
      }
      return await res.json() as PipelineTask
    }
    catch (err) {
      error.value = String(err)
      return null
    }
  }

  async function reject(feedback: string): Promise<void> {
    const id = taskId()
    if (!id)
      return
    error.value = null
    try {
      const endpoint = actionEndpoint('reject_plan', id)!
      const res = await fetch(endpoint, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ feedback }),
      })
      if (!res.ok) {
        const body = await res.json().catch(() => null)
        error.value = (body as { error?: string } | null)?.error ?? `Reject failed: ${res.status}`
      }
    }
    catch (err) {
      error.value = String(err)
    }
  }

  return {
    gateState,
    approvedPlan,
    loading,
    error,
    fetchStatus,
    start,
    stop,
    approve,
    reject,
  }
}
