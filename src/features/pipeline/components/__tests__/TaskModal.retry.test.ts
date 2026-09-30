import { mount } from '@vue/test-utils'
import { describe, expect, it, vi } from 'vitest'
import { ref } from 'vue'
import TaskModal from '@/features/pipeline/components/TaskModal.vue'

const FULL_UUID = '812f85f4-2b20-405b-b133-0dd1ab73dff3'

function makeTask(overrides = {}) {
  return {
    id: FULL_UUID,
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
  } as any
}

function retryBudgetFor(status: string | null | undefined): number {
  return status === 'rate_limited' ? 36 : 3
}

function isRetryQueued(status: string | null | undefined): boolean {
  return status === 'requeued' || status === 'rate_limited'
}

function retryChip(status: string | null | undefined, count: number, secondsLeft: number) {
  const budget = retryBudgetFor(status)
  if (isRetryQueued(status)) {
    return {
      label: `Retrying · ${count}/${budget}${secondsLeft > 0 ? ` · ${secondsLeft}s` : ''}`,
      title: `Auto-retry queued (attempt ${count} of ${budget})`,
    }
  }
  return {
    label: `Retry ${count}/${budget}`,
    title: `Auto-retry attempt ${count} of ${budget} in progress`,
  }
}

vi.mock('@/features/pipeline/composables/usePipelineConfig', () => ({
  usePipelineConfig: () => ({
    maxAutoRetries: ref(3),
    maxRateLimitRetries: ref(36),
    config: ref(null),
    retryBudgetFor,
    isRetryQueued,
    retryChip,
  }),
}))

vi.mock('@vueuse/core', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@vueuse/core')>()
  return {
    ...actual,
    useClipboard: () => ({
      copy: vi.fn(),
      copied: ref(false),
    }),
  }
})

vi.mock('@/composables/useAgents', () => ({
  useAgents: () => ({ agents: ref([]) }),
}))

vi.mock('@/composables/useProjects', () => ({
  useProjects: () => ({ projects: ref([]) }),
}))

vi.mock('@/composables/useSpawners', () => ({
  useSpawners: () => ({ spawners: ref([]) }),
}))

vi.mock('@/features/pipeline/composables/useTasks', () => ({
  fetchStageRuns: vi.fn().mockResolvedValue([]),
  fetchTaskPermissions: vi.fn().mockResolvedValue([]),
  fetchPendingPermissionRequests: vi.fn().mockResolvedValue([]),
  fetchTaskFeedback: vi.fn().mockResolvedValue([]),
  fetchDependencies: vi.fn().mockResolvedValue([]),
  fetchDependents: vi.fn().mockResolvedValue([]),
  fetchStageRunAgentOutput: vi.fn().mockResolvedValue(''),
  addTaskDependency: vi.fn(),
  analyzeTask: vi.fn(),
  bulkResolvePermissionRequests: vi.fn(),
  cancelTask: vi.fn(),
  grantTaskPermission: vi.fn(),
  progressTask: vi.fn(),
  removeTaskDependency: vi.fn(),
  resolvePermissionRequest: vi.fn(),
  resumeStageTask: vi.fn(),
  retryTask: vi.fn(),
}))

vi.mock('@/components/AgentChatStream.vue', () => ({ default: { template: '<div />' } }))
vi.mock('@/components/AuditLogTab.vue', () => ({ default: { template: '<div />' } }))
vi.mock('@/components/DependencyGraph.vue', () => ({ default: { template: '<div />' } }))
vi.mock('@/components/GitStatusPanel.vue', () => ({ default: { template: '<div />' } }))
vi.mock('@/features/pipeline/components/RefineStatusPanel.vue', () => ({ default: { template: '<div />' } }))
vi.mock('@/features/pipeline/components/StageCostWaterfall.vue', () => ({ default: { template: '<div />' } }))
vi.mock('@/features/pipeline/components/StageOutputView.vue', () => ({ default: { template: '<div />' } }))
vi.mock('@/features/pipeline/components/TaskSlashCommandMenu.vue', () => ({ default: { template: '<div />', methods: { onKeydown: () => {} } } }))
vi.mock('@/components/WorktreeCommandRunner.vue', () => ({ default: { template: '<div />' } }))
vi.mock('@/components/WorktreePanel.vue', () => ({ default: { template: '<div />' } }))
vi.mock('@/components/ui/AppModal.vue', () => ({
  default: {
    props: ['open', 'zIndex', 'labelledBy'],
    template: '<div v-if="open"><slot /></div>',
    emits: ['close'],
  },
}))
vi.mock('@/components/ui/AppButton.vue', () => ({ default: { template: '<button><slot /></button>' } }))
vi.mock('@/components/ui/AppInput.vue', () => ({ default: { template: '<input />' } }))

describe('taskModal retry chip', () => {
  it('divides a rate-limited run by the rate-limit budget, not the infra budget', () => {
    const wrapper = mount(TaskModal, {
      props: {
        task: makeTask({
          latestStageRunStatus: 'rate_limited',
          autoRetryCount: 7,
          nextRetryAt: new Date(Date.now() + 30000).toISOString(),
        }),
      },
      attachTo: document.body,
    })

    expect(wrapper.text()).toContain('Retrying · 7/36')
    expect(wrapper.text()).not.toMatch(/7\/3(?!\d)/)

    wrapper.unmount()
  })

  it('divides an infra retry by the auto-retry budget', () => {
    const wrapper = mount(TaskModal, {
      props: {
        task: makeTask({
          latestStageRunStatus: 'requeued',
          autoRetryCount: 2,
          nextRetryAt: new Date(Date.now() + 30000).toISOString(),
        }),
      },
      attachTo: document.body,
    })

    expect(wrapper.text()).toContain('Retrying · 2/3')

    wrapper.unmount()
  })

  it('renders a running-phase chip without a countdown when the retry is in progress', () => {
    const wrapper = mount(TaskModal, {
      props: {
        task: makeTask({
          latestStageRunStatus: 'running',
          autoRetryCount: 1,
          nextRetryAt: null,
        }),
      },
      attachTo: document.body,
    })

    expect(wrapper.text()).toContain('Retry 1/3')
    expect(wrapper.text()).not.toContain('Retrying')
    expect(wrapper.find('[title*="in progress"]').exists()).toBe(true)

    wrapper.unmount()
  })
})
