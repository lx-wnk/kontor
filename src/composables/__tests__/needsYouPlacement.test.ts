import type { NextThing } from '@/features/mission'
import { describe, expect, it } from 'vitest'
import { DOCKED_KINDS, KIND_RANK } from '@/features/mission/composables/useNextThing'
import { needsYouPlacement, triageKeysActive, triageListFor } from '../needsYouPlacement'

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

const things = (['permission', 'plan', 'finished', 'failed'] as const).map(kind => ({ kind, key: `${kind}:s` }) as NextThing)

describe('triageListFor', () => {
  it('drops finished turns under the hub placement', () => {
    expect(triageListFor(things, needsYouPlacement({ view: 'zentrale', pageHasHub: true, error: false })).map(t => t.kind)).toEqual(['permission', 'plan', 'failed'])
  })

  it('keeps only plan reviews under the dashboard placement', () => {
    expect(triageListFor(things, needsYouPlacement({ view: 'dashboard', pageHasHub: false, error: false })).map(t => t.kind)).toEqual(['plan'])
  })

  it('keeps every item under the error placement', () => {
    expect(triageListFor(things, needsYouPlacement({ view: 'zentrale', pageHasHub: true, error: true }))).toEqual(things)
  })
})

describe('triageKeysActive', () => {
  it('is false without a mounted queue or without items', () => {
    expect(triageKeysActive(0, things)).toBe(false)
    expect(triageKeysActive(1, [])).toBe(false)
  })

  it('is true with a mounted queue and items', () => {
    expect(triageKeysActive(2, things)).toBe(true)
  })
})
