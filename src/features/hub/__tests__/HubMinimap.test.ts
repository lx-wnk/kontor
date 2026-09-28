import { mount } from '@vue/test-utils'
import { describe, expect, it } from 'vitest'
import HubMinimap from '../components/HubMinimap.vue'
import { buildSectors } from '../hubGeometry'

const sectors = buildSectors([{ key: 'a', label: 'a', weight: 1 }, { key: 'b', label: 'b', weight: 1 }])

function mountMap(cam = { k: 2, tx: 100, ty: 50 }) {
  return mount(HubMinimap, {
    props: { cam, size: { width: 400, height: 300 }, sectors, agents: [{ x: 90, y: 0, state: 'waiting' as const }] },
  })
}

function viewport(w: ReturnType<typeof mountMap>) {
  const rect = w.get('rect')
  return ['x', 'y', 'width', 'height'].map(a => Number(rect.attributes(a)))
}

describe('hubMinimap', () => {
  it('draws one wedge per sector and one dot per agent in the overview map', () => {
    const w = mountMap()
    expect(w.get('svg').attributes('aria-label')).toBe('Overview map')
    expect(w.get('svg').attributes('viewBox')).toBe('-540 -540 1080 1080')
    expect(w.get('svg').attributes('data-hub-layer')).toBeDefined()
    expect(w.findAll('path')).toHaveLength(2)
    expect(w.findAll('circle')).toHaveLength(1)
  })

  it('frames the part of the world the camera shows and follows the camera', async () => {
    const w = mountMap()
    expect(viewport(w)).toEqual([-50, -25, 200, 150])
    await w.setProps({ cam: { k: 1, tx: 200, ty: 150 } })
    expect(viewport(w)).toEqual([-200, -150, 400, 300])
  })

  it('emits the world point under a click, the core at the centre', async () => {
    const w = mountMap()
    w.get('svg').element.getBoundingClientRect = () => ({ left: 10, top: 20, width: 108, height: 108 }) as DOMRect
    await w.get('svg').trigger('click', { clientX: 64, clientY: 74 })
    await w.get('svg').trigger('click', { clientX: 118, clientY: 20 })
    const [[cx, cy], [ex, ey]] = w.emitted('fly') as Array<[number, number]>
    expect(cx).toBeCloseTo(0)
    expect(cy).toBeCloseTo(0)
    expect(ex).toBeCloseTo(540)
    expect(ey).toBeCloseTo(-540)
  })
})
