import { mount } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { ref } from 'vue'
import TaskCard from '@/features/pipeline/components/TaskCard.vue'

function retryBudgetFor(status: string | null | undefined): number {
  return status === 'rate_limited' ? 36 : 5
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
    maxAutoRetries: ref(5),
    maxRateLimitRetries: ref(36),
    config: ref(null),
    retryBudgetFor,
    isRetryQueued,
    retryChip,
  }),
}))

vi.mock('@vueuse/core', () => ({
  useIntervalFn: vi.fn(),
  useSortable: vi.fn(() => ({ option: vi.fn(), destroy: vi.fn() })),
  useClipboard: vi.fn(() => ({ copy: vi.fn(), copied: ref(false) })),
}))

beforeEach(() => {
  vi.stubGlobal('fetch', vi.fn(() => Promise.resolve({ ok: true, json: async () => null })))
})
afterEach(() => {
  vi.unstubAllGlobals()
  vi.clearAllMocks()
})

const baseTask = {
  id: 't1',
  slug: 'TSK-1',
  title: 'Test task',
  description: null,
  cwd: '/tmp',
  worktreePath: null,
  sourceBranch: null,
  targetBranch: null,
  currentStage: 'implementation' as const,
  parentTaskId: null,
  maxIterations: 5,
  tokenBudget: null,
  costBudgetCents: null,
  createdAt: new Date().toISOString(),
  updatedAt: new Date().toISOString(),
  metadata: null,
  silverBullet: false,
  priority: 'medium' as const,
  userId: null,
}

describe('taskCard retry state', () => {
  it('renders retry chip with attempt count when task is requeued', () => {
    const wrapper = mount(TaskCard, {
      props: {
        task: {
          ...baseTask,
          latestStageRunStatus: 'requeued',
          autoRetryCount: 2,
          nextRetryAt: new Date(Date.now() + 30000).toISOString(),
          needsUser: false,
        } as any,
      },
    })
    expect(wrapper.text()).toContain('Retrying · 2/5')
  })

  it('renders a running-phase chip without a countdown when the retry is in progress', () => {
    const wrapper = mount(TaskCard, {
      props: {
        task: {
          ...baseTask,
          latestStageRunStatus: 'running',
          autoRetryCount: 1,
          nextRetryAt: null,
          needsUser: false,
        } as any,
      },
    })
    expect(wrapper.text()).toContain('Retry 1/5')
    expect(wrapper.text()).not.toContain('Retrying')
    expect(wrapper.find('[title*="in progress"]').exists()).toBe(true)
  })

  it('divides a rate-limited run by the rate-limit budget, not the infra budget', () => {
    const wrapper = mount(TaskCard, {
      props: {
        task: {
          ...baseTask,
          latestStageRunStatus: 'rate_limited',
          autoRetryCount: 7,
          nextRetryAt: new Date(Date.now() + 30000).toISOString(),
          needsUser: false,
        } as any,
      },
    })
    expect(wrapper.text()).toContain('7/36')
    expect(wrapper.text()).not.toContain('7/5')
  })

  it('does NOT render retry chip for normal failed task, but DOES show needs-user block', () => {
    const wrapper = mount(TaskCard, {
      props: {
        task: {
          ...baseTask,
          latestStageRunStatus: 'awaiting_user',
          needsUser: true,
          autoRetryCount: null,
          nextRetryAt: null,
        } as any,
      },
    })
    expect(wrapper.text()).not.toContain('Retrying')
    expect(wrapper.find('[data-testid="attention-cause-chip"]').text()).toContain('Needs input')
  })

  it('does NOT render needs-user block for requeued task', () => {
    const wrapper = mount(TaskCard, {
      props: {
        task: {
          ...baseTask,
          latestStageRunStatus: 'requeued',
          autoRetryCount: 2,
          nextRetryAt: null,
          needsUser: false,
        } as any,
      },
    })
    expect(wrapper.text()).not.toContain('Needs Permission')
  })
})

describe('secondsUntil util', () => {
  it('returns positive seconds for a future timestamp', async () => {
    const { secondsUntil } = await import('@/utils/retryCountdown')
    const future = new Date(Date.now() + 10000).toISOString()
    expect(secondsUntil(future)).toBeGreaterThan(0)
  })

  it('clamps to 0 for a past timestamp', async () => {
    const { secondsUntil } = await import('@/utils/retryCountdown')
    const past = new Date(Date.now() - 5000).toISOString()
    expect(secondsUntil(past)).toBe(0)
  })

  it('returns 0 for null', async () => {
    const { secondsUntil } = await import('@/utils/retryCountdown')
    expect(secondsUntil(null)).toBe(0)
  })
})
