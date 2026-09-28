import { flushPromises, mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { computed, ref } from 'vue'

const session = {
  pid: ref<number | null>(null),
  status: ref<'idle' | 'starting' | 'running' | 'error'>('idle'),
  error: ref(''),
  pendingPrompt: ref<string | null>(null),
  refresh: vi.fn(),
  send: vi.fn(),
  end: vi.fn(),
  renew: vi.fn(),
  takePendingPrompt: vi.fn(() => {
    const t = session.pendingPrompt.value
    session.pendingPrompt.value = null
    return t
  }),
}
const activeView = ref('zentrale')
const agents = ref<Array<{ pid: number }>>([])
const paneprefillMock = vi.fn()

vi.mock('../composables/useKontorSession', () => ({
  useKontorSession: () => session,
  useKontorAgent: () => computed(() => agents.value.find(a => a.pid === session.pid.value) ?? null),
}))
vi.mock('@/features/agents', () => ({
  useAgents: () => ({ agents }),
  AgentSessionPane: {
    props: ['agent', 'title'],
    template: '<div data-testid="stub-pane" :data-pid="agent.pid" :data-title="title"><slot name="actions" /></div>',
    setup(_props: unknown, { expose }: { expose: (exposed: Record<string, unknown>) => void }) {
      expose({ prefill: paneprefillMock })
    },
  },
}))
vi.mock('@/composables/useViewState', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/composables/useViewState')>()
  return { ...actual, useViewState: () => ({ activeView }) }
})

const { default: KontorTile } = await import('../components/KontorTile.vue')

async function typeInto(wrapper: ReturnType<typeof mount>, text: string) {
  await wrapper.get('[data-testid="kontor-input"]').setValue(text)
  await flushPromises()
}
function inputValue(wrapper: ReturnType<typeof mount>) {
  return (wrapper.get('[data-testid="kontor-input"]').element as HTMLInputElement).value
}

beforeEach(() => {
  session.pid.value = null
  session.status.value = 'idle'
  session.error.value = ''
  session.pendingPrompt.value = null
  for (const fn of [session.refresh, session.send, session.end, session.renew])
    fn.mockReset()
  session.takePendingPrompt.mockClear()
  session.send.mockResolvedValue(true)
  session.renew.mockResolvedValue(true)
  activeView.value = 'zentrale'
  agents.value = []
  paneprefillMock.mockClear()
})

describe('kontorTile', () => {
  it('reattaches on mount and shows the empty state when no session runs', () => {
    const w = mount(KontorTile)
    expect(session.refresh).toHaveBeenCalledOnce()
    expect(w.get('[data-testid="kontor-state"]').text()).toBe('No session')
    expect(w.find('[data-testid="kontor-empty"]').exists()).toBe(true)
    expect(w.find('[data-testid="stub-pane"]').exists()).toBe(false)
    expect(w.get('[data-testid="kontor-end"]').attributes('disabled')).toBeDefined()
    w.unmount()
  })

  it('starts a session with free text and clears the input once delivered', async () => {
    const w = mount(KontorTile)
    await typeInto(w, 'plan phase 4')
    expect(w.get('[data-testid="kontor-reading-label"]').text()).toBe('START KONTOR')
    // Badge and sentence must stay apart in the text layer, not only on screen.
    expect(w.get('[data-testid="kontor-reading"]').text()).toMatch(/START KONTOR:\s\S/)
    await w.get('[data-testid="kontor-input-submit"]').trigger('click')
    await flushPromises()
    expect(session.send).toHaveBeenCalledWith('plan phase 4')
    expect(inputValue(w)).toBe('')
    w.unmount()
  })

  it('shows the starting state and blocks a second submit', async () => {
    session.status.value = 'starting'
    const w = mount(KontorTile)
    expect(w.get('[data-testid="kontor-state"]').text()).toBe('Starting…')
    await typeInto(w, 'again')
    expect(w.get('[data-testid="kontor-input-submit"]').attributes('disabled')).toBeDefined()
    w.unmount()
  })

  it('keeps the tile input for a running session the scanner has not listed yet', async () => {
    session.pid.value = 1234
    session.status.value = 'running'
    agents.value = [{ pid: 99 }]
    const w = mount(KontorTile)
    expect(w.get('[data-testid="kontor-state"]').text()).toBe('Running')
    expect(w.find('[data-testid="stub-pane"]').exists()).toBe(false)
    expect(w.get('[data-testid="kontor-empty"]').text()).toBe('Starting a Kontor session…')
    await typeInto(w, '/compact')
    expect(w.get('[data-testid="kontor-reading-label"]').text()).toBe('SEND TO KONTOR')
    await w.get('[data-testid="kontor-input-submit"]').trigger('click')
    await flushPromises()
    expect(session.send).toHaveBeenCalledWith('/compact')
    w.unmount()
  })

  it('keeps the text and shows the error when the session could not take it', async () => {
    session.send.mockImplementation(async () => {
      session.status.value = 'error'
      session.error.value = 'Could not mint a session key'
      return false
    })
    const w = mount(KontorTile)
    await typeInto(w, 'plan phase 4')
    await w.get('[data-testid="kontor-input-submit"]').trigger('click')
    await flushPromises()
    expect(w.get('[data-testid="kontor-state"]').text()).toBe('Failed')
    expect(w.get('[data-testid="kontor-input-problem"]').text()).toContain('Could not mint a session key')
    expect(inputValue(w)).toBe('plan phase 4')
    w.unmount()
  })

  it('navigates on a view name without touching the session', async () => {
    const w = mount(KontorTile)
    await typeInto(w, 'go to pipeline')
    await w.get('[data-testid="kontor-input-submit"]').trigger('click')
    await flushPromises()
    expect(activeView.value).toBe('pipeline')
    expect(session.send).not.toHaveBeenCalled()
    w.unmount()
  })

  it('refuses a slash command when no session runs', async () => {
    const w = mount(KontorTile)
    await typeInto(w, '/grant Bash')
    await w.get('[data-testid="kontor-input-submit"]').trigger('click')
    await flushPromises()
    expect(session.send).not.toHaveBeenCalled()
    expect(w.get('[data-testid="kontor-input-problem"]').text()).toContain('running Kontor session')
    w.unmount()
  })

  it('ends the session, and renews it with or without a first prompt', async () => {
    session.pid.value = 1234
    session.status.value = 'running'
    const w = mount(KontorTile)
    await w.get('[data-testid="kontor-end"]').trigger('click')
    expect(session.end).toHaveBeenCalledOnce()
    await w.get('[data-testid="kontor-new"]').trigger('click')
    await flushPromises()
    expect(session.renew).toHaveBeenCalledWith('')
    await typeInto(w, 'fresh start')
    await w.get('[data-testid="kontor-new"]').trigger('click')
    await flushPromises()
    expect(session.renew).toHaveBeenLastCalledWith('fresh start')
    expect(inputValue(w)).toBe('')
    w.unmount()
  })

  it('shows a listed session as its chat pane, with its own prompt instead of the tile input', async () => {
    session.pid.value = 1234
    session.status.value = 'running'
    agents.value = [{ pid: 99 }, { pid: 1234 }]
    const w = mount(KontorTile)
    const pane = w.get('[data-testid="stub-pane"]')
    expect(pane.attributes('data-pid')).toBe('1234')
    expect(pane.attributes('data-title')).toBe('Kontor')
    expect(w.find('[data-testid="kontor-input"]').exists()).toBe(false)
    await pane.get('[data-testid="kontor-new"]').trigger('click')
    await flushPromises()
    expect(session.renew).toHaveBeenCalledWith('')
    await pane.get('[data-testid="kontor-end"]').trigger('click')
    expect(session.end).toHaveBeenCalledOnce()
    // A renew in flight keeps the old pid listed; a second New must not start another.
    session.status.value = 'starting'
    await flushPromises()
    expect(pane.get('[data-testid="kontor-new"]').attributes('disabled')).toBeDefined()
    expect(pane.get('[data-testid="kontor-end"]').attributes('disabled')).toBeDefined()
    w.unmount()
  })

  it('prefills its own input with a pending prompt when no agent runs yet', async () => {
    const w = mount(KontorTile)
    session.pendingPrompt.value = '[[notes/a]] '
    await flushPromises()
    expect(inputValue(w)).toBe('[[notes/a]] ')
    expect(session.takePendingPrompt).toHaveBeenCalledOnce()
    expect(session.pendingPrompt.value).toBeNull()
    w.unmount()
  })

  it('forwards a pending prompt to the running session\'s own prompt input', async () => {
    session.pid.value = 1234
    session.status.value = 'running'
    agents.value = [{ pid: 1234 }]
    const w = mount(KontorTile)
    session.pendingPrompt.value = '[[notes/a]] '
    await flushPromises()
    expect(paneprefillMock).toHaveBeenCalledWith('[[notes/a]] ')
    expect(session.pendingPrompt.value).toBeNull()
    w.unmount()
  })

  it('forwards a prompt already pending when it mounts with a listed session', async () => {
    session.pid.value = 1234
    session.status.value = 'running'
    agents.value = [{ pid: 1234 }]
    session.pendingPrompt.value = '[[notes/a]] '
    const w = mount(KontorTile)
    await flushPromises()
    expect(paneprefillMock).toHaveBeenCalledWith('[[notes/a]] ')
    expect(session.pendingPrompt.value).toBeNull()
    w.unmount()
  })

  it('hands unsent text from its own input to the pane once the agent appears, and clears the own input', async () => {
    const w = mount(KontorTile)
    session.pendingPrompt.value = '[[notes/a]] '
    await flushPromises()
    expect(inputValue(w)).toBe('[[notes/a]] ')

    session.pid.value = 1234
    session.status.value = 'running'
    agents.value = [{ pid: 1234 }]
    await flushPromises()

    expect(paneprefillMock).toHaveBeenCalledWith('[[notes/a]] ')
    expect(w.find('[data-testid="kontor-input"]').exists()).toBe(false)
    w.unmount()
  })
})
