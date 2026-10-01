// sending = fetch in flight, sent = server acknowledged (HTTP 200), delivered = serverText seen in the JSONL transcript
export type DeliveryState = 'sending' | 'sent' | 'delivered'

export interface TrackedMessage {
  id: string
  text: string
  serverText: string
  state: DeliveryState
  sentAt: number
}

export function reconcileDelivery(
  tracked: TrackedMessage[],
  transcriptUserTexts: string[],
): TrackedMessage[] {
  return tracked.map((msg) => {
    if (msg.state === 'delivered')
      return msg
    if (transcriptUserTexts.includes(msg.serverText))
      return { ...msg, state: 'delivered' as const }
    return msg
  })
}
