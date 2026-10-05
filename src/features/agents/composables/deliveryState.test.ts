import type { TrackedMessage } from './deliveryState'
import type { OutputMessage } from '@/types'
import { describe, expect, it } from 'vitest'
import { reconcileDelivery } from './deliveryState'

const T0 = Date.parse('2026-10-02T10:00:00.000Z')

function stamp(at: number): string {
  return new Date(at).toISOString()
}

function makeMsg(overrides: Partial<TrackedMessage> = {}): TrackedMessage {
  return {
    id: 'msg-1',
    text: 'hello',
    serverText: 'hello',
    state: 'sent',
    sentAt: T0,
    ...overrides,
  }
}

function human(content: string, at: number | string | undefined): OutputMessage {
  const timestamp = typeof at === 'number' ? stamp(at) : at
  return { role: 'human', content, timestamp }
}

describe('reconcileDelivery', () => {
  it('marks a send delivered by the transcript entry that follows it', () => {
    const entry = human('hello', T0 + 1500)
    const { bubbleState } = reconcileDelivery([makeMsg()], [entry])
    expect(bubbleState.get(entry)).toBe('delivered')
  })

  it('also delivers a send whose acknowledgement has not arrived yet', () => {
    const entry = human('hello', T0 + 200)
    const { bubbleState } = reconcileDelivery([makeMsg({ state: 'sending' })], [entry])
    expect(bubbleState.get(entry)).toBe('delivered')
  })

  it('matches the sanitized server text, not the typed text', () => {
    const entry = human('hello world', T0 + 200)
    const { bubbleState } = reconcileDelivery([makeMsg({ text: 'hello  world ', serverText: 'hello world' })], [entry])
    expect(bubbleState.get(entry)).toBe('delivered')
  })

  it('keeps a re-send at sent while only an earlier identical message exists', () => {
    const earlier = human('yes', T0 - 60_000)
    const echo = human('yes', T0)
    const { bubbleState, echoed } = reconcileDelivery(
      [makeMsg({ text: 'yes', serverText: 'yes' })],
      [earlier],
      [echo],
    )
    expect(bubbleState.get(echo)).toBe('sent')
    expect(echoed.has(echo)).toBe(false)
  })

  it('does not show the new badge on an older bubble with the same text', () => {
    const earlier = human('yes', T0 - 60_000)
    const fresh = human('yes', T0 + 500)
    const { bubbleState } = reconcileDelivery(
      [makeMsg({ text: 'yes', serverText: 'yes' })],
      [earlier, fresh],
    )
    expect(bubbleState.has(earlier)).toBe(false)
    expect(bubbleState.get(fresh)).toBe('delivered')
  })

  it('accepts an entry stamped up to a second before the send, no more', () => {
    const within = human('hello', T0 - 900)
    const outside = human('hello', T0 - 1100)
    expect(reconcileDelivery([makeMsg()], [within]).bubbleState.get(within)).toBe('delivered')
    expect(reconcileDelivery([makeMsg()], [outside]).bubbleState.has(outside)).toBe(false)
  })

  it('gives two rapid identical sends one entry each', () => {
    const tracked = [
      makeMsg({ id: 'a', sentAt: T0 }),
      makeMsg({ id: 'b', sentAt: T0 + 200 }),
    ]
    const echoA = human('hello', T0)
    const echoB = human('hello', T0 + 200)
    const first = human('hello', T0 + 1000)
    const second = human('hello', T0 + 1500)
    const { bubbleState, echoed } = reconcileDelivery(tracked, [first, second], [echoA, echoB])
    expect(bubbleState.get(first)).toBe('delivered')
    expect(bubbleState.get(second)).toBe('delivered')
    expect(echoed).toEqual(new Set([echoA, echoB]))
  })

  it('delivers only the first of two identical sends when one entry exists', () => {
    const tracked = [
      makeMsg({ id: 'a', sentAt: T0 }),
      makeMsg({ id: 'b', sentAt: T0 + 200 }),
    ]
    const echoA = human('hello', T0)
    const echoB = human('hello', T0 + 200)
    const entry = human('hello', T0 + 1000)
    const { bubbleState, echoed } = reconcileDelivery(tracked, [entry], [echoA, echoB])
    expect(bubbleState.get(entry)).toBe('delivered')
    expect(echoed).toEqual(new Set([echoA]))
    expect(bubbleState.get(echoB)).toBe('sent')
  })

  it('claims sends oldest first regardless of array order', () => {
    const newer = makeMsg({ id: 'newer', sentAt: T0 + 200 })
    const older = makeMsg({ id: 'older', sentAt: T0 })
    const echoOlder = human('hello', T0)
    const echoNewer = human('hello', T0 + 200)
    const { echoed } = reconcileDelivery([newer, older], [human('hello', T0 + 1000)], [echoNewer, echoOlder])
    expect(echoed).toEqual(new Set([echoOlder]))
  })

  it('keeps the local echo visible until its own entry arrives', () => {
    const echo = human('hello', T0)
    const tracked = [makeMsg()]
    expect(reconcileDelivery(tracked, [], [echo]).echoed.size).toBe(0)
    expect(reconcileDelivery(tracked, [human('hello', T0 + 300)], [echo]).echoed).toEqual(new Set([echo]))
  })

  it('shows the in-flight state on the echo of a send', () => {
    const echo = human('hello', T0)
    const { bubbleState } = reconcileDelivery([makeMsg({ state: 'sending' })], [], [echo])
    expect(bubbleState.get(echo)).toBe('sending')
  })

  it('pairs the next send with its own echo when an earlier identical send failed', () => {
    const failedEcho = human('hello', T0)
    const sentEcho = human('hello', T0 + 400)
    const tracked = [makeMsg({ sentAt: T0 + 400 })]
    const { echoed } = reconcileDelivery(tracked, [human('hello', T0 + 900)], [failedEcho, sentEcho])
    expect(echoed).toEqual(new Set([sentEcho]))
  })

  it('hides the echo of a queued send once the drained message shows up in the transcript', () => {
    const echo = human('hello', T0)
    const entry = human('hello', T0 + 60_000)
    const { bubbleState, echoed } = reconcileDelivery([makeMsg({ state: 'queued' })], [entry], [echo])
    expect(bubbleState.get(entry)).toBe('delivered')
    expect(echoed).toEqual(new Set([echo]))
  })

  it('shows the queued state on the echo until the drained message arrives', () => {
    const echo = human('hello', T0)
    const { bubbleState, echoed } = reconcileDelivery([makeMsg({ state: 'queued' })], [], [echo])
    expect(bubbleState.get(echo)).toBe('queued')
    expect(echoed.size).toBe(0)
  })

  it('keeps the echo of a failed send visible with the failed state', () => {
    const echo = human('hello', T0)
    const { bubbleState, echoed } = reconcileDelivery([makeMsg({ state: 'failed' })], [], [echo])
    expect(bubbleState.get(echo)).toBe('failed')
    expect(echoed.size).toBe(0)
  })

  it('never promotes a failed send, even when an identical entry follows it', () => {
    const echo = human('hello', T0)
    const entry = human('hello', T0 + 500)
    const { bubbleState, echoed } = reconcileDelivery([makeMsg({ state: 'failed' })], [entry], [echo])
    expect(bubbleState.get(echo)).toBe('failed')
    expect(bubbleState.has(entry)).toBe(false)
    expect(echoed.size).toBe(0)
  })

  it('lets a later identical send claim the entry a failed send must not take', () => {
    const failedEcho = human('hello', T0)
    const sentEcho = human('hello', T0 + 5000)
    const entry = human('hello', T0 + 6000)
    const tracked = [
      makeMsg({ id: 'failed', state: 'failed', sentAt: T0 }),
      makeMsg({ id: 'sent', state: 'sent', sentAt: T0 + 5000 }),
    ]
    const { bubbleState, echoed } = reconcileDelivery(tracked, [entry], [failedEcho, sentEcho])
    expect(bubbleState.get(entry)).toBe('delivered')
    expect(bubbleState.get(failedEcho)).toBe('failed')
    expect(echoed).toEqual(new Set([sentEcho]))
  })

  it.each([
    ['missing', undefined],
    ['unparsable', 'not a date'],
  ])('never delivers on an entry with a %s timestamp', (_label, timestamp) => {
    const entry = human('hello', timestamp)
    expect(reconcileDelivery([makeMsg()], [entry]).bubbleState.has(entry)).toBe(false)
  })

  it('only counts human entries', () => {
    const reply: OutputMessage = { role: 'assistant', content: 'hello', timestamp: stamp(T0 + 100) }
    expect(reconcileDelivery([makeMsg()], [reply]).bubbleState.size).toBe(0)
  })

  it('keeps an already delivered send delivered without a transcript entry', () => {
    const echo = human('hello', T0)
    const { bubbleState } = reconcileDelivery([makeMsg({ state: 'delivered' })], [], [echo])
    expect(bubbleState.get(echo)).toBe('delivered')
  })

  it('handles an empty tracked array', () => {
    const { bubbleState, echoed } = reconcileDelivery([], [human('anything', T0)], [human('anything', T0)])
    expect(bubbleState.size).toBe(0)
    expect(echoed.size).toBe(0)
  })
})
