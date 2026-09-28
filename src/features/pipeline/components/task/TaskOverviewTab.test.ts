import { flushPromises, mount } from '@vue/test-utils'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { ref } from 'vue'
import TaskOverviewTab from '@/features/pipeline/components/task/TaskOverviewTab.vue'
import { TaskDetailsKey, TaskRefKey } from '@/features/pipeline/composables/taskModalContext'

vi.mock('@/composables/useProjects', () => ({
  useProjects: () => ({ projects: ref([]) }),
}))

vi.mock('@/composables/useSpawners', () => ({
  useSpawners: () => ({ spawners: ref([]) }),
}))

vi.mock('@/features/pipeline/composables/useTasks', () => ({
  refreshTask: vi.fn().mockResolvedValue(undefined),
  fetchStageRuns: vi.fn().mockResolvedValue([]),
  fetchTaskPermissions: vi.fn().mockResolvedValue([]),
  fetchPendingPermissionRequests: vi.fn().mockResolvedValue([]),
  fetchTaskFeedback: vi.fn().mockResolvedValue([]),
  fetchDependencies: vi.fn().mockResolvedValue([]),
  fetchDependents: vi.fn().mockResolvedValue([]),
  fetchStageRunAgentOutput: vi.fn().mockResolvedValue(''),
}))

vi.mock('@/components/AgentChatStream.vue', () => ({ default: { template: '<div />' } }))
vi.mock('@/components/GitStatusPanel.vue', () => ({ default: { template: '<div />' } }))
vi.mock('@/features/pipeline/components/RefineStatusPanel.vue', () => ({ default: { template: '<div />' } }))
vi.mock('@/features/pipeline/components/StageOutputView.vue', () => ({ default: { template: '<div />' } }))
vi.mock('@/components/WorktreeCommandRunner.vue', () => ({ default: { template: '<div />' } }))
vi.mock('@/components/WorktreePanel.vue', () => ({ default: { template: '<div />' } }))
vi.mock('@/components/ui/AppButton.vue', () => ({
  default: {
    props: ['variant', 'size', 'disabled'],
    template: '<button :disabled="disabled" v-bind="$attrs"><slot /></button>',
  },
}))
vi.mock('@/components/ui/AppChip.vue', () => ({ default: { template: '<span><slot /></span>' } }))

function makeTask(overrides: Record<string, unknown> = {}) {
  return {
    id: 'task-1',
    slug: 'my-task',
    title: 'Test Task',
    description: null,
    cwd: '/home/user',
    worktreePath: null,
    sourceBranch: null,
    targetBranch: null,
    currentStage: 'ready',
    currentIteration: 0,
    maxIterations: 10,
    tokenBudget: null,
    costBudgetCents: null,
    stageTimeoutSeconds: 3600,
    createdAt: new Date().toISOString(),
    updatedAt: new Date().toISOString(),
    metadata: null,
    silverBullet: false,
    priority: 'medium',
    userId: null,
    projectId: null,
    spawnerId: null,
    parentTaskId: null,
    activeSessionId: null,
    activePid: null,
    latestStageRunStatus: null,
    blockedByPendingPermissions: false,
    refineStatus: null,
    refineError: null,
    isBlocked: false,
    ...overrides,
  }
}

function makeDetails() {
  return {
    stageRuns: ref([]),
    pendingRequests: ref([]),
    latestStageRun: ref(null),
    latestRunError: ref(null),
    latestRunAgentMessage: ref(null),
    sessionAgentText: ref(null),
    sessionAgentTextLoading: ref(false),
    pipelineAgent: ref(null),
    totalTokensUsed: ref(0),
    totalCostCents: ref(0),
    permissions: ref([]),
    pendingByStageRun: ref([]),
    isActing: ref(false),
    actionError: ref(''),
    actionSuccess: ref(''),
    handleAction: vi.fn(),
    isFailedRun: ref(false),
  }
}

function mountTab(taskOverrides: Record<string, unknown> = {}) {
  const task = ref(makeTask(taskOverrides))
  const details = makeDetails()
  return mount(TaskOverviewTab, {
    global: {
      provide: {
        [TaskRefKey as symbol]: task,
        [TaskDetailsKey as symbol]: details,
      },
    },
  })
}

describe('taskOverviewTab — autonomy selector', () => {
  // AppSelect is a custom listbox (button trigger + teleported panel), not a
  // native <select> — the selected value is asserted via the trigger's
  // rendered label instead of select.value (see DashboardToolbar.test.ts).
  it('renders the autonomy select with spec_gated when autonomy is undefined', () => {
    const wrapper = mountTab()
    const select = wrapper.find('[data-testid="task-autonomy-select"]')
    expect(select.exists()).toBe(true)
    expect(select.text()).toContain('Spec-gated')
  })

  it('reflects the task autonomy value', () => {
    const wrapper = mountTab({ autonomy: 'manual' })
    expect(wrapper.find('[data-testid="task-autonomy-select"]').text()).toContain('Manual')
  })
})

describe('taskOverviewTab — concept viewer', () => {
  // The concept viewer now reads spec/plan off a dedicated GET /api/tasks/{id}
  // fetch (the list response that populates the injected task strips
  // metadata) — stub fetch to answer that single-task GET with the given
  // metadata, and any other request (e.g. the approve_spec POST) with ok:true.
  function stubTaskFetch(metadata: Record<string, unknown> | null) {
    vi.stubGlobal('fetch', vi.fn((url: string) => {
      if (url === '/api/tasks/task-1') {
        return Promise.resolve({ ok: true, json: () => Promise.resolve({ metadata }) } as Response)
      }
      return Promise.resolve({ ok: true } as Response)
    }))
  }

  afterEach(() => {
    vi.unstubAllGlobals()
  })

  it('renders nothing for concept viewer when metadata has no spec or plan', async () => {
    stubTaskFetch(null)
    const wrapper = mountTab({ metadata: null })
    await flushPromises()
    expect(wrapper.find('[data-testid="concept-viewer"]').exists()).toBe(false)
  })

  it('renders the concept block when metadata has a spec', async () => {
    stubTaskFetch({ spec: 'This is the spec text.' })
    const wrapper = mountTab()
    await flushPromises()
    expect(wrapper.find('[data-testid="concept-viewer"]').exists()).toBe(true)
    expect(wrapper.text()).toContain('This is the spec text.')
    expect(wrapper.text()).toContain('Spec')
  })

  it('renders the concept block when metadata has a plan', async () => {
    stubTaskFetch({ plan: '1. Step one\n2. Step two' })
    const wrapper = mountTab()
    await flushPromises()
    expect(wrapper.find('[data-testid="concept-viewer"]').exists()).toBe(true)
    expect(wrapper.text()).toContain('1. Step one')
    expect(wrapper.text()).toContain('Plan')
  })

  it('renders both spec and plan sections when both are present', async () => {
    stubTaskFetch({ spec: 'The spec', plan: 'The plan' })
    const wrapper = mountTab()
    await flushPromises()
    const viewer = wrapper.find('[data-testid="concept-viewer"]')
    expect(viewer.text()).toContain('The spec')
    expect(viewer.text()).toContain('The plan')
  })

  it('hides Approve Spec button when refineStatus is not draft_ready', async () => {
    stubTaskFetch({ spec: 'Some spec' })
    const wrapper = mountTab({ refineStatus: 'done' })
    await flushPromises()
    expect(wrapper.find('[data-testid="approve-spec-btn"]').exists()).toBe(false)
  })

  it('shows Approve Spec button when refineStatus is draft_ready', async () => {
    stubTaskFetch({ spec: 'Some spec' })
    const wrapper = mountTab({ refineStatus: 'draft_ready' })
    await flushPromises()
    expect(wrapper.find('[data-testid="approve-spec-btn"]').exists()).toBe(true)
  })

  it('calls runAction approve_spec when Approve Spec is clicked', async () => {
    stubTaskFetch({ spec: 'Some spec' })
    const wrapper = mountTab({ refineStatus: 'draft_ready' })
    await flushPromises()
    await wrapper.find('[data-testid="approve-spec-btn"]').trigger('click')
    await flushPromises()
    expect(fetch).toHaveBeenCalledWith(
      '/api/refine/task-1/confirm',
      expect.objectContaining({ method: 'POST' }),
    )
  })
})
