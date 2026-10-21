import type { Agent } from '@/types'
import { mount } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { nextTick } from 'vue'
import AgentCardGrid from './AgentCardGrid.vue'

vi.mock('@/features/agents/composables/useAgentIdentity', () => ({
  useAgentIdentity: () => ({
    getIdentity: () => ({ emoji: '🤖' }),
  }),
}))

const stubs = {
  MachineBadge: true,
  ProviderBadge: true,
  AppBadge: true,
  PromptInput: true,
}

function finishedAgent(pid = 4242): Agent {
  return {
    pid,
    sessionId: 's1',
    provider: 'claude',
    projectName: 'p',
    projectPath: '/p',
    status: 'finished',
    channelAvailable: true,
    liveInjectable: false,
    uptime: 1,
    lastActivity: new Date().toISOString(),
    tokenUsage: { inputTokens: 0, outputTokens: 0, cacheCreationTokens: 0, cacheReadTokens: 0 },
    costEstimate: 0,
    cacheCreationCostEstimate: 0,
    cacheReadCostEstimate: 0,
    costUnknown: false,
    healthScore: 100,
    model: 'claude-opus-4-8',
    subagents: [],
    tasks: [],
    lastOutput: 'done',
  } as unknown as Agent
}

function makeLocalStorageMock() {
  const store = new Map<string, string>()
  return {
    getItem: (key: string) => store.get(key) ?? null,
    setItem: (key: string, value: string) => store.set(key, value),
    removeItem: (key: string) => store.delete(key),
    clear: () => store.clear(),
    get length() { return store.size },
    key: (index: number) => Array.from(store.keys())[index] ?? null,
  }
}

describe('agentCardGrid dismiss forwarding', () => {
  beforeEach(() => {
    vi.restoreAllMocks()
    vi.stubGlobal('fetch', vi.fn(() => Promise.resolve({ ok: true, status: 204 })))
  })

  afterEach(() => {
    vi.unstubAllGlobals()
  })

  it('re-emits dismiss with the pid when a finished card is dismissed', async () => {
    const w = mount(AgentCardGrid, {
      props: { agents: [finishedAgent(4242)] },
      global: { stubs },
    })
    await w.find('[data-testid="agent-card-dismiss"]').trigger('click')
    expect(w.emitted('dismiss')?.[0]).toEqual([4242])
  })
})

describe('agentCardGrid collapsible groups', () => {
  let localStorageMock: ReturnType<typeof makeLocalStorageMock>

  beforeEach(() => {
    vi.restoreAllMocks()
    vi.stubGlobal('fetch', vi.fn(() => Promise.resolve({ ok: true, status: 204 })))
    localStorageMock = makeLocalStorageMock()
    vi.stubGlobal('localStorage', localStorageMock)
  })

  afterEach(() => {
    vi.unstubAllGlobals()
  })

  it('hides a group\'s cards when its header is toggled', async () => {
    const groupA = { key: 'project-a', label: 'Project A', agents: [finishedAgent(1)] }
    const groupB = { key: 'project-b', label: 'Project B', agents: [finishedAgent(2)] }

    // Seed stored state so the first-load default (collapse all but first) is skipped.
    localStorageMock.setItem('agent-dashboard-collapsed-groups', '[]')

    const w = mount(AgentCardGrid, {
      props: {
        agents: [finishedAgent(1), finishedAgent(2)],
        groups: [groupA, groupB],
      },
      global: { stubs },
      attachTo: document.body,
    })

    // Both groups' card grids should be visible initially
    const grids = w.findAll('[data-testid="group-card-grid"]')
    expect(grids).toHaveLength(2)
    expect(grids[0].isVisible()).toBe(true)
    expect(grids[1].isVisible()).toBe(true)

    // Toggle the first group header
    const toggles = w.findAll('[data-testid="group-header-toggle"]')
    await toggles[0].trigger('click')
    await nextTick()

    // First group's card grid should now be hidden
    expect(grids[0].isVisible()).toBe(false)
    // Second group remains visible
    expect(grids[1].isVisible()).toBe(true)
  })

  it('persists collapsed keys to localStorage', async () => {
    const groupA = { key: 'project-a', label: 'Project A', agents: [finishedAgent(1)] }

    const w = mount(AgentCardGrid, {
      props: {
        agents: [finishedAgent(1)],
        groups: [groupA],
      },
      global: { stubs },
    })

    await w.find('[data-testid="group-header-toggle"]').trigger('click')

    const stored = localStorageMock.getItem('agent-dashboard-collapsed-groups')
    expect(stored).not.toBeNull()
    const parsed = JSON.parse(stored!)
    expect(parsed).toContain('project-a')
  })

  it('collapses all groups except the first on first load', async () => {
    const groups = [
      { key: 'active', label: 'Live', agents: [finishedAgent(1)] },
      { key: 'waiting', label: 'Quiet', agents: [finishedAgent(2)] },
      { key: 'idle', label: 'Idle', agents: [finishedAgent(3)] },
    ]

    const w = mount(AgentCardGrid, {
      props: { agents: groups.flatMap(g => g.agents), groups },
      global: { stubs },
      attachTo: document.body,
    })
    await nextTick()

    const grids = w.findAll('[data-testid="group-card-grid"]')
    expect(grids).toHaveLength(3)
    expect(grids[0].isVisible()).toBe(true)
    expect(grids[1].isVisible()).toBe(false)
    expect(grids[2].isVisible()).toBe(false)
  })

  it('namespaces collapsed state per grouping mode and re-applies the default on switch', async () => {
    const statusGroups = [
      { key: 'active', label: 'Live', agents: [finishedAgent(1)] },
      { key: 'waiting', label: 'Quiet', agents: [finishedAgent(2)] },
    ]
    const projectGroups = [
      { key: 'project-a', label: 'Project A', agents: [finishedAgent(1)] },
      { key: 'project-b', label: 'Project B', agents: [finishedAgent(2)] },
    ]

    const w = mount(AgentCardGrid, {
      props: { agents: statusGroups.flatMap(g => g.agents), groups: statusGroups, groupBy: 'status' as const },
      global: { stubs },
      attachTo: document.body,
    })
    await nextTick()

    // Status mode persists under its own namespaced key.
    expect(localStorageMock.getItem('agent-dashboard-collapsed-groups:status')).not.toBeNull()
    expect(localStorageMock.getItem('agent-dashboard-collapsed-groups:project')).toBeNull()

    // Switch to project grouping: the status keys must not leak; the default
    // (first group open, rest collapsed) re-applies and persists separately.
    await w.setProps({ groups: projectGroups, groupBy: 'project' as const })
    await nextTick()

    const projectStored = JSON.parse(localStorageMock.getItem('agent-dashboard-collapsed-groups:project')!)
    expect(projectStored).toEqual(['project-b'])
    expect(projectStored).not.toContain('waiting')

    const grids = w.findAll('[data-testid="group-card-grid"]')
    expect(grids[0].isVisible()).toBe(true)
    expect(grids[1].isVisible()).toBe(false)
  })

  it('respects stored state over the first-load default', async () => {
    // Empty array = a valid saved state meaning "user expanded everything".
    localStorageMock.setItem('agent-dashboard-collapsed-groups', '[]')
    const groups = [
      { key: 'active', label: 'Live', agents: [finishedAgent(1)] },
      { key: 'waiting', label: 'Quiet', agents: [finishedAgent(2)] },
    ]

    const w = mount(AgentCardGrid, {
      props: { agents: groups.flatMap(g => g.agents), groups },
      global: { stubs },
      attachTo: document.body,
    })
    await nextTick()

    const grids = w.findAll('[data-testid="group-card-grid"]')
    expect(grids[0].isVisible()).toBe(true)
    expect(grids[1].isVisible()).toBe(true)
  })
})
