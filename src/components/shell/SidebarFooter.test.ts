import { mount } from '@vue/test-utils'
import { describe, expect, it } from 'vitest'
import SidebarFooter from './SidebarFooter.vue'

const base = {
  expanded: true,
  theme: 'dark' as const,
  canInstall: false,
}

describe('sidebarFooter', () => {
  it('emits open-sessions / toggle-theme', async () => {
    const w = mount(SidebarFooter, { props: base })
    await w.get('[data-testid="footer-sessions"]').trigger('click')
    await w.get('[data-testid="footer-theme"]').trigger('click')
    expect(w.emitted('openSessions')).toHaveLength(1)
    expect(w.emitted('toggleTheme')).toHaveLength(1)
  })

  it('has no quota bar (that one lives in the status bar)', () => {
    const w = mount(SidebarFooter, { props: base })
    expect(w.find('[role="progressbar"]').exists()).toBe(false)
  })

  // Settings sat in the topbar between #187 and the toolbar restructure; it is
  // back beside the other global actions now that the topbar keeps only the
  // view title, its CTA, and the offline badge.
  it('emits openSettings from the gear button', async () => {
    const w = mount(SidebarFooter, { props: base })
    await w.get('[data-testid="footer-settings"]').trigger('click')
    expect(w.emitted('openSettings')).toHaveLength(1)
  })

  it('hides install button unless canInstall', () => {
    const w = mount(SidebarFooter, { props: base })
    expect(w.find('[data-testid="footer-install"]').exists()).toBe(false)
  })

  it('shows install button and emits install when canInstall', async () => {
    const w = mount(SidebarFooter, { props: { ...base, canInstall: true } })
    const btn = w.get('[data-testid="footer-install"]')
    expect(btn.text()).toContain('Install PWA')
    await btn.trigger('click')
    expect(w.emitted('install')).toHaveLength(1)
  })

  // The last nav group is bottom-anchored, so the footer's height sets where
  // Insights sits. A row when expanded and a column when collapsed made it
  // ~80px shorter on hover and slid those items out from under the pointer.
  it('lays its actions out the same way in both states', () => {
    const collapsed = mount(SidebarFooter, { props: { ...base, expanded: false } })
    const expanded = mount(SidebarFooter, { props: { ...base, expanded: true } })

    const collapsedClasses = collapsed.get('[data-testid="footer-actions"]').classes()
    const expandedClasses = expanded.get('[data-testid="footer-actions"]').classes()
    expect(expandedClasses).toEqual(collapsedClasses)
    expect(collapsedClasses).toContain('flex-col')
  })
})
