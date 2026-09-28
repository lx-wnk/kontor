import { mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'

// Provide a minimal localStorage stub (jsdom not active at this test path level).
const store: Record<string, string> = {}
globalThis.localStorage = {
  getItem: (k: string) => store[k] ?? null,
  setItem: (k: string, v: string) => { store[k] = v },
  removeItem: (k: string) => { delete store[k] },
  clear: () => { Object.keys(store).forEach(k => delete store[k]) },
  length: 0,
  key: () => null,
}

async function load() {
  vi.resetModules()
  localStorage.clear()
  const mod = await import('./AppSidebar.vue')
  const { useViewState } = await import('../../composables/useViewState')
  const { useSidebar } = await import('../../composables/useSidebar')
  return { AppSidebar: mod.default, useViewState, useSidebar }
}

const props = {
  agentCount: 12,
  attentionCount: 0,
  taskCount: 5,
  live: true,
  theme: 'dark' as const,
  canInstall: false,
}

describe('appSidebar', () => {
  beforeEach(() => localStorage.clear())

  it('renders group headers when expanded (pinned)', async () => {
    const { AppSidebar, useSidebar } = await load()
    useSidebar().togglePinned()
    const w = mount(AppSidebar, { props })
    expect(w.text()).toContain('Monitor')
    expect(w.text()).toContain('Build')
    expect(w.text()).toContain('Insights')
  })

  it('clicking a nav item sets activeView', async () => {
    const { AppSidebar, useViewState } = await load()
    const w = mount(AppSidebar, { props })
    const pipelineBtn = w.findAll('button').find(b => b.text().includes('Pipeline'))!
    await pipelineBtn.trigger('click')
    expect(useViewState().activeView.value).toBe('pipeline')
  })

  it('pin toggle button flips aria-expanded', async () => {
    const { AppSidebar } = await load()
    const w = mount(AppSidebar, { props })
    const toggle = w.get('[data-testid="sidebar-pin"]')
    expect(toggle.attributes('aria-expanded')).toBe('false')
    await toggle.trigger('click')
    expect(toggle.attributes('aria-expanded')).toBe('true')
  })

  it('shows agent count badge on Dashboard', async () => {
    const { AppSidebar, useSidebar } = await load()
    useSidebar().togglePinned()
    const w = mount(AppSidebar, { props })
    expect(w.text()).toContain('12')
  })

  it('shows task count badge on Pipeline', async () => {
    const { AppSidebar, useSidebar } = await load()
    useSidebar().togglePinned()
    const w = mount(AppSidebar, { props })
    expect(w.text()).toContain('5')
  })

  it('shows the live status line under the brand when expanded', async () => {
    const { AppSidebar, useSidebar } = await load()
    useSidebar().togglePinned()
    const w = mount(AppSidebar, { props })
    expect(w.text()).toContain('Live · all systems normal')
  })

  it('shows a reconnecting state when not live', async () => {
    const { AppSidebar, useSidebar } = await load()
    useSidebar().togglePinned()
    const w = mount(AppSidebar, { props: { ...props, live: false } })
    expect(w.text()).toContain('Reconnecting')
  })

  it('separates the nav groups with a rule when collapsed', async () => {
    const { AppSidebar } = await load()
    const w = mount(AppSidebar, { props })
    // Three groups, so two rules — the group captions that carry the split when
    // expanded are hidden in the icon rail.
    expect(w.findAll('[data-testid="nav-group-divider"]')).toHaveLength(2)
    expect(w.text()).not.toContain('Monitor')
  })

  // Expanding must not change how many rows sit above any nav item. The caption
  // used to render only when expanded, so hovering inserted three rows and slid
  // every item down by a different amount per group — the pointer was already
  // travelling toward an icon and landed on its neighbour.
  it('reserves the group caption row in both states, so nothing slides', async () => {
    const { AppSidebar, useSidebar } = await load()

    const collapsed = mount(AppSidebar, { props })
    const collapsedSlots = collapsed.findAll('[data-testid="nav-group-slot"]').length
    expect(collapsedSlots).toBeGreaterThan(0)

    useSidebar().togglePinned()
    const expanded = mount(AppSidebar, { props })
    expect(expanded.findAll('[data-testid="nav-group-slot"]')).toHaveLength(collapsedSlots)

    // Same box, same height class, whichever state it is in.
    for (const slot of [...collapsed.findAll('[data-testid="nav-group-slot"]'), ...expanded.findAll('[data-testid="nav-group-slot"]')])
      expect(slot.classes()).toContain('h-7')
  })

  it('drops the rules again once the captions are back', async () => {
    const { AppSidebar, useSidebar } = await load()
    useSidebar().togglePinned()
    const w = mount(AppSidebar, { props })
    expect(w.findAll('[data-testid="nav-group-divider"]')).toHaveLength(0)
  })

  it('keeps the rail at icon width while the hovered nav floats over the content', async () => {
    const { AppSidebar } = await load()
    const w = mount(AppSidebar, { props })
    const rail = w.get('[data-testid="sidebar-rail"]')
    const nav = w.get('nav')
    expect(rail.classes()).toContain('w-[56px]')
    expect(nav.classes()).toContain('absolute')

    await nav.trigger('mouseenter')
    expect(nav.classes()).toContain('w-[220px]')
    // Rail unchanged → the content behind it never reflows.
    expect(rail.classes()).toContain('w-[56px]')
    expect(nav.classes()).toContain('shadow-[4px_0_16px_rgba(0,0,0,0.18)]')
  })

  it('widens the rail instead of floating once pinned', async () => {
    const { AppSidebar, useSidebar } = await load()
    useSidebar().togglePinned()
    const w = mount(AppSidebar, { props })
    expect(w.get('[data-testid="sidebar-rail"]').classes()).toContain('w-[220px]')
    expect(w.get('nav').classes()).not.toContain('shadow-[4px_0_16px_rgba(0,0,0,0.18)]')
  })

  it('expands on keyboard focus and collapses when focus leaves the nav', async () => {
    const { AppSidebar } = await load()
    const w = mount(AppSidebar, { props })
    const nav = w.get('nav')
    await nav.trigger('focusin')
    expect(nav.classes()).toContain('w-[220px]')

    await nav.trigger('focusout', { relatedTarget: document.createElement('button') })
    expect(nav.classes()).toContain('w-[56px]')
  })

  // The floating nav covers the left 220px of the view it just navigated to, so
  // a nav pick with the pointer still on it would swallow the next click there.
  it('collapses after a nav pick while the pointer is still on it', async () => {
    const { AppSidebar } = await load()
    const w = mount(AppSidebar, { props })
    const nav = w.get('nav')
    await nav.trigger('mouseenter')
    expect(nav.classes()).toContain('w-[220px]')

    await w.findAll('button').find(b => b.text().includes('Pipeline'))!.trigger('click')
    expect(nav.classes()).toContain('w-[56px]')
  })

  // A browser focuses the button as part of the click, which `trigger('click')`
  // alone does not reproduce. Re-picking the active view is the case with no
  // safety net: App.vue only moves focus to #main-content when activeView
  // actually changes, so nothing else would ever collapse the nav again.
  it('collapses after a nav pick that also focuses the item', async () => {
    const { AppSidebar } = await load()
    const w = mount(AppSidebar, { props, attachTo: document.body })
    const nav = w.get('nav')
    await nav.trigger('mouseenter')

    const dashboard = w.findAll('button').find(b => b.text().includes('Dashboard'))!
    await dashboard.trigger('focusin')
    await dashboard.trigger('click')

    expect(nav.classes()).toContain('w-[56px]')
    await nav.trigger('mouseleave')
    expect(nav.classes()).toContain('w-[56px]')
  })

  it('expands again once the pointer has left and comes back', async () => {
    const { AppSidebar } = await load()
    const w = mount(AppSidebar, { props })
    const nav = w.get('nav')
    await nav.trigger('mouseenter')
    await w.findAll('button').find(b => b.text().includes('Pipeline'))!.trigger('click')

    await nav.trigger('mouseleave')
    await nav.trigger('mouseenter')
    expect(nav.classes()).toContain('w-[220px]')
  })

  it('leaves keyboard expansion untouched by a pointer suppression', async () => {
    const { AppSidebar } = await load()
    const w = mount(AppSidebar, { props })
    const nav = w.get('nav')
    await nav.trigger('mouseenter')
    await w.findAll('button').find(b => b.text().includes('Pipeline'))!.trigger('click')
    expect(nav.classes()).toContain('w-[56px]')

    await nav.trigger('focusin')
    expect(nav.classes()).toContain('w-[220px]')
  })

  it('keeps the nav open while focus moves between its own items', async () => {
    const { AppSidebar } = await load()
    const w = mount(AppSidebar, { props })
    const nav = w.get('nav')
    await nav.trigger('focusin')
    await nav.trigger('focusout', { relatedTarget: nav.element.querySelector('button') })
    expect(nav.classes()).toContain('w-[220px]')
  })
})
