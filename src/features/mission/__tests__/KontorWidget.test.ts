import { mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { computed, nextTick, ref } from 'vue'

const session = {
  pid: ref<number | null>(42),
  status: ref<'idle' | 'starting' | 'running' | 'error'>('running'),
  openRequested: ref(false),
  ask(prefill = '') {
    void prefill
    session.openRequested.value = true
  },
}
const agents = ref([{ pid: 42, status: 'active', working: true, lastOutput: 'PR #467 has four red checks.' }])

vi.mock('../composables/useKontorSession', () => ({
  useKontorSession: () => session,
  useKontorAgent: () => computed(() => agents.value.find(a => a.pid === session.pid.value) ?? null),
}))
vi.mock('@/features/agents', () => ({ useAgents: () => ({ agents }) }))
vi.mock('../components/KontorTile.vue', () => ({ default: { template: '<div data-testid="stub-kontor-tile" />' } }))

const { default: KontorWidget } = await import('../components/KontorWidget.vue')

beforeEach(() => {
  session.pid.value = 42
  session.status.value = 'running'
  session.openRequested.value = false
  agents.value = [{ pid: 42, status: 'active', working: true, lastOutput: 'PR #467 has four red checks.' }]
})

describe('kontorWidget', () => {
  it('shows state and the last output collapsed', () => {
    const w = mount(KontorWidget, { attachTo: document.body })
    expect(w.get('[data-testid="kontor-collapsed-state"]').text()).toContain('working')
    expect(w.get('[data-testid="kontor-collapsed-last"]').text()).toBe('PR #467 has four red checks.')
    expect(document.querySelector('[data-testid="kontor-expanded"]')).toBeNull()
    w.unmount()
  })

  it('grows on click and on "/", and collapses on Escape', async () => {
    const w = mount(KontorWidget, { attachTo: document.body })
    await w.get('[data-testid="kontor-collapsed"]').trigger('click')
    expect(document.querySelector('[data-testid="kontor-expanded"] [data-testid="stub-kontor-tile"]')).not.toBeNull()
    window.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape' }))
    await nextTick()
    expect(document.querySelector('[data-testid="kontor-expanded"]')).toBeNull()
    window.dispatchEvent(new KeyboardEvent('keydown', { key: '/' }))
    await nextTick()
    expect(document.querySelector('[data-testid="kontor-expanded"]')).not.toBeNull()
    w.unmount()
  })

  // "/" typed into a field is text, not a shortcut.
  it('ignores "/" typed into an input', async () => {
    const w = mount(KontorWidget, { attachTo: document.body })
    const input = document.createElement('input')
    document.body.appendChild(input)
    input.dispatchEvent(new KeyboardEvent('keydown', { key: '/', bubbles: true }))
    await nextTick()
    expect(document.querySelector('[data-testid="kontor-expanded"]')).toBeNull()
    input.remove()
    w.unmount()
  })

  it('leaves a second "/" alone while already open', async () => {
    const w = mount(KontorWidget, { attachTo: document.body })
    await w.get('[data-testid="kontor-collapsed"]').trigger('click')
    const event = new KeyboardEvent('keydown', { key: '/', cancelable: true })
    window.dispatchEvent(event)
    await nextTick()
    expect(event.defaultPrevented).toBe(false)
    w.unmount()
  })

  it('opens at the collapsed cell size, then grows the height on the next frame', async () => {
    const w = mount(KontorWidget, { attachTo: document.body })
    const cellEl = w.get('[data-testid="kontor-collapsed"]').element as HTMLElement
    const rect = { top: 300, bottom: 340, left: 10, right: 390, width: 380, height: 40, x: 10, y: 300, toJSON: () => {} } as DOMRect
    vi.spyOn(cellEl, 'getBoundingClientRect').mockReturnValue(rect)
    const innerHeightSpy = vi.spyOn(window, 'innerHeight', 'get').mockReturnValue(800)
    const raf: { cb: FrameRequestCallback | null } = { cb: null }
    const rafSpy = vi.spyOn(window, 'requestAnimationFrame').mockImplementation((cb) => {
      raf.cb = cb
      return 0
    })

    await w.get('[data-testid="kontor-collapsed"]').trigger('click')
    const overlay = document.querySelector('[data-testid="kontor-expanded"]') as HTMLElement
    expect(overlay.style.height).toBe('40px')

    raf.cb?.(0)
    await nextTick()
    expect(overlay.style.height).toBe('484px')

    rafSpy.mockRestore()
    innerHeightSpy.mockRestore()
    w.unmount()
  })

  it('re-places on window resize while open, capped to 64% of the new height', async () => {
    const w = mount(KontorWidget, { attachTo: document.body })
    const cellEl = w.get('[data-testid="kontor-collapsed"]').element as HTMLElement
    const rect = { top: 300, bottom: 340, left: 10, right: 390, width: 380, height: 40, x: 10, y: 300, toJSON: () => {} } as DOMRect
    vi.spyOn(cellEl, 'getBoundingClientRect').mockReturnValue(rect)
    const innerHeightSpy = vi.spyOn(window, 'innerHeight', 'get').mockReturnValue(800)
    const raf: { cb: FrameRequestCallback | null } = { cb: null }
    const rafSpy = vi.spyOn(window, 'requestAnimationFrame').mockImplementation((cb) => {
      raf.cb = cb
      return 0
    })

    await w.get('[data-testid="kontor-collapsed"]').trigger('click')
    raf.cb?.(0)
    await nextTick()
    const overlay = document.querySelector('[data-testid="kontor-expanded"]') as HTMLElement
    expect(overlay.style.height).toBe('484px')

    innerHeightSpy.mockReturnValue(400)
    window.dispatchEvent(new Event('resize'))
    await nextTick()
    expect(overlay.style.height).toBe('256px')

    rafSpy.mockRestore()
    innerHeightSpy.mockRestore()
    w.unmount()
  })

  it('does nothing on window resize once collapsed', async () => {
    const w = mount(KontorWidget, { attachTo: document.body })
    const cellEl = w.get('[data-testid="kontor-collapsed"]').element as HTMLElement
    const rect = { top: 300, bottom: 340, left: 10, right: 390, width: 380, height: 40, x: 10, y: 300, toJSON: () => {} } as DOMRect
    const getRectSpy = vi.spyOn(cellEl, 'getBoundingClientRect').mockReturnValue(rect)
    const innerHeightSpy = vi.spyOn(window, 'innerHeight', 'get').mockReturnValue(800)

    await w.get('[data-testid="kontor-collapsed"]').trigger('click')
    window.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape' }))
    await nextTick()
    getRectSpy.mockClear()

    window.dispatchEvent(new Event('resize'))
    await nextTick()
    expect(getRectSpy).not.toHaveBeenCalled()

    innerHeightSpy.mockRestore()
    w.unmount()
  })

  it('does not re-place on a scroll inside the overlay, but does on a scroll elsewhere', async () => {
    const w = mount(KontorWidget, { attachTo: document.body })
    const cellEl = w.get('[data-testid="kontor-collapsed"]').element as HTMLElement
    const getRectSpy = vi.spyOn(cellEl, 'getBoundingClientRect')
    await w.get('[data-testid="kontor-collapsed"]').trigger('click')
    getRectSpy.mockClear()

    const inner = document.querySelector('[data-testid="stub-kontor-tile"]') as HTMLElement
    inner.dispatchEvent(new Event('scroll'))
    await nextTick()
    expect(getRectSpy).not.toHaveBeenCalled()

    document.dispatchEvent(new Event('scroll'))
    await nextTick()
    expect(getRectSpy).toHaveBeenCalled()

    w.unmount()
  })

  it('opens when ask() requests it, and resets the request flag', async () => {
    const w = mount(KontorWidget, { attachTo: document.body })
    expect(document.querySelector('[data-testid="kontor-expanded"]')).toBeNull()
    session.ask('[[notes/a]] ')
    await nextTick()
    expect(document.querySelector('[data-testid="kontor-expanded"]')).not.toBeNull()
    expect(session.openRequested.value).toBe(false)
    w.unmount()
  })

  // ask() may run before this widget mounts, e.g. across a page navigation.
  it('opens already-expanded when mounted after ask() was called', async () => {
    session.ask('[[notes/a]] ')
    const w = mount(KontorWidget, { attachTo: document.body })
    await nextTick()
    expect(document.querySelector('[data-testid="kontor-expanded"]')).not.toBeNull()
    w.unmount()
  })
})
