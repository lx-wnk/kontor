import type { Agent, SubAgent } from '@/types'
import { mount } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import AgentCard from './AgentCard.vue'

vi.mock('@/features/agents/composables/useAgentIdentity', () => ({
  useAgentIdentity: () => ({
    getIdentity: () => ({ emoji: '🤖' }),
  }),
}))

const baseAgent: Agent = {
  pid: 1234,
  sessionId: 'sess-1',
  provider: 'claude',
  projectPath: '/home/user/my-project',
  projectName: 'my-project',
  cwd: '/home/user/my-project',
  entrypoint: 'cli',
  status: 'active',
  uptime: 60,
  lastActivity: '2026-01-01T00:00:00Z',
  lastTools: [],
  tasks: [],
  subagents: [],
  tokenUsage: { inputTokens: 0, outputTokens: 0, cacheCreationTokens: 0, cacheReadTokens: 0 },
  costEstimate: 0,
  cacheCreationCostEstimate: 0,
  cacheReadCostEstimate: 0,
  healthScore: 80,
  conversationTurns: 0,
  toolCounts: {},
  channelAvailable: false,
  working: false,
  permissionsBypassed: false,
  convergenceAlert: false,
  meta: null,
}

const activeSubagent: SubAgent = {
  id: 'sa-1',
  type: 'researcher',
  status: 'active',
  currentAction: 'Read',
  sessionFile: '/tmp/sa-1.jsonl',
  tokensUsed: 5000,
  durationSeconds: 90,
  latestOutput: 'Analyzing the codebase for relevant patterns',
}

const stubs = {
  MachineBadge: true,
  ProviderBadge: true,
  AppBadge: true,
  PromptInput: true,
}

describe('agentCard', () => {
  it('renders a real button with data-testid="agent-card-open"', () => {
    const wrapper = mount(AgentCard, {
      props: { agent: baseAgent },
      global: { stubs },
    })
    expect(wrapper.find('button[data-testid="agent-card-open"]').exists()).toBe(true)
  })

  it('aria-label on the open button contains the friendly project name', () => {
    const wrapper = mount(AgentCard, {
      props: { agent: baseAgent },
      global: { stubs },
    })
    const btn = wrapper.find('button[data-testid="agent-card-open"]')
    expect(btn.attributes('aria-label')).toContain('My Project')
  })

  it('clicking the open button emits select with the agent', async () => {
    const wrapper = mount(AgentCard, {
      props: { agent: baseAgent },
      global: { stubs },
    })
    await wrapper.find('button[data-testid="agent-card-open"]').trigger('click')
    expect(wrapper.emitted('select')).toBeTruthy()
    expect(wrapper.emitted('select')![0]).toEqual([baseAgent])
  })
})

describe('agentCard — active subagents block', () => {
  it('hides the block when there are no active subagents', () => {
    const agent = { ...baseAgent, subagents: [{ ...activeSubagent, status: 'completed' as const }] }
    const wrapper = mount(AgentCard, {
      props: { agent },
      global: { stubs },
    })
    expect(wrapper.find('[data-testid="active-subagents-block"]').exists()).toBe(false)
  })

  it('hides the block when subagents is empty', () => {
    const wrapper = mount(AgentCard, {
      props: { agent: baseAgent },
      global: { stubs },
    })
    expect(wrapper.find('[data-testid="active-subagents-block"]').exists()).toBe(false)
  })

  it('shows the block with type, token count, and output snippet for active subagents', () => {
    const agent = { ...baseAgent, subagents: [activeSubagent] }
    const wrapper = mount(AgentCard, {
      props: { agent },
      global: { stubs },
    })
    const block = wrapper.find('[data-testid="active-subagents-block"]')
    expect(block.exists()).toBe(true)
    expect(block.text()).toContain('researcher')
    expect(block.text()).toContain('5k tok')
    expect(wrapper.find('[data-testid="subagent-latest-output"]').text()).toContain('Analyzing the codebase')
  })

  it('shows the expand toggle when latestOutput is non-empty', () => {
    const agent = { ...baseAgent, subagents: [activeSubagent] }
    const wrapper = mount(AgentCard, {
      props: { agent },
      global: { stubs },
    })
    expect(wrapper.find('[data-testid="subagent-expand-toggle"]').exists()).toBe(true)
  })

  it('does not show expand toggle when latestOutput is empty', () => {
    const agent = { ...baseAgent, subagents: [{ ...activeSubagent, latestOutput: '' }] }
    const wrapper = mount(AgentCard, {
      props: { agent },
      global: { stubs },
    })
    expect(wrapper.find('[data-testid="subagent-expand-toggle"]').exists()).toBe(false)
  })

  it('expand toggle reveals full latestOutput on click', async () => {
    const agent = { ...baseAgent, subagents: [activeSubagent] }
    const wrapper = mount(AgentCard, {
      props: { agent },
      global: { stubs },
    })
    const output = wrapper.find('[data-testid="subagent-latest-output"]')
    expect(output.classes()).toContain('truncate')

    await wrapper.find('[data-testid="subagent-expand-toggle"]').trigger('click')
    expect(output.classes()).not.toContain('truncate')
    expect(output.classes()).toContain('whitespace-pre-wrap')
  })
})

describe('agentCard working badge', () => {
  const badgeStubs = { MachineBadge: true, ProviderBadge: true, PromptInput: true }

  it('shows Working badge when agent.working, overriding status', () => {
    const w = mount(AgentCard, { props: { agent: { ...baseAgent, status: 'waiting', working: true } }, global: { stubs: badgeStubs } })
    expect(w.text()).toContain('Working')
    expect(w.text()).not.toContain('Waiting')
  })

  it('shows the "Quiet" label (not the ambiguous "Waiting") when not working', () => {
    const w = mount(AgentCard, { props: { agent: { ...baseAgent, status: 'waiting', working: false } }, global: { stubs: badgeStubs } })
    expect(w.text()).toContain('Quiet')
    expect(w.text()).not.toContain('Waiting')
  })
})

describe('agentCard finished state', () => {
  beforeEach(() => {
    vi.stubGlobal('fetch', vi.fn(() => Promise.resolve({ ok: true, status: 204 })))
  })

  afterEach(() => {
    vi.unstubAllGlobals()
  })

  it('shows no dismiss button for a live agent', () => {
    const wrapper = mount(AgentCard, {
      props: { agent: { ...baseAgent, status: 'active' } },
      global: { stubs },
    })
    expect(wrapper.find('[data-testid="agent-card-dismiss"]').exists()).toBe(false)
  })

  it('shows a dismiss button for a finished agent', () => {
    const wrapper = mount(AgentCard, {
      props: { agent: { ...baseAgent, status: 'finished' } },
      global: { stubs },
    })
    expect(wrapper.find('[data-testid="agent-card-dismiss"]').exists()).toBe(true)
  })

  it('calls the DELETE endpoint and emits dismiss on click', async () => {
    const wrapper = mount(AgentCard, {
      props: { agent: { ...baseAgent, pid: 4242, status: 'finished' } },
      global: { stubs },
    })
    await wrapper.find('[data-testid="agent-card-dismiss"]').trigger('click')
    expect(fetch).toHaveBeenCalledWith('/api/agents/4242/channel', expect.objectContaining({ method: 'DELETE' }))
    expect(wrapper.emitted('dismiss')?.[0]).toEqual([4242])
  })
})

describe('agentCard your-turn marker', () => {
  const badgeStubs = { MachineBadge: true, ProviderBadge: true, PromptInput: true }

  it('marks a live agent that stopped on its own', () => {
    const w = mount(AgentCard, { props: { agent: { ...baseAgent, status: 'idle', working: false } }, global: { stubs: badgeStubs } })
    expect(w.get('[data-testid="agent-awaiting-input"]').text()).toBe('your turn')
  })

  it('shows nothing while the agent is working', () => {
    const w = mount(AgentCard, { props: { agent: { ...baseAgent, status: 'active', working: true } }, global: { stubs: badgeStubs } })
    expect(w.find('[data-testid="agent-awaiting-input"]').exists()).toBe(false)
  })

  it('shows nothing for a finished agent — there is nothing to continue', () => {
    const w = mount(AgentCard, { props: { agent: { ...baseAgent, status: 'finished', working: false } }, global: { stubs: badgeStubs } })
    expect(w.find('[data-testid="agent-awaiting-input"]').exists()).toBe(false)
  })
})

describe('agentCard internal process badge', () => {
  const badgeStubs = { MachineBadge: true, ProviderBadge: true, PromptInput: true }

  it('shows the internal-process badge when agent.internalProcess is true', () => {
    const w = mount(AgentCard, { props: { agent: { ...baseAgent, internalProcess: true } }, global: { stubs: badgeStubs } })
    expect(w.find('[data-testid="agent-card-internal-badge"]').exists()).toBe(true)
  })

  it('hides the internal-process badge for a normal session', () => {
    const w = mount(AgentCard, { props: { agent: { ...baseAgent, internalProcess: false } }, global: { stubs: badgeStubs } })
    expect(w.find('[data-testid="agent-card-internal-badge"]').exists()).toBe(false)
  })
})

describe('agentCard terminal access', () => {
  const badgeStubs = { MachineBadge: true, ProviderBadge: true, PromptInput: true }

  // The terminal used to live in the agent modal's bottom drawer. Removing that
  // drawer must not remove the only way to reach a live session's terminal.
  // AppModal teleports to <body>, so the overlay is asserted through the document.
  it('offers a terminal for a session with an attachable pty terminal', async () => {
    const w = mount(AgentCard, {
      props: { agent: { ...baseAgent, liveInjectable: true, terminalAttachable: true } },
      global: { stubs: badgeStubs },
      attachTo: document.body,
    })
    expect(document.querySelector('[data-testid="agent-terminal-modal"]')).toBeNull()

    await w.get('[data-testid="agent-card-terminal"]').trigger('click')
    expect(document.querySelector('[data-testid="agent-terminal-modal"]')).not.toBeNull()

    w.unmount()
    document.body.innerHTML = ''
  })

  it('offers none when the session cannot be driven', () => {
    const w = mount(AgentCard, {
      props: { agent: { ...baseAgent, liveInjectable: false } },
      global: { stubs: badgeStubs },
    })
    expect(w.find('[data-testid="agent-card-terminal"]').exists()).toBe(false)
  })

  // A tmux session is injectable but has no pty broker; its socket would only ever answer 409.
  it('offers none for a tmux session', () => {
    const w = mount(AgentCard, {
      props: { agent: { ...baseAgent, liveInjectable: true, terminalAttachable: false } },
      global: { stubs: badgeStubs },
    })
    expect(w.find('[data-testid="agent-card-terminal"]').exists()).toBe(false)
  })
})
