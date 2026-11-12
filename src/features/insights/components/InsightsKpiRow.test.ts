import type { InsightsSummary, InsightsTotals } from '../insightsApi'
import { mount } from '@vue/test-utils'
import { describe, expect, it } from 'vitest'
import InsightsKpiRow from './InsightsKpiRow.vue'

function totals(over: Partial<InsightsTotals> = {}): InsightsTotals {
  return {
    costUsd: 11.2,
    agentMs: 3_600_000,
    tokens: 5000,
    sessions: 10,
    estimated: 0,
    legacy: 0,
    ...over,
  }
}
function summary(previous: InsightsTotals | null, current = totals()): InsightsSummary {
  return { from: '2026-10-01', to: '2026-10-07', current, previous }
}

describe('insightsKpiRow', () => {
  it('hides delta when previous is null', () => {
    const w = mount(InsightsKpiRow, { props: { summary: summary(null) } })
    expect(w.text()).not.toMatch(/[▲▼]/)
  })

  it('hides delta when the previous value is 0', () => {
    const w = mount(InsightsKpiRow, { props: { summary: summary(totals({ costUsd: 0 })) } })
    expect(w.find('[data-testid="kpi-cost"]').text()).not.toMatch(/[▲▼]/)
  })

  it('shows ▲ and percent', () => {
    const w = mount(InsightsKpiRow, { props: { summary: summary(totals({ costUsd: 10 })) } })
    expect(w.find('[data-testid="kpi-cost"]').text()).toContain('▲12%')
  })

  it('shows ▼ when lower', () => {
    const w = mount(InsightsKpiRow, { props: { summary: summary(totals({ sessions: 20 })) } })
    expect(w.find('[data-testid="kpi-sessions"]').text()).toContain('▼50%')
  })

  it('shows estimated count', () => {
    const w = mount(InsightsKpiRow, { props: { summary: summary(null, totals({ estimated: 3 })) } })
    expect(w.text()).toContain('3 of 10 sessions estimated')
  })

  it('marks cost with ≈ when sessions are estimated', () => {
    const w = mount(InsightsKpiRow, { props: { summary: summary(null, totals({ estimated: 3 })) } })
    expect(w.find('[data-testid="kpi-cost"]').text()).toContain('≈')
  })

  it('has no ≈ or hint when nothing is estimated', () => {
    const w = mount(InsightsKpiRow, { props: { summary: summary(totals()) } })
    expect(w.text()).not.toContain('≈')
    expect(w.text()).not.toContain('per-session tracking')
  })

  it('hides token and agent-time deltas and shows the hint when current has legacy rows', () => {
    const w = mount(InsightsKpiRow, { props: { summary: summary(totals({ tokens: 2500, agentMs: 1_800_000 }), totals({ estimated: 2, legacy: 2 })) } })
    expect(w.find('[data-testid="kpi-tokens"]').text()).not.toMatch(/[▲▼]/)
    expect(w.find('[data-testid="kpi-agent"]').text()).not.toMatch(/[▲▼]/)
    expect(w.find('[data-testid="kpi-cost"]').text()).toMatch(/[▲▼]/)
    expect(w.text()).toContain('2 sessions recorded before per-session tracking')
  })

  it('hides those deltas when only the previous window has legacy rows', () => {
    const w = mount(InsightsKpiRow, { props: { summary: summary(totals({ tokens: 2500, legacy: 4 })) } })
    expect(w.find('[data-testid="kpi-tokens"]').text()).not.toMatch(/[▲▼]/)
    expect(w.text()).toContain('4 sessions recorded before per-session tracking')
  })

  it('exposes a screen-reader label beside ≈', () => {
    const w = mount(InsightsKpiRow, { props: { summary: summary(null, totals({ estimated: 3 })) } })
    expect(w.find('[data-testid="kpi-cost"] .sr-only').text()).toBe('estimated')
  })

  it('renders four tiles without a summary', () => {
    const w = mount(InsightsKpiRow, { props: { summary: null } })
    expect(w.findAll('[data-testid^="kpi-"]')).toHaveLength(4)
  })
})
