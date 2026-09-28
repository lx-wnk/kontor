import type { WorkspaceLayout } from '../layout'
import type { WorkspaceLock } from '../useWorkspace'
import { flushPromises, mount } from '@vue/test-utils'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { defineComponent, h, ref } from 'vue'
import { useViewState } from '@/composables/useViewState'
import { WIDGET_SPECS } from '../widgetSpecs'

// Real panels would fetch from jsdom; the swap/add candidates are the four
// stub widgets below so the assertions on which options are offered hold.
vi.mock('../widgetRegistry', () => {
  const ids = ['agents', 'github', 'pipeline', 'live-work'] as const
  const stub = (id: string) => defineComponent({ name: id, render: () => h('p', `${id} body`) })
  return {
    WIDGETS: Object.fromEntries(ids.map(id => [id, { ...WIDGET_SPECS[id], component: stub(id) }])),
    widgetIds: () => ids,
  }
})

const ws = {
  layout: ref<WorkspaceLayout>({ version: 1, pages: [] }),
  loaded: ref(true),
  locked: ref<WorkspaceLock | null>(null),
  saveError: ref<string | null>(null),
  editing: ref(true),
  wide: ref<string | null>(null),
  load: vi.fn(async () => {}),
  reset: vi.fn(async () => {}),
  retry: vi.fn(async () => {}),
  save: vi.fn(async (next: WorkspaceLayout) => {
    ws.layout.value = next
    return true
  }),
  page: (id: string) => ws.layout.value.pages.find(p => p.id === id),
}
vi.mock('../useWorkspace', () => ({ useWorkspace: () => ws }))

const { default: WorkspaceGrid } = await import('./WorkspaceGrid.vue')
const { default: WorkspaceEditBar } = await import('./WorkspaceEditBar.vue')
const { default: WorkspacePage } = await import('./WorkspacePage.vue')

const page = {
  id: 'zentrale',
  title: 'Zentrale',
  tiles: [
    { widget: 'agents', col: 1, row: 1, colSpan: 3, rowSpan: 3 },
    { widget: 'github', col: 4, row: 1, colSpan: 3, rowSpan: 3 },
  ],
}

describe('edit mode', () => {
  it('moves a focused tile with the arrow keys', async () => {
    const w = mount(WorkspaceGrid, { props: { page, editing: true } })
    await w.get('[data-testid="workspace-tile-agents"]').trigger('keydown', { key: 'ArrowDown' })
    expect(w.emitted('change')?.[0]?.[0]).toMatchObject({ tiles: [{ widget: 'agents', row: 2 }, { widget: 'github' }] })
    w.unmount()
  })

  // Refuse, never displace: the model is unchanged and the reason is said.
  it('refuses a move onto another tile and reports why', async () => {
    const w = mount(WorkspaceGrid, { props: { page, editing: true } })
    for (const _ of [1, 2, 3])
      await w.get('[data-testid="workspace-tile-agents"]').trigger('keydown', { key: 'ArrowRight' })
    expect(w.emitted('change')?.length ?? 0).toBeLessThan(3)
    expect(w.emitted('refuse')?.[0]?.[0]).toMatch(/overlap/i)
    w.unmount()
  })

  it('swaps a tile in place from its chrome, keeping anchor and span', async () => {
    const w = mount(WorkspaceGrid, { props: { page, editing: true } })
    await w.get('[data-testid="workspace-swap-agents"]').setValue('pipeline')
    expect(w.emitted('change')?.[0]?.[0]).toMatchObject({ tiles: [{ widget: 'pipeline', col: 1, row: 1, colSpan: 3, rowSpan: 3 }, { widget: 'github' }] })
    w.unmount()
  })

  it('removes a tile from its chrome', async () => {
    const w = mount(WorkspaceGrid, { props: { page, editing: true } })
    await w.get('[data-testid="workspace-remove-agents"]').trigger('click')
    expect(w.emitted('change')?.[0]?.[0]).toMatchObject({ tiles: [{ widget: 'github' }] })
    w.unmount()
  })

  it('leaves keys pressed on a tile\'s own controls to those controls', async () => {
    const w = mount(WorkspaceGrid, { props: { page, editing: true } })
    for (const key of ['ArrowDown', 'Backspace']) {
      await w.get('[data-testid="workspace-swap-agents"]').trigger('keydown', { key })
      await w.get('[data-testid="workspace-resize-agents"]').trigger('keydown', { key })
    }
    expect(w.emitted('change')).toBeUndefined()
    w.unmount()
  })

  it('shows no chrome and ignores keys outside edit mode', async () => {
    const w = mount(WorkspaceGrid, { props: { page, editing: false } })
    expect(w.find('[data-testid="workspace-remove-agents"]').exists()).toBe(false)
    await w.get('[data-testid="workspace-tile-agents"]').trigger('keydown', { key: 'ArrowDown' })
    expect(w.emitted('change')).toBeUndefined()
    w.unmount()
  })
})

describe('drag and resize', () => {
  // jsdom's PointerEvent constructor does not carry clientX/clientY/pointerId
  // through Vue Test Utils' trigger() init dict, so pointer events used to
  // determine a drop cell are constructed and dispatched directly.
  function pointer(type: string, init: { clientX: number, clientY: number, pointerId: number, button?: number }) {
    return new PointerEvent(type, { bubbles: true, cancelable: true, ...init })
  }

  it('drops a dragged tile on the cell under the pointer', async () => {
    const w = mount(WorkspaceGrid, { props: { page, editing: true }, attachTo: document.body })
    const grid = w.get('[data-testid="workspace-grid"]').element as HTMLElement
    // 12 columns, 12px gaps (matches .workspace-grid); 3 rows over a 320px grid.
    const rect = vi.fn(() => ({ left: 0, top: 0, width: 1190, height: 320, right: 1190, bottom: 320, x: 0, y: 0, toJSON: () => ({}) }))
    grid.getBoundingClientRect = rect
    const tile = w.get('[data-testid="workspace-tile-agents"]').element
    tile.dispatchEvent(pointer('pointerdown', { clientX: 5, clientY: 5, pointerId: 1, button: 0 }))
    tile.dispatchEvent(pointer('pointermove', { clientX: 605, clientY: 5, pointerId: 1 }))
    tile.dispatchEvent(pointer('pointermove', { clientX: 705, clientY: 5, pointerId: 1 }))
    await w.vm.$nextTick()
    expect(w.find('[data-testid="workspace-ghost"]').exists()).toBe(true)
    tile.dispatchEvent(pointer('pointerup', { clientX: 705, clientY: 5, pointerId: 1 }))
    await w.vm.$nextTick()
    expect(w.emitted('change')?.at(-1)?.[0]).toMatchObject({ tiles: [{ widget: 'agents', col: 8, row: 1 }, { widget: 'github' }] })
    expect(rect).toHaveBeenCalledTimes(1)
    w.unmount()
  })

  // Refuse, never displace: the model is unchanged and the reason is said.
  it('snaps back and reports when the drop would overlap', async () => {
    const w = mount(WorkspaceGrid, { props: { page, editing: true }, attachTo: document.body })
    const grid = w.get('[data-testid="workspace-grid"]').element as HTMLElement
    grid.getBoundingClientRect = () => ({ left: 0, top: 0, width: 1190, height: 320, right: 1190, bottom: 320, x: 0, y: 0, toJSON: () => ({}) })
    const tile = w.get('[data-testid="workspace-tile-agents"]').element
    tile.dispatchEvent(pointer('pointerdown', { clientX: 5, clientY: 5, pointerId: 1, button: 0 }))
    tile.dispatchEvent(pointer('pointermove', { clientX: 405, clientY: 5, pointerId: 1 }))
    await w.vm.$nextTick()
    expect(w.get('[data-testid="workspace-ghost"]').classes()).toContain('workspace-ghost--invalid')
    tile.dispatchEvent(pointer('pointerup', { clientX: 405, clientY: 5, pointerId: 1 }))
    await w.vm.$nextTick()
    expect(w.emitted('change')).toBeUndefined()
    expect(w.emitted('refuse')?.[0]?.[0]).toMatch(/overlap/i)
    w.unmount()
  })

  it('grows a tile from its resize handle, keeping the anchor', async () => {
    const w = mount(WorkspaceGrid, { props: { page, editing: true }, attachTo: document.body })
    const grid = w.get('[data-testid="workspace-grid"]').element as HTMLElement
    grid.getBoundingClientRect = () => ({ left: 0, top: 0, width: 1190, height: 320, right: 1190, bottom: 320, x: 0, y: 0, toJSON: () => ({}) })
    const handle = w.get('[data-testid="workspace-resize-agents"]').element
    // (250, 250) sits in agents' bottom-right cell (col 3, row 3); (250, 340)
    // is one row further down (col 3, row 4) — same columns, one row taller.
    handle.dispatchEvent(pointer('pointerdown', { clientX: 250, clientY: 250, pointerId: 1, button: 0 }))
    handle.dispatchEvent(pointer('pointermove', { clientX: 250, clientY: 340, pointerId: 1 }))
    await w.vm.$nextTick()
    handle.dispatchEvent(pointer('pointerup', { clientX: 250, clientY: 340, pointerId: 1 }))
    await w.vm.$nextTick()
    expect(w.emitted('change')?.at(-1)?.[0]).toMatchObject({ tiles: [{ widget: 'agents', col: 1, row: 1, colSpan: 3, rowSpan: 4 }, { widget: 'github' }] })
    w.unmount()
  })

  it('refuses a resize below the widget minimum and reports why', async () => {
    const w = mount(WorkspaceGrid, { props: { page, editing: true }, attachTo: document.body })
    const grid = w.get('[data-testid="workspace-grid"]').element as HTMLElement
    grid.getBoundingClientRect = () => ({ left: 0, top: 0, width: 1190, height: 320, right: 1190, bottom: 320, x: 0, y: 0, toJSON: () => ({}) })
    const handle = w.get('[data-testid="workspace-resize-agents"]').element
    handle.dispatchEvent(pointer('pointerdown', { clientX: 250, clientY: 250, pointerId: 1, button: 0 }))
    // (150, 250) is col 2, row 3 — a 2-column-wide target, below agents' minimum of 3.
    handle.dispatchEvent(pointer('pointermove', { clientX: 150, clientY: 250, pointerId: 1 }))
    await w.vm.$nextTick()
    expect(w.get('[data-testid="workspace-ghost"]').classes()).toContain('workspace-ghost--invalid')
    handle.dispatchEvent(pointer('pointerup', { clientX: 150, clientY: 250, pointerId: 1 }))
    await w.vm.$nextTick()
    expect(w.emitted('change')).toBeUndefined()
    expect(w.emitted('refuse')?.[0]?.[0]).toMatch(/needs at least 3 × 2/)
    w.unmount()
  })

  it('emits nothing when a resize ends where it started', async () => {
    const w = mount(WorkspaceGrid, { props: { page, editing: true }, attachTo: document.body })
    const grid = w.get('[data-testid="workspace-grid"]').element as HTMLElement
    grid.getBoundingClientRect = () => ({ left: 0, top: 0, width: 1190, height: 320, right: 1190, bottom: 320, x: 0, y: 0, toJSON: () => ({}) })
    const handle = w.get('[data-testid="workspace-resize-agents"]').element
    handle.dispatchEvent(pointer('pointerdown', { clientX: 250, clientY: 250, pointerId: 1, button: 0 }))
    handle.dispatchEvent(pointer('pointerup', { clientX: 250, clientY: 250, pointerId: 1 }))
    await w.vm.$nextTick()
    expect(w.emitted('change')).toBeUndefined()
    expect(w.emitted('refuse')).toBeUndefined()
    w.unmount()
  })
})

describe('workspace edit bar', () => {
  it('adds a tile, excluding widgets already on the page', async () => {
    const w = mount(WorkspaceEditBar, { props: { page, refusal: null } })
    const options = w.findAll('option').map(o => o.attributes('value')).filter(v => v)
    expect(options).not.toContain('agents')
    expect(options).not.toContain('github')
    await w.get('[data-testid="workspace-add"]').setValue('pipeline')
    await w.get('[data-testid="workspace-add-submit"]').trigger('click')
    expect(w.emitted('change')?.[0]?.[0]).toMatchObject({ tiles: [{ widget: 'agents' }, { widget: 'github' }, { widget: 'pipeline' }] })
    w.unmount()
  })
})

describe('page rename and delete', () => {
  const morning = { id: 'p-morning', title: 'Morning', tiles: [] }

  it('offers neither rename nor delete for the Zentrale', () => {
    const w = mount(WorkspaceEditBar, { props: { page, refusal: null } })
    expect(w.find('[data-testid="workspace-rename"]').exists()).toBe(false)
    expect(w.find('[data-testid="workspace-delete-page"]').exists()).toBe(false)
    w.unmount()
  })

  // setValue fires `change` as well, which is the browser's commit on blur or Enter.
  it('emits the new title once the rename is committed', async () => {
    const w = mount(WorkspaceEditBar, { props: { page: morning, refusal: null } })
    const input = w.get('[data-testid="workspace-rename"]')
    expect((input.element as HTMLInputElement).value).toBe('Morning')
    await input.setValue('Dawn')
    expect(w.emitted('rename')?.[0]).toEqual(['Dawn'])
    w.unmount()
  })

  it('deletes only after a second, named confirmation', async () => {
    const w = mount(WorkspaceEditBar, { props: { page: morning, refusal: null } })
    await w.get('[data-testid="workspace-delete-page"]').trigger('click')
    expect(w.emitted('remove')).toBeUndefined()
    expect(w.get('[data-testid="workspace-delete-confirm"]').text()).toBe('Delete Morning and its tiles?')
    await w.get('[data-testid="workspace-delete-cancel"]').trigger('click')
    expect(w.find('[data-testid="workspace-delete-confirm"]').exists()).toBe(false)

    await w.get('[data-testid="workspace-delete-page"]').trigger('click')
    await w.get('[data-testid="workspace-delete-confirm"]').trigger('click')
    expect(w.emitted('remove')).toHaveLength(1)
    w.unmount()
  })

  function seed() {
    ws.layout.value = { version: 1, pages: [{ id: 'zentrale', title: 'Zentrale', tiles: [] }, morning] }
    ws.editing.value = true
    ws.save.mockClear()
    useViewState().activeView.value = 'page:p-morning'
  }

  it('saves a renamed page, and keeps the stored title when the name is refused', async () => {
    seed()
    const w = mount(WorkspacePage, { props: { pageId: 'p-morning' } })
    const input = w.get('[data-testid="workspace-rename"]')
    await input.setValue('Dawn')
    expect(ws.save.mock.calls[0]![0].pages[1]).toMatchObject({ id: 'p-morning', title: 'Dawn' })
    expect((w.get('[data-testid="workspace-rename"]').element as HTMLInputElement).value).toBe('Dawn')

    await input.setValue('   ')
    expect(ws.save).toHaveBeenCalledTimes(1)
    expect(w.get('[data-testid="workspace-refusal"]').text()).toMatch(/1 to 80 characters/)
    expect((w.get('[data-testid="workspace-rename"]').element as HTMLInputElement).value).toBe('Dawn')
    w.unmount()
  })

  it('removes a deleted page and goes back to the Zentrale', async () => {
    seed()
    const w = mount(WorkspacePage, { props: { pageId: 'p-morning' } })
    await w.get('[data-testid="workspace-delete-page"]').trigger('click')
    await w.get('[data-testid="workspace-delete-confirm"]').trigger('click')
    await flushPromises()
    expect(ws.save.mock.calls[0]![0].pages.map((p: { id: string }) => p.id)).toEqual(['zentrale'])
    expect(useViewState().activeView.value).toBe('zentrale')
    expect(ws.editing.value).toBe(false)
    w.unmount()
  })

  it('stays on the page when the store refuses the delete', async () => {
    seed()
    ws.save.mockResolvedValueOnce(false)
    const w = mount(WorkspacePage, { props: { pageId: 'p-morning' } })
    await w.get('[data-testid="workspace-delete-page"]').trigger('click')
    await w.get('[data-testid="workspace-delete-confirm"]').trigger('click')
    await flushPromises()
    expect(ws.save).toHaveBeenCalledTimes(1)
    expect(useViewState().activeView.value).toBe('page:p-morning')
    expect(ws.editing.value).toBe(true)
    w.unmount()
  })
})

describe('a locked or loading layout', () => {
  const zentrale = { version: 1 as const, pages: [page] }

  afterEach(() => {
    ws.locked.value = null
    ws.loaded.value = true
  })

  // Only an unreadable value may be replaced wholesale; a failed request says nothing about what is stored.
  it('offers Retry, not Reset, when the layout could not be loaded', async () => {
    ws.layout.value = zentrale
    ws.locked.value = { kind: 'unloaded', message: 'The saved layout could not be loaded, so editing is locked until it loads.' }
    const w = mount(WorkspacePage, { props: { pageId: 'zentrale' } })
    expect(w.find('[data-testid="workspace-reset"]').exists()).toBe(false)
    await w.get('[data-testid="workspace-retry"]').trigger('click')
    expect(ws.retry).toHaveBeenCalledTimes(1)
    w.unmount()
  })

  it('offers Reset, not Retry, when the stored layout is unreadable', () => {
    ws.layout.value = zentrale
    ws.locked.value = { kind: 'unreadable', message: 'The saved layout could not be read.' }
    const w = mount(WorkspacePage, { props: { pageId: 'zentrale' } })
    expect(w.find('[data-testid="workspace-retry"]').exists()).toBe(false)
    expect(w.get('[data-testid="workspace-locked"]').text()).toContain('could not be read')
    expect(w.find('[data-testid="workspace-reset"]').exists()).toBe(true)
    w.unmount()
  })

  it('leaves edit mode when the layout locks', async () => {
    ws.layout.value = zentrale
    ws.editing.value = true
    const w = mount(WorkspacePage, { props: { pageId: 'zentrale' } })
    ws.locked.value = { kind: 'unloaded', message: 'locked' }
    await w.vm.$nextTick()
    expect(ws.editing.value).toBe(false)
    w.unmount()
  })

  // Tiles mounted on the built-in layout would fetch, then remount under the stored one.
  it('shows no tiles before the stored layout has loaded', () => {
    ws.layout.value = zentrale
    ws.loaded.value = false
    const w = mount(WorkspacePage, { props: { pageId: 'zentrale' } })
    expect(w.find('[data-testid="workspace-grid"]').exists()).toBe(false)
    w.unmount()
  })
})

describe('a refused save', () => {
  const zentrale = { version: 1 as const, pages: [page] }

  afterEach(() => {
    ws.locked.value = null
  })

  it('shows a reloading reason when the store refuses an edit', async () => {
    ws.layout.value = zentrale
    ws.editing.value = true
    ws.loaded.value = true
    ws.save.mockResolvedValueOnce(false)
    const w = mount(WorkspacePage, { props: { pageId: 'zentrale' } })
    await w.get('[data-testid="workspace-tile-agents"]').trigger('keydown', { key: 'ArrowDown' })
    await flushPromises()
    expect(w.get('[data-testid="workspace-refusal"]').text()).toMatch(/reloading/i)
    w.unmount()
  })

  it('shows the lock message instead when the layout is locked', async () => {
    ws.layout.value = zentrale
    ws.editing.value = true
    ws.loaded.value = true
    ws.locked.value = { kind: 'unloaded', message: 'The saved layout could not be loaded, so editing is locked until it loads.' }
    ws.save.mockResolvedValueOnce(false)
    const w = mount(WorkspacePage, { props: { pageId: 'zentrale' } })
    await w.get('[data-testid="workspace-tile-agents"]').trigger('keydown', { key: 'ArrowDown' })
    await flushPromises()
    expect(w.get('[data-testid="workspace-refusal"]').text()).toMatch(/could not be loaded/i)
    w.unmount()
  })
})
