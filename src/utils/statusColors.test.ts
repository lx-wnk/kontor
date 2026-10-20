import { describe, expect, it } from 'vitest'
import { agentDisplayStatus, agentStatusTone, runStatusTone, stageTone, statusLabel } from './statusColors'

describe('agentDisplayStatus', () => {
  it('shows a fresh agent whose turn has closed as replied, a running turn as working', () => {
    expect(agentDisplayStatus({ status: 'active', working: false })).toBe('replied')
    expect(agentDisplayStatus({ status: 'active', working: true })).toBe('working')
    expect(agentDisplayStatus({ status: 'waiting', working: false })).toBe('waiting')
  })

  it('labels and tones replied as its own state', () => {
    expect(statusLabel('replied')).toBe('Replied')
    expect(agentStatusTone('replied')).toBe('accent')
  })
})

describe('statusLabel', () => {
  it('returns human-readable labels for known statuses', () => {
    expect(statusLabel('active')).toBe('Active')
    expect(statusLabel('waiting')).toBe('Quiet')
    expect(statusLabel('idle')).toBe('Idle')
    expect(statusLabel('completed')).toBe('Completed')
    expect(statusLabel('error')).toBe('Error')
    expect(statusLabel('info')).toBe('Info')
  })

  it('returns the raw string for unknown values (pass-through)', () => {
    expect(statusLabel('unknown-status')).toBe('unknown-status')
    expect(statusLabel('')).toBe('')
    expect(statusLabel('custom')).toBe('custom')
  })
})

describe('stageTone', () => {
  it('done → success', () => expect(stageTone('done')).toBe('success'))
  it('cancelled → danger', () => expect(stageTone('cancelled')).toBe('danger'))
  it('on_hold → warning', () => expect(stageTone('on_hold')).toBe('warning'))
  it('implementation → info', () => expect(stageTone('implementation')).toBe('info'))
  it('unknown → neutral', () => expect(stageTone('some-future-stage')).toBe('neutral'))
})

describe('runStatusTone', () => {
  it('running → info', () => expect(runStatusTone('running')).toBe('info'))
  it('done → success', () => expect(runStatusTone('done')).toBe('success'))
  it('failed → danger', () => expect(runStatusTone('failed')).toBe('danger'))
  it('on_hold → warning', () => expect(runStatusTone('on_hold')).toBe('warning'))
  it('awaiting_user → warning', () => expect(runStatusTone('awaiting_user')).toBe('warning'))
  it('on_hold and awaiting_user return the same tone', () => {
    expect(runStatusTone('on_hold')).toBe(runStatusTone('awaiting_user'))
  })
  it('pending → neutral', () => expect(runStatusTone('pending')).toBe('neutral'))
  it('unknown → neutral', () => expect(runStatusTone('truly-unknown')).toBe('neutral'))
})

describe('agentStatusTone', () => {
  it('active → success', () => expect(agentStatusTone('active')).toBe('success'))
  it('completed → success', () => expect(agentStatusTone('completed')).toBe('success'))
  it('waiting → warning', () => expect(agentStatusTone('waiting')).toBe('warning'))
  it('error → danger', () => expect(agentStatusTone('error')).toBe('danger'))
  it('idle → neutral', () => expect(agentStatusTone('idle')).toBe('neutral'))
  it('finished → neutral', () => expect(agentStatusTone('finished')).toBe('neutral'))
  it('unknown → neutral', () => expect(agentStatusTone('unknown')).toBe('neutral'))
})
