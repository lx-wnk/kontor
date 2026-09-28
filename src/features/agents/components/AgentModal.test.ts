import type { Agent } from '@/types'
import { mount } from '@vue/test-utils'
import { describe, expect, it, vi } from 'vitest'

import AgentModal from './AgentModal.vue'

vi.mock('@/features/agents/composables/useAgentIdentity', () => ({
  useAgentIdentity: () => ({ getIdentity: () => ({ emoji: '🤖' }) }),
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
  lastTools: [{ name: 'Read', detail: 'src/main.ts' }],
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

const stubs = {
  AppModal: { template: '<div><slot /></div>' },
  AgentChatStream: true,
  CrossLinkBanner: true,
  MachineBadge: true,
  PromptInput: true,
  SubAgentList: true,
  TaskList: true,
  ToolTimeline: true,
  AppBadge: true,
  AgentTerminal: true,
}

function mountModal(agent: Agent = baseAgent) {
  return mount(AgentModal, { props: { agent }, global: { stubs } })
}

describe('agentModal subagent transcript', () => {
  const subagent = {
    id: '33333333-3333-3333-3333-333333333333',
    type: 'subagent',
    status: 'completed',
    currentAction: '',
    sessionFile: '/tmp/x.jsonl',
    tokensUsed: 0,
    durationSeconds: 0,
    latestOutput: '',
  }

  function mountWithSubagent() {
    return mount(AgentModal, {
      props: { agent: { ...baseAgent, subagents: [subagent] } as Agent },
      global: { stubs: { ...stubs, SubAgentList: false, PromptInput: { name: 'PromptInput', template: '<div />', methods: { focus() {} } } } },
    })
  }

  it('opens the subagent transcript in place of the session transcript', async () => {
    const w = mountWithSubagent()
    expect(w.find('[data-testid="subagent-transcript"]').exists()).toBe(false)

    await w.get('[data-testid="subagent-open"]').trigger('click')
    expect(w.get('[data-testid="subagent-transcript"]').attributes('sessionid')).toBe(subagent.id)
  })

  it('returns to the session transcript', async () => {
    const w = mountWithSubagent()
    await w.get('[data-testid="subagent-open"]').trigger('click')
    await w.get('[data-testid="subagent-back"]').trigger('click')
    expect(w.find('[data-testid="subagent-transcript"]').exists()).toBe(false)
  })

  // The input used to sit outside both branches: it stayed bound to the parent
  // while a subagent transcript was on screen, so typing there answered the
  // wrong session.
  it('takes the reply box away while a subagent transcript is shown', async () => {
    const w = mountWithSubagent()
    expect(w.findComponent({ name: 'PromptInput' }).exists()).toBe(true)

    await w.get('[data-testid="subagent-open"]').trigger('click')
    expect(w.findComponent({ name: 'PromptInput' }).exists()).toBe(false)
    expect(w.get('[data-testid="subagent-readonly"]').text()).toContain('no channel of its own')

    await w.get('[data-testid="subagent-back"]').trigger('click')
    expect(w.findComponent({ name: 'PromptInput' }).exists()).toBe(true)
    expect(w.find('[data-testid="subagent-readonly"]').exists()).toBe(false)
  })

  it('names the subagent kind and what it spent', async () => {
    const w = mount(AgentModal, {
      props: { agent: { ...baseAgent, subagents: [{ ...subagent, type: 'Explore', tokensUsed: 12500, durationSeconds: 3725 }] } as Agent },
      global: { stubs: { ...stubs, SubAgentList: false, PromptInput: { name: 'PromptInput', template: '<div />', methods: { focus() {} } } } },
    })
    await w.get('[data-testid="subagent-open"]').trigger('click')

    const metrics = w.get('[data-testid="subagent-metrics"]').text()
    expect(metrics).toContain('12.5k')
    expect(metrics).toContain('1h 2m')
  })

  // The modal is reused across agents; a stale subagent would open on the wrong session.
  it('drops the open subagent when the modal switches agents', async () => {
    const w = mountWithSubagent()
    await w.get('[data-testid="subagent-open"]').trigger('click')

    await w.setProps({ agent: { ...baseAgent, sessionId: 'other-session', subagents: [] } as Agent })
    expect(w.find('[data-testid="subagent-transcript"]').exists()).toBe(false)
  })
})

describe('agentModal session context', () => {
  const withContext = {
    ...baseAgent,
    tasks: [{ subject: 'Ship it', status: 'in_progress' }],
    subagents: [],
  } as unknown as Agent

  // The bottom drawer is gone: what you read while reading the transcript sits
  // beside it, and the modal keeps no tab bar at its foot.
  it('renders session context beside the transcript, not in a drawer', () => {
    const w = mount(AgentModal, { props: { agent: withContext }, global: { stubs: { ...stubs, TaskList: false } } })
    expect(w.find('[data-testid="agent-context"]').exists()).toBe(true)
    expect(w.find('details').exists()).toBe(false)
  })

  it('omits the context block when the agent has none', () => {
    const w = mountModal({ ...baseAgent, lastTools: [], tasks: [], subagents: [], recentHookEvents: [] } as unknown as Agent)
    expect(w.find('[data-testid="agent-context"]').exists()).toBe(false)
  })

  it('offers the token breakdown from the header instead of a token table', async () => {
    const w = mountModal()
    expect(w.findComponent({ name: 'MetricsPopover' }).exists()).toBe(false)
    await w.get('[data-testid="agent-modal-metrics"]').trigger('click')
    expect(w.findComponent({ name: 'MetricsPopover' }).exists()).toBe(true)
  })

  // The terminal moved to the card; mounting xterm from the modal would defeat that.
  it('mounts no terminal', () => {
    const w = mountModal({ ...baseAgent, liveInjectable: true })
    expect(w.html()).not.toContain('agent-terminal')
  })
})
