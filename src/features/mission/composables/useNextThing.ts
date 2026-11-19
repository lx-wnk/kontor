import type { PermissionItem } from '@/composables/usePendingPermissions'
import type { PendingCapabilityDecision } from '@/sdk.generated'
import type { Agent, PermissionRequest, PipelineTask } from '@/types'
import type { DetectedConfirm, DetectedQuestion } from '@/utils/askQuestionScreen'
import { attentionFor } from '@/utils/attention'
import { formatErrorState } from '@/utils/format'
import { taskAttentionCause } from '@/utils/taskAttentionCause'

/**
 * The one item mission control puts in the centre, and the reason it is first.
 *
 * The rule is about who is WAITING, not about how important the work is: a
 * pipeline stopped on a permission has an agent sitting idle, while a plan
 * waiting for approval costs nothing until a human looks. Ordering by
 * blocked-ness is what makes the answer explainable in one sentence, and an
 * order nobody can explain is one people stop trusting.
 */
export type NextKind = 'permission' | 'question' | 'dialog' | 'capability' | 'plan' | 'failed' | 'finished'

export interface NextThing {
  kind: NextKind
  /** Stable identity of this item, the key the server stores marks under. */
  key: string
  /** ISO time of the event behind the item; a mark hides it only while seenThrough >= since. */
  since: string
  taskId: string
  /** The task's own words, for the plan kind and for context on both. */
  taskTitle: string
  projectName: string
  stage: string
  /** The request to resolve, for the permission kind. */
  request?: PermissionRequest
  /** The agent to answer, for the question and dialog kinds. */
  pid?: number
  sessionId?: string
  /** For the dialog kind: whether the dashboard terminal can attach (pty broker, not tmux). */
  terminalAttachable?: boolean
  /** Whichever screen that agent is holding open — one of the two, never both. */
  question?: DetectedQuestion
  confirm?: DetectedConfirm
  decision?: PendingCapabilityDecision
  /** What the centre puts in its headline. */
  title?: string
  /** Why this one is first, shown verbatim — never a rank number. */
  why: string
}

/** Lower sorts first. Exported so a new kind has to state its place. */
export const KIND_RANK: Record<NextKind, number> = { permission: 0, question: 1, dialog: 2, capability: 3, plan: 4, failed: 5, finished: 6 }

export const WHY: Record<NextKind, string> = {
  permission: 'First because an agent is stopped until you answer.',
  question: 'An agent asked you something and cannot go on until you reply.',
  dialog: 'A session holds a picker open in its terminal and waits until you answer it there.',
  capability: 'A run is paused until you allow or deny this capability.',
  plan: 'Waiting on your approval — nothing is running while it waits.',
  failed: 'The run stopped with an error. Look at it or send the agent a new instruction.',
  finished: 'The agent finished its turn and waits for your next instruction.',
}

/**
 * Every candidate, in the order the centre hands them over. The caller takes
 * [0] for the centre and counts the rest, so the centre can never disagree
 * with what "2 more after this" says.
 */
export function agentOutcome(agent: Agent): 'failed' | 'finished' | null {
  if (agent.status === 'finished')
    return 'finished'
  const kind = attentionFor(agent, null)?.kind
  if (kind === 'error')
    return 'failed'
  return kind === 'yourTurn' ? 'finished' : null
}

export function rankNextThings(items: PermissionItem[], tasks: PipelineTask[], agents: Agent[] = [], decisions: PendingCapabilityDecision[] = []): NextThing[] {
  const out: NextThing[] = []

  for (const item of items) {
    const task = tasks.find(t => t.id === item.taskId)
    for (const request of item.requests) {
      out.push({
        kind: 'permission',
        key: `permission:${request.id}`,
        since: request.requestedAt,
        taskId: item.taskId,
        taskTitle: item.title,
        projectName: item.projectName,
        stage: task?.currentStage ?? '',
        request,
        title: request.pattern ? `${request.tool}(${request.pattern})` : request.tool,
        why: WHY.permission,
      })
    }
  }

  // An agent holding a question on its terminal is as stopped as one holding a
  // permission — it ranks below only because a permission blocks a tool call
  // already in flight. Both screens count: a multi-question flow ends on the
  // review/submit screen, and leaving that out would let the centre fall silent
  // at the last step of the very flow it surfaced.
  for (const agent of agents) {
    const screen = agent.pendingQuestion ?? agent.pendingConfirm
    if (!screen)
      continue
    out.push({
      kind: 'question',
      key: `question:${agent.sessionId}`,
      since: agent.lastActivity,
      sessionId: agent.sessionId,
      taskId: agent.pipelineTaskId ?? '',
      taskTitle: agent.projectName,
      projectName: agent.projectName,
      stage: '',
      pid: agent.pid,
      question: agent.pendingQuestion ?? undefined,
      confirm: agent.pendingConfirm ?? undefined,
      title: agent.pendingQuestion?.question ?? 'Ready to submit?',
      why: WHY.question,
    })
  }

  for (const agent of agents) {
    if (!agent.awaitingTerminalDialog)
      continue
    out.push({
      kind: 'dialog',
      key: `dialog:${agent.sessionId}`,
      since: agent.lastActivity,
      sessionId: agent.sessionId,
      taskId: agent.pipelineTaskId ?? '',
      taskTitle: agent.projectName,
      projectName: agent.projectName,
      stage: '',
      pid: agent.pid,
      terminalAttachable: agent.terminalAttachable ?? false,
      title: 'A dialog is open in this session\'s terminal',
      why: WHY.dialog,
    })
  }

  for (const decision of decisions) {
    out.push({
      kind: 'capability',
      key: `capability:${decision.id}`,
      since: decision.requestedAt,
      taskId: '',
      taskTitle: '',
      projectName: '',
      stage: '',
      decision,
      why: WHY.capability,
    })
  }

  for (const task of tasks) {
    if (taskAttentionCause(task)?.kind !== 'plan_approval')
      continue
    out.push({
      kind: 'plan',
      key: `plan:${task.id}`,
      since: task.updatedAt,
      taskId: task.id,
      taskTitle: task.title,
      projectName: '',
      stage: task.currentStage,
      title: task.title,
      why: WHY.plan,
    })
  }

  for (const agent of agents) {
    const outcome = agentOutcome(agent)
    if (!outcome)
      continue
    out.push({
      kind: outcome,
      key: `${outcome}:${agent.sessionId}`,
      since: agent.lastActivity,
      taskId: agent.pipelineTaskId ?? '',
      taskTitle: agent.projectName,
      projectName: agent.projectName,
      stage: '',
      pid: agent.pid,
      sessionId: agent.sessionId,
      title: outcome === 'failed' ? (agent.errorState ? formatErrorState(agent.errorState) : 'Run failed') : (agent.lastOutput ?? 'Finished'),
      why: WHY[outcome],
    })
  }

  return out.sort((a, b) => {
    const byKind = KIND_RANK[a.kind] - KIND_RANK[b.kind]
    // Same kind: the longest wait first, so nothing starves behind a steady
    // trickle of newer requests.
    return byKind !== 0 ? byKind : waitedSince(a) - waitedSince(b)
  })
}

function waitedSince(n: NextThing): number {
  const at = n.request?.requestedAt ?? n.decision?.requestedAt
  if (!at)
    return Number.MAX_SAFE_INTEGER
  const t = Date.parse(at)
  return Number.isNaN(t) ? Number.MAX_SAFE_INTEGER : t
}
