import { mount } from '@vue/test-utils'
import { describe, expect, it } from 'vitest'
import AppTopbar from './AppTopbar.vue'

describe('appTopbar', () => {
  it('renders the view title', () => {
    const w = mount(AppTopbar, { props: { activeView: 'insights' } })
    expect(w.text()).toContain('Insights')
  })

  it('renders the cta slot', () => {
    const w = mount(AppTopbar, {
      props: { activeView: 'dashboard' },
      slots: { cta: '<button>+ New Agent</button>' },
    })
    expect(w.text()).toContain('+ New Agent')
  })

  // Search narrows the agent roster only, so it lives in the roster toolbar; on
  // every other view the topbar field promised a filter that never ran.
  it('carries no search field', () => {
    const w = mount(AppTopbar, { props: { activeView: 'pipeline' } })
    expect(w.find('input').exists()).toBe(false)
  })

  it('carries no settings button — it sits with the other global actions in the sidebar', () => {
    const w = mount(AppTopbar, { props: { activeView: 'dashboard' } })
    expect(w.find('button[aria-label="Settings"]').exists()).toBe(false)
  })
})
