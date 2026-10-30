import { flushPromises, mount } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { defineComponent, h } from 'vue'
import InsightsView from './InsightsView.vue'

vi.mock('@/features/analytics', () => ({
  CostAnalyticsView: defineComponent({ props: ['from', 'to', 'project'], setup: p => () => h('div', { 'data-testid': 'cost', 'data-project': p.project }) }),
  EvalView: defineComponent({ props: ['hours'], setup: () => () => h('div', { 'data-testid': 'eval' }) }),
  useCostAnalytics: () => ({ summary: { value: { byDay: [] } }, setRange: vi.fn(), start: vi.fn() }),
  useEvalMetrics: () => ({ openAlerts: { value: [] }, start: vi.fn() }),
}))
vi.mock('@/features/workflows', () => ({
  WorkflowsView: defineComponent({ setup: () => () => h('div', { 'data-testid': 'workflows' }) }),
  SessionDagChart: defineComponent({ setup: () => () => h('div') }),
}))

const store: Record<string, string> = {}
const ls = {
  getItem: (k: string) => store[k] ?? null,
  setItem: (k: string, v: string) => { store[k] = v },
}

const totals = { costUsd: 1, agentMs: 1000, tokens: 10, sessions: 1, estimated: 0 }

describe('insightsView', () => {
  const fetchMock = vi.fn()
  beforeEach(() => {
    for (const k of Object.keys(store)) delete store[k]
    vi.stubGlobal('localStorage', ls)
    fetchMock.mockReset()
    fetchMock.mockImplementation(async (url: string) => {
      let body: unknown = []
      if (url.startsWith('/api/insights/summary'))
        body = { from: '', to: '', current: totals, previous: null }
      else if (url.startsWith('/api/insights/sessions'))
        body = { rows: [], total: 0, limit: 50, offset: 0 }
      else if (url.startsWith('/api/insights/projects'))
        body = [{ projectPath: '/code/app', projectName: 'app', sessions: 1 }]
      return { ok: true, json: async () => body }
    })
    vi.stubGlobal('fetch', fetchMock)
  })
  afterEach(() => vi.unstubAllGlobals())

  const mountView = () => mount(InsightsView, { attachTo: document.body })
  const summaryUrls = () => fetchMock.mock.calls.map(c => c[0] as string).filter(u => u.startsWith('/api/insights/summary'))

  it('defaults to the overview tab', async () => {
    const w = mountView()
    await flushPromises()
    expect(w.get('[data-testid="tab-overview"]').attributes('aria-selected')).toBe('true')
    expect(w.find('[data-testid="insights-overview"]').exists()).toBe(true)
    w.unmount()
  })

  it('persists the tab on click', async () => {
    const w = mountView()
    await w.get('[data-testid="tab-sessions"]').trigger('click')
    expect(store['insights-tab']).toBe('sessions')
    w.unmount()
  })

  it('passes the project to the cost tab', async () => {
    store['insights-tab'] = 'cost'
    const w = mountView()
    await flushPromises()
    await w.get('[data-testid="project-select"]').setValue('/code/app')
    expect(w.get('[data-testid="cost"]').attributes('data-project')).toBe('/code/app')
    w.unmount()
  })

  it('disables the project select with a hint on the quality tab', async () => {
    store['insights-tab'] = 'quality'
    const w = mountView()
    await flushPromises()
    expect((w.get('[data-testid="project-select"]').element as HTMLSelectElement).disabled).toBe(true)
    expect(w.text()).toContain('Project filter does not apply to this tab.')
    w.unmount()
  })

  it('refetches the summary when the preset changes', async () => {
    const w = mountView()
    await flushPromises()
    const before = summaryUrls().length
    await w.get('[data-testid="preset-all"]').trigger('click')
    await flushPromises()
    const urls = summaryUrls()
    expect(urls.length).toBeGreaterThan(before)
    expect(urls.at(-1)).not.toContain('from=')
    w.unmount()
  })

  it('moves focus to the next tab on ArrowRight', async () => {
    const w = mountView()
    await flushPromises()
    await w.get('[data-testid="tab-overview"]').trigger('keydown', { key: 'ArrowRight' })
    await flushPromises()
    expect(document.activeElement?.getAttribute('data-testid')).toBe('tab-sessions')
    w.unmount()
  })
})
