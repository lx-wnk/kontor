import { mount } from '@vue/test-utils'
import { describe, expect, it } from 'vitest'
import FleetFilterChips from './FleetFilterChips.vue'

const counts = { all: 5, needsYou: 2, working: 1, idle: 2 }

function mountChips(modelValue: 'all' | 'needsYou' | 'working' | 'idle' = 'all') {
  return mount(FleetFilterChips, { props: { modelValue, counts } })
}

describe('fleetFilterChips', () => {
  it('renders one labelled button per filter with its live count', () => {
    const buttons = mountChips().findAll('button')
    expect(buttons.map(b => b.text().replace(/\s+/g, ' '))).toEqual(['All 5', 'Needs you 2', 'Working 1', 'Idle or done 2'])
  })

  it('exposes a labelled group and presses only the active chip', () => {
    const wrapper = mountChips('working')
    const group = wrapper.get('[role="group"]')
    expect(group.attributes('aria-label')).toBe('Filter agents')
    expect(wrapper.findAll('button').map(b => b.attributes('aria-pressed'))).toEqual(['false', 'false', 'true', 'false'])
  })

  it('emits the clicked filter', async () => {
    const wrapper = mountChips()
    await wrapper.get('[data-testid="fleet-chip-working"]').trigger('click')
    expect(wrapper.emitted('update:modelValue')).toEqual([['working']])
  })
})
