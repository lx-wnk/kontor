import type { TrackedMessage } from './deliveryState'
import { describe, expect, it } from 'vitest'
import { reconcileDelivery } from './deliveryState'

function makeMsg(overrides: Partial<TrackedMessage> = {}): TrackedMessage {
  return {
    id: 'msg-1',
    text: 'hello',
    serverText: 'hello',
    state: 'sending',
    sentAt: Date.now(),
    ...overrides,
  }
}

describe('reconcileDelivery', () => {
  it('transitions sent to delivered when serverText found in transcript', () => {
    const tracked = [makeMsg({ state: 'sent', serverText: 'hello world' })]
    const result = reconcileDelivery(tracked, ['hello world'])
    expect(result[0].state).toBe('delivered')
  })

  it('transitions sending to delivered when the transcript already shows the message', () => {
    const tracked = [makeMsg({ state: 'sending', serverText: 'hello' })]
    const result = reconcileDelivery(tracked, ['hello'])
    expect(result[0].state).toBe('delivered')
  })

  it('keeps delivered messages unchanged', () => {
    const tracked = [makeMsg({ state: 'delivered' })]
    const result = reconcileDelivery(tracked, [])
    expect(result[0].state).toBe('delivered')
  })

  it('keeps sent messages as sent when no transcript match', () => {
    const tracked = [makeMsg({ state: 'sent', serverText: 'hello' })]
    const result = reconcileDelivery(tracked, ['something else'])
    expect(result[0].state).toBe('sent')
  })

  it('returns new array without mutating input', () => {
    const tracked = [makeMsg({ state: 'sent', serverText: 'match' })]
    const result = reconcileDelivery(tracked, ['match'])
    expect(result).not.toBe(tracked)
    expect(tracked[0].state).toBe('sent')
  })

  it('handles multiple messages independently', () => {
    const tracked = [
      makeMsg({ id: 'a', state: 'sent', serverText: 'first' }),
      makeMsg({ id: 'b', state: 'sent', serverText: 'second' }),
    ]
    const result = reconcileDelivery(tracked, ['first'])
    expect(result[0].state).toBe('delivered')
    expect(result[1].state).toBe('sent')
  })

  it('handles empty tracked array', () => {
    expect(reconcileDelivery([], ['anything'])).toEqual([])
  })
})
