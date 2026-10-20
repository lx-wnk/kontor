import type { Agent } from '@/types'
import { mount } from '@vue/test-utils'
import { describe, expect, it, vi } from 'vitest'

import AgentRow from './AgentRow.vue'

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

async function mountExpanded(agent: Agent) {
  const wrapper = mount(AgentRow, { props: { agent } })
  await wrapper.find('button[aria-expanded]').trigger('click')
  return wrapper
}

describe('agentRow internal process badge', () => {
  it('shows the internal-process badge when agent.internalProcess is true', async () => {
    const wrapper = await mountExpanded({ ...baseAgent, internalProcess: true })
    expect(wrapper.find('[data-testid="agent-row-internal-badge"]').exists()).toBe(true)
  })

  it('hides the internal-process badge for a normal session', async () => {
    const wrapper = await mountExpanded({ ...baseAgent, internalProcess: false })
    expect(wrapper.find('[data-testid="agent-row-internal-badge"]').exists()).toBe(false)
  })
})

describe('agentRow status dot', () => {
  it('matches the badge: accent for an agent that has just replied, success while it works', () => {
    const dot = (agent: Agent) => mount(AgentRow, { props: { agent } }).get('[data-testid="agent-row-status-dot"]').classes()
    expect(dot({ ...baseAgent, status: 'active', working: false })).toContain('bg-accent')
    expect(dot({ ...baseAgent, status: 'active', working: true })).toContain('bg-success-dot')
  })
})
