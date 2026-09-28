import { mount } from '@vue/test-utils'
import { describe, expect, it, vi } from 'vitest'
import { ref } from 'vue'
import CostTodayWidget from './CostTodayWidget.vue'

vi.mock('@/composables/useTodayCost', () => ({
  useTodayCost: () => ({ todayUsd: ref(4.2), start: vi.fn() }),
}))

describe('costTodayWidget', () => {
  it('renders the icon and the cost in the figure slot', () => {
    const wrapper = mount(CostTodayWidget)
    expect(wrapper.get('header').text()).toContain('$')
    expect(wrapper.get('[data-testid="cost-today-value"]').text()).toBe('$4.20')
    wrapper.unmount()
  })
})
