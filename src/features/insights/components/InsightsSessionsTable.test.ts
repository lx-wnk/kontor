import type { InsightsSessionsPage } from '../insightsApi'
import { mount } from '@vue/test-utils'
import { describe, expect, it } from 'vitest'
import { sessionRow } from './fixtures'
import InsightsSessionsTable from './InsightsSessionsTable.vue'

function page(over: Partial<InsightsSessionsPage> = {}): InsightsSessionsPage {
  return {
    rows: [sessionRow(), sessionRow({ sessionId: 's-2', title: 'Est', costSource: 'estimate' })],
    total: 120,
    limit: 50,
    offset: 0,
    ...over,
  }
}
function mountTable(p = page(), sort = 'cost' as const) {
  return mount(InsightsSessionsTable, { props: { page: p, sort, order: 'desc' } })
}

describe('insightsSessionsTable', () => {
  it('emits sort on header click', async () => {
    const w = mountTable()
    await w.find('[data-testid="sort-agent_ms"]').trigger('click')
    expect(w.emitted('sort')?.[0]).toEqual(['agent_ms'])
  })

  it('sets aria-sort on the active column only', () => {
    const w = mountTable()
    expect(w.find('[data-testid="sort-cost"]').element.closest('th')?.getAttribute('aria-sort')).toBe('descending')
    expect(w.find('[data-testid="sort-agent_ms"]').element.closest('th')?.getAttribute('aria-sort')).toBe('none')
  })

  it('marks estimate rows with ≈', () => {
    const w = mountTable()
    const rows = w.findAll('tbody tr')
    expect(rows[0].text()).not.toContain('≈')
    expect(rows[1].text()).toContain('≈')
  })

  it('opens a row on click and Enter with the row element', async () => {
    const w = mountTable()
    const row = w.findAll('tbody tr')[0]
    expect(row.attributes('tabindex')).toBe('0')
    await row.trigger('click')
    await row.trigger('keydown.enter')
    const emitted = w.emitted('open')!
    expect(emitted).toHaveLength(2)
    expect((emitted[0][0] as { sessionId: string }).sessionId).toBe('s-1')
    expect(emitted[0][1]).toBe(row.element)
  })

  it('pages with prev/next', async () => {
    const w = mountTable(page({ offset: 50 }))
    await w.find('[data-testid="page-next"]').trigger('click')
    await w.find('[data-testid="page-prev"]').trigger('click')
    expect(w.emitted('page')).toEqual([[100], [0]])
  })

  it('disables prev on the first page and next on the last', () => {
    expect(mountTable().find('[data-testid="page-prev"]').attributes('disabled')).toBeDefined()
    const last = mountTable(page({ offset: 100 }))
    expect(last.find('[data-testid="page-next"]').attributes('disabled')).toBeDefined()
  })
})
