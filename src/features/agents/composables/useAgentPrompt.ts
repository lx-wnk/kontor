import type { Ref } from 'vue'
import type { TrackedMessage } from '@/features/agents/composables/deliveryState'
import type { Agent, OutputMessage } from '@/types'
import { onUnmounted, ref } from 'vue'
import { dispatchSlashCommand, parseSlashCommand, SLASH_COMMAND_DEFS } from '@/composables/useSlashCommands'
import { errorMessage } from '@/utils/errorMessage'
import { addPending } from '@/utils/pendingMessages'
import { BACKGROUND_SYNC_TAG } from '@/utils/swConstants'
import { SEND_STATUS_RESET_MS, SW_READY_TIMEOUT_MS } from '@/utils/timing'

export type OnMessageSent = (msg: OutputMessage) => void

export interface AgentPromptContext {
  taskId?: string
  cwd?: string
}

async function registerBackgroundSync(): Promise<void> {
  if (!('serviceWorker' in navigator) || !('SyncManager' in window))
    return
  try {
    // `ready` resolves once a worker controls the page and otherwise never
    // settles — the desktop shell registers none — so awaiting it alone would
    // hold the send path open forever with the message already queued.
    const registration = await Promise.race([
      navigator.serviceWorker.ready,
      new Promise<null>(resolve => setTimeout(resolve, SW_READY_TIMEOUT_MS, null)),
    ])
    if (!registration)
      return
    // @ts-expect-error: Remove when lib.dom.d.ts ships ServiceWorkerRegistration.sync (Background Sync API)
    await registration.sync.register(BACKGROUND_SYNC_TAG)
  }
  catch {
    // Background Sync not supported or SW not yet active — SSE-reconnect fallback handles this
  }
}

function isNetworkFailure(err: unknown): boolean {
  return err instanceof TypeError
}

/**
 * `attachments` is an optional ref to image-attachment absolute paths (full
 * variant only). They are appended to the message as trailing "@<path>" tokens
 * so Claude Code resolves them as image references, and cleared once folded
 * into a sent/staged message.
 */
export function useAgentPrompt(
  getAgent: () => Agent | null,
  onMessageSent?: OnMessageSent,
  ctx?: AgentPromptContext,
  attachments?: Ref<string[]>,
) {
  const promptInput = ref('')
  const isSending = ref(false)
  const sendStatus = ref<'sent' | 'error' | 'queued' | null>(null)
  const sendError = ref('')
  const resumeConfirm = ref<string | null>(null)
  const trackedMessages = ref<TrackedMessage[]>([])

  /**
   * Combines the typed text with any pending attachment paths into the final
   * message. Attachments are appended as space-separated "@<path>" tokens so
   * a text-less send (attachments only) still produces non-empty content.
   */
  function buildMessage(): string {
    const text = promptInput.value.trim()
    const paths = attachments?.value ?? []
    if (paths.length === 0)
      return text
    return [text, ...paths.map(p => `@${p}`)].filter(Boolean).join(' ')
  }

  /**
   * Shared delivery helper. Performs the correct fetch for inject vs resume,
   * handles optimistic echo, isSending, sendStatus, offline queueing, and
   * the 3s auto-clear — so neither handleSend nor confirmResume duplicate this logic.
   */
  async function deliver(agent: Agent, msg: string, mode: 'inject' | 'resume'): Promise<void> {
    // Optimistic: show message immediately before the network round-trip
    onMessageSent?.({ role: 'human', content: msg, timestamp: new Date().toISOString() })
    isSending.value = true
    sendStatus.value = null

    const trackId = crypto.randomUUID()
    trackedMessages.value = [...trackedMessages.value, {
      id: trackId,
      text: msg,
      serverText: msg,
      state: 'sending',
      sentAt: Date.now(),
    }]

    try {
      let serverText = msg
      if (mode === 'inject') {
        // Channel inject keyed by PID — route is /api/agents/{pid}/message
        const res = await fetch(`/api/agents/${agent.pid}/message`, {
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({ message: msg }),
        })
        if (!res.ok) {
          const data = await res.json().catch(() => ({}))
          throw new Error(data.error || `Send failed (${res.status})`)
        }
        // Server returns {text: sanitized} — the canonical text that appears in the JSONL transcript.
        const body = await res.json().catch(() => ({}))
        if (body.text)
          serverText = body.text
      }
      else {
        const res = await fetch('/api/agents/spawn', {
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({
            prompt: msg,
            cwd: agent.cwd,
            resumeSessionId: agent.sessionId,
          }),
        })
        if (!res.ok) {
          const data = await res.json().catch(() => ({}))
          throw new Error(data.error || `Resume failed (${res.status})`)
        }
      }
      trackedMessages.value = trackedMessages.value.map(m =>
        m.id === trackId ? { ...m, state: 'sent' as const, serverText } : m,
      )
      sendStatus.value = 'sent'
    }
    catch (err) {
      if (isNetworkFailure(err)) {
        const useChannel = mode === 'inject'
        try {
          await addPending({
            agentPid: agent.pid,
            sessionId: agent.sessionId,
            message: msg,
            timestamp: Date.now(),
            useChannel,
            cwd: agent.cwd,
          })
          await registerBackgroundSync()
          sendStatus.value = 'queued'
          sendError.value = 'Offline — message queued'
        }
        catch {
          sendStatus.value = 'error'
          sendError.value = 'Offline and could not queue message'
        }
      }
      else {
        sendStatus.value = 'error'
        sendError.value = errorMessage(err, 'Failed')
      }
    }
    finally {
      isSending.value = false
      if (sendStatus.value !== 'queued') {
        setTimeout(() => {
          sendStatus.value = null
        }, SEND_STATUS_RESET_MS)
      }
    }
  }

  async function handleSend() {
    const agent = getAgent()
    const msg = buildMessage()
    if (!msg || isSending.value || !agent)
      return

    // Slash-command intercept: only known dashboard commands are intercepted;
    // unrecognised slash commands (e.g. /btw, /compact, /review) fall through to the agent.
    const parsed = parseSlashCommand(msg)
    if (parsed) {
      const [cmd, args] = parsed
      const isKnownCommand = SLASH_COMMAND_DEFS.some(def => def.name === cmd)
      if (isKnownCommand) {
        promptInput.value = ''
        if (attachments)
          attachments.value = []
        isSending.value = true
        sendError.value = ''
        try {
          const result = await dispatchSlashCommand(cmd, args, {
            taskId: ctx?.taskId ?? agent?.pipelineTaskId,
            cwd: ctx?.cwd ?? agent?.cwd,
          })
          sendStatus.value = result.ok ? 'sent' : 'error'
          sendError.value = result.ok ? '' : result.message
          if (result.ok && result.message) {
            onMessageSent?.({ role: 'channel_reply', content: result.message, timestamp: new Date().toISOString() })
          }
        }
        finally {
          isSending.value = false
          setTimeout(() => {
            sendStatus.value = null
          }, SEND_STATUS_RESET_MS)
        }
        return
      }
    }

    if (agent.liveInjectable) {
      // Live inject — immediate, no confirmation needed
      promptInput.value = ''
      await deliver(agent, msg, 'inject')
      // Only clear attachments once delivery didn't fail outright — a queued
      // (offline) send still consumed the paths into the queued message.
      if (attachments && sendStatus.value !== 'error')
        attachments.value = []
    }
    else {
      // Non-injectable session: require explicit user confirmation before resuming
      // to prevent silent duplicate detached processes on each send. The staged
      // message already carries any attachment "@<path>" tokens.
      resumeConfirm.value = msg
      promptInput.value = ''
      if (attachments)
        attachments.value = []
    }
  }

  /**
   * Called when the user explicitly confirms resuming a non-injectable session.
   * Runs the resume delivery with the pending message and clears the confirm state.
   */
  async function confirmResume(): Promise<void> {
    if (resumeConfirm.value === null)
      return
    const msg = resumeConfirm.value
    resumeConfirm.value = null

    // Re-fetch the agent at confirm time — it may have changed (e.g. become
    // live-injectable). If it now has a live channel, inject instead of
    // resuming, so confirming never spawns a duplicate detached process.
    const agent = getAgent()
    if (!agent)
      return

    await deliver(agent, msg, agent.liveInjectable ? 'inject' : 'resume')
  }

  /**
   * Cancels a pending resume confirmation, restoring the message to the input.
   */
  function cancelResume(): void {
    promptInput.value = resumeConfirm.value ?? ''
    resumeConfirm.value = null
  }

  const onDrainSuccess = () => {
    if (sendStatus.value === 'queued')
      sendStatus.value = null
  }
  window.addEventListener('drain-success', onDrainSuccess)
  onUnmounted(() => window.removeEventListener('drain-success', onDrainSuccess))

  return { promptInput, isSending, sendStatus, sendError, handleSend, resumeConfirm, confirmResume, cancelResume, trackedMessages }
}
