import { mount } from '@vue/test-utils'
import { describe, expect, it } from 'vitest'
import { NAV_ITEMS } from '@/utils/navConfig'
import HubControls from './components/HubControls.vue'
import { boxesOverlap } from './hubCanvas'
import { LAUNCHER_PX, polar } from './hubGeometry'
import { CONTROLS_BUTTON_COUNT, CONTROLS_BUTTON_PX, CONTROLS_GAP_PX, CONTROLS_RIGHT_INSET_PX, controlsBox, launcherAngles, launchersFor, launcherSlotDeg, MAX_LAUNCHERS } from './hubLaunchers'

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

describe('launcherAngles', () => {
  const RING = 100

  function boxAt(deg: number) {
    const [x, y] = polar(RING, deg)
    return { x: x - LAUNCHER_PX / 2, y: y - LAUNCHER_PX / 2, w: LAUNCHER_PX, h: LAUNCHER_PX }
  }

  it('keeps the default slots when nothing is blocked', () => {
    expect(launcherAngles(4, RING, [])).toEqual([0, 1, 2, 3].map(launcherSlotDeg))
  })

  it('rotates the whole ring off a box that covers a default slot, keeping order and spacing', () => {
    const blocked = [boxAt(launcherSlotDeg(0))]
    const angles = launcherAngles(3, RING, blocked)
    expect(angles).not.toBeNull()
    expect(angles).not.toEqual([0, 1, 2].map(launcherSlotDeg))
    for (let i = 1; i < angles!.length; i++)
      expect(angles![i] - angles![i - 1]).toBe(45)
    for (const deg of angles!)
      expect(blocked.some(b => boxesOverlap(boxAt(deg), b))).toBe(false)
  })

  it('signals to dock when no rotation clears the blocked arc', () => {
    const blocked = [{ x: -1000, y: -1000, w: 2000, h: 2000 }]
    expect(launcherAngles(4, RING, blocked)).toBeNull()
  })
})

describe('controlsBox', () => {
  it('is a 30x166 column, right-aligned and vertically centred on a wide stage', () => {
    expect(controlsBox(1092, 1127)).toEqual({ x: 1092 - 10 - 30, y: (1127 - 166) / 2, w: 30, h: 166 })
  })

  it('is a 30x166 column, right-aligned and vertically centred on a narrow stage', () => {
    expect(controlsBox(584, 734)).toEqual({ x: 584 - 10 - 30, y: (734 - 166) / 2, w: 30, h: 166 })
  })

  it('matches what HubControls renders', () => {
    const column = mount(HubControls, { props: { level: 1, wide: false } }).get('div')
    expect(column.findAll('button')).toHaveLength(CONTROLS_BUTTON_COUNT)
    expect(column.attributes('style')).toContain(`right: ${CONTROLS_RIGHT_INSET_PX}px`)
    expect(column.attributes('style')).toContain(`gap: ${CONTROLS_GAP_PX}px`)
    expect(column.attributes('style')).toContain(`--controls-button: ${CONTROLS_BUTTON_PX}px`)
  })
})
