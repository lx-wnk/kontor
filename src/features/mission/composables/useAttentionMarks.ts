import type { NextThing } from './useNextThing'
import { ref, shallowRef } from 'vue'
import { createSseResource } from '@/composables/useSseResource'
import { toast } from '@/composables/useToast'

interface AttentionMark {
  key: string
  seenThrough: string
}

interface AttentionMarksResponse {
  baseline: string
  marks: AttentionMark[]
}

interface AttentionEvent {
  type: 'attention_marked'
  mark: AttentionMark
}

const marks = shallowRef<ReadonlyMap<string, string>>(new Map())
const baseline = ref<string | null>(null)

async function fetchMarks(): Promise<void> {
  try {
    const res = await fetch('/api/attention/marks')
    if (!res.ok)
      return
    const body = await res.json() as AttentionMarksResponse
    baseline.value = body.baseline
    marks.value = new Map(body.marks.map(m => [m.key, m.seenThrough]))
  }
  catch {
    // the stream's fallback poll retries
  }
}

function handleMessage(data: string): void {
  try {
    const event = JSON.parse(data) as AttentionEvent
    if (event.type === 'attention_marked')
      marks.value = new Map(marks.value).set(event.mark.key, event.mark.seenThrough)
  }
  catch {
    // ignore malformed messages
  }
}

const sse = createSseResource({
  streamUrl: '/api/attention/stream',
  fetchInitial: fetchMarks,
  onMessage: handleMessage,
  onConnected: fetchMarks,
})

export async function markSeen(key: string, since: string): Promise<void> {
  const previous = marks.value
  marks.value = new Map(previous).set(key, since)
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
    marks.value = previous
    toast.error('Could not put that away')
  }
}

export function isPutAway(t: NextThing, marked: ReadonlyMap<string, string>, base: string | null): boolean {
  const m = marked.get(t.key)
  if (m !== undefined && Date.parse(m) >= Date.parse(t.since))
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
