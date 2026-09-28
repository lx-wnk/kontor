import type { AgentStatus, PipelineStage, StageRunStatus } from '../types'

export type ChipTone = 'success' | 'warning' | 'danger' | 'info' | 'neutral' | 'accent'

export type AgentDisplayStatus = AgentStatus | 'working'

/**
 * The status the UI shows for an agent: live turn state (TurnOpen ||
 * recentOutput, server/internal/merger) overrides the time-bucketed status
 * the server sends, so the badge and any grouping stay in agreement about
 * the same agent.
 */
export function agentDisplayStatus(agent: { status: AgentStatus, working: boolean }): AgentDisplayStatus {
  return agent.working ? 'working' : agent.status
}

export function stageTone(stage: PipelineStage | string): ChipTone {
  switch (stage) {
    case 'done': return 'success'
    case 'cancelled': return 'danger'
    case 'on_hold': return 'warning'
    case 'implementation': return 'info'
    default: return 'neutral'
  }
}

export const RUN_STATUS_LABELS: Record<StageRunStatus, string> = {
  pending: 'Pending',
  running: 'Running',
  awaiting_user: 'Waiting',
  on_hold: 'On Hold',
  done: 'Done',
  failed: 'Failed',
  requeued: 'Requeued',
  rate_limited: 'Paused: Usage Limit',
  cancelled: 'Cancelled',
}

export function runStatusLabel(status: StageRunStatus): string {
  return RUN_STATUS_LABELS[status] ?? status
}

export function runStatusTone(status: StageRunStatus | string): ChipTone {
  switch (status) {
    case 'running': return 'info'
    case 'done': return 'success'
    case 'failed': return 'danger'
    case 'on_hold':
    case 'awaiting_user':
    case 'rate_limited': return 'warning'
    default: return 'neutral'
  }
}

export function agentStatusTone(status: string): ChipTone {
  switch (status) {
    case 'active': return 'success'
    case 'working': return 'info'
    case 'waiting': return 'warning'
    case 'error': return 'danger'
    case 'completed': return 'success'
    case 'finished': return 'neutral'
    default: return 'neutral'
  }
}

/** Maps an agent/badge status string to a human-readable display label (SSOT, UX-016). */
export function statusLabel(status: string): string {
  switch (status) {
    case 'active': return 'Active'
    case 'working': return 'Working'
    // 'waiting' means 30s-5min since last activity (server/internal/merger/
    // merger.go CalculateStatus) — the agent is alive and recently active. "Waiting"
    // read as "waiting for you", colliding with the needs-you band's meaning.
    case 'waiting': return 'Quiet'
    case 'idle': return 'Idle'
    case 'finished': return 'Finished'
    case 'completed': return 'Completed'
    case 'error': return 'Error'
    case 'info': return 'Info'
    default: return status
  }
}
