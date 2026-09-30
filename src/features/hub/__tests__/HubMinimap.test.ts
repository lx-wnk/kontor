import type { Circle } from '../hubPack'
import type { Agent } from '@/types'
import { mount } from '@vue/test-utils'
import { describe, expect, it } from 'vitest'
import HubMinimap from '../components/HubMinimap.vue'

const categories = new Map<string, Circle>([['a', { x: -100, y: 0, r: 80 }], ['b', { x: 100, y: 0, r: 80 }]])

function placed(pid: number, x: number) {
  return { agent: { pid } as Agent, x, y: 0, state: 'waiting' as const }
}

function mountMap(cam = { k: 2, tx: 100, ty: 50 }) {
  return mount(HubMinimap, {
    props: { cam, size: { width: 400, height: 300 }, categories, agents: [placed(1, 90)] },
  })
}

function viewport(w: ReturnType<typeof mountMap>) {
  const rect = w.get('rect')
  return ['x', 'y', 'width', 'height'].map(a => Number(rect.attributes(a)))
}

describe('hubMinimap', () => {
  it('draws one circle per category and one dot per agent in the overview map', () => {
    const w = mountMap()
    expect(w.get('svg').attributes('aria-label')).toBe('Overview map')
    expect(w.get('svg').attributes('aria-hidden')).toBe('true')
    expect(w.get('svg').attributes('viewBox')).toBe('-540 -540 1080 1080')
    expect(w.get('svg').attributes('data-hub-layer')).toBeDefined()
    expect(w.findAll('path')).toHaveLength(0)
    expect(w.findAll('[data-testid^="hub-category-"]')).toHaveLength(2)
    expect(w.findAll('circle')).toHaveLength(3)
  })

  // Keyed by pid, an exiting agent takes its own circle with it; keyed by index, Vue would patch the
  // survivor's data into the node the exiting agent held — invisible today, a wrong-colour flash
  // the moment a circle carries a transition.
  it('keeps a surviving agent on its own circle when another exits', async () => {
    const w = mount(HubMinimap, {
      props: { cam: { k: 2, tx: 100, ty: 50 }, size: { width: 400, height: 300 }, categories, agents: [placed(1, 10), placed(2, 90)] },
    })
    const agentDots = () => w.findAll('circle').filter(c => !c.attributes('data-testid'))
    const survivor = agentDots()[1].element
    await w.setProps({ agents: [placed(2, 90)] })
    expect(agentDots()[0].element).toBe(survivor)
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
