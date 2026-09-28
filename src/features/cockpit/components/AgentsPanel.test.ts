import { mount } from '@vue/test-utils'
import { describe, expect, it } from 'vitest'
import AgentsPanel from './AgentsPanel.vue'

describe('agentsPanel', () => {
  it('renders the icon', () => {
    const wrapper = mount(AgentsPanel)
    expect(wrapper.get('header').text()).toContain('◉')
    wrapper.unmount()
  })
})
