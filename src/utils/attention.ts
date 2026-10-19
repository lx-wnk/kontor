import type { Agent } from '../types'
import { isAwaitingInput, isStalled, STALLED_THRESHOLD_SECONDS } from './format'

export interface Attention {
  kind: 'question' | 'permission' | 'dialog' | 'error' | 'stalled' | 'yourTurn'
  label: string
  tone: 'warning' | 'danger' | 'neutral'
  weight: number
  // Whether this kind is evidence that a PERMISSION prompt is on screen — the
  // only basis on which the triage band may offer a standing grant for the
  // agent's pending tool call. A question is answered, never granted: its
  // prompt is about something else entirely, so treating it as evidence would
  // offer a rule for a tool nobody asked about. A required field so a future
  // kind must decide this explicitly instead of inheriting it by default.
  grantable: boolean
}

export function attentionFor(agent: Agent, secondsSinceActivity: number | null): Attention | null {
  // A finished agent's process is gone: its reconstructed errorState/pendingToolUse
  // are historical and not actionable from the triage band (dismiss lives on the card).
  if (agent.status === 'finished')
    return null
  // A real, answerable AskUserQuestion outranks a generic permission prompt.
  if (agent.pendingQuestion)
    return { kind: 'question', label: 'Question', tone: 'warning', weight: -1, grantable: false }
  // The review/submit screen is equally answerable and equally blocking: the
  // session sits there until someone presses a key.
  if (agent.pendingConfirm)
    return { kind: 'question', label: 'Confirm answers', tone: 'warning', weight: -1, grantable: false }
  // A PreToolUse hook call the bridge is holding open: the session is blocked
  // right now and one click releases or refuses it. Ranked above a pipeline
  // request because it expires — nobody answering means the run falls back to
  // its terminal, where the dashboard can no longer help.
  if (agent.heldPermissions && agent.heldPermissions.length > 0)
    return { kind: 'permission', label: 'Awaiting your decision', tone: 'warning', weight: 0, grantable: true }
  // A pipeline stage run's stored request: answered through the task's approve
  // control, which also records the decision against the task.
  if (agent.pendingPermissions && agent.pendingPermissions.length > 0)
    return { kind: 'permission', label: 'Needs permission', tone: 'warning', weight: 0, grantable: true }
  // The session is showing its own prompt: the bridge lapsed before anyone
  // decided, or is not installed for it. Not answerable from here — but it is
  // the one signal that positively means a permission prompt is on screen.
  //
  // A standing rule for next time is only honest when the prompt and the tool
  // the button would name are the same call. The notice fires once when the
  // prompt opens and never when it is answered, so it outlives its prompt,
  // while pendingToolUse is derived independently from the transcript — the
  // pair drifts, and the grant would be for a tool nobody asked about. The
  // bridge names the call when it held it; without that name there is no
  // evidence to offer a rule on.
  if (agent.awaitingTerminalPermission) {
    const named = Boolean(agent.terminalPermissionToolUseId)
      && agent.terminalPermissionToolUseId === agent.pendingToolUse?.id
    return { kind: 'permission', label: 'Answer in terminal', tone: 'warning', weight: 0, grantable: named }
  }
  // A CLI picker (/login, /model, …) holds the session until someone answers it
  // in the terminal. Ranked above an error: /login is how an expired login gets fixed.
  if (agent.awaitingTerminalDialog)
    return { kind: 'dialog', label: 'Waiting in terminal', tone: 'warning', weight: 0, grantable: false }
  if (agent.errorState)
    return { kind: 'error', label: 'Run failed', tone: 'danger', weight: 1, grantable: false }
  // An unresolved tool_use is the only signal a plain JSONL gives for "blocked on
  // a permission prompt" — but a session that never prompts produces the same
  // shape while a tool is simply still running, and permissionsBypassed only
  // catches the two flags that skip prompting outright: an allow-listed tool or
  // a partial permission mode silences the real prompt just as completely, and
  // neither reaches this payload. Naming the tool as if a prompt were on screen
  // was the bug the report described. Past the same dwell used for a stalled
  // session, the honest claim is just that nothing has happened in a while — not
  // which tool, and not that anyone is waiting on it.
  const toolUseStalled = Boolean(agent.pendingToolUse) && !agent.permissionsBypassed
    && secondsSinceActivity != null && secondsSinceActivity > STALLED_THRESHOLD_SECONDS
  if (isStalled(agent.status, secondsSinceActivity) || toolUseStalled)
    return { kind: 'stalled', label: 'No activity', tone: 'warning', weight: 2, grantable: false }
  // Turn finished, process alive: ready for the next instruction rather than
  // blocked on one, so it ranks below every other attention kind.
  if (isAwaitingInput(agent))
    return { kind: 'yourTurn', label: 'Your turn', tone: 'neutral', weight: 3, grantable: false }
  return null
}

export function needsAttention(agent: Agent, secondsSinceActivity: number | null): boolean {
  return attentionFor(agent, secondsSinceActivity) !== null
}

// Sorts attention-needing agents first (by ascending weight), then by longer-waiting first.
// Non-attention agents are appended in their original order.
export function sortByTriage(agents: Agent[], secsOf: (a: Agent) => number | null): Agent[] {
  const attention: Agent[] = []
  const rest: Agent[] = []

  for (const agent of agents) {
    const secs = secsOf(agent)
    if (needsAttention(agent, secs))
      attention.push(agent)
    else
      rest.push(agent)
  }

  attention.sort((a, b) => {
    const attA = attentionFor(a, secsOf(a))!
    const attB = attentionFor(b, secsOf(b))!
    if (attA.weight !== attB.weight)
      return attA.weight - attB.weight
    // Longer-waiting first: higher secondsSince = more urgent
    const secsA = secsOf(a) ?? 0
    const secsB = secsOf(b) ?? 0
    return secsB - secsA
  })

  return [...attention, ...rest]
}
