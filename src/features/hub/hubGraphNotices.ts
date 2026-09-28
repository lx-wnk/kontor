import type { GraphStatus } from './composables/useObsidianGraph'

export const GRAPH_NOTICES: Partial<Record<GraphStatus, string>> = {
  unconfigured: 'Connect Obsidian to see your notes here.',
  denied: 'Memory reads are not granted, so your notes stay hidden.',
  failed: 'Your notes could not be loaded; retrying when you come back to this window.',
}

// denied/failed reuse GRAPH_NOTICES' wording so the two can't drift; ready (empty list) is list-only.
export const LIST_GRAPH_NOTICES: Partial<Record<GraphStatus, string>> = {
  unconfigured: 'Connect Obsidian to see recently touched notes.',
  denied: GRAPH_NOTICES.denied,
  failed: GRAPH_NOTICES.failed,
  ready: 'No notes yet.',
}
