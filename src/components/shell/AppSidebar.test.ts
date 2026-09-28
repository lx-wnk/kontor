import type { WorkspaceLayout } from '../../features/workspace'
import { flushPromises, mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { nextTick, ref } from 'vue'

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

let ws: ReturnType<typeof workspaceStub>

function workspaceStub(pages: WorkspaceLayout['pages']) {
  return {
    layout: ref<WorkspaceLayout>({ version: 1, pages }),
    loaded: ref(true),
    locked: ref<string | null>(null),
    editing: ref(false),
    save: vi.fn(async (next: WorkspaceLayout) => {
      ws.layout.value = next
      return true
    }),
  }
}

vi.mock('@/features/workspace', async importOriginal => ({
  ...await importOriginal<typeof import('../../features/workspace')>(),
  useWorkspace: () => ws,
}))

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
  beforeEach(() => {
    localStorage.clear()
    ws = workspaceStub([
      { id: 'zentrale', title: 'Zentrale', tiles: [] },
      { id: 'p-a', title: 'Morning', tiles: [] },
    ])
  })

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

  it('the rule is the visible one when collapsed', async () => {
    const { AppSidebar } = await load()
    const w = mount(AppSidebar, { props })
    // Four groups (three core, one of pages), so three rules — always in the DOM, crossfaded against the
    // caption inside the same reserved box so nothing resizes on expansion.
    const dividers = w.findAll('[data-testid="nav-group-divider"]')
    expect(dividers).toHaveLength(3)
    for (const divider of dividers)
      expect(divider.classes()).toContain('opacity-100')

    const captions = w.findAll('[data-testid="nav-group-slot"] span').filter(s => s.text() === 'Monitor')
    expect(captions).toHaveLength(1)
    expect(captions[0]!.classes()).toContain('opacity-0')
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

  it('the caption is the visible one once the rules are back to hairlines', async () => {
    const { AppSidebar, useSidebar } = await load()
    useSidebar().togglePinned()
    const w = mount(AppSidebar, { props })
    // Still in the DOM (crossfade, not removal) — just faded out.
    const dividers = w.findAll('[data-testid="nav-group-divider"]')
    expect(dividers).toHaveLength(3)
    for (const divider of dividers)
      expect(divider.classes()).toContain('opacity-0')

    const captions = w.findAll('[data-testid="nav-group-slot"] span').filter(s => s.text() === 'Monitor')
    expect(captions).toHaveLength(1)
    expect(captions[0]!.classes()).toContain('opacity-100')
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

  it('lists the operator\'s own pages, not the Zentrale, and opens one', async () => {
    const { AppSidebar, useViewState } = await load()
    const w = mount(AppSidebar, { props })
    const pages = w.get('[data-testid="nav-pages"]')
    expect(pages.get('[data-testid="nav-page-p-a"]').text()).toContain('Morning')
    expect(pages.find('[data-testid="nav-page-zentrale"]').exists()).toBe(false)

    await w.get('[data-testid="nav-page-p-a"]').trigger('click')
    expect(useViewState().activeView.value).toBe('page:p-a')
    expect(w.get('[data-testid="nav-page-p-a"]').attributes('aria-current')).toBe('page')
  })

  // This component-only mount has no App.vue watcher to apply edit mode — asserts the declaration createPage hands it instead.
  it('creates a named page, opens it and declares edit mode for the navigation watcher', async () => {
    const { AppSidebar, useViewState } = await load()
    const w = mount(AppSidebar, { props, attachTo: document.body })
    await w.get('[data-testid="nav-new-page"]').trigger('click')
    await nextTick()
    const input = w.get('[data-testid="nav-new-page-input"]')
    expect(document.activeElement).toBe(input.element)
    await input.setValue('Evening')
    await input.trigger('keydown', { key: 'Enter' })
    await flushPromises()

    const saved = ws.save.mock.calls[0]![0]
    expect(saved.pages).toHaveLength(3)
    expect(saved.pages[2]).toMatchObject({ title: 'Evening', tiles: [] })
    expect(useViewState().activeView.value).toBe(`page:${saved.pages[2]!.id}`)
    expect(useViewState().editAfterNavigation.value).toBe(true)
    expect(w.find('[data-testid="nav-new-page-input"]').exists()).toBe(false)
    w.unmount()
  })

  // No attachTo — focus()/blur() are no-ops here, so this isolates the explicit close from the @blur handler.
  it('closes the input explicitly after a successful save, not merely via blur', async () => {
    const { AppSidebar } = await load()
    const w = mount(AppSidebar, { props })
    await w.get('[data-testid="nav-new-page"]').trigger('click')
    await w.get('[data-testid="nav-new-page-input"]').setValue('Evening')
    await w.get('[data-testid="nav-new-page-input"]').trigger('keydown', { key: 'Enter' })
    await flushPromises()
    expect(w.find('[data-testid="nav-new-page-input"]').exists()).toBe(false)
  })

  // The focus() call itself is App.vue's watcher's, absent here — this asserts the declaration createPage hands it.
  it('declares the new page\'s nav item as the focus target after creating it', async () => {
    const { AppSidebar, useViewState } = await load()
    const w = mount(AppSidebar, { props, attachTo: document.body })
    await w.get('[data-testid="nav-new-page"]').trigger('click')
    await w.get('[data-testid="nav-new-page-input"]').setValue('Evening')
    await w.get('[data-testid="nav-new-page-input"]').trigger('keydown', { key: 'Enter' })
    await flushPromises()
    const newId = ws.save.mock.calls[0]![0].pages[2]!.id
    expect(useViewState().focusAfterNavigation.value).toBe(`[data-testid="nav-page-${newId}"]`)
    w.unmount()
  })

  it('opens the new page input when a new page is requested from elsewhere', async () => {
    const { AppSidebar, useSidebar } = await load()
    const w = mount(AppSidebar, { props, attachTo: document.body })
    useSidebar().requestNewPage()
    await flushPromises()
    expect(document.activeElement).toBe(w.get('[data-testid="nav-new-page-input"]').element)
    w.unmount()
  })

  // The hub's launcher reaches startNewPage() through requestNewPage(), bypassing the
  // nav button's own disabled state — the guard has to live in startNewPage() itself.
  it('ignores a new page request from elsewhere while the button would be disabled', async () => {
    const { AppSidebar, useSidebar } = await load()
    ws.loaded.value = false
    const w = mount(AppSidebar, { props, attachTo: document.body })
    useSidebar().requestNewPage()
    await flushPromises()
    expect(w.find('[data-testid="nav-new-page-input"]').exists()).toBe(false)

    ws.loaded.value = true
    ws.locked.value = 'unreadable'
    useSidebar().requestNewPage()
    await flushPromises()
    expect(w.find('[data-testid="nav-new-page-input"]').exists()).toBe(false)
    w.unmount()
  })

  it('cancels a new page on Escape or an empty title', async () => {
    const { AppSidebar } = await load()
    const w = mount(AppSidebar, { props, attachTo: document.body })
    await w.get('[data-testid="nav-new-page"]').trigger('click')
    await w.get('[data-testid="nav-new-page-input"]').setValue('Evening')
    await w.get('[data-testid="nav-new-page-input"]').trigger('keydown', { key: 'Escape' })
    expect(w.find('[data-testid="nav-new-page-input"]').exists()).toBe(false)

    await w.get('[data-testid="nav-new-page"]').trigger('click')
    await w.get('[data-testid="nav-new-page-input"]').setValue('   ')
    await w.get('[data-testid="nav-new-page-input"]').trigger('keydown', { key: 'Enter' })
    expect(w.find('[data-testid="nav-new-page-input"]').exists()).toBe(false)
    expect(ws.save).not.toHaveBeenCalled()
    w.unmount()
  })

  it('neither opens nor edits a page the store refused to save', async () => {
    const { AppSidebar, useViewState } = await load()
    ws.save.mockResolvedValueOnce(false)
    const w = mount(AppSidebar, { props })
    await w.get('[data-testid="nav-new-page"]').trigger('click')
    await w.get('[data-testid="nav-new-page-input"]').setValue('Evening')
    await w.get('[data-testid="nav-new-page-input"]').trigger('keydown', { key: 'Enter' })
    await flushPromises()
    expect(ws.save).toHaveBeenCalledTimes(1)
    expect(useViewState().activeView.value).toBe('zentrale')
    expect(ws.editing.value).toBe(false)
    expect(w.find('[data-testid="nav-new-page-input"]').exists()).toBe(true)
  })

  // Refuse, never displace: the input stays open and says why.
  it('keeps the input open with the reason when a page cannot be added', async () => {
    const { AppSidebar } = await load()
    const w = mount(AppSidebar, { props })
    await w.get('[data-testid="nav-new-page"]').trigger('click')
    const input = w.get('[data-testid="nav-new-page-input"]')
    await input.setValue('x'.repeat(81))
    await input.trigger('keydown', { key: 'Enter' })
    expect(ws.save).not.toHaveBeenCalled()
    expect(w.find('[data-testid="nav-new-page-input"]').exists()).toBe(true)
    expect((input.element as HTMLInputElement).validationMessage).toMatch(/1 to 80 characters/)
  })

  // The input swaps in for the button inside the same box, so no row below moves.
  it('swaps the new-page button for its input inside one fixed box', async () => {
    const { AppSidebar } = await load()
    const w = mount(AppSidebar, { props })
    const box = () => w.get('[data-testid="nav-new-page-slot"]')
    expect(box().classes()).toContain('h-10')
    await w.get('[data-testid="nav-new-page"]').trigger('click')
    expect(box().find('[data-testid="nav-new-page-input"]').exists()).toBe(true)
    expect(box().classes()).toContain('h-10')
  })

  // Disabled, not hidden, while unloaded: hiding it would shift the Insights group, and enabling early would save over a layout not yet arrived.
  it('disables the new-page button until the layout has loaded, and while it is locked', async () => {
    const { AppSidebar } = await load()
    ws.loaded.value = false
    const w = mount(AppSidebar, { props })
    expect(w.get('[data-testid="nav-new-page"]').attributes('disabled')).toBeDefined()
    ws.loaded.value = true
    await nextTick()
    expect(w.get('[data-testid="nav-new-page"]').attributes('disabled')).toBeUndefined()
    ws.locked.value = 'unreadable'
    await nextTick()
    expect(w.get('[data-testid="nav-new-page"]').attributes('disabled')).toBeDefined()
  })

  it('renders the new-page slot from first paint, so the Insights group never shifts once loaded', async () => {
    const { AppSidebar } = await load()
    ws.loaded.value = false
    const w = mount(AppSidebar, { props })
    expect(w.find('[data-testid="nav-new-page-slot"]').exists()).toBe(true)
  })
})
