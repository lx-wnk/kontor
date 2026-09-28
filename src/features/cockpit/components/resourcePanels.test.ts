import { flushPromises, mount } from '@vue/test-utils'
import { afterEach, describe, expect, it, vi } from 'vitest'
import MemoryPanel from './MemoryPanel.vue'
import RoutinesPanel from './RoutinesPanel.vue'

function recordFetch(): string[] {
  const calls: string[] = []
  vi.stubGlobal('fetch', vi.fn(async (url: string) => {
    calls.push(String(url))
    return new Response('[]', { status: 200, headers: { 'Content-Type': 'application/json' } })
  }))
  return calls
}

afterEach(() => vi.unstubAllGlobals())

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
})
