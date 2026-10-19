import { mount } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { nextTick } from 'vue'
import PlanUsageWidget from './PlanUsageWidget.vue'

class MockEventSource {
  static instances: MockEventSource[] = []
  onmessage: ((e: MessageEvent) => void) | null = null
  onerror: (() => void) | null = null
  closed = false

  constructor(public url: string) {
    MockEventSource.instances.push(this)
  }

  close() {
    this.closed = true
  }

  emit(payload: unknown) {
    this.onmessage?.({ data: JSON.stringify({ type: 'plan_usage_updated', payload }) } as MessageEvent)
  }
}

const NOW = Date.parse('2026-10-01T12:00:00Z')
const minutes = (n: number) => new Date(NOW + n * 60_000).toISOString()

const fresh = {
  fiveHour: { usedPct: 42, resetsAt: minutes(130) },
  sevenDay: { usedPct: 18, resetsAt: minutes(3 * 24 * 60 + 4 * 60) },
  sampledAt: minutes(-5),
}

function summary(accounts: Record<string, unknown>, staleMinutes = 15) {
  return { accounts, staleMinutes }
}

function mountWidget() {
  const wrapper = mount(PlanUsageWidget)
  return { wrapper, es: MockEventSource.instances[0] }
}

beforeEach(() => {
  vi.useFakeTimers()
  vi.setSystemTime(NOW)
  MockEventSource.instances = []
  vi.stubGlobal('EventSource', MockEventSource)
  vi.stubGlobal('fetch', vi.fn().mockResolvedValue({
    ok: true,
    json: () => Promise.resolve(summary({ '/home/me/.claude-fallback': fresh })),
  }))
})

afterEach(() => {
  vi.useRealTimers()
  vi.unstubAllGlobals()
})

describe('planUsageWidget', () => {
  it('subscribes to the plan-usage stream', () => {
    const { wrapper, es } = mountWidget()
    expect(es.url).toBe('/api/plan-usage/stream')
    wrapper.unmount()
  })

  it('renders the account name and both percentages', async () => {
    const { wrapper, es } = mountWidget()
    es.emit(summary({ '/home/me/.claude-personal': fresh }))
    await nextTick()

    expect(wrapper.get('[data-testid="plan-usage-row"]').text()).toContain('.claude-personal')
    expect(wrapper.get('[data-testid="plan-usage-fiveHour"] [data-testid="plan-usage-pct"]').text()).toBe('42%')
    expect(wrapper.get('[data-testid="plan-usage-sevenDay"] [data-testid="plan-usage-pct"]').text()).toBe('18%')
    expect(wrapper.get('[role="progressbar"]').attributes('aria-valuenow')).toBe('42')
    wrapper.unmount()
  })

  it('renders the reset countdown and ticks it every minute', async () => {
    const { wrapper, es } = mountWidget()
    es.emit(summary({ '/home/me/.claude': fresh }))
    await nextTick()

    const countdown = (key: string) => wrapper.get(`[data-testid="plan-usage-${key}"] [data-testid="plan-usage-countdown"]`).text()
    expect(countdown('fiveHour')).toBe('2h 10m')
    expect(countdown('sevenDay')).toBe('3d 4h')

    vi.advanceTimersByTime(60_000)
    await nextTick()
    expect(countdown('fiveHour')).toBe('2h 9m')
    wrapper.unmount()
  })

  it('shows how old the sample is', async () => {
    const { wrapper, es } = mountWidget()
    es.emit(summary({ '/home/me/.claude': fresh }))
    await nextTick()

    expect(wrapper.get('[data-testid="plan-usage-asof"]').text()).toBe('as of 5m ago')
    wrapper.unmount()
  })

  it('marks only rows older than staleMinutes as stale', async () => {
    const { wrapper, es } = mountWidget()
    es.emit(summary({
      '/home/me/.claude-fresh': { ...fresh, sampledAt: minutes(-14) },
      '/home/me/.claude-old': { ...fresh, sampledAt: minutes(-16) },
    }))
    await nextTick()

    const [freshRow, oldRow] = wrapper.findAll('[data-testid="plan-usage-row"]')
    expect(freshRow.classes()).not.toContain('stale')
    expect(oldRow.classes()).toContain('stale')
    wrapper.unmount()
  })

  it('shows the empty state when there are no accounts', async () => {
    const { wrapper, es } = mountWidget()
    es.emit(summary({}))
    await nextTick()

    expect(wrapper.text()).toContain('No plan usage data. Wire the statusline hook to see rate limits.')
    expect(wrapper.find('[data-testid="plan-usage-row"]').exists()).toBe(false)
    wrapper.unmount()
  })

  it('shows an em dash for an absent window, whether null or omitted', async () => {
    const { wrapper, es } = mountWidget()
    es.emit(summary({ '/home/me/.claude': { fiveHour: null, sampledAt: minutes(-1) } }))
    await nextTick()

    for (const key of ['fiveHour', 'sevenDay']) {
      const line = wrapper.get(`[data-testid="plan-usage-${key}"]`)
      expect(line.text()).toContain('—')
      expect(line.find('[data-testid="plan-usage-pct"]').exists()).toBe(false)
    }
    wrapper.unmount()
  })

  it('falls back to GET /api/plan-usage when the stream errors before any data', async () => {
    const { wrapper, es } = mountWidget()
    es.onerror?.()
    await vi.advanceTimersByTimeAsync(0)

    expect(fetch).toHaveBeenCalledWith('/api/plan-usage')
    expect(wrapper.get('[data-testid="plan-usage-row"]').text()).toContain('.claude-fallback')
    wrapper.unmount()
  })

  it('does not refetch on a stream error once the stream has delivered data', async () => {
    const { wrapper, es } = mountWidget()
    es.emit(summary({ '/home/me/.claude': fresh }))
    es.onerror?.()
    await vi.advanceTimersByTimeAsync(0)

    expect(fetch).not.toHaveBeenCalled()
    wrapper.unmount()
  })

  it('closes the stream and stops ticking on unmount', () => {
    const { wrapper, es } = mountWidget()
    wrapper.unmount()

    expect(es.closed).toBe(true)
    expect(vi.getTimerCount()).toBe(0)
  })
})
