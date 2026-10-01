import type { PipelineTask } from '@/types'
import { describe, expect, it } from 'vitest'
import { taskAttentionCause } from './taskAttentionCause'

function task(overrides: Partial<PipelineTask> = {}): PipelineTask {
  return {
    id: 'test-id',
    slug: 'test-task',
    title: 'Test Task',
    description: null,
    cwd: '/repo',
    worktreePath: null,
    sourceBranch: null,
    targetBranch: null,
    currentStage: 'implementation',
    parentTaskId: null,
    maxIterations: 10,
    tokenBudget: null,
    costBudgetCents: null,
    createdAt: new Date().toISOString(),
    updatedAt: new Date().toISOString(),
    metadata: null,
    silverBullet: false,
    planMode: false,
    priority: 'medium',
    userId: null,
    ...overrides,
  } as PipelineTask
}

describe('taskAttentionCause', () => {
  // Each cause fires correctly
  it('returns permission when hasPendingPermissions is true', () => {
    const result = taskAttentionCause(task({ hasPendingPermissions: true }))
    expect(result).toMatchObject({ kind: 'permission', label: 'Needs permission', tone: 'warning' })
  })

  it('returns plan_approval when awaiting_user in plan_review', () => {
    const result = taskAttentionCause(task({
      latestStageRunStatus: 'awaiting_user',
      currentStage: 'plan_review',
    }))
    expect(result).toMatchObject({ kind: 'plan_approval', label: 'Plan awaiting approval', tone: 'info' })
  })

  it('returns needs_input for awaiting_user outside plan_review', () => {
    const result = taskAttentionCause(task({
      latestStageRunStatus: 'awaiting_user',
      currentStage: 'implementation',
    }))
    expect(result).toMatchObject({ kind: 'needs_input', label: 'Needs input', tone: 'warning' })
  })

  it('populates title from waitReason when present', () => {
    const result = taskAttentionCause(task({
      latestStageRunStatus: 'awaiting_user',
      currentStage: 'implementation',
      waitReason: 'review cycle limit (3) reached',
    }))
    expect(result?.title).toBe('review cycle limit (3) reached')
  })

  it('returns usage_limit for rate_limited status', () => {
    const result = taskAttentionCause(task({ latestStageRunStatus: 'rate_limited' }))
    expect(result).toMatchObject({ kind: 'usage_limit', tone: 'warning' })
    expect(result?.label).toBe('Paused: usage limit')
  })

  it('includes reset time in usage_limit label when nextRetryAt is present', () => {
    const result = taskAttentionCause(task({
      latestStageRunStatus: 'rate_limited',
      nextRetryAt: '2026-09-25T14:30:00Z',
    }))
    expect(result?.label).toContain('Paused: usage limit — resets')
  })

  it('returns unsatisfiable with first blocking slug', () => {
    const result = taskAttentionCause(task({
      isUnsatisfiable: true,
      blockingUpstreams: [{ slug: 'upstream-a', stage: 'cancelled' }],
    }))
    expect(result).toMatchObject({
      kind: 'unsatisfiable',
      label: 'Unsatisfiable: upstream-a cancelled',
      tone: 'warning',
    })
  })

  it('returns blocked with slug(stage) list', () => {
    const result = taskAttentionCause(task({
      isBlocked: true,
      blockingUpstreams: [
        { slug: 'upstream-a', stage: 'implementation' },
        { slug: 'upstream-b', stage: 'self_review' },
      ],
    }))
    expect(result).toMatchObject({ kind: 'blocked', tone: 'neutral' })
    expect(result?.label).toBe('Waiting for: upstream-a (implementation), upstream-b (self_review)')
  })

  it('returns null when no cause applies', () => {
    expect(taskAttentionCause(task())).toBeNull()
  })

  it('returns null for a running task with no issues', () => {
    expect(taskAttentionCause(task({ latestStageRunStatus: 'running' }))).toBeNull()
  })

  // Precedence tests
  it('permission beats plan_approval', () => {
    const result = taskAttentionCause(task({
      hasPendingPermissions: true,
      latestStageRunStatus: 'awaiting_user',
      currentStage: 'plan_review',
    }))
    expect(result?.kind).toBe('permission')
  })

  it('plan_approval beats needs_input (same status, different stage)', () => {
    // plan_review + awaiting_user = plan_approval, not needs_input
    const result = taskAttentionCause(task({
      latestStageRunStatus: 'awaiting_user',
      currentStage: 'plan_review',
    }))
    expect(result?.kind).toBe('plan_approval')
  })

  it('usage_limit beats unsatisfiable', () => {
    const result = taskAttentionCause(task({
      latestStageRunStatus: 'rate_limited',
      isUnsatisfiable: true,
      blockingUpstreams: [{ slug: 'x', stage: 'cancelled' }],
    }))
    expect(result?.kind).toBe('usage_limit')
  })

  it('unsatisfiable beats blocked', () => {
    const result = taskAttentionCause(task({
      isUnsatisfiable: true,
      isBlocked: true,
      blockingUpstreams: [{ slug: 'x', stage: 'cancelled' }],
    }))
    expect(result?.kind).toBe('unsatisfiable')
  })
})
