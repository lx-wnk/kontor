import type { HubNote } from './composables/useObsidianGraph'
import { describe, expect, it } from 'vitest'
import { DAY_MS } from './hubGeometry'
import { HEALTH_LENSES, isStale, isUnlinked, notesInLens, STALE_AFTER_DAYS } from './hubHealth'

function note(overrides: Partial<HubNote>): HubNote {
  return { index: 0, path: 'note.md', title: 'note', mtimeMs: 0, kind: 'concept', links: [], backlinks: [], ...overrides }
}

const NOW = 1_000_000_000_000

describe('isUnlinked', () => {
  it('sessions never count, even without links', () => {
    expect(isUnlinked(note({ kind: 'sessions', links: [], backlinks: [] }))).toBe(false)
  })

  it('is false when a note has links', () => {
    expect(isUnlinked(note({ links: [1], backlinks: [] }))).toBe(false)
  })

  it('is false when a note has backlinks', () => {
    expect(isUnlinked(note({ links: [], backlinks: [2] }))).toBe(false)
  })

  it('is true for a know-how note with no links or backlinks', () => {
    expect(isUnlinked(note({ links: [], backlinks: [] }))).toBe(true)
  })
})

describe('isStale', () => {
  it('sessions never count, even when old', () => {
    expect(isStale(note({ kind: 'sessions', mtimeMs: 0 }), NOW)).toBe(false)
  })

  it('is not stale exactly at the threshold', () => {
    const mtimeMs = NOW - STALE_AFTER_DAYS * DAY_MS
    expect(isStale(note({ mtimeMs }), NOW)).toBe(false)
  })

  it('is stale just beyond the threshold', () => {
    const mtimeMs = NOW - STALE_AFTER_DAYS * DAY_MS - 1
    expect(isStale(note({ mtimeMs }), NOW)).toBe(true)
  })
})

describe('notesInLens', () => {
  const notes: HubNote[] = [
    note({ index: 0, kind: 'sessions', links: [], backlinks: [], mtimeMs: 0 }),
    note({ index: 1, kind: 'concept', links: [], backlinks: [], mtimeMs: 0 }),
    note({ index: 2, kind: 'concept', links: [1], backlinks: [], mtimeMs: NOW }),
  ]

  it('unlinked lens returns only unlinked know-how notes', () => {
    expect(notesInLens(notes, 'unlinked', NOW)).toEqual(new Set([1]))
  })

  it('stale lens returns only stale know-how notes', () => {
    expect(notesInLens(notes, 'stale', NOW)).toEqual(new Set([1]))
  })

  it('exposes both lenses', () => {
    expect(HEALTH_LENSES).toEqual(['unlinked', 'stale'])
  })
})
