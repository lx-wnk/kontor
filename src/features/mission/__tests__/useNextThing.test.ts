import type { PermissionItem } from '@/composables/usePendingPermissions'
import type { PendingCapabilityDecision } from '@/sdk.generated'
import type { Agent, PermissionRequest, PipelineTask } from '@/types'
import { describe, expect, it } from 'vitest'
import { KIND_RANK, rankNextThings, WHY } from '../composables/useNextThing'

function task(id: string, over: Partial<PipelineTask> = {}): PipelineTask {
  return { id, slug: id, title: `task ${id}`, currentStage: 'implementation', cwd: '/repo', ...over } as PipelineTask
}

function req(id: string, at: string, over: Partial<PermissionRequest> = {}): PermissionRequest {
  return { id, stageRunId: 'run', tool: 'Bash', pattern: 'task lint', requestedAt: at, resolvedAt: null, outcome: null, ...over } as PermissionRequest
}

function item(taskId: string, requests: PermissionRequest[]): PermissionItem {
  return { taskId, title: `task ${taskId}`, projectName: 'Dashboard', routineId: null, requests }
}

function agent(pid: number, over: Partial<Agent> = {}): Agent {
  return {
    pid,
    sessionId: `s-${pid}`,
    projectName: 'Dashboard',
    pendingQuestion: null,
    pendingConfirm: null,
    ...over,
  } as unknown as Agent
}

const colourQuestion = {
  header: 'Colour',
  question: 'Which colour should the report use?',
  multiSelect: false,
  options: [{ index: 1, label: 'Red', description: '' }, { index: 2, label: 'Blue', description: '' }],
  typeSomethingIndex: 3,
  chatAboutIndex: 4,
}

describe('rankNextThings', () => {
  // The whole point of the centre: a stopped agent outranks a decision that
  // nothing is waiting on.
  it('puts a blocked permission ahead of a plan waiting for approval', () => {
    const out = rankNextThings(
      [item('a', [req('r1', '2026-09-18T12:00:00Z')])],
      [task('a'), task('b', { currentStage: 'plan_review' })],
    )
    expect(out.map(n => n.kind)).toEqual(['permission', 'plan'])
    expect(out[0].taskId).toBe('a')
  })

  // Without this a trickle of new requests starves the oldest one.
  it('serves the longest wait first within one kind', () => {
    const out = rankNextThings(
      [item('a', [req('new', '2026-09-18T12:00:00Z')]), item('b', [req('old', '2026-09-18T09:00:00Z')])],
      [task('a'), task('b')],
    )
    expect(out.map(n => n.request?.id)).toEqual(['old', 'new'])
  })

  it('names the request by tool and pattern, which is the headline', () => {
    const out = rankNextThings([item('a', [req('r1', '2026-09-18T12:00:00Z')])], [task('a')])
    expect(out[0].title).toBe('Bash(task lint)')
    expect(out[0].why).toContain('stopped until you answer')
  })

  // The centre's empty state promises agents will interrupt there. An agent
  // holding a question on its terminal was not surfaced at all, so the promise
  // was false exactly when it mattered.
  it('surfaces an agent that is waiting on an answer', () => {
    const out = rankNextThings([], [], [agent(4711, { pendingQuestion: colourQuestion as never })])
    expect(out.map(n => n.kind)).toEqual(['question'])
    expect(out[0].pid).toBe(4711)
    expect(out[0].title).toBe('Which colour should the report use?')
    expect(out[0].why).toContain('cannot go on until you reply')
  })

  // A multi-question flow ends on the review/submit screen. Leaving it out
  // would let the centre fall silent at the last step of the flow it surfaced.
  it('surfaces the review screen that closes a question flow', () => {
    const confirm = { header: 'Ready to submit?', options: [{ index: 1, label: 'Submit', description: '' }] }
    const out = rankNextThings([], [], [agent(4712, { pendingConfirm: confirm as never })])
    expect(out.map(n => n.kind)).toEqual(['question'])
    expect(out[0].confirm).toBeTruthy()
    expect(out[0].question).toBeUndefined()
  })

  it('leaves an agent with nothing on screen out', () => {
    expect(rankNextThings([], [], [agent(4713)])).toEqual([])
  })

  // A permission blocks a tool call already in flight; a question blocks the
  // turn around it; a plan blocks nothing until a human looks.
  it('orders permission, then question, then plan', () => {
    const out = rankNextThings(
      [item('a', [req('r1', '2026-09-18T12:00:00Z')])],
      [task('a'), task('b', { currentStage: 'plan_review' })],
      [agent(4711, { pendingQuestion: colourQuestion as never })],
    )
    expect(out.map(n => n.kind)).toEqual(['permission', 'question', 'plan'])
  })

  it('is empty when nothing is blocked and nothing waits for approval', () => {
    expect(rankNextThings([], [task('a'), task('b')])).toHaveLength(0)
  })

  // A kind that forgets its place sorts as undefined and lands anywhere.
  it('gives every kind a distinct place in the order', () => {
    const ranks = Object.values(KIND_RANK)
    expect(new Set(ranks).size).toBe(ranks.length)
  })

  it('ranks a pending capability decision after questions and before plans', () => {
    const decision: PendingCapabilityDecision = { id: 'd1', capability: 'net.fetch', value: 'api.github.com', context: 'routine:nightly', reason: 'not granted', requestedAt: '2026-09-22T10:00:00Z' }
    const planTask = task('t-plan', { currentStage: 'plan_review' })
    const ranked = rankNextThings([], [planTask], [], [decision])
    expect(ranked.map(n => n.kind)).toEqual(['capability', 'plan'])
    expect(ranked[0]).toMatchObject({ kind: 'capability', decision, why: WHY.capability })
  })

  // Pushed newest-first, capability items still serve the longest wait first, same as permissions.
  it('ranks several capability decisions oldest-first regardless of push order', () => {
    const newer: PendingCapabilityDecision = { id: 'd-new', capability: 'net.fetch', value: 'api.github.com', context: 'routine:nightly', reason: 'not granted', requestedAt: '2026-09-22T12:00:00Z' }
    const older: PendingCapabilityDecision = { id: 'd-old', capability: 'fs.write', value: '/tmp/out', context: 'routine:nightly', reason: 'not granted', requestedAt: '2026-09-22T09:00:00Z' }
    const ranked = rankNextThings([], [], [], [newer, older])
    expect(ranked.map(n => n.decision?.id)).toEqual(['d-old', 'd-new'])
  })
})
