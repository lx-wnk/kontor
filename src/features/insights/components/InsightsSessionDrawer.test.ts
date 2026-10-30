import { flushPromises, mount } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { sessionRow } from './fixtures'
import InsightsSessionDrawer from './InsightsSessionDrawer.vue'

const stubs = { SessionDagChart: { template: '<div data-testid="dag" />' } }
function opts(row: ReturnType<typeof sessionRow> | null) {
  return { props: { row }, global: { stubs }, attachTo: document.body }
}

describe('insightsSessionDrawer', () => {
  const fetchMock = vi.fn()
  beforeEach(() => {
    fetchMock.mockReset()
    fetchMock.mockResolvedValue({ ok: true, json: async () => ({ nodes: [], edges: [] }) })
    vi.stubGlobal('fetch', fetchMock)
  })
  afterEach(() => {
    vi.unstubAllGlobals()
    document.body.innerHTML = ''
  })

  it('renders the title and token split', async () => {
    const w = mount(InsightsSessionDrawer, opts(sessionRow()))
    await flushPromises()
    const text = document.body.textContent ?? ''
    expect(text).toContain('Fix the login bug')
    expect(text).toContain('1.0k')
    expect(text).toContain('2.0k')
    expect(text).toContain('3.0k')
    expect(text).toContain('4.0k')
    w.unmount()
  })

  it('falls back to the session id when the title is empty', async () => {
    const w = mount(InsightsSessionDrawer, opts(sessionRow({ title: '' })))
    await flushPromises()
    expect(document.body.querySelector('h2')?.textContent).toContain('s-1')
    w.unmount()
  })

  it('shows per-model cost when present', async () => {
    const w = mount(InsightsSessionDrawer, opts(sessionRow({ modelCosts: { 'claude-opus-4': 1.2, 'claude-haiku-4': 0.3 } })))
    await flushPromises()
    const text = document.body.textContent ?? ''
    expect(text).toContain('claude-haiku-4')
    expect(text).toContain('$0.30')
    w.unmount()
  })

  it('fetches the dag for the session id', async () => {
    const w = mount(InsightsSessionDrawer, opts(sessionRow({ sessionId: 'abc 1' })))
    await flushPromises()
    expect(fetchMock.mock.calls[0][0]).toBe('/api/visualizations/dag?session=abc+1')
    w.unmount()
  })

  it('aborts the previous fetch when the row changes', async () => {
    const w = mount(InsightsSessionDrawer, opts(sessionRow()))
    const first = fetchMock.mock.calls[0][1].signal as AbortSignal
    await w.setProps({ row: sessionRow({ sessionId: 's-2' }) })
    expect(first.aborted).toBe(true)
    w.unmount()
  })

  it('does not fetch without a row', () => {
    const w = mount(InsightsSessionDrawer, opts(null))
    expect(fetchMock).not.toHaveBeenCalled()
    w.unmount()
  })

  it('emits navigate with the session id', async () => {
    const w = mount(InsightsSessionDrawer, opts(sessionRow()))
    await flushPromises()
    ;(document.body.querySelector('[data-testid="open-session"]') as HTMLElement).click()
    expect(w.emitted('navigate')?.[0]).toEqual(['s-1'])
    w.unmount()
  })

  it('marks estimated agent time', async () => {
    const w = mount(InsightsSessionDrawer, opts(sessionRow({ agentTimeSource: 'estimate' })))
    await flushPromises()
    expect(document.body.querySelector('[data-testid="drawer-agent"]')?.textContent).toContain('≈')
    w.unmount()
  })
})
