import type { Agent, PipelineTask } from '@/types'
import { mount } from '@vue/test-utils'
import { describe, expect, it, vi } from 'vitest'
import TaskCard from '@/features/pipeline/components/TaskCard.vue'

vi.mock('@/composables/useAgentIdentity', () => ({
  useAgentIdentity: () => ({
    getIdentity: () => ({ emoji: '🤖' }),
  }),
}))

const baseTask: PipelineTask = {
  id: 'task-1',
  slug: 'my-task',
  title: 'My Task Title',
  description: null,
  cwd: '/tmp',
  worktreePath: null,
  sourceBranch: null,
  targetBranch: null,
  currentStage: 'ready',
  parentTaskId: null,
  maxIterations: 3,
  tokenBudget: null,
  costBudgetCents: null,
  createdAt: '2026-01-01T00:00:00Z',
  updatedAt: '2026-01-01T00:00:00Z',
  metadata: null,
  silverBullet: false,
  planMode: false,
  priority: 'medium',
  userId: null,
}

const activeChild: NonNullable<PipelineTask['activeChild']> = {
  tokensUsed: 12000,
  costCents: 50,
  durationSeconds: 180,
  currentStage: 'implementation',
  latestOutput: 'Writing unit tests for the formatter utility',
}

const stubs = {
  WorktreePill: true,
}

const baseAgent: Agent = {
  pid: 1234,
  sessionId: 'sess-abc',
  provider: 'claude' as any,
  projectPath: '/projects/my-app',
  projectName: 'my-app',
  cwd: '/projects/my-app',
  entrypoint: 'cli' as any,
  status: 'active',
  uptime: 0,
  lastActivity: new Date().toISOString(),
  lastTools: [],
  tasks: [],
  subagents: [],
  tokenUsage: { inputTokens: 0, outputTokens: 0, cacheCreationTokens: 0, cacheReadTokens: 0 },
  costEstimate: 0,
  cacheCreationCostEstimate: 0,
  cacheReadCostEstimate: 0,
  healthScore: 100,
  conversationTurns: 0,
  toolCounts: {},
  channelAvailable: false,
  working: false,
  permissionsBypassed: false,
  convergenceAlert: false,
  pipelineTaskId: 'task-1',
  meta: null,
}

describe('taskCard', () => {
  it('renders a real button with data-testid="task-card-open"', () => {
    const wrapper = mount(TaskCard, {
      props: { task: baseTask },
      global: { stubs },
    })
    expect(wrapper.find('button[data-testid="task-card-open"]').exists()).toBe(true)
  })

  it('aria-label on the open button contains the task title', () => {
    const wrapper = mount(TaskCard, {
      props: { task: baseTask },
      global: { stubs },
    })
    const btn = wrapper.find('button[data-testid="task-card-open"]')
    expect(btn.attributes('aria-label')).toContain('My Task Title')
  })

  it('clicking the open button emits select with the task', async () => {
    const wrapper = mount(TaskCard, {
      props: { task: baseTask },
      global: { stubs },
    })
    await wrapper.find('button[data-testid="task-card-open"]').trigger('click')
    expect(wrapper.emitted('select')).toBeTruthy()
    expect(wrapper.emitted('select')![0]).toEqual([baseTask])
  })

  it('shows Continue Chat button when currentStage is backlog', () => {
    const task = { ...baseTask, currentStage: 'backlog' as const }
    const wrapper = mount(TaskCard, {
      props: { task },
      global: { stubs },
    })
    expect(wrapper.find('button[data-testid="task-card-open"]').exists()).toBe(true)
    const buttons = wrapper.findAll('button')
    const continueChat = buttons.find(b => b.text().includes('Continue Chat'))
    expect(continueChat).toBeTruthy()
  })

  it('continue Chat emits openChat and NOT select (click.stop)', async () => {
    const task = { ...baseTask, currentStage: 'backlog' as const }
    const wrapper = mount(TaskCard, {
      props: { task },
      global: { stubs },
    })
    const buttons = wrapper.findAll('button')
    const continueChat = buttons.find(b => b.text().includes('Continue Chat'))!
    await continueChat.trigger('click')
    expect(wrapper.emitted('openChat')).toBeTruthy()
    expect(wrapper.emitted('openChat')![0]).toEqual([task])
    expect(wrapper.emitted('select')).toBeFalsy()
  })
})

describe('taskCard — PR error chip', () => {
  it('shows the chip with the error as tooltip when metadata.pr_error is set', () => {
    const task = { ...baseTask, metadata: { pr_error: 'gh: not logged in' } }
    const wrapper = mount(TaskCard, { props: { task }, global: { stubs } })
    const chip = wrapper.find('[data-testid="task-card-pr-error"]')
    expect(chip.exists()).toBe(true)
    expect(chip.text()).toContain('PR not created')
    expect(chip.attributes('title')).toBe('gh: not logged in')
  })

  it('hides the chip without pr_error', () => {
    const wrapper = mount(TaskCard, { props: { task: baseTask }, global: { stubs } })
    expect(wrapper.find('[data-testid="task-card-pr-error"]').exists()).toBe(false)
  })
})

describe('taskCard — agent chip', () => {
  it('renders the agent chip when workingAgent is provided', () => {
    const wrapper = mount(TaskCard, {
      props: { task: baseTask, workingAgent: baseAgent },
      global: { stubs },
    })
    expect(wrapper.find('button[data-testid="task-agent-chip"]').exists()).toBe(true)
  })

  it('does not render the agent chip when workingAgent is null', () => {
    const wrapper = mount(TaskCard, {
      props: { task: baseTask, workingAgent: null },
      global: { stubs },
    })
    expect(wrapper.find('button[data-testid="task-agent-chip"]').exists()).toBe(false)
  })

  it('does not render the agent chip when workingAgent is absent', () => {
    const wrapper = mount(TaskCard, {
      props: { task: baseTask },
      global: { stubs },
    })
    expect(wrapper.find('button[data-testid="task-agent-chip"]').exists()).toBe(false)
  })

  it('clicking the chip emits navigateAgent with the sessionId and does NOT emit select', async () => {
    const wrapper = mount(TaskCard, {
      props: { task: baseTask, workingAgent: baseAgent },
      global: { stubs },
    })
    const chip = wrapper.find('button[data-testid="task-agent-chip"]')
    await chip.trigger('click')
    expect(wrapper.emitted('navigateAgent')).toBeTruthy()
    expect(wrapper.emitted('navigateAgent')![0]).toEqual(['sess-abc'])
    expect(wrapper.emitted('select')).toBeFalsy()
  })

  it('chip aria-label references the agent projectName', () => {
    const wrapper = mount(TaskCard, {
      props: { task: baseTask, workingAgent: baseAgent },
      global: { stubs },
    })
    const chip = wrapper.find('button[data-testid="task-agent-chip"]')
    expect(chip.attributes('aria-label')).toContain('my-app')
  })
})

describe('taskCard — active child block', () => {
  it('hides the block when childCount is 0', () => {
    const task = { ...baseTask, childCount: 0, activeChildCount: 0, activeChild }
    const wrapper = mount(TaskCard, {
      props: { task },
      global: { stubs },
    })
    expect(wrapper.find('[data-testid="active-child-block"]').exists()).toBe(false)
  })

  it('hides the block when activeChild is null', () => {
    const task = { ...baseTask, childCount: 2, activeChildCount: 0, activeChild: null }
    const wrapper = mount(TaskCard, {
      props: { task },
      global: { stubs },
    })
    expect(wrapper.find('[data-testid="active-child-block"]').exists()).toBe(false)
  })

  it('hides the block when childCount is absent', () => {
    const wrapper = mount(TaskCard, {
      props: { task: baseTask },
      global: { stubs },
    })
    expect(wrapper.find('[data-testid="active-child-block"]').exists()).toBe(false)
  })

  it('shows the block with stage, token count, cost, and output snippet', () => {
    const task = { ...baseTask, childCount: 3, activeChildCount: 2, activeChild }
    const wrapper = mount(TaskCard, {
      props: { task },
      global: { stubs },
    })
    const block = wrapper.find('[data-testid="active-child-block"]')
    expect(block.exists()).toBe(true)
    expect(block.text()).toContain('2/3')
    expect(block.text()).toContain('12k tok')
    expect(block.text()).toContain('Implementation')
    expect(wrapper.find('[data-testid="active-child-latest-output"]').text()).toContain('Writing unit tests')
  })

  it('expand toggle reveals full latestOutput on click', async () => {
    const task = { ...baseTask, childCount: 1, activeChildCount: 1, activeChild }
    const wrapper = mount(TaskCard, {
      props: { task },
      global: { stubs },
    })
    const output = wrapper.find('[data-testid="active-child-latest-output"]')
    expect(output.classes()).toContain('truncate')

    await wrapper.find('[data-testid="active-child-expand-toggle"]').trigger('click')
    expect(output.classes()).not.toContain('truncate')
    expect(output.classes()).toContain('whitespace-pre-wrap')
  })
})

describe('taskCard — drag handle', () => {
  it('renders the drag handle when sortable is not specified (default behaviour)', () => {
    const wrapper = mount(TaskCard, { props: { task: baseTask }, global: { stubs } })
    expect(wrapper.find('.task-drag-handle').exists()).toBe(true)
  })

  it('renders the drag handle when sortable is explicitly true', () => {
    const wrapper = mount(TaskCard, { props: { task: baseTask, sortable: true }, global: { stubs } })
    expect(wrapper.find('.task-drag-handle').exists()).toBe(true)
  })

  it('hides the drag handle when sortable is false', () => {
    const wrapper = mount(TaskCard, { props: { task: baseTask, sortable: false }, global: { stubs } })
    expect(wrapper.find('.task-drag-handle').exists()).toBe(false)
  })
})
