import type { ActiveView } from './useViewState'
import type { NextKind } from '@/features/mission'
import { KIND_RANK } from '@/features/mission/composables/useNextThing'

export const DOCKED_KINDS: NextKind[] = (Object.keys(KIND_RANK) as NextKind[]).filter(k => k !== 'finished')

export interface NeedsYouPlacement {
  strip: boolean
  kinds?: NextKind[]
}

// error: the error line replaces the page, and with it the hub or the triage band.
// dashboard: its triage band shows permissions and questions, not plan reviews.
// pageHasHub: the hub docks the queue itself, without finished turns that would fill its height share.
export function needsYouPlacement({ view, pageHasHub, error }: { view: ActiveView, pageHasHub: boolean, error: boolean }): NeedsYouPlacement {
  if (error)
    return { strip: true }
  if (view === 'dashboard')
    return { strip: true, kinds: ['plan'] }
  return pageHasHub ? { strip: false, kinds: DOCKED_KINDS } : { strip: true }
}
