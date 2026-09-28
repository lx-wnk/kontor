import { mount } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { defineComponent, nextTick, ref } from 'vue'

const agents = ref<Array<{ pid: number, status: string }>>([])
vi.mock('@/features/agents', () => ({ useAgents: () => ({ agents }) }))

type Mod = typeof import('../composables/useKontorSession')
let useKontorSession: Mod['useKontorSession']
const fetchMock = vi.fn()

function reply(body: unknown, status = 200) {
  return { ok: status >= 200 && status < 300, status, json: async () => body }
}
function body(call: number) {
  return JSON.parse((fetchMock.mock.calls[call][1] as RequestInit).body as string)
}

beforeEach(async () => {
  // The state is module-level on purpose; a fresh module per test keeps it from leaking.
  vi.resetModules()
  fetchMock.mockReset()
  vi.stubGlobal('fetch', fetchMock)
  agents.value = []
  ;({ useKontorSession } = await import('../composables/useKontorSession'))
})
afterEach(() => vi.unstubAllGlobals())

describe('useKontorSession', () => {
  it('reattaches to the running session and shares it with every caller', async () => {
    fetchMock.mockResolvedValueOnce(reply({ pid: 1234 }))
    const s = useKontorSession()
    await s.refresh()
    expect(fetchMock.mock.calls[0][0]).toBe('/api/kontor-session')
    expect(s.pid.value).toBe(1234)
    expect(s.status.value).toBe('running')
    expect(useKontorSession().pid.value).toBe(1234)
  })

  it('starts a session with the text as its first prompt when none runs', async () => {
    fetchMock.mockResolvedValueOnce(reply({ pid: null })).mockResolvedValueOnce(reply({ pid: 1234 }))
    const s = useKontorSession()
    expect(await s.send('plan phase 4')).toBe(true)
    expect(fetchMock.mock.calls[1][0]).toBe('/api/kontor-session')
    expect((fetchMock.mock.calls[1][1] as RequestInit).method).toBe('POST')
    expect(body(1)).toEqual({ prompt: 'plan phase 4' })
    expect(s.pid.value).toBe(1234)
    expect(s.status.value).toBe('running')
  })

  // POST returns a running session unchanged and drops its prompt, so text
  // for a session this window never saw must go as a message.
  it('sends to a session another window started instead of starting one', async () => {
    fetchMock.mockResolvedValueOnce(reply({ pid: 99 })).mockResolvedValue(reply({}))
    const s = useKontorSession()
    expect(await s.send('/compact')).toBe(true)
    expect(fetchMock.mock.calls[1][0]).toBe('/api/agents/99/message')
    expect(body(1)).toEqual({ message: '/compact' })
    await s.send('again')
    expect(fetchMock).toHaveBeenCalledTimes(3)
  })

  it('reports a start the server refused and holds no pid', async () => {
    fetchMock.mockResolvedValueOnce(reply({ pid: null })).mockResolvedValueOnce(reply({ error: 'could not mint key' }, 500))
    const s = useKontorSession()
    expect(await s.send('hi')).toBe(false)
    expect(s.status.value).toBe('error')
    expect(s.error.value).toBe('could not mint key')
    expect(s.pid.value).toBeNull()
  })

  it('keeps the session when a message fails and says why', async () => {
    fetchMock.mockResolvedValueOnce(reply({ pid: 1234 })).mockResolvedValueOnce(reply({ error: 'rate limited' }, 429))
    const s = useKontorSession()
    await s.refresh()
    expect(await s.send('hi')).toBe(false)
    expect(s.status.value).toBe('running')
    expect(s.error.value).toBe('rate limited')
  })

  it('ends the session', async () => {
    fetchMock.mockResolvedValueOnce(reply({ pid: 1234 })).mockResolvedValueOnce(reply(null, 204))
    const s = useKontorSession()
    await s.refresh()
    await s.end()
    expect(fetchMock.mock.calls[1]).toEqual(['/api/kontor-session', { method: 'DELETE' }])
    expect(s.pid.value).toBeNull()
    expect(s.status.value).toBe('idle')
  })

  it('renews into a fresh pid and does not mistake the old one leaving for an exit', async () => {
    fetchMock.mockResolvedValueOnce(reply({ pid: 1234 })).mockResolvedValueOnce(reply({ pid: 5678 }))
    const s = useKontorSession()
    await s.refresh()
    agents.value = [{ pid: 1234, status: 'active' }]
    await nextTick()
    expect(await s.renew('')).toBe(true)
    expect(fetchMock.mock.calls[1][0]).toBe('/api/kontor-session/renew')
    expect(body(1)).toEqual({ prompt: '' })
    agents.value = []
    await nextTick()
    expect(s.pid.value).toBe(5678)
  })

  it('returns to idle when the session agent exits', async () => {
    fetchMock.mockResolvedValueOnce(reply({ pid: 1234 }))
    const s = useKontorSession()
    await s.refresh()
    agents.value = [{ pid: 1234, status: 'active' }]
    await nextTick()
    agents.value = [{ pid: 1234, status: 'finished' }]
    await nextTick()
    expect(s.pid.value).toBeNull()
    expect(s.status.value).toBe('idle')
  })

  // The exit watcher must outlive whichever component's setup() happened to
  // call useKontorSession() first, or every later view loses exit detection.
  it('keeps noticing the agent exit after the first mounting component unmounts', async () => {
    fetchMock.mockResolvedValueOnce(reply({ pid: 1234 }))
    const TinyComponent = defineComponent({
      setup() {
        useKontorSession()
        return () => null
      },
    })
    const wrapper = mount(TinyComponent)
    const s = useKontorSession()
    await s.refresh()
    wrapper.unmount()

    agents.value = [{ pid: 1234, status: 'active' }]
    await nextTick()
    agents.value = [{ pid: 1234, status: 'finished' }]
    await nextTick()
    expect(s.pid.value).toBeNull()
    expect(s.status.value).toBe('idle')
  })

  // POST /api/kontor-session can answer started: false when another window's
  // session won the race; the prompt it carried must not be silently dropped.
  it('delivers the text as a message when the start POST finds a session another window already started', async () => {
    fetchMock
      .mockResolvedValueOnce(reply({ pid: null }))
      .mockResolvedValueOnce(reply({ pid: 7, started: false }))
      .mockResolvedValueOnce(reply({}))
    const s = useKontorSession()
    expect(await s.send('plan phase 4')).toBe(true)
    expect(fetchMock.mock.calls[2][0]).toBe('/api/agents/7/message')
    expect(body(2)).toEqual({ message: 'plan phase 4' })
    expect(s.pid.value).toBe(7)
    expect(s.status.value).toBe('running')
  })

  it('clears a stale error once a later call succeeds', async () => {
    fetchMock.mockResolvedValueOnce(reply({ error: 'boom' }, 500)).mockResolvedValueOnce(reply({ pid: 1234 }))
    const s = useKontorSession()
    await s.refresh()
    expect(s.error.value).toBe('boom')
    await s.refresh()
    expect(s.error.value).toBe('')
    expect(s.pid.value).toBe(1234)
  })

  it('opens the tile without staging a prompt when asked with no text', () => {
    const s = useKontorSession()
    s.ask()
    expect(s.openRequested.value).toBe(true)
    expect(s.pendingPrompt.value).toBeNull()
    s.ask('[[notes/a]] ')
    expect(s.pendingPrompt.value).toBe('[[notes/a]] ')
  })

  it('does not start a session when the pre-send refresh fails', async () => {
    fetchMock.mockResolvedValueOnce(reply({ error: 'boom' }, 500)).mockResolvedValue(reply({ pid: 5678 }))
    const s = useKontorSession()
    expect(await s.send('hi')).toBe(false)
    expect(fetchMock).toHaveBeenCalledTimes(1)
    expect(s.status.value).toBe('error')
    expect(s.error.value).toBe('boom')
    expect(s.pid.value).toBeNull()
  })
})
