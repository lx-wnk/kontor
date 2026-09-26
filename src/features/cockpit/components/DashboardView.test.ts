import { mount } from '@vue/test-utils'
import { describe, expect, it, vi } from 'vitest'
import { useViewState } from '@/composables/useViewState'
import { useAgents } from '@/features/agents'
import DashboardView from './DashboardView.vue'

// The roster reads the module-level singletons in useAgents/useViewState, so a
// test drives it by stubbing those modules rather than by passing props. Only
// the two genuinely per-caller values (permissionItems, focusedSessionId) are
// props — see usePendingPermissions, which is not a singleton.
vi.mock('@/features/agents', async () => {
  const { ref, shallowRef, computed } = await import('vue')
  const agents = shallowRef<any[]>([])
  return {
    useAgents: () => ({
      agents,
      filteredAgents: computed(() => agents.value),
      attentionAgents: computed(() => []),
      pendingCapabilityDecisions: ref([]),
      searchQuery: ref(''),
      selectAgent: vi.fn(),
      dismissAgent: vi.fn(),
    }),
    AgentCardGrid: { name: 'AgentCardGrid', template: '<div data-testid="agent-card-grid" />' },
    AgentTable: { name: 'AgentTable', template: '<div data-testid="agent-table" />' },
    AgentTriageBand: { name: 'AgentTriageBand', template: '<div data-testid="triage-band" />' },
    EmptyAgentState: { name: 'EmptyAgentState', template: '<div data-testid="empty-state" />' },
  }
})

const stubs = {
  AutoApprovingStrip: { template: '<div data-testid="auto-approving-strip" />' },
  DashboardToolbar: { template: '<div data-testid="dashboard-toolbar" />' },
  ChannelScriptCallout: { template: '<div data-testid="channel-script-callout" />' },
}

function mountWithStoredProject(stored: string, agents: any[]) {
  useViewState().dashboardProject.value = stored
  useAgents({ autoStart: false }).agents.value = agents
  return mount(DashboardView, {
    props: { permissionItems: [], focusedSessionId: null },
    global: { stubs },
  })
}

describe('dashboardView', () => {
  it('migrates a stored project name to that project\'s key', () => {
    const wrapper = mountWithStoredProject('Kontor', [{ projectId: 'p1', projectName: 'Kontor' }])
    expect(useViewState().dashboardProject.value).toBe('p1')
    wrapper.unmount()
  })

  it('falls back to all projects when the stored value matches no agent', () => {
    const wrapper = mountWithStoredProject('gone', [{ projectId: 'p1', projectName: 'Kontor' }])
    expect(useViewState().dashboardProject.value).toBe('all')
    wrapper.unmount()
  })

  it('renders the toolbar, the triage band and the empty state when no agent is live', () => {
    useAgents({ autoStart: false }).agents.value = []
    const wrapper = mount(DashboardView, {
      attachTo: document.body,
      props: { permissionItems: [], focusedSessionId: null },
      global: { stubs },
    })

    expect(wrapper.find('[data-testid="triage-band"]').exists()).toBe(true)
    expect(wrapper.find('[data-testid="dashboard-toolbar"]').exists()).toBe(true)
    expect(wrapper.find('[data-testid="empty-state"]').exists()).toBe(true)
    expect(wrapper.find('[data-testid="agent-card-grid"]').exists()).toBe(false)
  })
})
