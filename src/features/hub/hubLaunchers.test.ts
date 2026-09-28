import { describe, expect, it } from 'vitest'
import { NAV_ITEMS } from '@/utils/navConfig'
import { launchersFor, MAX_LAUNCHERS } from './hubLaunchers'

describe('launchersFor', () => {
  it('lists the other core views and ends with New page', () => {
    const l = launchersFor(NAV_ITEMS, [], 'zentrale')
    expect(l.map(x => x.id)).toEqual([...NAV_ITEMS.filter(i => i.view !== 'zentrale').map(i => i.view), 'new-page'])
    expect(l.at(-1)!.kind).toBe('new-page')
  })
  it('adds own pages after the core views', () => {
    const l = launchersFor(NAV_ITEMS, [{ id: 'morning', title: 'Morning' }], 'zentrale')
    expect(l.at(-2)).toMatchObject({ id: 'page:morning', label: 'Morning', view: 'page:morning', kind: 'view' })
  })
  it('turns the eighth slot into More when there are more than eight', () => {
    const pages = [{ id: 'a', title: 'A' }, { id: 'b', title: 'B' }, { id: 'c', title: 'C' }]
    const l = launchersFor(NAV_ITEMS, pages, 'zentrale')
    expect(l).toHaveLength(MAX_LAUNCHERS)
    expect(l.at(-1)).toMatchObject({ id: 'more', kind: 'more' })
  })
  it('lists every view without a More slot when uncapped', () => {
    const pages = [{ id: 'a', title: 'A' }, { id: 'b', title: 'B' }, { id: 'c', title: 'C' }]
    const l = launchersFor(NAV_ITEMS, pages, 'zentrale', Infinity)
    expect(l.map(x => x.id)).toEqual(expect.arrayContaining(['page:a', 'page:b', 'page:c', 'new-page']))
    expect(l.map(x => x.id)).not.toContain('more')
  })
  it('leaves out the view the hub is on', () => {
    expect(launchersFor(NAV_ITEMS, [{ id: 'a', title: 'A' }], 'page:a').map(x => x.id)).not.toContain('page:a')
  })
})
