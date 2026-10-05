import type { Agent } from '@/types'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { addPending } from '@/utils/pendingMessages'
import { SW_READY_TIMEOUT_MS } from '@/utils/timing'
import { useAgentPrompt } from './useAgentPrompt'

vi.mock('@/utils/pendingMessages', () => ({ addPending: vi.fn(async () => {}) }))

function makeAgent(over: Partial<Agent> = {}): Agent {
  return {
    sessionId: 's1',
    pid: 123,
    cwd: '/projects/x',
    status: 'active',
    channelAvailable: false,
    ...over,
  } as Agent
}

describe('useAgentPrompt routing', () => {
  beforeEach(() => {
    vi.stubGlobal('fetch', vi.fn(async () => ({ ok: true, json: async () => ({}) })))
  })

  it('injects live when liveInjectable — even when status is idle', async () => {
    const agent = makeAgent({ liveInjectable: true, status: 'idle' })
    const { promptInput, handleSend } = useAgentPrompt(() => agent)
    promptInput.value = 'hello'
    await handleSend()
    expect(fetch).toHaveBeenCalledWith(
      '/api/agents/123/message',
      expect.objectContaining({ method: 'POST' }),
    )
  })

  it('does NOT fetch and does NOT call onMessageSent when !liveInjectable — sets resumeConfirm instead', async () => {
    const agent = makeAgent({ channelAvailable: false, liveInjectable: false, status: 'active' })
    const onMessageSent = vi.fn()
    const { promptInput, handleSend, resumeConfirm } = useAgentPrompt(() => agent, onMessageSent)
    promptInput.value = 'hello world'
    await handleSend()
    expect(fetch).not.toHaveBeenCalled()
    expect(onMessageSent).not.toHaveBeenCalled()
    expect(resumeConfirm.value).toBe('hello world')
    expect(promptInput.value).toBe('')
  })

  it('confirmResume POSTs /api/agents/spawn with resumeSessionId and prompt', async () => {
    const agent = makeAgent({ channelAvailable: false, liveInjectable: false, status: 'active', sessionId: 'sess42' })
    const onMessageSent = vi.fn()
    const { promptInput, handleSend, confirmResume, resumeConfirm, sendStatus } = useAgentPrompt(() => agent, onMessageSent)
    promptInput.value = 'do something'
    await handleSend()
    // guard: confirm is set, no fetch yet
    expect(resumeConfirm.value).toBe('do something')
    expect(fetch).not.toHaveBeenCalled()

    await confirmResume()

    expect(fetch).toHaveBeenCalledWith(
      '/api/agents/spawn',
      expect.objectContaining({ method: 'POST' }),
    )
    const body = JSON.parse((fetch as ReturnType<typeof vi.fn>).mock.calls[0][1].body)
    expect(body.resumeSessionId).toBe('sess42')
    expect(body.prompt).toBe('do something')
    expect(onMessageSent).toHaveBeenCalledWith(
      expect.objectContaining({ role: 'human', content: 'do something' }),
    )
    expect(resumeConfirm.value).toBeNull()
    expect(sendStatus.value).toBe('sent')
  })

  it('cancelResume restores promptInput and clears resumeConfirm without fetching', async () => {
    const agent = makeAgent({ channelAvailable: false, liveInjectable: false, status: 'active' })
    const { promptInput, handleSend, cancelResume, resumeConfirm } = useAgentPrompt(() => agent)
    promptInput.value = 'my draft message'
    await handleSend()
    expect(resumeConfirm.value).toBe('my draft message')
    expect(promptInput.value).toBe('')

    cancelResume()

    expect(fetch).not.toHaveBeenCalled()
    expect(resumeConfirm.value).toBeNull()
    expect(promptInput.value).toBe('my draft message')
  })

  it('confirmResume does nothing if resumeConfirm is null', async () => {
    const agent = makeAgent({ liveInjectable: false })
    const { confirmResume, resumeConfirm } = useAgentPrompt(() => agent)
    expect(resumeConfirm.value).toBeNull()
    await confirmResume()
    expect(fetch).not.toHaveBeenCalled()
  })

  it('keeps the tracked message as failed when the server rejects the send, so its echo is not mistaken for delivered', async () => {
    vi.stubGlobal('fetch', vi.fn(async () => ({ ok: false, status: 502, json: async () => ({ error: 'tmux gone' }) })))
    const agent = makeAgent({ liveInjectable: true })
    const { promptInput, handleSend, trackedMessages, sendStatus } = useAgentPrompt(() => agent)
    promptInput.value = 'hello'
    await handleSend()
    expect(sendStatus.value).toBe('error')
    expect(trackedMessages.value).toEqual([expect.objectContaining({ text: 'hello', state: 'failed' })])
  })

  it('confirmResume clears state gracefully when getAgent returns null at confirm time', async () => {
    let agent: Agent | null = makeAgent({ liveInjectable: false, sessionId: 'gone' })
    const { promptInput, handleSend, confirmResume, resumeConfirm } = useAgentPrompt(() => agent)
    promptInput.value = 'hi'
    await handleSend()
    expect(resumeConfirm.value).toBe('hi')

    agent = null
    await confirmResume()

    expect(fetch).not.toHaveBeenCalled()
    expect(resumeConfirm.value).toBeNull()
  })
})

// The desktop shell registers no service worker, so `ready` never settles there.
describe('useAgentPrompt offline queueing', () => {
  beforeEach(() => {
    vi.stubGlobal('fetch', vi.fn(async () => {
      throw new TypeError('Failed to fetch')
    }))
    vi.stubGlobal('SyncManager', class {})
    vi.stubGlobal('navigator', {
      serviceWorker: { ready: new Promise(() => {}) },
    })
  })

  afterEach(() => {
    vi.useRealTimers()
    vi.unstubAllGlobals()
  })

  it('reports the message as queued even when the service worker never becomes ready', async () => {
    vi.useFakeTimers()
    const agent = makeAgent({ liveInjectable: true })
    const { promptInput, handleSend, isSending, sendStatus } = useAgentPrompt(() => agent)
    promptInput.value = 'offline message'

    const sent = handleSend()
    await vi.advanceTimersByTimeAsync(SW_READY_TIMEOUT_MS)
    await sent

    expect(addPending).toHaveBeenCalledWith(expect.objectContaining({ message: 'offline message' }))
    expect(sendStatus.value).toBe('queued')
    expect(isSending.value).toBe(false)
  })

  it('keeps the tracked message as queued once the offline send is stored', async () => {
    vi.stubGlobal('navigator', {})
    const agent = makeAgent({ liveInjectable: true })
    const { promptInput, handleSend, trackedMessages } = useAgentPrompt(() => agent)
    promptInput.value = 'offline message'
    await handleSend()
    expect(trackedMessages.value).toEqual([expect.objectContaining({ text: 'offline message', state: 'queued' })])
  })

  it('marks the tracked message failed when the offline send cannot be stored', async () => {
    vi.stubGlobal('navigator', {})
    vi.mocked(addPending).mockRejectedValueOnce(new Error('idb unavailable'))
    const agent = makeAgent({ liveInjectable: true })
    const { promptInput, handleSend, trackedMessages, sendStatus } = useAgentPrompt(() => agent)
    promptInput.value = 'offline message'
    await handleSend()
    expect(sendStatus.value).toBe('error')
    expect(trackedMessages.value).toEqual([expect.objectContaining({ text: 'offline message', state: 'failed' })])
  })
})
