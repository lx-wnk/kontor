import type { ActiveView } from './useViewState'
import type { NextKind, NextThing } from '@/features/mission'
import { DOCKED_KINDS } from '@/features/mission'

export interface NeedsYouPlacement {
  strip: boolean
  kinds?: readonly NextKind[]
}

// error: the error line replaces the page, and with it the hub or the triage band.
// dashboard: its triage band shows permissions and questions, not plan reviews.
// pageHasHub: the hub docks the queue itself
export function needsYouPlacement({ view, pageHasHub, error }: { view: ActiveView, pageHasHub: boolean, error: boolean }): NeedsYouPlacement {
  if (error)
    return { strip: true }
  if (view === 'dashboard')
    return { strip: true, kinds: ['plan'] }
  return pageHasHub ? { strip: false, kinds: DOCKED_KINDS } : { strip: true }
}

// Exactly the list the queue renders on this view, so a key never acts on a card that is not on screen.
export function triageListFor(needsYou: readonly NextThing[], placement: NeedsYouPlacement): NextThing[] {
  const kinds = placement.kinds
  return kinds ? needsYou.filter(t => kinds.includes(t.kind)) : [...needsYou]
}

export function triageKeysActive(queueCount: number, list: readonly NextThing[]): boolean {
  return queueCount > 0 && list.length > 0
}
