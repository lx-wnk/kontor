import { afterEach, describe, expect, it, vi } from 'vitest'
import { usePlanReview } from '@/features/pipeline/composables/usePlanReview'

function mockFetch(impl: (url: string, init?: RequestInit) => Promise<unknown>) {
  vi.stubGlobal('fetch', vi.fn((url: string, init?: RequestInit) =>
    impl(url, init).then(value => ({
      ok: true,
      status: 200,
      json: async () => value,
    })),
  ))
}

function mockFetchOnce(value: unknown, ok = true, status = 200) {
  vi.stubGlobal('fetch', vi.fn().mockResolvedValue({
    ok,
    status,
    json: async () => value,
  }))
}

afterEach(() => {
  vi.unstubAllGlobals()
  vi.restoreAllMocks()
  vi.useRealTimers()
})

describe('usePlanReview.fetchStatus', () => {
  it('fetches GET /api/plan/{id}/status and exposes gate_state and approved_plan', async () => {
    mockFetchOnce({ gate_state: 'awaiting_user', approved_plan: null })

    const pr = usePlanReview(() => 'task-42')
    await pr.fetchStatus()

    expect(pr.gateState.value).toBe('awaiting_user')
    expect(pr.loading.value).toBe(false)
    const [url] = (globalThis.fetch as ReturnType<typeof vi.fn>).mock.calls[0]
    expect(url).toBe('/api/plan/task-42/status')
  })

  it('exposes approved_plan when present', async () => {
    mockFetchOnce({ gate_state: 'done', approved_plan: { steps: ['build'] } })

    const pr = usePlanReview(() => 'task-99')
    await pr.fetchStatus()

    expect(pr.gateState.value).toBe('done')
    expect(pr.approvedPlan.value).toEqual({ steps: ['build'] })
  })

  it('exposes plan_ready from the server and defaults to false when absent', async () => {
    mockFetchOnce({ gate_state: 'awaiting_user', approved_plan: { steps: ['build'] }, plan_ready: true })
    const ready = usePlanReview(() => 'task-ready')
    await ready.fetchStatus()
    expect(ready.planReady.value).toBe(true)

    mockFetchOnce({ gate_state: 'awaiting_user', approved_plan: { validation_error: 'x' } })
    const absent = usePlanReview(() => 'task-absent')
    await absent.fetchStatus()
    expect(absent.planReady.value).toBe(false)
  })

  it('no-ops when taskId is null', async () => {
    const fetchSpy = vi.fn()
    vi.stubGlobal('fetch', fetchSpy)

    const pr = usePlanReview(() => null)
    await pr.fetchStatus()

    expect(fetchSpy).not.toHaveBeenCalled()
  })

  it('sets error when fetch returns non-ok', async () => {
    mockFetchOnce({}, false, 500)

    const pr = usePlanReview(() => 'task-1')
    await pr.fetchStatus()

    expect(pr.error.value).toBeTruthy()
  })
})

describe('usePlanReview.approve', () => {
  it('pOSTs to /api/plan/{id}/approve and returns the updated task', async () => {
    const updatedTask = { id: 'task-1', currentStage: 'implementation' }
    mockFetch((url, init) => {
      if (url === '/api/plan/task-1/approve' && init?.method === 'POST')
        return Promise.resolve(updatedTask)
      return Promise.resolve({ gate_state: 'awaiting_user', approved_plan: null })
    })

    const pr = usePlanReview(() => 'task-1')
    const result = await pr.approve()

    expect(result).toMatchObject({ id: 'task-1', currentStage: 'implementation' })
    const calls = (globalThis.fetch as ReturnType<typeof vi.fn>).mock.calls
    const approveCall = calls.find((call: any[]) => String(call[0]).includes('/approve'))
    expect(approveCall).toBeTruthy()
    expect(approveCall![1]?.method).toBe('POST')
  })

  it('returns null and sets error on failure', async () => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue({
      ok: false,
      status: 500,
      json: async () => ({ error: 'approve failed' }),
    }))

    const pr = usePlanReview(() => 'task-1')
    const result = await pr.approve()

    expect(result).toBeNull()
    expect(pr.error.value).toBeTruthy()
  })
})

describe('usePlanReview.reject', () => {
  it('pOSTs to /api/plan/{id}/reject with feedback body', async () => {
    const postedBodies: unknown[] = []
    vi.stubGlobal('fetch', vi.fn((url: string, init?: RequestInit) => {
      if (url === '/api/plan/task-1/reject') {
        postedBodies.push(JSON.parse(init?.body as string))
        return Promise.resolve({ ok: true, status: 204, json: async () => ({}) })
      }
      return Promise.resolve({ ok: true, status: 200, json: async () => ({ gate_state: 'awaiting_user' }) })
    }))

    const pr = usePlanReview(() => 'task-1')
    await pr.reject('Please add more detail on step 3')

    expect(postedBodies[0]).toMatchObject({ feedback: 'Please add more detail on step 3' })
  })
})

describe('usePlanReview polling', () => {
  it('polls until gate_state becomes awaiting_user then stops', async () => {
    vi.useFakeTimers()
    let callCount = 0
    vi.stubGlobal('fetch', vi.fn(() => {
      callCount++
      const state = callCount < 3 ? 'running' : 'awaiting_user'
      return Promise.resolve({ ok: true, status: 200, json: async () => ({ gate_state: state, approved_plan: null }) })
    }))

    const pr = usePlanReview(() => 'task-poll')
    await pr.start()

    // After start() the first fetch lands with 'running'; two more polls needed.
    expect(pr.gateState.value).toBe('running')
    expect(callCount).toBe(1)

    await vi.advanceTimersByTimeAsync(1500)
    expect(callCount).toBe(2)
    expect(pr.gateState.value).toBe('running')

    await vi.advanceTimersByTimeAsync(1500)
    expect(callCount).toBe(3)
    expect(pr.gateState.value).toBe('awaiting_user')

    // No further fetches after reaching awaiting_user.
    await vi.advanceTimersByTimeAsync(3000)
    expect(callCount).toBe(3)
  })

  it('stop() cancels a pending poll', async () => {
    vi.useFakeTimers()
    let callCount = 0
    vi.stubGlobal('fetch', vi.fn(() => {
      callCount++
      return Promise.resolve({ ok: true, status: 200, json: async () => ({ gate_state: 'running', approved_plan: null }) })
    }))

    const pr = usePlanReview(() => 'task-stop')
    await pr.start()
    expect(callCount).toBe(1)

    pr.stop()

    await vi.advanceTimersByTimeAsync(3000)
    expect(callCount).toBe(1)
  })

  it('abandons polling when taskId changes mid-flight', async () => {
    vi.useFakeTimers()
    let currentTaskId = 'task-a'
    let callCount = 0
    vi.stubGlobal('fetch', vi.fn(() => {
      callCount++
      return Promise.resolve({ ok: true, status: 200, json: async () => ({ gate_state: 'running', approved_plan: null }) })
    }))

    const pr = usePlanReview(() => currentTaskId)
    await pr.start()
    expect(callCount).toBe(1)

    // Switch task before the scheduled poll fires.
    currentTaskId = 'task-b'

    await vi.advanceTimersByTimeAsync(1500)
    // pollUntilDone sees taskId() !== captured id and returns early without fetching.
    expect(callCount).toBe(1)
  })

  it('discards a status response that resolves after the task switched', async () => {
    let current = 'task-a'
    let resolveA: (() => void) | null = null
    vi.stubGlobal('fetch', vi.fn(() => new Promise<any>((resolve) => {
      resolveA = () => resolve({
        ok: true,
        status: 200,
        json: async () => ({ gate_state: 'running', approved_plan: { content: 'task A plan' } }),
      })
    })))

    const pr = usePlanReview(() => current)
    const pending = pr.fetchStatus()
    current = 'task-b' // switch before task A's response resolves
    resolveA!()
    await pending

    // The late task-A response must not clobber state now belonging to task B.
    expect(pr.gateState.value).toBe('unknown')
    expect(pr.approvedPlan.value).toBeNull()
  })
})
