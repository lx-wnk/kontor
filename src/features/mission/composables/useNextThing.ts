import type { PermissionItem } from '@/composables/usePendingPermissions'
import type { Agent, PermissionRequest, PipelineTask } from '@/types'
import type { DetectedConfirm, DetectedQuestion } from '@/utils/askQuestionScreen'

/**
 * The one item mission control puts in the centre, and the reason it is first.
 *
 * The rule is about who is WAITING, not about how important the work is: a
 * pipeline stopped on a permission has an agent sitting idle, while a plan
 * waiting for approval costs nothing until a human looks. Ordering by
 * blocked-ness is what makes the answer explainable in one sentence, and an
 * order nobody can explain is one people stop trusting.
 */
export type NextKind = 'permission' | 'question' | 'plan'

export interface NextThing {
  kind: NextKind
  taskId: string
  /** The task's own words, for the plan kind and for context on both. */
  taskTitle: string
  projectName: string
  stage: string
  /** The request to resolve, for the permission kind. */
  request?: PermissionRequest
  /** The agent to answer, for the question kind. */
  pid?: number
  /** Whichever screen that agent is holding open — one of the two, never both. */
  question?: DetectedQuestion
  confirm?: DetectedConfirm
  /** What the centre puts in its headline. */
  title: string
  /** Why this one is first, shown verbatim — never a rank number. */
  why: string
}

/** Lower sorts first. Exported so a new kind has to state its place. */
export const KIND_RANK: Record<NextKind, number> = { permission: 0, question: 1, plan: 2 }

export const WHY: Record<NextKind, string> = {
  permission: 'First because an agent is stopped until you answer.',
  question: 'An agent asked you something and cannot go on until you reply.',
  plan: 'Waiting on your approval — nothing is running while it waits.',
}

/**
 * Every candidate, in the order the centre hands them over. The caller takes
 * [0] for the centre and counts the rest, so the centre can never disagree
 * with what "2 more after this" says.
 */
export function rankNextThings(items: PermissionItem[], tasks: PipelineTask[], agents: Agent[] = []): NextThing[] {
  const out: NextThing[] = []

  for (const item of items) {
    const task = tasks.find(t => t.id === item.taskId)
    for (const request of item.requests) {
      out.push({
        kind: 'permission',
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

  for (const task of tasks) {
    if (task.currentStage !== 'plan_review')
      continue
    out.push({
      kind: 'plan',
      taskId: task.id,
      taskTitle: task.title,
      projectName: '',
      stage: task.currentStage,
      title: task.title,
      why: WHY.plan,
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
  const at = n.request?.requestedAt
  if (!at)
    return Number.MAX_SAFE_INTEGER
  const t = Date.parse(at)
  return Number.isNaN(t) ? Number.MAX_SAFE_INTEGER : t
}
