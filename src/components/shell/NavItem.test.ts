import { mount } from '@vue/test-utils'
import { describe, expect, it } from 'vitest'
import NavItem from './NavItem.vue'

describe('navItem', () => {
  it('renders label when expanded', () => {
    const w = mount(NavItem, { props: { icon: '▦', label: 'Dashboard', active: false, expanded: true } })
    expect(w.text()).toContain('Dashboard')
  })

  it('keeps the label present but transparent when collapsed (icon-only)', () => {
    const w = mount(NavItem, { props: { icon: '▦', label: 'Dashboard', active: false, expanded: false } })
    expect(w.find('.sr-only').exists()).toBe(false)
    const label = w.findAll('span').find(s => s.text() === 'Dashboard')!
    expect(label.classes()).toContain('opacity-0')
  })

  it('sets aria-current=page when active', () => {
    const w = mount(NavItem, { props: { icon: '▦', label: 'Dashboard', active: true, expanded: true } })
    expect(w.get('button').attributes('aria-current')).toBe('page')
  })

  it('emits select on click', async () => {
    const w = mount(NavItem, { props: { icon: '▦', label: 'Dashboard', active: false, expanded: true } })
    await w.get('button').trigger('click')
    expect(w.emitted('select')).toHaveLength(1)
  })

  it('renders badge slot content', () => {
    const w = mount(NavItem, {
      props: { icon: '▦', label: 'Dashboard', active: false, expanded: true },
      slots: { badge: '12' },
    })
    expect(w.text()).toContain('12')
  })

  // The collapsed rail is 43px wide inside a scroll container; an absolutely
  // positioned label box next to the icon widened its scrollable overflow to
  // 132px and put a horizontal scrollbar in the rail. The native title plus the
  // hover/focus expansion carry the label instead.
  it('renders no positioned label box when collapsed, only the native title', () => {
    const w = mount(NavItem, { props: { icon: '▦', label: 'Dashboard', active: false, expanded: false } })
    expect(w.get('button').attributes('title')).toBe('Dashboard')
    expect(w.findAll('span').some(s => s.classes().includes('absolute'))).toBe(false)
  })
})
