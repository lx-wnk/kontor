import type { Agent } from '@/types'
import { mount } from '@vue/test-utils'
import { describe, expect, it, vi } from 'vitest'

const selectAgent = vi.fn()
vi.mock('@/features/agents', () => ({
  useAgents: () => ({ selectAgent }),
}))

const { default: HubAgentCard } = await import('../components/HubAgentCard.vue')

const agent = { pid: 7, projectName: 'web-app', status: 'waiting', working: false, lastOutput: 'Tests pass.' } as Agent

describe('hubAgentCard', () => {
  it('shows the agent, its state as a word and its last output', () => {
    const w = mount(HubAgentCard, { props: { agent } })
    const card = w.get('[role="dialog"]')
    expect(card.attributes('data-hub-layer')).toBeDefined()
    expect(card.attributes('aria-label')).toBe('Web App')
    expect(w.get('[data-testid="hub-card-state"]').text()).toBe('Quiet')
    expect(w.text()).toContain('Tests pass.')
  })

  it('opens the agent session', async () => {
    const w = mount(HubAgentCard, { props: { agent } })
    await w.findAll('button').find(b => b.text() === 'Open session')!.trigger('click')
    expect(selectAgent).toHaveBeenCalledWith(agent)
  })

  it('closes from its button', async () => {
    const w = mount(HubAgentCard, { props: { agent } })
    await w.get('button[aria-label="Close card"]').trigger('click')
    expect(w.emitted('close')).toHaveLength(1)
  })
})
