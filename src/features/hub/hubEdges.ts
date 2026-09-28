import type { HubNote } from './composables/useObsidianGraph'
import type { Agent, NoteTouch, NoteTouchKind } from '@/types'

export const EDGE_WINDOW_MS = 10 * 60_000
const ALPHA_FRESH = 0.9
const ALPHA_OLD = 0.2

export interface HubEdge { from: [number, number], to: number, kind: NoteTouchKind, alpha: number }
export interface AgentNoteRow { path: string, title: string, kind: NoteTouchKind, at: string }

export function edgeAlpha(ageMs: number): number {
  const t = Math.min(Math.max(ageMs / EDGE_WINDOW_MS, 0), 1)
  return ALPHA_FRESH + (ALPHA_OLD - ALPHA_FRESH) * t
}

// Re-applies the window on the client clock; an unparsable `at` is NaN and drops.
function notesOnMap(agent: Agent, byPath: ReadonlyMap<string, HubNote>, now: number) {
  return (agent.recentNotes ?? []).flatMap((touch: NoteTouch) => {
    const note = byPath.get(touch.path)
    const ageMs = now - Date.parse(touch.at)
    return note && ageMs <= EDGE_WINDOW_MS ? [{ touch, note, ageMs }] : []
  })
}

export function liveEdges(
  placed: ReadonlyArray<{ agent: Agent, x: number, y: number }>,
  drawn: ReadonlySet<number>,
  byPath: ReadonlyMap<string, HubNote>,
  now: number,
): HubEdge[] {
  return placed
    .filter(p => drawn.has(p.agent.pid))
    .flatMap(p => notesOnMap(p.agent, byPath, now).map(({ touch, note, ageMs }): HubEdge => ({
      from: [p.x, p.y],
      to: note.index,
      kind: touch.kind,
      alpha: edgeAlpha(ageMs),
    })))
}

export function agentNoteRows(agent: Agent, byPath: ReadonlyMap<string, HubNote>, now: number): AgentNoteRow[] {
  return notesOnMap(agent, byPath, now).map(({ touch, note }) => ({ path: touch.path, title: note.title, kind: touch.kind, at: touch.at }))
}
