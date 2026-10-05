import type { OutputMessage } from '@/types'

// sending = fetch in flight, sent = server acknowledged (HTTP 200), queued = stored offline for the service worker to drain,
// delivered = its own entry seen in the JSONL transcript, failed = rejected or unstorable: never delivered, never auto-promoted
export type DeliveryState = 'sending' | 'sent' | 'queued' | 'delivered' | 'failed'

export interface TrackedMessage {
  id: string
  text: string
  serverText: string
  state: DeliveryState
  sentAt: number
}

export interface DeliveryReconciliation {
  // Delivery state of the send behind each human bubble, transcript entry or local echo alike. Bubbles no send accounts for are absent.
  bubbleState: Map<OutputMessage, DeliveryState>
  // Local echoes whose own transcript entry has arrived and which can be hidden.
  echoed: Set<OutputMessage>
}

// Browser and server share one clock (127.0.0.1 only); this absorbs rounding between the two stamps, not skew.
const CLOCK_TOLERANCE_MS = 1000

function epochMs(timestamp: string | undefined): number {
  return timestamp ? Date.parse(timestamp) : Number.NaN
}

// Oldest send first, each takes the best-ranked unclaimed candidate with its text; no candidate is claimed twice.
// `rank` returns null when a candidate cannot belong to the send, lower wins otherwise. NaN timestamps never rank.
function claim(
  tracked: TrackedMessage[],
  candidates: OutputMessage[],
  textOf: (msg: TrackedMessage) => string,
  rank: (at: number, sentAt: number) => number | null,
): Map<string, OutputMessage> {
  const claimed = new Map<string, OutputMessage>()
  const taken = new Set<OutputMessage>()
  for (const msg of [...tracked].sort((a, b) => a.sentAt - b.sentAt)) {
    let best: OutputMessage | undefined
    let bestRank = Infinity
    for (const candidate of candidates) {
      if (taken.has(candidate) || candidate.content !== textOf(msg))
        continue
      const r = rank(epochMs(candidate.timestamp), msg.sentAt)
      if (r !== null && r < bestRank) {
        best = candidate
        bestRank = r
      }
    }
    if (best) {
      claimed.set(msg.id, best)
      taken.add(best)
    }
  }
  return claimed
}

const humanOnly = (messages: OutputMessage[]) => messages.filter(m => m.role === 'human')

export function reconcileDelivery(
  tracked: TrackedMessage[],
  transcript: OutputMessage[],
  echoes: OutputMessage[] = [],
): DeliveryReconciliation {
  // The transcript carries the server's sanitized text, stamped when Claude Code wrote it: never before the send.
  // A failed send wrote nothing, so an identical entry belongs to someone else.
  const seen = claim(tracked.filter(m => m.state !== 'failed'), humanOnly(transcript), m => m.serverText, (at, sentAt) =>
    at >= sentAt - CLOCK_TOLERANCE_MS ? at : null)
  // Echo and tracked message are created in one call, so the nearest stamp is the pair.
  const echoOf = claim(tracked, humanOnly(echoes), m => m.text, (at, sentAt) => {
    const gap = Math.abs(at - sentAt)
    return gap <= CLOCK_TOLERANCE_MS ? gap : null
  })

  const bubbleState = new Map<OutputMessage, DeliveryState>()
  const echoed = new Set<OutputMessage>()
  for (const msg of tracked) {
    const entry = seen.get(msg.id)
    const state: DeliveryState = entry ? 'delivered' : msg.state
    if (entry)
      bubbleState.set(entry, state)
    const echo = echoOf.get(msg.id)
    if (echo) {
      bubbleState.set(echo, state)
      if (entry)
        echoed.add(echo)
    }
  }
  return { bubbleState, echoed }
}
