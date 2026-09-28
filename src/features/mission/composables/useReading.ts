import type { CoreView } from '@/composables/useViewState'
import { ACTIVE_VIEWS } from '@/composables/useViewState'

/**
 * What the input will do with what you typed, decided BEFORE you press Enter.
 * A reading shown here is a promise: navigate, hand the text to Kontor, or
 * refuse a slash command there is no session to run it in.
 */
export type ReadingKind = 'navigate' | 'command' | 'ask' | 'empty'

export const SLASH_COMMAND_REFUSAL = 'Slash commands need a running Kontor session. Start one, or type it in an agent’s own prompt.'

export interface Reading {
  kind: ReadingKind
  /** The badge, in the user's words. */
  label: string
  /** What pressing Enter does, stated as a consequence. */
  will: string
  /** For navigate: the view to switch to. */
  view?: CoreView
}

export function readInput(raw: string, sessionRunning = false): Reading {
  const text = raw.trim()
  if (!text)
    return { kind: 'empty', label: '', will: '' }

  const ask: Reading = sessionRunning
    ? { kind: 'ask', label: 'SEND TO KONTOR', will: 'Sends this to the running Kontor session.' }
    : { kind: 'ask', label: 'START KONTOR', will: 'Starts a Kontor session with this as its first prompt. It creates a task only when you ask for one.' }

  if (text.startsWith('/')) {
    return sessionRunning
      ? ask
      : { kind: 'command', label: 'COMMAND', will: 'Needs a running Kontor session. Start one, or type it in an agent\'s own prompt.' }
  }

  const lower = text.toLowerCase()
  const view = ACTIVE_VIEWS.find(v => lower === v || lower === `go to ${v}` || lower === `open ${v}`)
  if (view)
    return { kind: 'navigate', label: 'GO TO', will: `Switches to the ${view} view. Nothing is created.`, view }

  return ask
}
