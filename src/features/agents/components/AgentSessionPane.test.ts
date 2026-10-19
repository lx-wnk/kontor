import { flushPromises, shallowMount } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import AgentSessionPane from './AgentSessionPane.vue'

beforeEach(() => {
  vi.stubGlobal('fetch', vi.fn().mockResolvedValue({ ok: true, json: async () => [], body: null }))
  vi.stubGlobal('localStorage', { getItem: () => null, setItem: vi.fn(), removeItem: vi.fn() })
  vi.stubGlobal('requestIdleCallback', () => 0)
})
afterEach(() => vi.unstubAllGlobals())

function makeAgent(over: Record<string, unknown> = {}) {
  return {
    pid: 7,
    sessionId: 's-7',
    projectName: 'P',
    projectPath: '/p',
    status: 'idle',
    model: 'opus',
    costEstimate: 0,
    uptime: 0,
    liveInjectable: true,
    tokenUsage: { inputTokens: 0, outputTokens: 0, cacheReadTokens: 0, cacheCreationTokens: 0 },
    tasks: [],
    subagents: [],
    lastTools: [],
    ...over,
  } as any
}

const stubs = {
  AppModal: { props: ['open'], template: '<div v-if="open" data-testid="stub-modal"><slot /></div>' },
  AgentTerminal: { props: ['pid'], template: '<div data-testid="stub-terminal" :data-pid="pid" />' },
}

describe('agentSessionPane terminal dialogs', () => {
  it('points at the terminal while a picker is open there, and opens it on request', async () => {
    const w = shallowMount(AgentSessionPane, { props: { agent: makeAgent() }, global: { stubs } })
    expect(w.find('[data-testid="terminal-dialog-notice"]').exists()).toBe(false)

    await w.setProps({ agent: makeAgent({ awaitingTerminalDialog: true }) })
    expect(w.find('[data-testid="stub-terminal"]').exists()).toBe(false)
    await w.get('[data-testid="terminal-dialog-open"]').trigger('click')
    expect(w.get('[data-testid="stub-terminal"]').attributes('data-pid')).toBe('7')
  })

  it('opens the terminal as soon as the prompt sends an interactive command to a live session', async () => {
    const w = shallowMount(AgentSessionPane, { props: { agent: makeAgent() }, global: { stubs } })
    w.findComponent({ name: 'PromptInput' }).vm.$emit('interactiveCommand')
    await flushPromises()
    expect(w.get('[data-testid="stub-terminal"]').attributes('data-pid')).toBe('7')
  })

  it('leaves the terminal closed for a session it cannot reach live', async () => {
    const w = shallowMount(AgentSessionPane, { props: { agent: makeAgent({ liveInjectable: false }) }, global: { stubs } })
    w.findComponent({ name: 'PromptInput' }).vm.$emit('interactiveCommand')
    await flushPromises()
    expect(w.find('[data-testid="stub-terminal"]').exists()).toBe(false)
  })
})
