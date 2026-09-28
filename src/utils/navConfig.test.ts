import { describe, expect, it } from 'vitest'
import { ACTIVE_VIEWS } from '../composables/useViewState'
import { NAV_GROUPS, NAV_ITEMS, viewTitle } from './navConfig'

describe('navConfig', () => {
  // Derived from ACTIVE_VIEWS, not from a list typed out here. The earlier
  // version compared against a literal, so it never saw ACTIVE_VIEWS at all:
  // the mission view was added there, got no entry here, and shipped green —
  // unreachable from the sidebar, and titled "Dashboard" by viewTitle's
  // fallback. A test that states an invariant must read both of its sides.
  it('has one item per ActiveView', () => {
    expect([...NAV_ITEMS.map(i => i.view)].sort()).toEqual([...ACTIVE_VIEWS].sort())
  })

  // The fallback turns a missing entry into a plausible wrong title rather
  // than an obvious gap, which is what let the above go unnoticed.
  it('never falls back to the Dashboard title for a view that is not dashboard', () => {
    for (const view of ACTIVE_VIEWS) {
      if (view !== 'dashboard')
        expect(viewTitle(view)).not.toBe('Dashboard')
    }
  })

  it('the Zentrale is the first Monitor item and has a title', () => {
    expect(NAV_ITEMS[0].view).toBe('zentrale')
    expect(viewTitle('zentrale')).toBe('Zentrale')
  })

  it('groups are Monitor, Build and Insights', () => {
    expect(NAV_GROUPS).toEqual(['Monitor', 'Build', 'Insights'])
  })

  it('groups Workflows, Cost and Eval under Insights', () => {
    const insights = NAV_ITEMS.filter(i => i.group === 'Insights').map(i => i.view)
    expect(insights).toEqual(['workflows', 'cost', 'eval'])
  })

  it('every item belongs to a known group', () => {
    for (const item of NAV_ITEMS)
      expect(NAV_GROUPS).toContain(item.group)
  })

  it('viewTitle returns the label for a view', () => {
    expect(viewTitle('dashboard')).toBe('Dashboard')
    expect(viewTitle('cost')).toBe('Cost')
  })

  it('viewTitle returns a page\'s own title, and no core title for an unknown page', () => {
    const pages = [{ id: 'zentrale', title: 'Zentrale' }, { id: 'p-a', title: 'Morning' }]
    expect(viewTitle('page:p-a', pages)).toBe('Morning')
    expect(viewTitle('page:p-gone', pages)).toBe('')
  })
})
