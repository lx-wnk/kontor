import type { Launcher } from '../hubLaunchers'
import { mount } from '@vue/test-utils'
import { describe, expect, it } from 'vitest'
import HubLaunchers from '../components/HubLaunchers.vue'
import { LAUNCHER_RING_RADIUS, polar } from '../hubGeometry'
import { launcherSlotDeg } from '../hubLaunchers'

const launchers: Launcher[] = [
  { id: 'dashboard', label: 'Dashboard', icon: '▦', kind: 'view', view: 'dashboard' },
  { id: 'pipeline', label: 'Pipeline', icon: '▤', kind: 'view', view: 'pipeline' },
  { id: 'new-page', label: 'New page', icon: '+', kind: 'new-page' },
]
const cam = { k: 2, tx: 500, ty: 400 }

function mountLaunchers(docked: boolean) {
  return mount(HubLaunchers, { props: { launchers, cam, docked } })
}

function position(w: ReturnType<typeof mountLaunchers>, i: number): [number, number] {
  const [, x, y] = /translate\(([-\d.]+)px, ([-\d.]+)px/.exec(w.findAll('button')[i].attributes('style')!)!
  return [Number(x), Number(y)]
}

describe('hubLaunchers', () => {
  it('renders the launchers in ring order with their slot number in the title', () => {
    const w = mountLaunchers(false)
    const buttons = w.findAll('button')
    expect(buttons.map(b => b.attributes('aria-label'))).toEqual(['Dashboard', 'Pipeline', 'New page'])
    expect(buttons.map(b => b.attributes('title'))).toEqual(['Dashboard (1)', 'Pipeline (2)', 'New page (3)'])
    launchers.forEach((_, i) => {
      const [wx, wy] = polar(LAUNCHER_RING_RADIUS, launcherSlotDeg(i))
      const [x, y] = position(w, i)
      expect(x).toBeCloseTo(wx * cam.k + cam.tx)
      expect(y).toBeCloseTo(wy * cam.k + cam.ty)
    })
  })

  it('stacks the launchers in a column along the left edge when docked', () => {
    const w = mountLaunchers(true)
    expect(launchers.map((_, i) => position(w, i))).toEqual([[30, 150], [30, 196], [30, 242]])
  })

  it('emits the pressed launcher', async () => {
    const w = mountLaunchers(false)
    await w.findAll('button')[2].trigger('click')
    expect(w.emitted('launch')).toEqual([[launchers[2]]])
  })
})
