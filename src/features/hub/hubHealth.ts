import type { HubNote } from './composables/useObsidianGraph'
import { DAY_MS, SESSIONS_KIND } from './hubGeometry'

export const STALE_AFTER_DAYS = 90

export function isKnowHow(note: HubNote): boolean {
  return note.kind !== SESSIONS_KIND
}

export function isUnlinked(note: HubNote): boolean {
  return isKnowHow(note) && note.links.length + note.backlinks.length === 0
}

export function isStale(note: HubNote, nowMs: number, days = STALE_AFTER_DAYS): boolean {
  return isKnowHow(note) && nowMs - note.mtimeMs > days * DAY_MS
}

export type HealthLens = 'unlinked' | 'stale'

export const HEALTH_LENSES: readonly HealthLens[] = ['unlinked', 'stale']

export function notesInLens(notes: readonly HubNote[], lens: HealthLens, nowMs: number): Set<number> {
  const predicate = lens === 'unlinked' ? isUnlinked : (note: HubNote) => isStale(note, nowMs)
  const indices = new Set<number>()
  for (const note of notes) {
    if (predicate(note))
      indices.add(note.index)
  }
  return indices
}
