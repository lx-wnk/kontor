import type { WorkspaceLayout } from '../layout'
import type { WorkspaceLock } from '../useWorkspace'
import { mount } from '@vue/test-utils'
import { describe, expect, it, vi } from 'vitest'
import { defineComponent, h, ref } from 'vue'
import { DEFAULT_LAYOUT } from '../layout'
import { WIDGET_SPECS } from '../widgetSpecs'

vi.mock('../widgetRegistry', () => {
  const ids = Object.keys(WIDGET_SPECS)
  const stub = (id: string) => defineComponent({ name: id, render: () => h('p', `${id} body`) })
  return {
    WIDGETS: Object.fromEntries(ids.map(id => [id, { ...WIDGET_SPECS[id as keyof typeof WIDGET_SPECS], component: stub(id) }])),
    widgetIds: () => ids,
  }
})

const ws = {
  layout: ref<WorkspaceLayout>(DEFAULT_LAYOUT),
  loaded: ref(true),
  locked: ref<WorkspaceLock | null>(null),
  saveError: ref<string | null>(null),
  editing: ref(false),
  wide: ref<string | null>(null),
  load: vi.fn(async () => {}),
  reset: vi.fn(async () => {}),
  retry: vi.fn(async () => {}),
  save: vi.fn(async () => true),
  page: (id: string) => ws.layout.value.pages.find(p => p.id === id),
}
vi.mock('../useWorkspace', () => ({ useWorkspace: () => ws }))

const { default: WorkspacePage } = await import('./WorkspacePage.vue')

describe('workspacePage wide mode', () => {
  it('renders only the wide tile and its column band, both spanning all 12 columns', () => {
    ws.wide.value = 'hub'
    const w = mount(WorkspacePage, { props: { pageId: 'zentrale' } })
    const tiles = w.findAll('[data-testid^="workspace-tile-"]')
    expect(tiles.map(t => t.attributes('data-testid')).sort()).toEqual(['workspace-tile-hub', 'workspace-tile-kontor'])
    for (const t of tiles) {
      expect(t.attributes('style')).toContain('--col: 1')
      expect(t.attributes('style')).toContain('--col-span: 12')
    }
    w.unmount()
  })

  it('renders all nine tiles when nothing is wide', () => {
    ws.wide.value = null
    const w = mount(WorkspacePage, { props: { pageId: 'zentrale' } })
    expect(w.findAll('[data-testid^="workspace-tile-"]')).toHaveLength(9)
    w.unmount()
  })

  it('renders the stored tiles while editing even if a wide tile was left set', () => {
    ws.wide.value = 'hub'
    ws.editing.value = true
    const w = mount(WorkspacePage, { props: { pageId: 'zentrale' } })
    expect(w.findAll('[data-testid^="workspace-tile-"]')).toHaveLength(9)
    w.unmount()
    ws.editing.value = false
  })
})
