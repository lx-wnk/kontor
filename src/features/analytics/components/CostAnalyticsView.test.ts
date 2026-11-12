import { flushPromises, mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { toast } from '@/composables/useToast'
import CostAnalyticsView from '@/features/analytics/components/CostAnalyticsView.vue'

// Minimal EventSource stub — jsdom has no native EventSource.
class MockEventSource {
  static instances: MockEventSource[] = []
  onmessage: ((e: MessageEvent) => void) | null = null
  onerror: ((e: Event) => void) | null = null
  readyState = 0

  constructor(public url: string) {
    MockEventSource.instances.push(this)
  }

  close() {
    this.readyState = 2
  }
}

const emptyApiResponse = {
  byModel: [],
  byProject: [],
  byDay: [],
  byWeek: [],
  totalUsd: 0,
  totalInputTokens: 0,
  totalOutputTokens: 0,
  from: '2026-05-03',
  to: '2026-06-02',
  updatedAt: 0,
}

const richApiResponse = {
  byModel: [
    { model: 'claude-opus-4', costUsd: 1.5, inputTokens: 80000, outputTokens: 20000, sessions: 2 },
    { model: 'claude-sonnet-4', costUsd: 0.3, inputTokens: 15000, outputTokens: 5000, sessions: 1 },
  ],
  byProject: [
    { projectPath: '/Users/alex/projects/agent-dashboard', projectName: 'agent-dashboard', costUsd: 1.2, inputTokens: 60000, outputTokens: 15000, sessions: 3 },
    { projectPath: '/Users/alex/projects/other', projectName: 'other', costUsd: 0.6, inputTokens: 35000, outputTokens: 10000, sessions: 2 },
  ],
  byDay: [
    { day: '2026-05-22', model: 'claude-opus-4', costUsd: 1.5 },
    { day: '2026-05-23', model: 'claude-sonnet-4', costUsd: 0.3 },
  ],
  byWeek: [
    { week: '2026-W21', costUsd: 1.8 },
  ],
  totalUsd: 1.8,
  totalInputTokens: 95000,
  totalOutputTokens: 25000,
  from: '2026-05-03',
  to: '2026-06-02',
  updatedAt: Date.now(),
}

beforeEach(() => {
  MockEventSource.instances = []
  vi.stubGlobal('EventSource', MockEventSource)
})

describe('costAnalyticsView', () => {
  it('follows page filters: sends project and hides its own range bar', async () => {
    const mockFetch = vi.fn().mockResolvedValue({ ok: true, json: async () => emptyApiResponse })
    vi.stubGlobal('fetch', mockFetch)
    const wrapper = mount(CostAnalyticsView, { props: { from: '2026-10-01', to: '2026-10-30', project: '/a' } })
    await flushPromises()
    const url = String(mockFetch.mock.calls[0][0])
    expect(url).toContain('project=%2Fa')
    expect(url).toContain('from=2026-10-01')
    expect(url).toContain('to=2026-10-30')
    expect(wrapper.findAll('button').some(b => b.text() === '7d')).toBe(false)
    expect(wrapper.find('input[type="date"]').exists()).toBe(false)
  })

  it('without props keeps its preset buttons and sends no project', async () => {
    const mockFetch = vi.fn().mockResolvedValue({ ok: true, json: async () => emptyApiResponse })
    vi.stubGlobal('fetch', mockFetch)
    const wrapper = mount(CostAnalyticsView)
    await flushPromises()
    expect(wrapper.findAll('button').some(b => b.text() === '7d')).toBe(true)
    expect(String(mockFetch.mock.calls[0][0])).not.toContain('project=')
  })

  it('renders empty-state message when API returns no rows', async () => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue({
      ok: true,
      json: async () => emptyApiResponse,
    }))
    const wrapper = mount(CostAnalyticsView)
    await flushPromises()
    expect(wrapper.text()).toContain('Cost Analytics')
    expect(wrapper.text()).toContain('No cost data yet')
  })

  it('empty state contains accurate copy about automatic import', async () => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue({
      ok: true,
      json: async () => emptyApiResponse,
    }))
    const wrapper = mount(CostAnalyticsView)
    await flushPromises()
    const text = wrapper.text()
    // Must NOT contain the old false claim about stage agents
    expect(text).not.toContain('Once stage agents finish runs')
    // Must contain the accurate description
    expect(text).toContain('imported automatically from your Claude sessions')
  })

  it('empty state renders a "Rescan now" button', async () => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue({
      ok: true,
      json: async () => emptyApiResponse,
    }))
    const wrapper = mount(CostAnalyticsView)
    await flushPromises()
    const buttons = wrapper.findAll('button')
    const rescanBtn = buttons.find(b => b.text().includes('Rescan now'))
    expect(rescanBtn).toBeTruthy()
  })

  it('clicking "Rescan now" issues POST /api/history/import', async () => {
    const mockFetch = vi.fn()
      .mockResolvedValueOnce({
        ok: true,
        json: async () => emptyApiResponse,
      })
      .mockResolvedValueOnce({
        ok: true,
        json: async () => ({}),
      })
    vi.stubGlobal('fetch', mockFetch)

    const wrapper = mount(CostAnalyticsView)
    await flushPromises()

    const buttons = wrapper.findAll('button')
    const rescanBtn = buttons.find(b => b.text().includes('Rescan now'))
    expect(rescanBtn).toBeTruthy()
    await rescanBtn!.trigger('click')
    await flushPromises()

    expect(mockFetch).toHaveBeenCalledWith('/api/history/import', { method: 'POST' })
  })

  it('button is disabled while a scan is in flight', async () => {
    // First fetch: summary (returns no data), second fetch: POST import (stays pending to simulate in-flight)
    let resolveImport!: () => void
    const importPending = new Promise<void>(res => (resolveImport = res))

    const mockFetch = vi.fn()
      .mockResolvedValueOnce({
        ok: true,
        json: async () => emptyApiResponse,
      })
      .mockReturnValueOnce(
        importPending.then(() => ({ ok: true, json: async () => ({}) })),
      )
    vi.stubGlobal('fetch', mockFetch)

    const wrapper = mount(CostAnalyticsView)
    await flushPromises()

    const buttons = wrapper.findAll('button')
    const rescanBtn = buttons.find(b => b.text().includes('Rescan now'))!
    await rescanBtn.trigger('click')
    // Don't await flushPromises — import POST is still in flight

    // The button should be disabled (isImporting = true) and show "Scanning…"
    expect(rescanBtn.attributes('disabled')).toBeDefined()
    expect(rescanBtn.text()).toContain('Scanning')

    // Clean up
    resolveImport()
  })

  it('calls refresh() when stream reports done:true', async () => {
    // fetch call #1: summary (empty), fetch call #2: POST import (ok)
    // fetch call #3: refresh() triggers another summary fetch
    const mockFetch = vi.fn()
      .mockResolvedValue({
        ok: true,
        json: async () => emptyApiResponse,
      })
    vi.stubGlobal('fetch', mockFetch)

    const wrapper = mount(CostAnalyticsView)
    await flushPromises()

    const buttons = wrapper.findAll('button')
    const rescanBtn = buttons.find(b => b.text().includes('Rescan now'))!
    await rescanBtn.trigger('click')
    await flushPromises()

    // Simulate SSE stream: one progress event then done
    const es = MockEventSource.instances[MockEventSource.instances.length - 1]
    expect(es).toBeTruthy()
    expect(es.url).toBe('/api/history/import/status')

    es.onmessage?.(new MessageEvent('message', {
      data: JSON.stringify({ total: 10, processed: 5, imported: 5, errors: 0, done: false }),
    }))
    await flushPromises()

    es.onmessage?.(new MessageEvent('message', {
      data: JSON.stringify({ total: 10, processed: 10, imported: 10, errors: 0, done: true }),
    }))
    await flushPromises()

    // EventSource should be closed
    expect(es.readyState).toBe(2)
    // refresh() should have fired — fetch called again (beyond the initial + POST)
    expect(mockFetch.mock.calls.length).toBeGreaterThanOrEqual(3)
    // importStatus shows imported count
    expect(wrapper.text()).toContain('Imported 10 sessions')
  })

  it('renders model breakdown and total cost when API returns data', async () => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue({
      ok: true,
      json: async () => richApiResponse,
    }))
    const wrapper = mount(CostAnalyticsView)
    await flushPromises()
    const text = wrapper.text()
    expect(text).toContain('Spend by Model')
    expect(text).toContain('claude-opus-4')
    expect(text).toContain('claude-sonnet-4')
    // formatCost output should contain the dollar amount
    expect(text).toMatch(/\$1\.80|\$1\.8/)
  })

  it('surfaces a toast when fetch fails', async () => {
    const errorSpy = vi.spyOn(toast, 'error')
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue({
      ok: false,
      status: 500,
      text: async () => 'boom',
    }))
    const wrapper = mount(CostAnalyticsView)
    await flushPromises()
    expect(errorSpy).toHaveBeenCalledWith(expect.stringContaining('500'))
    expect(wrapper.find('.text-danger-text').exists()).toBe(false)
  })

  // New tests for tokens, project breakdown, and range filter

  it('renders total tokens in the header when data has token counts', async () => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue({
      ok: true,
      json: async () => richApiResponse,
    }))
    const wrapper = mount(CostAnalyticsView)
    await flushPromises()
    const text = wrapper.text()
    // totalInputTokens=95000, totalOutputTokens=25000 → 120000 → '120.0k'
    expect(text).toContain('120.0k')
    expect(text).toContain('tokens')
  })

  it('renders per-model token counts in the model breakdown section', async () => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue({
      ok: true,
      json: async () => richApiResponse,
    }))
    const wrapper = mount(CostAnalyticsView)
    await flushPromises()
    const text = wrapper.text()
    // claude-opus-4: inputTokens=80000, outputTokens=20000 → 100000 → '100.0k'
    expect(text).toContain('100.0k')
    // claude-sonnet-4: inputTokens=15000, outputTokens=5000 → 20000 → '20.0k'
    expect(text).toContain('20.0k')
  })

  it('renders the "Spend by Project" section with project rows', async () => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue({
      ok: true,
      json: async () => richApiResponse,
    }))
    const wrapper = mount(CostAnalyticsView)
    await flushPromises()
    const text = wrapper.text()
    expect(text).toContain('Spend by Project')
    expect(text).toContain('agent-dashboard')
    expect(text).toContain('other')
    // project costs
    expect(text).toMatch(/\$1\.20|\$1\.2/)
    expect(text).toMatch(/\$0\.60|\$0\.6/)
  })

  it('does not render "Spend by Project" section when byProject is empty', async () => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue({
      ok: true,
      json: async () => emptyApiResponse,
    }))
    const wrapper = mount(CostAnalyticsView)
    await flushPromises()
    expect(wrapper.text()).not.toContain('Spend by Project')
  })

  it('clicking the 7d preset triggers a fetch with from= query param', async () => {
    const mockFetch = vi.fn().mockResolvedValue({
      ok: true,
      json: async () => emptyApiResponse,
    })
    vi.stubGlobal('fetch', mockFetch)

    const wrapper = mount(CostAnalyticsView)
    await flushPromises()

    // Find and click the 7d preset button
    const buttons = wrapper.findAll('button')
    const btn7d = buttons.find(b => b.text() === '7d')
    expect(btn7d).toBeTruthy()
    await btn7d!.trigger('click')
    await flushPromises()

    // At least one fetch call after the initial should include from= param
    const calls = mockFetch.mock.calls as [string, ...unknown[]][]
    const rangeCall = calls.find(([url]) => typeof url === 'string' && url.includes('from='))
    expect(rangeCall).toBeTruthy()
    expect(rangeCall![0]).toContain('from=')
    expect(rangeCall![0]).toContain('to=')
  })

  it('clicking the 90d preset triggers a fetch with from= query param', async () => {
    const mockFetch = vi.fn().mockResolvedValue({
      ok: true,
      json: async () => emptyApiResponse,
    })
    vi.stubGlobal('fetch', mockFetch)

    const wrapper = mount(CostAnalyticsView)
    await flushPromises()

    const buttons = wrapper.findAll('button')
    const btn90d = buttons.find(b => b.text() === '90d')
    expect(btn90d).toBeTruthy()
    await btn90d!.trigger('click')
    await flushPromises()

    const calls = mockFetch.mock.calls as [string, ...unknown[]][]
    const rangeCall = calls.find(([url]) => typeof url === 'string' && url.includes('from='))
    expect(rangeCall).toBeTruthy()
  })

  it('applying custom date range triggers a fetch with the provided from/to', async () => {
    const mockFetch = vi.fn().mockResolvedValue({
      ok: true,
      json: async () => emptyApiResponse,
    })
    vi.stubGlobal('fetch', mockFetch)

    const wrapper = mount(CostAnalyticsView)
    await flushPromises()

    // Set custom date inputs
    const dateInputs = wrapper.findAll('input[type="date"]')
    expect(dateInputs.length).toBe(2)
    await dateInputs[0].setValue('2026-01-01')
    await dateInputs[1].setValue('2026-03-31')

    // Click Apply
    const buttons = wrapper.findAll('button')
    const applyBtn = buttons.find(b => b.text() === 'Apply')
    expect(applyBtn).toBeTruthy()
    await applyBtn!.trigger('click')
    await flushPromises()

    const calls = mockFetch.mock.calls as [string, ...unknown[]][]
    const rangeCall = calls.find(([url]) => typeof url === 'string' && url.includes('from=2026-01-01'))
    expect(rangeCall).toBeTruthy()
    expect(rangeCall![0]).toContain('to=2026-03-31')
  })
})
