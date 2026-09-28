import { describe, expect, it } from 'vitest'
import { readInput } from '../composables/useReading'

describe('readInput', () => {
  it('reads nothing from blank input', () => {
    expect(readInput('   ').kind).toBe('empty')
  })

  it('navigates on an exact view name whether or not a session runs', () => {
    expect(readInput('go to pipeline')).toMatchObject({ kind: 'navigate', view: 'pipeline' })
    expect(readInput('pipeline', true)).toMatchObject({ kind: 'navigate', view: 'pipeline' })
  })

  it('offers to start Kontor for free text when no session runs', () => {
    expect(readInput('plan phase 4')).toMatchObject({ kind: 'ask', label: 'START KONTOR' })
  })

  it('sends free text to a running session', () => {
    expect(readInput('plan phase 4', true)).toMatchObject({ kind: 'ask', label: 'SEND TO KONTOR' })
  })

  it('refuses a slash command without a session and sends it to a running one', () => {
    expect(readInput('/compact').kind).toBe('command')
    expect(readInput('/compact', true)).toMatchObject({ kind: 'ask', label: 'SEND TO KONTOR' })
  })
})
