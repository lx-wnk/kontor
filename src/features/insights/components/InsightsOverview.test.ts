import { flushPromises, mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { shallowRef } from 'vue'
import InsightsOverview from './InsightsOverview.vue'

const byDay = shallowRef([
  { day: '2026-10-02', model: 'a', costUsd: 1 },
  { day: '2026-10-01', model: 'a', costUsd: 2 },
  { day: '2026-10-01', model: 'b', costUsd: 3 },
])
vi.mock('@/features/analytics', () => ({
  useCostAnalytics: () => ({ summary: shallowRef({ byDay: byDay.value }), setRange: vi.fn(), start: vi.fn() }),
  useEvalMetrics: () => ({ openAlerts: shallowRef([]), start: vi.fn() }),
}))

describe('insightsOverview', () => {
  const fetchMock = vi.fn()
  beforeEach(() => {
    fetchMock.mockReset()
    fetchMock.mockResolvedValue({ ok: true, json: async () => ({ rows: [], total: 0, limit: 5, offset: 0 }) })
    vi.stubGlobal('fetch', fetchMock)
  })

  it('sums byDay per day, ascending', () => {
    const w = mount(InsightsOverview, { props: { filters: { preset: '30d', to: '2026-10-29' } } })
    expect((w.vm as unknown as { perDay: unknown }).perDay).toEqual([
      { day: '2026-10-01', costUsd: 5 },
      { day: '2026-10-02', costUsd: 1 },
    ])
  })

  it('queries the top 5 sessions by cost', async () => {
    mount(InsightsOverview, { props: { filters: { preset: '30d', to: '2026-10-29' } } })
    await flushPromises()
    const url = fetchMock.mock.calls[0][0] as string
    expect(url).toContain('sort=cost')
    expect(url).toContain('order=desc')
    expect(url).toContain('limit=5')
  })
})
