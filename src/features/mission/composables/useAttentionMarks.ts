import type { NextThing } from './useNextThing'
import type { Agent } from '@/types'
import { ref, shallowRef } from 'vue'
import { createSseResource } from '@/composables/useSseResource'
import { toast } from '@/composables/useToast'
import { agentOutcome, outcomeKey } from './useNextThing'

interface AttentionMark {
  key: string
  seenThrough: string
}

interface AttentionMarksResponse {
  baseline: string
  marks: AttentionMark[]
}

type AttentionEvent = { type: 'attention_marked', mark: AttentionMark } | { type: 'attention_ready' }

const marks = shallowRef<ReadonlyMap<string, string>>(new Map())
const baseline = ref<string | null>(null)

function covers(stored: string | undefined, since: string): boolean {
  return stored !== undefined && Date.parse(stored) >= Date.parse(since)
}

function applyMark(key: string, seenThrough: string): void {
  if (covers(marks.value.get(key), seenThrough))
    return
  marks.value = new Map(marks.value).set(key, seenThrough)
}

async function fetchMarks(): Promise<void> {
  try {
    const res = await fetch('/api/attention/marks')
    if (!res.ok)
      return
    const body = await res.json() as AttentionMarksResponse
    baseline.value = body.baseline
    const next = new Map(marks.value)
    for (const m of body.marks) {
      if (!covers(next.get(m.key), m.seenThrough))
        next.set(m.key, m.seenThrough)
    }
    marks.value = next
  }
  catch {
    // the next (re)connect refetches
  }
}

function handleMessage(data: string): void {
  try {
    const event = JSON.parse(data) as AttentionEvent
    if (event.type === 'attention_marked')
      applyMark(event.mark.key, event.mark.seenThrough)
  }
  catch {}
}

const sse = createSseResource({
  streamUrl: '/api/attention/stream',
  fetchInitial: fetchMarks,
  onMessage: handleMessage,
  onConnected: fetchMarks,
})

export async function markSeen(key: string, since: string): Promise<void> {
  if (covers(marks.value.get(key), since))
    return
  const previous = marks.value.get(key)
  applyMark(key, since)
  try {
    const res = await fetch('/api/attention/marks', {
      method: 'PUT',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ key, seenThrough: since }),
    })
    if (!res.ok)
      throw new Error(`HTTP ${res.status}`)
  }
  catch {
    if (marks.value.get(key) === since) {
      const next = new Map(marks.value)
      if (previous === undefined)
        next.delete(key)
      else
        next.set(key, previous)
      marks.value = next
    }
    toast.error('Could not put that away')
  }
}

export function markOutcomeSeen(agent: Agent, outcome: 'failed' | 'finished'): Promise<void> {
  if (agentOutcome(agent) !== outcome)
    return Promise.resolve()
  return markSeen(outcomeKey(outcome, agent.sessionId), agent.lastActivity)
}

export function isPutAway(t: NextThing, marked: ReadonlyMap<string, string>, base: string | null): boolean {
  if (covers(marked.get(t.key), t.since))
    return true
  if (t.kind !== 'failed' && t.kind !== 'finished')
    return false
  return base === null || Date.parse(t.since) <= Date.parse(base)
}

export function visibleNextThings(list: NextThing[], marked: ReadonlyMap<string, string>, base: string | null): NextThing[] {
  return list.filter(t => !isPutAway(t, marked, base))
}

export function useAttentionMarks() {
  return { marks, baseline, start: sse.startStream }
}
