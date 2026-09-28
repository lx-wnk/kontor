import { mount } from '@vue/test-utils'
import { describe, expect, it } from 'vitest'
import PipelinePanel from './PipelinePanel.vue'

describe('pipelinePanel', () => {
  it('renders the icon', () => {
    const wrapper = mount(PipelinePanel)
    expect(wrapper.get('header').text()).toContain('▦')
    wrapper.unmount()
  })
})
