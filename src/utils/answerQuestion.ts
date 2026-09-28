import type { AnswerIntent } from './answerKeys'

/**
 * Send a human's answer to whatever screen an agent's terminal is holding open.
 *
 * One route and one payload shape for every place that answers — the needs-you
 * band and mission control both call this. A second copy is a second thing to
 * keep in step with the server when the contract moves.
 */
export async function sendQuestionAnswer(pid: number, intent: AnswerIntent): Promise<void> {
  const res = await fetch(`/api/agents/${pid}/answer-question`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(intent),
  })
  if (!res.ok)
    throw new Error(`HTTP ${res.status}`)
}
