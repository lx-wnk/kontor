import type { ActiveView } from './useViewState'
import type { NextKind } from '@/features/mission'

export interface NeedsYouPlacement {
  strip: boolean
  kinds?: NextKind[]
}

// error: the error line replaces the page, and with it the hub or the triage band.
// dashboard: its triage band shows permissions and questions, not plan reviews.
// pageHasHub: the hub docks the queue itself.
export function needsYouPlacement({ view, pageHasHub, error }: { view: ActiveView, pageHasHub: boolean, error: boolean }): NeedsYouPlacement {
  if (error)
    return { strip: true }
  if (view === 'dashboard')
    return { strip: true, kinds: ['plan'] }
  return { strip: !pageHasHub }
}
