import { beforeEach, describe, expect, it, vi } from 'vitest'
import { presetHours, presetRange } from './insightsApi'
import { INSIGHTS_TAB_KEY, readInsightsTab, writeInsightsTab } from './insightsTab'

const store: Record<string, string> = {}
const ls = {
  getItem: (k: string) => store[k] ?? null,
  setItem: (k: string, v: string) => { store[k] = v },
  removeItem: (k: string) => { delete store[k] },
}

describe('insightsTab', () => {
  beforeEach(() => {
    for (const k of Object.keys(store)) delete store[k]
    vi.stubGlobal('localStorage', ls)
  })

  it('defaults to overview and rejects an unknown stored value', () => {
    expect(readInsightsTab()).toBe('overview')
    store[INSIGHTS_TAB_KEY] = 'nope'
    expect(readInsightsTab()).toBe('overview')
  })

  it('round-trips a tab', () => {
    writeInsightsTab('quality')
    expect(readInsightsTab()).toBe('quality')
  })

  it('survives throwing storage', () => {
    vi.stubGlobal('localStorage', {
      getItem: () => { throw new Error('denied') },
      setItem: () => { throw new Error('denied') },
    })
    expect(readInsightsTab()).toBe('overview')
    expect(() => writeInsightsTab('cost')).not.toThrow()
  })
})

describe('presetRange / presetHours', () => {
  it('all has no from', () => {
    expect(presetRange('all', new Date('2026-10-29'))).toEqual({ to: '2026-10-29' })
  })

  it('7d reaches back seven days', () => {
    expect(presetRange('7d', new Date('2026-10-29'))).toEqual({ from: '2026-10-22', to: '2026-10-29' })
  })

  it('maps presets to eval hours', () => {
    expect(presetHours('7d')).toBe(168)
    expect(presetHours('30d')).toBe(720)
    expect(presetHours('90d')).toBe(2160)
    expect(presetHours('all')).toBe(8760)
  })
})
