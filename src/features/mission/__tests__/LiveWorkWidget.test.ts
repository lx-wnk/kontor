import { mount } from '@vue/test-utils'
import { describe, expect, it, vi } from 'vitest'
import { ref } from 'vue'
import LiveWorkWidget from '../components/LiveWorkWidget.vue'

vi.mock('@/features/pipeline', () => ({
  useTasks: () => ({
    tasks: ref([
      { id: 'a', slug: 'a', title: 'a', currentStage: 'implementation', cwd: '/repo' },
      { id: 'b', slug: 'b', title: 'b', currentStage: 'backlog', cwd: '/repo' },
    ]),
    isLoading: ref(false),
  }),
}))

describe('liveWorkWidget', () => {
  it('renders the icon and counts only the running tasks in the figure slot', () => {
    const wrapper = mount(LiveWorkWidget)
    expect(wrapper.get('header').text()).toContain('▶')
    expect(wrapper.get('[data-testid="live-work-count"]').text()).toBe('1')
    wrapper.unmount()
  })
})
