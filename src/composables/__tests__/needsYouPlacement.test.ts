import { describe, expect, it } from 'vitest'
import { KIND_RANK } from '@/features/mission/composables/useNextThing'
import { DOCKED_KINDS, needsYouPlacement } from '../needsYouPlacement'

describe('needsYouPlacement', () => {
  it('docks into the hub on a workspace page that holds one, without finished turns', () => {
    expect(needsYouPlacement({ view: 'zentrale', pageHasHub: true, error: false })).toEqual({ strip: false, kinds: DOCKED_KINDS })
    expect(needsYouPlacement({ view: 'page:p-morning', pageHasHub: true, error: false })).toEqual({ strip: false, kinds: DOCKED_KINDS })
  })

  it('lists every kind but finished as docked', () => {
    expect(DOCKED_KINDS).not.toContain('finished')
    expect([...DOCKED_KINDS].sort()).toEqual(Object.keys(KIND_RANK).filter(k => k !== 'finished').sort())
  })

  it('is a strip on a workspace page without a hub', () => {
    expect(needsYouPlacement({ view: 'page:p-morning', pageHasHub: false, error: false })).toEqual({ strip: true })
    expect(needsYouPlacement({ view: 'zentrale', pageHasHub: false, error: false })).toEqual({ strip: true })
  })

  it('on the dashboard is a strip of only the plan reviews its triage band lacks', () => {
    expect(needsYouPlacement({ view: 'dashboard', pageHasHub: false, error: false })).toEqual({ strip: true, kinds: ['plan'] })
  })

  it('is a full strip when the error line replaces the page, hub or triage band alike', () => {
    expect(needsYouPlacement({ view: 'zentrale', pageHasHub: true, error: true })).toEqual({ strip: true })
    expect(needsYouPlacement({ view: 'dashboard', pageHasHub: false, error: true })).toEqual({ strip: true })
  })

  it('is a strip on every other core view', () => {
    for (const view of ['cloud', 'workflows', 'pipeline', 'cost', 'schedules', 'eval'] as const)
      expect(needsYouPlacement({ view, pageHasHub: false, error: false })).toEqual({ strip: true })
  })
})
