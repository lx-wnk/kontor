import { mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { ref } from 'vue'
import TaskCard from '@/features/pipeline/components/TaskCard.vue'

const FULL_UUID = '812f85f4-aaaa-bbbb-cccc-dddddddddddd'

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
    parentTaskId: null,
    maxIterations: 10,
    tokenBudget: null,
    costBudgetCents: null,
    createdAt: new Date().toISOString(),
    updatedAt: new Date().toISOString(),
    metadata: null,
    silverBullet: false,
    priority: 'medium',
    userId: null,
    ...overrides,
  } as any
}

vi.mock('@/components/WorktreePill.vue', () => ({ default: { template: '<span />' } }))

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

describe('taskCard — short-id chip', () => {
  beforeEach(() => {
    clipboardCopy.mockClear()
  })

  it('renders the first 8 chars of task.id in the chip', () => {
    const wrapper = mount(TaskCard, { props: { task: makeTask() } })
    expect(wrapper.text()).toContain('#812f85f4')
  })

  it('does NOT trigger the select emit when the copy button is clicked', async () => {
    const wrapper = mount(TaskCard, { props: { task: makeTask() } })
    const btn = wrapper.find('button[aria-label^="Copy task id"]')
    expect(btn.exists()).toBe(true)
    await btn.trigger('click')
    expect(wrapper.emitted('select')).toBeFalsy()
  })

  it('copy button has aria-label containing full UUID and title = full UUID', () => {
    const wrapper = mount(TaskCard, { props: { task: makeTask() } })
    const btn = wrapper.find('button[aria-label^="Copy task id"]')
    expect(btn.attributes('aria-label')).toContain(FULL_UUID)
    expect(btn.attributes('title')).toBe(FULL_UUID)
  })

  it('clicking copy button writes the FULL UUID to clipboard, not the truncated short-id', async () => {
    const wrapper = mount(TaskCard, { props: { task: makeTask() } })
    const btn = wrapper.find('button[aria-label^="Copy task id"]')
    await btn.trigger('click')
    expect(clipboardCopy).toHaveBeenCalledWith(FULL_UUID)
    expect(clipboardCopy).not.toHaveBeenCalledWith('#812f85f4')
  })
})

describe('taskCard — attention cause chip', () => {
  it('shows "Needs permission" when hasPendingPermissions is true', () => {
    const wrapper = mount(TaskCard, {
      props: { task: makeTask({ hasPendingPermissions: true }) },
    })
    const chip = wrapper.find('[data-testid="attention-cause-chip"]')
    expect(chip.exists()).toBe(true)
    expect(chip.text()).toContain('Needs permission')
    wrapper.unmount()
  })

  it('shows "Plan awaiting approval" for plan_review + awaiting_user', () => {
    const wrapper = mount(TaskCard, {
      props: {
        task: makeTask({
          currentStage: 'plan_review',
          latestStageRunStatus: 'awaiting_user',
        }),
      },
    })
    const chip = wrapper.find('[data-testid="attention-cause-chip"]')
    expect(chip.exists()).toBe(true)
    expect(chip.text()).toContain('Plan awaiting approval')
    wrapper.unmount()
  })

  it('shows "Needs input" for implementation + awaiting_user with waitReason title', () => {
    const wrapper = mount(TaskCard, {
      props: {
        task: makeTask({
          currentStage: 'implementation',
          latestStageRunStatus: 'awaiting_user',
          waitReason: 'review cycle limit (3) reached',
        }),
      },
    })
    const chip = wrapper.find('[data-testid="attention-cause-chip"]')
    expect(chip.exists()).toBe(true)
    expect(chip.text()).toContain('Needs input')
    expect(chip.attributes('title')).toBe('review cycle limit (3) reached')
    wrapper.unmount()
  })

  it('shows "Paused: usage limit" for rate_limited', () => {
    const wrapper = mount(TaskCard, {
      props: {
        task: makeTask({ latestStageRunStatus: 'rate_limited' }),
      },
    })
    const chip = wrapper.find('[data-testid="attention-cause-chip"]')
    expect(chip.exists()).toBe(true)
    expect(chip.text()).toContain('Paused: usage limit')
    wrapper.unmount()
  })

  it('shows unsatisfiable label with blocking slug', () => {
    const wrapper = mount(TaskCard, {
      props: {
        task: makeTask({
          isUnsatisfiable: true,
          blockingUpstreams: [{ slug: 'upstream-a', stage: 'cancelled' }],
        }),
      },
    })
    const chip = wrapper.find('[data-testid="attention-cause-chip"]')
    expect(chip.exists()).toBe(true)
    expect(chip.text()).toContain('Unsatisfiable: upstream-a cancelled')
    wrapper.unmount()
  })

  it('shows blocked label with upstream slug and stage', () => {
    const wrapper = mount(TaskCard, {
      props: {
        task: makeTask({
          isBlocked: true,
          blockingUpstreams: [{ slug: 'upstream-a', stage: 'implementation' }],
        }),
      },
    })
    const chip = wrapper.find('[data-testid="attention-cause-chip"]')
    expect(chip.exists()).toBe(true)
    expect(chip.text()).toContain('Waiting for: upstream-a (implementation)')
    wrapper.unmount()
  })

  it('shows no attention chip when no cause applies', () => {
    const wrapper = mount(TaskCard, {
      props: { task: makeTask({ latestStageRunStatus: 'running' }) },
    })
    expect(wrapper.find('[data-testid="attention-cause-chip"]').exists()).toBe(false)
    wrapper.unmount()
  })
})
