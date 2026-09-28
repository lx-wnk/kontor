import { flushPromises, mount } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { ref } from 'vue'
import { DEFAULT_LAYOUT, useWorkspace } from '@/features/workspace'
import SpotlightSearch from './SpotlightSearch.vue'

const activeView = ref('dashboard')
let searchBody: unknown = { tasks: [], agents: [] }

const send = vi.fn()
const refresh = vi.fn(async () => {})
const kontorError = ref('')
const kontorPid = ref<number | null>(null)
vi.mock('@/features/mission/composables/useKontorSession', () => ({
  useKontorSession: () => ({ send: (...a: unknown[]) => send(...a), refresh: () => refresh(), error: kontorError, pid: kontorPid }),
}))
vi.mock('@/composables/useViewState', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/composables/useViewState')>()
  return { ...actual, useViewState: () => ({ activeView }) }
})

const mockFetch = vi.fn(async () => ({
  ok: true,
  json: async () => searchBody,
}))

// The query watcher debounces by 200 ms before it searches, so a test that
// types has to let that elapse or it asserts against the pre-search state.
const DEBOUNCE_MS = 200

async function openSpotlight(text: string) {
  const wrapper = mount(SpotlightSearch, { attachTo: document.body })
  window.dispatchEvent(new KeyboardEvent('keydown', { key: 'k', metaKey: true }))
  await flushPromises()
  const input = document.querySelector<HTMLInputElement>('input[placeholder]')!
  input.value = text
  input.dispatchEvent(new Event('input'))
  await new Promise(resolve => setTimeout(resolve, DEBOUNCE_MS + 50))
  await flushPromises()
  return wrapper
}

beforeEach(() => {
  vi.stubGlobal('fetch', mockFetch)
  searchBody = { tasks: [], agents: [] }
  activeView.value = 'dashboard'
  send.mockReset().mockResolvedValue(true)
  refresh.mockReset().mockImplementation(async () => {})
  kontorError.value = ''
  kontorPid.value = null
})

afterEach(() => {
  vi.unstubAllGlobals()
  document.body.innerHTML = ''
  useWorkspace().layout.value = DEFAULT_LAYOUT
})

describe('spotlightSearch', () => {
  it('is hidden by default', () => {
    mount(SpotlightSearch)
    expect(document.querySelector('input[placeholder]')).toBeNull()
  })

  it('opens on Cmd+K', async () => {
    const wrapper = mount(SpotlightSearch, { attachTo: document.body })
    window.dispatchEvent(new KeyboardEvent('keydown', { key: 'k', metaKey: true }))
    await wrapper.vm.$nextTick()
    expect(document.querySelector('input[placeholder]')).not.toBeNull()
    wrapper.unmount()
  })

  it('closes on Escape', async () => {
    const wrapper = mount(SpotlightSearch, { attachTo: document.body })
    window.dispatchEvent(new KeyboardEvent('keydown', { key: 'k', metaKey: true }))
    await wrapper.vm.$nextTick()
    expect(document.querySelector('input[placeholder]')).not.toBeNull()
    window.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape' }))
    await wrapper.vm.$nextTick()
    expect(document.querySelector('input[placeholder]')).toBeNull()
    wrapper.unmount()
  })

  it('emits navigateTask on Enter when task selected', async () => {
    mockFetch.mockResolvedValueOnce({
      ok: true,
      json: async () => ({
        tasks: [{ id: 't1', title: 'Test Task', currentStage: 'implementation', slug: 'test-task', description: null, cwd: '/', worktreePath: null, sourceBranch: null, targetBranch: null, parentTaskId: null, maxIterations: 3, tokenBudget: null, costBudgetCents: null, stageTimeoutSeconds: 1800, createdAt: '', updatedAt: '', metadata: null, silverBullet: false, priority: 'medium', userId: null }],
        agents: [],
      }),
    })
    const wrapper = mount(SpotlightSearch, { attachTo: document.body })
    window.dispatchEvent(new KeyboardEvent('keydown', { key: 'k', metaKey: true }))
    await wrapper.vm.$nextTick()
    // The input lives inside a Teleport; set the reactive query directly on the vm
    const vm = wrapper.vm as unknown as { query: string }
    vm.query = 'test'
    await new Promise(resolve => setTimeout(resolve, 300))
    window.dispatchEvent(new KeyboardEvent('keydown', { key: 'Enter' }))
    await wrapper.vm.$nextTick()
    expect(wrapper.emitted('navigateTask')).toBeTruthy()
    wrapper.unmount()
  })
})

describe('spotlightSearch commands and hand-off', () => {
  it('offers a navigation command for a matching view and runs it', async () => {
    const wrapper = await openSpotlight('pipeline')
    const option = document.querySelector('[data-testid="spotlight-command-view:pipeline"]')
    expect(option).not.toBeNull()
    ;(option as HTMLElement).click()
    await flushPromises()
    expect(activeView.value).toBe('pipeline')
    wrapper.unmount()
  })

  it('offers a navigation command for a page of the operator\'s own, but none for the Zentrale page', async () => {
    const { layout } = useWorkspace()
    layout.value = { version: 1, pages: [...DEFAULT_LAYOUT.pages, { id: 'p-a', title: 'Morning', tiles: [] }] }
    const wrapper = await openSpotlight('go to')
    expect(document.querySelector('[data-testid="spotlight-command-view:page:zentrale"]')).toBeNull()
    const option = document.querySelector('[data-testid="spotlight-command-view:page:p-a"]')
    expect(option?.textContent).toContain('Go to Morning')
    ;(option as HTMLElement).click()
    await flushPromises()
    expect(activeView.value).toBe('page:p-a')
    wrapper.unmount()
  })

  // Text that matched nothing goes to the Kontor session, never to the backlog.
  it('hands free text that matched nothing to Kontor and switches to the Zentrale', async () => {
    const wrapper = await openSpotlight('plan phase 4 of the dashboard')
    expect(document.querySelector('[data-testid="spotlight-kontor"]')?.textContent).toContain('Kontor')
    window.dispatchEvent(new KeyboardEvent('keydown', { key: 'Enter' }))
    await flushPromises()
    expect(send).toHaveBeenCalledWith('plan phase 4 of the dashboard')
    expect(activeView.value).toBe('zentrale')
    expect(document.querySelector('input[placeholder]')).toBeNull()
    wrapper.unmount()
  })

  it('keeps the dialog open with the reason when Kontor cannot take the text', async () => {
    send.mockImplementation(async () => {
      kontorError.value = 'Could not start a Kontor session.'
      return false
    })
    const wrapper = await openSpotlight('something')
    window.dispatchEvent(new KeyboardEvent('keydown', { key: 'Enter' }))
    await flushPromises()
    expect(document.querySelector('[data-testid="spotlight-problem"]')?.textContent).toContain('Could not start a Kontor session.')
    expect(document.querySelector('input[placeholder]')).not.toBeNull()
    wrapper.unmount()
  })

  it('refuses a slash command when no session runs, and does not send it', async () => {
    const wrapper = await openSpotlight('/grant Bash')
    window.dispatchEvent(new KeyboardEvent('keydown', { key: 'Enter' }))
    await flushPromises()
    expect(send).not.toHaveBeenCalled()
    expect(document.querySelector('[data-testid="spotlight-problem"]')?.textContent).toContain('running Kontor session')
    expect(document.querySelector('input[placeholder]')).not.toBeNull()
    wrapper.unmount()
  })

  it('finds a session that started before this page load and sends the slash command', async () => {
    refresh.mockImplementation(async () => {
      kontorPid.value = 4321
    })
    const wrapper = await openSpotlight('/compact')
    window.dispatchEvent(new KeyboardEvent('keydown', { key: 'Enter' }))
    await flushPromises()
    expect(refresh).toHaveBeenCalled()
    expect(send).toHaveBeenCalledWith('/compact')
    expect(document.querySelector('[data-testid="spotlight-problem"]')).toBeNull()
    wrapper.unmount()
  })

  it('sends a slash command to the running session', async () => {
    kontorPid.value = 1234
    const wrapper = await openSpotlight('/compact')
    window.dispatchEvent(new KeyboardEvent('keydown', { key: 'Enter' }))
    await flushPromises()
    expect(send).toHaveBeenCalledWith('/compact')
    wrapper.unmount()
  })

  // A search hit and a hand-off are mutually exclusive.
  it('does not hand off when the search found a result', async () => {
    searchBody = { tasks: [{ id: 'x1', title: 'existing', currentStage: 'ready' }], agents: [] }
    const wrapper = await openSpotlight('existing')
    expect(document.querySelector('[data-testid="spotlight-kontor"]')).toBeNull()
    window.dispatchEvent(new KeyboardEvent('keydown', { key: 'Enter' }))
    await flushPromises()
    expect(send).not.toHaveBeenCalled()
    wrapper.unmount()
  })

  // The reply has to land somewhere visible, so hand-off finds the Kontor tile
  // before it navigates rather than always jumping to the Zentrale.
  it('navigates to an own page when the Zentrale has no Kontor tile but an own page does', async () => {
    const { layout } = useWorkspace()
    layout.value = {
      version: 1,
      pages: [
        { id: 'zentrale', title: 'Zentrale', tiles: [] },
        { id: 'p-a', title: 'Morning', tiles: [{ widget: 'kontor', col: 1, row: 1, colSpan: 3, rowSpan: 1 }] },
      ],
    }
    const wrapper = await openSpotlight('plan phase 4')
    window.dispatchEvent(new KeyboardEvent('keydown', { key: 'Enter' }))
    await flushPromises()
    expect(send).toHaveBeenCalledWith('plan phase 4')
    expect(activeView.value).toBe('page:p-a')
    wrapper.unmount()
  })

  it('stays put and reports the missing tile when no page has a Kontor tile', async () => {
    const { layout } = useWorkspace()
    layout.value = { version: 1, pages: [{ id: 'zentrale', title: 'Zentrale', tiles: [] }] }
    activeView.value = 'pipeline'
    const wrapper = await openSpotlight('plan phase 4')
    window.dispatchEvent(new KeyboardEvent('keydown', { key: 'Enter' }))
    await flushPromises()
    expect(send).not.toHaveBeenCalled()
    expect(activeView.value).toBe('pipeline')
    expect(document.querySelector('[data-testid="spotlight-problem"]')?.textContent).toContain('Kontor has no tile')
    wrapper.unmount()
  })
})
