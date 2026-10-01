import type { PipelineTask } from '@/types'
import type { ChipTone } from '@/utils/statusColors'

export type CauseKind
  = | 'permission'
    | 'plan_approval'
    | 'needs_input'
    | 'usage_limit'
    | 'unsatisfiable'
    | 'blocked'

export interface AttentionCause {
  kind: CauseKind
  label: string
  tone: ChipTone
  title?: string
}

/**
 * Determine the single most important attention cause for a pipeline task.
 * Fixed precedence: permission > plan_approval > needs_input > usage_limit > unsatisfiable > blocked.
 * Returns null when no cause applies.
 */
export function taskAttentionCause(task: PipelineTask): AttentionCause | null {
  // 1. Real pending permission request — agent is stopped.
  if (task.hasPendingPermissions) {
    return { kind: 'permission', label: 'Needs permission', tone: 'warning' }
  }

  // 2. Plan awaiting human approval.
  if (task.latestStageRunStatus === 'awaiting_user' && task.currentStage === 'plan_review') {
    return { kind: 'plan_approval', label: 'Plan awaiting approval', tone: 'info' }
  }

  // 3. Any other awaiting_user — generic "needs input".
  if (task.latestStageRunStatus === 'awaiting_user') {
    return {
      kind: 'needs_input',
      label: 'Needs input',
      tone: 'warning',
      title: task.waitReason ?? undefined,
    }
  }

  // 4. Rate limited — usage limit hit.
  if (task.latestStageRunStatus === 'rate_limited') {
    let label = 'Paused: usage limit'
    if (task.nextRetryAt) {
      const d = new Date(task.nextRetryAt)
      if (!Number.isNaN(d.getTime())) {
        label = `Paused: usage limit — resets ${d.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' })}`
      }
    }
    return { kind: 'usage_limit', label, tone: 'warning' }
  }

  // 5. Unsatisfiable dependency — upstream is terminal but wrong stage.
  if (task.isUnsatisfiable && task.blockingUpstreams?.length) {
    return {
      kind: 'unsatisfiable',
      label: `Unsatisfiable: ${task.blockingUpstreams[0].slug} cancelled`,
      tone: 'warning',
    }
  }

  // 6. Blocked by dependency — upstream still in progress.
  if (task.isBlocked && task.blockingUpstreams?.length) {
    const parts = task.blockingUpstreams.map(u => `${u.slug} (${u.stage})`)
    return {
      kind: 'blocked',
      label: `Waiting for: ${parts.join(', ')}`,
      tone: 'neutral',
    }
  }

  return null
}
