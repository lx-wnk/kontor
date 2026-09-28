import { flushPromises, mount } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { defineComponent, h } from 'vue'

vi.mock('../widgetRegistry', () => ({
  WIDGETS: {
    agents: { id: 'agents', title: 'Agents', component: defineComponent({ render: () => h('p', 'agents body') }) },
  },
  widgetIds: () => ['agents'],
}))

const { default: WorkspaceGrid } = await import('./WorkspaceGrid.vue')

const page = {
  id: 'zentrale',
  title: 'Zentrale',
  tiles: [
    { widget: 'obsidian__recent', col: 5, row: 2, colSpan: 4, rowSpan: 1 },
    { widget: 'agents', col: 1, row: 1, colSpan: 4, rowSpan: 2 },
  ],
}

describe('workspaceGrid', () => {
  it('anchors each tile where the layout says, in reading order', () => {
    const w = mount(WorkspaceGrid, { props: { page, editing: false } })
    const tiles = w.findAll('[data-testid^="workspace-tile-"]')
    expect(tiles.map(t => t.attributes('data-testid'))).toEqual(['workspace-tile-agents', 'workspace-tile-obsidian__recent'])
    const style = tiles[0].attributes('style')
    expect(style).toContain('--col: 1')
    expect(style).toContain('--col-span: 4')
    expect(style).toContain('--row: 1')
    expect(style).toContain('--row-span: 2')
    expect(w.get('[data-testid="workspace-grid"]').attributes('style')).toContain('--rows: 2')
    w.unmount()
  })

  // A module that is deactivated must not cost the operator their layout.
  it('renders an unknown widget as a placeholder naming it', () => {
    const w = mount(WorkspaceGrid, { props: { page, editing: false } })
    expect(w.get('[data-testid="workspace-unknown"]').text()).toContain('obsidian__recent')
    expect(w.text()).toContain('agents body')
    w.unmount()
  })
})

describe('workspaceGrid keyboard on the resize handle', () => {
  const editablePage = {
    id: 'zentrale',
    title: 'Zentrale',
    tiles: [{ widget: 'agents', col: 1, row: 1, colSpan: 4, rowSpan: 2 }],
  }

  it('resizes the tile on Shift+ArrowDown focused on the resize handle', async () => {
    const w = mount(WorkspaceGrid, { props: { page: editablePage, editing: true } })
    await w.get('[data-testid="workspace-resize-agents"]').trigger('keydown', { key: 'ArrowDown', shiftKey: true })
    const changed = w.emitted('change')
    expect(changed).toBeTruthy()
    expect((changed![0][0] as typeof editablePage).tiles[0]).toMatchObject({ rowSpan: 3 })
    w.unmount()
  })

  it('ignores Backspace focused on the resize handle', async () => {
    const w = mount(WorkspaceGrid, { props: { page: editablePage, editing: true } })
    await w.get('[data-testid="workspace-resize-agents"]').trigger('keydown', { key: 'Backspace' })
    expect(w.emitted('change')).toBeFalsy()
    w.unmount()
  })

  it('still ignores ArrowDown focused on the swap select', async () => {
    const w = mount(WorkspaceGrid, { props: { page: editablePage, editing: true } })
    await w.get('[data-testid="workspace-swap-agents"]').trigger('keydown', { key: 'ArrowDown' })
    expect(w.emitted('change')).toBeFalsy()
    w.unmount()
  })
})

describe('workspaceGrid drag geometry', () => {
  class MockResizeObserver {
    static instances: MockResizeObserver[] = []
    callback: ResizeObserverCallback
    observe = vi.fn()
    unobserve = vi.fn()
    disconnect = vi.fn()
    constructor(callback: ResizeObserverCallback) {
      this.callback = callback
      MockResizeObserver.instances.push(this)
    }
  }

  const draggablePage = {
    id: 'zentrale',
    title: 'Zentrale',
    tiles: [{ widget: 'agents', col: 1, row: 1, colSpan: 4, rowSpan: 2 }],
  }

  function rect(width: number): DOMRect {
    return { left: 0, top: 0, width, height: 240, right: width, bottom: 240, x: 0, y: 0, toJSON: () => ({}) } as DOMRect
  }

  function pointer(type: string, clientX: number) {
    return new PointerEvent(type, { bubbles: true, cancelable: true, pointerId: 1, button: 0, clientX, clientY: 50 })
  }

  beforeEach(() => {
    MockResizeObserver.instances = []
    vi.stubGlobal('ResizeObserver', MockResizeObserver)
  })

  afterEach(() => {
    vi.unstubAllGlobals()
  })

  // 12 columns, 12px gaps: a tile grabbed at x=50 and dropped at x=260 lands in column 3 against a 1200px grid and column 6 against a 600px one.
  it('drops against the geometry the grid has after a mid-drag resize', async () => {
    const w = mount(WorkspaceGrid, { props: { page: draggablePage, editing: true }, attachTo: document.body })
    await flushPromises()
    const grid = w.get('[data-testid="workspace-grid"]').element
    grid.getBoundingClientRect = () => rect(1200)
    const tile = w.get('[data-testid="workspace-tile-agents"]').element

    tile.dispatchEvent(pointer('pointerdown', 50))
    grid.getBoundingClientRect = () => rect(600)
    const observer = MockResizeObserver.instances.at(-1)!
    observer.callback([{ contentRect: { width: 600, height: 240 } } as ResizeObserverEntry], observer as unknown as ResizeObserver)
    tile.dispatchEvent(pointer('pointermove', 260))
    tile.dispatchEvent(pointer('pointerup', 260))
    await flushPromises()

    const changed = w.emitted('change')
    expect(changed, 'the drop emitted a change').toBeTruthy()
    expect((changed![0][0] as typeof draggablePage).tiles[0]).toMatchObject({ col: 6, row: 1 })
    w.unmount()
  })
})
