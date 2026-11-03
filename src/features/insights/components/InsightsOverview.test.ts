import { flushPromises, mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { ref, shallowRef } from 'vue'
import { sessionRow } from './fixtures'
import InsightsOverview from './InsightsOverview.vue'

const byDay = shallowRef([
  { day: '2026-10-02', model: 'a', costUsd: 1 },
  { day: '2026-10-01', model: 'a', costUsd: 2 },
  { day: '2026-10-01', model: 'b', costUsd: 3 },
])
const costError = ref<string | null>(null)
const evalError = ref<string | null>(null)
const setRange = vi.fn()
const startAlerts = vi.fn()
vi.mock('@/features/analytics', () => ({
  useCostAnalytics: () => ({ summary: shallowRef({ byDay: byDay.value }), error: costError, setRange }),
  useEvalMetrics: () => ({ openAlerts: shallowRef([]), error: evalError, startAlerts }),
}))

const props = { filters: { preset: '30d' as const, to: '2026-10-29' }, estimated: false }

describe('insightsOverview', () => {
  const fetchMock = vi.fn()
  beforeEach(() => {
    fetchMock.mockReset()
    setRange.mockClear()
    costError.value = null
    evalError.value = null
    fetchMock.mockResolvedValue({ ok: true, json: async () => ({ rows: [], total: 0, limit: 5, offset: 0 }) })
    vi.stubGlobal('fetch', fetchMock)
  })

  it('sums byDay per day, ascending', () => {
    const w = mount(InsightsOverview, { props })
    expect(w.findAll('tbody tr').map(r => r.text())).toEqual(['2026-10-01$5.00', '2026-10-02$1.00'])
  })

  it('passes the all-time floor and project to setRange', () => {
    mount(InsightsOverview, { props: { filters: { preset: 'all', to: '2026-10-29', project: '/p' }, estimated: false } })
    expect(setRange).toHaveBeenCalledWith('2000-01-01', '2026-10-29', '/p')
  })

  it('shows cost and eval errors instead of empty text', () => {
    costError.value = 'HTTP 500'
    evalError.value = 'HTTP 502'
    const w = mount(InsightsOverview, { props })
    expect(w.text()).toContain('HTTP 500')
    expect(w.text()).toContain('HTTP 502')
    expect(w.text()).not.toContain('No cost data')
    expect(w.text()).not.toContain('No open drift alerts')
  })

  it('marks the cost-per-day heading when estimated', () => {
    const w = mount(InsightsOverview, { props: { ...props, estimated: true } })
    expect(w.text()).toContain('≈Cost per day')
  })

  it('never requests eval metrics', async () => {
    mount(InsightsOverview, { props })
    await flushPromises()
    expect(startAlerts).toHaveBeenCalled()
    expect(fetchMock.mock.calls.some(c => String(c[0]).includes('/api/eval/metrics'))).toBe(false)
  })

  it('queries the top 5 sessions by cost', async () => {
    mount(InsightsOverview, { props })
    await flushPromises()
    const url = fetchMock.mock.calls[0][0] as string
    expect(url).toContain('sort=cost')
    expect(url).toContain('order=desc')
    expect(url).toContain('limit=5')
  })

  it('marks estimated and unknown-model costs in the top 5', async () => {
    fetchMock.mockResolvedValue({
      ok: true,
      json: async () => ({
        rows: [
          sessionRow({ sessionId: 'a', costSource: 'estimate' }),
          sessionRow({ sessionId: 'b', costUnknownModel: true }),
          sessionRow({ sessionId: 'c' }),
        ],
        total: 3,
        limit: 5,
        offset: 0,
      }),
    })
    const w = mount(InsightsOverview, { props })
    await flushPromises()
    const costs = w.findAll('[data-testid="top-session-cost"]')
    expect(costs[0].text()).toContain('≈')
    expect(costs[0].attributes('title')).toContain('estimate')
    expect(costs[1].text()).toContain('≈')
    expect(costs[1].attributes('title')).toContain('no known price')
    expect(costs[2].text()).not.toContain('≈')
  })
})
