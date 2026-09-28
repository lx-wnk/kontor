import type { GraphStatus, HubNote } from '@/features/hub/composables/useObsidianGraph'
import { flushPromises, mount } from '@vue/test-utils'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { ref, shallowRef } from 'vue'

const graph = {
  status: ref<GraphStatus>('idle'),
  notes: shallowRef<HubNote[]>([]),
  recentNotes: (count: number) => [...graph.notes.value].sort((a, b) => b.mtimeMs - a.mtimeMs).slice(0, count),
  refresh: vi.fn(async () => {}),
}
const focusInHub = vi.fn(() => true)
vi.mock('@/features/hub', () => ({
  useObsidianGraph: () => graph,
  focusInHub,
  NO_HUB_PAGE_MESSAGE: 'No page shows the Zentrale hub; add the hub tile to a page.',
}))

const { default: MemoryPanel } = await import('./MemoryPanel.vue')
const { default: RoutinesPanel } = await import('./RoutinesPanel.vue')

function recordFetch(): string[] {
  const calls: string[] = []
  vi.stubGlobal('fetch', vi.fn(async (url: string) => {
    calls.push(String(url))
    return new Response('[]', { status: 200, headers: { 'Content-Type': 'application/json' } })
  }))
  return calls
}

function stubResource() {
  vi.stubGlobal('fetch', vi.fn(async () => new Response(JSON.stringify([
    { id: 'm1', kind: 'memory_space', slug: 'm1', name: 'Space One', scopeKind: 'global', scopeRef: '', nodeId: '', state: 'active', version: '1', origin: '', originRef: '', createdAt: '', updatedAt: '' },
  ]), { status: 200, headers: { 'Content-Type': 'application/json' } })))
}

afterEach(() => {
  vi.unstubAllGlobals()
  graph.status.value = 'idle'
  graph.notes.value = []
  graph.refresh.mockClear()
  focusInHub.mockClear()
})

/**
 * Both panels read the registry through useResources, which fetches on mount
 * with its own default kind. A panel that then asks for its kind issues a
 * second request, and whichever response lands last wins — so the card renders
 * by network timing rather than by what it asked for. One request per panel is
 * the assertion that pins that down.
 */
describe('cockpit registry panels', () => {
  it('the memory panel asks once, for memory spaces', async () => {
    const calls = recordFetch()
    const wrapper = mount(MemoryPanel)
    await flushPromises()

    expect(calls, calls.join('\n')).toHaveLength(1)
    expect(calls[0]).toContain('kind=memory_space')
    wrapper.unmount()
  })

  it('the routines panel asks once, for routines', async () => {
    const calls = recordFetch()
    const wrapper = mount(RoutinesPanel)
    await flushPromises()

    expect(calls, calls.join('\n')).toHaveLength(1)
    expect(calls[0]).toContain('kind=routine')
    wrapper.unmount()
  })

  it('the memory panel renders its icon', async () => {
    recordFetch()
    const wrapper = mount(MemoryPanel)
    await flushPromises()
    expect(wrapper.get('header').text()).toContain('✎')
    wrapper.unmount()
  })

  it('the routines panel renders its icon', async () => {
    recordFetch()
    const wrapper = mount(RoutinesPanel)
    await flushPromises()
    expect(wrapper.get('header').text()).toContain('⟳')
    wrapper.unmount()
  })

  it('the memory panel refreshes the vault graph on mount', async () => {
    recordFetch()
    const wrapper = mount(MemoryPanel)
    await flushPromises()
    expect(graph.refresh).toHaveBeenCalledOnce()
    wrapper.unmount()
  })

  it('the memory panel lists recently touched notes once the vault is ready, and focuses the hub on click', async () => {
    stubResource()
    graph.status.value = 'ready'
    graph.notes.value = [{ index: 0, path: 'a/One.md', title: 'One', mtimeMs: Date.now() - 3600_000, links: [], backlinks: [] }]
    const wrapper = mount(MemoryPanel)
    await flushPromises()
    const row = wrapper.get('[data-testid="cockpit-memory-recent-note"]')
    expect(row.text()).toContain('One')
    await row.trigger('click')
    expect(focusInHub).toHaveBeenCalledWith({ kind: 'note', path: 'a/One.md' })
    wrapper.unmount()
  })

  it('the memory panel shows no recently touched notes while the vault is not ready, even with resources present', async () => {
    stubResource()
    graph.status.value = 'unconfigured'
    const wrapper = mount(MemoryPanel)
    await flushPromises()
    expect(wrapper.find('[data-testid="cockpit-memory-recent-note"]').exists()).toBe(false)
    wrapper.unmount()
  })
})
