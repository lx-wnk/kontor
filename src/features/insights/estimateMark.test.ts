import { describe, expect, it } from 'vitest'
import { estimateMark } from './estimateMark'

describe('estimateMark', () => {
  it('marks estimates with a prefix and a title', () => {
    const m = estimateMark('estimate')
    expect(m.prefix).toBe('≈')
    expect(m.title).toContain('estimate')
  })

  it('marks an unknown model and names it', () => {
    const m = estimateMark('cost_state', true)
    expect(m.prefix).toBe('≈')
    expect(m.title).toContain('model')
  })

  it('leaves recorded values unmarked', () => {
    expect(estimateMark('cost_state')).toEqual({ prefix: '', title: '' })
  })
})
