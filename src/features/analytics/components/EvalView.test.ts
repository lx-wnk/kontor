import { flushPromises, mount } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { nextTick } from 'vue'
import { toast } from '@/composables/useToast'
import EvalView from '@/features/analytics/components/EvalView.vue'
import { openListbox, optionByLabel } from '@/utils/testSelect'

const emptyAlerts: unknown[] = []

const openAlert = {
  id: 'a1',
  spawnerId: 'sp1',
  model: 'claude-opus-4',
  stage: 'implementation',
  metricKey: 'success_rate',
  status: 'open',
  direction: 'down',
  baselineValue: 0.9,
  recentValue: 0.7,
  delta: -0.2,
  threshold: 0.1,
  sampleCount: 10,
  detectedAt: '2026-06-15T00:00:00Z',
  acknowledgedAt: null,
}

function snapshot(metricKey: string, value: number, recordedAt: string) {
  return {
    id: `${metricKey}-${recordedAt}`,
    spawnerId: 'sp1',
    model: 'default',
    stage: 'implementation',
    metricKey,
    value,
    sampleCount: 3,
    windowStart: '2026-09-11T00:00:00Z',
    windowEnd: recordedAt,
    recordedAt,
  }
}

// Answers every metric request with real snapshots. The default factory returns
// [] for metrics, which is why a view that never drew its charts stayed green.
function mockFetchWithMetrics() {
  return vi.fn().mockImplementation((url: string) => {
    const u = String(url)
    if (u.includes('/api/eval/drift'))
      return Promise.resolve({ ok: true, json: async () => [] })
    const key = new URL(u, 'http://localhost').searchParams.get('metric') ?? 'success_rate'
    return Promise.resolve({
      ok: true,
      json: async () => [
        snapshot(key, 0.5, '2026-09-16T00:00:00Z'),
        snapshot(key, 0.8, '2026-09-17T00:00:00Z'),
        snapshot(key, 1, '2026-09-18T00:00:00Z'),
      ],
    })
  })
}

function mockFetchFactory(alerts: unknown[]) {
  return vi.fn().mockImplementation((url: string) => {
    if (String(url).includes('/api/eval/drift'))
      return Promise.resolve({ ok: true, json: async () => alerts })
    return Promise.resolve({ ok: true, json: async () => [] })
  })
}

beforeEach(() => {
  vi.stubGlobal('fetch', mockFetchFactory(emptyAlerts))
})

afterEach(() => {
  document.body.innerHTML = ''
})

describe('evalView charts', () => {
  // The <svg> does not exist until data arrives — the card renders the empty
  // reason until then. A render scheduled before Vue patches the DOM finds a
  // null ref and draws nothing, leaving nine empty cards that nothing
  // re-triggers. Assert the element actually received content.
  it('draws into every chart once snapshots arrive', async () => {
    vi.stubGlobal('fetch', mockFetchWithMetrics())
    const wrapper = mount(EvalView, { attachTo: document.body })
    await flushPromises()
    await nextTick()
    await nextTick()

    const svgs = wrapper.findAll('svg')
    expect(svgs.length).toBeGreaterThan(0)
    const blank = svgs.filter(s => s.element.childNodes.length === 0)
    expect(blank.length).toBe(0)
    wrapper.unmount()
  })

  it('says why a card is empty when there are no snapshots', async () => {
    vi.stubGlobal('fetch', mockFetchFactory(emptyAlerts))
    const wrapper = mount(EvalView, { attachTo: document.body })
    await flushPromises()
    await nextTick()

    expect(wrapper.text()).toContain('No stage runs in the last')
    expect(wrapper.findAll('svg').length).toBe(0)
    wrapper.unmount()
  })
})

describe('evalView', () => {
  it('renders the heading', async () => {
    const wrapper = mount(EvalView)
    await flushPromises()
    expect(wrapper.text()).toContain('Eval / Drift Detection')
  })

  it('renders a "Run scan now" button', async () => {
    const wrapper = mount(EvalView)
    await flushPromises()
    const btn = wrapper.findAll('button').find(b => b.text().includes('Run scan now'))
    expect(btn).toBeTruthy()
  })

  it('shows empty-state message when no alerts', async () => {
    const wrapper = mount(EvalView)
    await flushPromises()
    expect(wrapper.text()).toContain('No open drift alerts')
  })

  it('renders open alerts with metric label and direction', async () => {
    vi.stubGlobal('fetch', mockFetchFactory([openAlert]))
    const wrapper = mount(EvalView)
    await flushPromises()
    const text = wrapper.text()
    expect(text).toContain('Success rate')
    expect(text).toContain('0.900')
    expect(text).toContain('0.700')
    expect(text).toContain('n=10')
  })

  it('renders acknowledge button for each open alert', async () => {
    vi.stubGlobal('fetch', mockFetchFactory([openAlert]))
    const wrapper = mount(EvalView)
    await flushPromises()
    const ackBtn = wrapper.findAll('button').find(b => b.text() === 'Acknowledge')
    expect(ackBtn).toBeTruthy()
  })

  it('clicking "Acknowledge" calls POST /api/eval/drift/{id}/ack', async () => {
    const mockFetch = vi.fn()
      .mockImplementation((url: string, opts?: RequestInit) => {
        if (opts?.method === 'POST' && String(url).includes('/ack'))
          return Promise.resolve({ ok: true, json: async () => ({}) })
        if (String(url).includes('/api/eval/drift'))
          return Promise.resolve({ ok: true, json: async () => [openAlert] })
        return Promise.resolve({ ok: true, json: async () => [] })
      })
    vi.stubGlobal('fetch', mockFetch)

    const wrapper = mount(EvalView)
    await flushPromises()

    const ackBtn = wrapper.findAll('button').find(b => b.text() === 'Acknowledge')
    expect(ackBtn).toBeTruthy()
    await ackBtn!.trigger('click')
    await flushPromises()

    const ackCall = (mockFetch.mock.calls as [string, RequestInit][]).find(([url, opts]) =>
      String(url).includes('/api/eval/drift/a1/ack') && opts?.method === 'POST',
    )
    expect(ackCall).toBeTruthy()
  })

  it('clicking "Run scan now" calls POST /api/eval/scan', async () => {
    const mockFetch = vi.fn()
      .mockImplementation((url: string, opts?: RequestInit) => {
        if (opts?.method === 'POST' && String(url).includes('/api/eval/scan'))
          return Promise.resolve({ ok: true, json: async () => ({ ok: true }) })
        if (String(url).includes('/api/eval/drift'))
          return Promise.resolve({ ok: true, json: async () => [] })
        return Promise.resolve({ ok: true, json: async () => [] })
      })
    vi.stubGlobal('fetch', mockFetch)

    const wrapper = mount(EvalView)
    await flushPromises()

    const scanBtn = wrapper.findAll('button').find(b => b.text().includes('Run scan now'))
    expect(scanBtn).toBeTruthy()
    await scanBtn!.trigger('click')
    await flushPromises()

    const scanCall = (mockFetch.mock.calls as [string, RequestInit][]).find(([url, opts]) =>
      String(url).includes('/api/eval/scan') && opts?.method === 'POST',
    )
    expect(scanCall).toBeTruthy()
  })

  it('surfaces a toast and renders a persistent error banner on fetch failure, not the empty state', async () => {
    const errorSpy = vi.spyOn(toast, 'error')
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue({ ok: false, status: 503 }))
    const wrapper = mount(EvalView)
    await flushPromises()
    expect(errorSpy).toHaveBeenCalledWith(expect.stringContaining('503'))
    const banner = wrapper.find('[role="alert"]')
    expect(banner.exists()).toBe(true)
    expect(banner.text()).toContain('503')
    expect(wrapper.text()).not.toContain('No stage runs in the last')
  })

  it('names the reason and window in the empty state when there is no error', async () => {
    const wrapper = mount(EvalView)
    await flushPromises()
    expect(wrapper.find('[role="alert"]').exists()).toBe(false)
    expect(wrapper.text()).toContain('No stage runs in the last 7 days.')
  })

  it('picking a wider range requests the widened hours value', async () => {
    const mockFetch = mockFetchFactory(emptyAlerts)
    vi.stubGlobal('fetch', mockFetch)
    const wrapper = mount(EvalView, { attachTo: document.body })
    await flushPromises()

    const panel = await openListbox(wrapper.get('[role="combobox"]'))
    optionByLabel(panel, 'Last 30 days').dispatchEvent(new MouseEvent('click', { bubbles: true }))
    await flushPromises()

    const call = (mockFetch.mock.calls as [string][]).find(([url]) =>
      String(url).includes('/api/eval/metrics') && String(url).includes('hours=720'),
    )
    expect(call).toBeTruthy()
  })

  it('renders dimension label grouping for alerts', async () => {
    vi.stubGlobal('fetch', mockFetchFactory([openAlert]))
    const wrapper = mount(EvalView)
    await flushPromises()
    expect(wrapper.text()).toContain('sp1 / claude-opus-4 / implementation')
  })
})
