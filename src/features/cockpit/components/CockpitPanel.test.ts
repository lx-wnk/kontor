import type { PanelState } from '../panelState'
import { mount } from '@vue/test-utils'
import { describe, expect, it } from 'vitest'
import { PANEL_STATES } from '../panelState'
import CockpitPanel from './CockpitPanel.vue'

// The five-state rule, as an assertion rather than a promise: for every state
// exactly one state marker is in the DOM, and the content slot appears only in
// "ready". A panel that collapsed two states would fail here, once, for every
// panel that uses the shell.
describe('cockpitPanel', () => {
  const others = (state: PanelState) => PANEL_STATES.filter(s => s !== state)

  it.each(PANEL_STATES)('renders exactly the %s marker and nothing else', (state) => {
    const wrapper = mount(CockpitPanel, {
      props: { id: 'demo', title: 'Demo', state, message: 'because' },
      slots: { default: '<p data-testid="demo-content">rows</p>' },
    })

    if (state === 'ready') {
      expect(wrapper.find('[data-testid="demo-content"]').exists()).toBe(true)
    }
    else {
      expect(wrapper.find(`[data-testid="cockpit-demo-${state}"]`).exists()).toBe(true)
      expect(wrapper.find('[data-testid="demo-content"]').exists()).toBe(false)
    }

    for (const other of others(state))
      expect(wrapper.findAll(`[data-testid="cockpit-demo-${other}"]`)).toHaveLength(0)
  })

  it('shows the server message on denied and on failed, and never invents one', () => {
    const denied = mount(CockpitPanel, { props: { id: 'demo', title: 'Demo', state: 'denied', message: 'memory.read is not granted in this scope' } })
    expect(denied.get('[data-testid="cockpit-demo-denied"]').text()).toContain('memory.read is not granted in this scope')

    const failed = mount(CockpitPanel, { props: { id: 'demo', title: 'Demo', state: 'failed', message: 'HTTP 500' } })
    expect(failed.get('[data-testid="cockpit-demo-failed"]').text()).toContain('HTTP 500')
  })

  it('renders the icon, the uppercase label and the figure slot when ready', () => {
    const w = mount(CockpitPanel, {
      props: { id: 'x', title: 'Today', icon: '$', state: 'ready' },
      slots: { figure: '<span data-testid="fig">$4.20</span>', default: '<p>body</p>' },
    })
    const header = w.get('header')
    expect(header.text()).toContain('$')
    expect(header.get('h2').classes()).toContain('uppercase')
    expect(w.find('[data-testid="fig"]').exists()).toBe(true)
  })

  it('hides the figure while loading', () => {
    const w = mount(CockpitPanel, { props: { id: 'x', title: 'Today', state: 'loading' }, slots: { figure: '<span data-testid="fig">1</span>' } })
    expect(w.find('[data-testid="fig"]').exists()).toBe(false)
  })
})
