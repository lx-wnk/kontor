import { mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'
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

const clipboardCopy = vi.fn().mockResolvedValue(undefined)

vi.mock('@vueuse/core', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@vueuse/core')>()
  return {
    ...actual,
    useClipboard: () => ({
      copy: clipboardCopy,
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

describe('taskModal — id copy button', () => {
  beforeEach(() => {
    clipboardCopy.mockClear()
  })

  it('clicking the id copy button writes the FULL UUID to clipboard', async () => {
    const wrapper = mount(TaskModal, {
      props: { task: makeTask() },
      attachTo: document.body,
    })

    const btn = wrapper.find('button[aria-label^="Copy task id"]')
    expect(btn.exists()).toBe(true)
    await btn.trigger('click')
    expect(clipboardCopy).toHaveBeenCalledWith(FULL_UUID)
    expect(clipboardCopy).not.toHaveBeenCalledWith(expect.stringContaining('#'))

    wrapper.unmount()
  })
})
