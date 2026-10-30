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

  it('renders four tiles without a summary', () => {
    const w = mount(InsightsKpiRow, { props: { summary: null } })
    expect(w.findAll('[data-testid^="kpi-"]')).toHaveLength(4)
  })
})
