import type { CloudSessionView } from '../composables/useCloudSessions'
import { mount } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { defineComponent, nextTick } from 'vue'

class MockEventSource {
  static instances: MockEventSource[] = []
  onmessage: ((e: MessageEvent) => void) | null = null
  onerror: ((e: Event) => void) | null = null
  readyState = 0
  static CONNECTING = 0
  static OPEN = 1
  static CLOSED = 2

  constructor(public url: string) {
    MockEventSource.instances.push(this)
  }

  close() {
    this.readyState = 2
  }
}

let mod: typeof import('../composables/useCloudSessions')

function makeSession(id: string, overrides: Partial<CloudSessionView> = {}): CloudSessionView {
  return {
    id,
    sessionId: `session_${id}`,
    url: `https://claude.ai/code/session_${id}`,
    spawnerId: 'sp1',
    title: `Session ${id}`,
    origin: 'imported',
    lastMessage: '',
    lastMessageAt: null,
    lastError: '',
    createdAt: '2026-01-01T00:00:00Z',
    updatedAt: '2026-01-01T00:00:00Z',
    ...overrides,
  }
}

function jsonResponse(status: number, body: unknown) {
  return { ok: status >= 200 && status < 300, status, json: () => Promise.resolve(body) }
}

function withSetup<T>(composable: () => T) {
  let result!: T
  const Wrapper = defineComponent({
    setup() {
      result = composable()
      return {}
    },
    template: '<div />',
  })
  const wrapper = mount(Wrapper, { attachTo: document.body })
  return { result, wrapper }
}

function emit(frame: unknown) {
  MockEventSource.instances[0].onmessage?.(new MessageEvent('message', { data: JSON.stringify(frame) }))
}

beforeEach(async () => {
  MockEventSource.instances = []
  vi.stubGlobal('EventSource', MockEventSource)
  vi.stubGlobal('fetch', vi.fn().mockResolvedValue(jsonResponse(200, [makeSession('c1')])))
  vi.resetModules()
  mod = await import('../composables/useCloudSessions')
})

afterEach(() => {
  vi.unstubAllGlobals()
})

describe('useCloudSessions', () => {
  it('loads /api/cloud-sessions and opens /api/cloud-sessions/stream', async () => {
    const { result, wrapper } = withSetup(() => mod.useCloudSessions())
    await Promise.resolve()
    await Promise.resolve()
    await nextTick()

    expect(fetch).toHaveBeenCalledWith('/api/cloud-sessions')
    expect(result.sessions.value.map(s => s.id)).toEqual(['c1'])
    expect(MockEventSource.instances[0].url).toBe('/api/cloud-sessions/stream')
    wrapper.unmount()
  })

  it('prepends on cloud_session_created and ignores a duplicate', async () => {
    const { result, wrapper } = withSetup(() => mod.useCloudSessions())
    await Promise.resolve()
    await nextTick()

    const created = { type: 'cloud_session_created', cloudSessionId: 'c2', payload: makeSession('c2') }
    emit(created)
    emit(created)

    expect(result.sessions.value.map(s => s.id)).toEqual(['c2', 'c1'])
    wrapper.unmount()
  })

  it('replaces the entry on cloud_session_updated', async () => {
    const { result, wrapper } = withSetup(() => mod.useCloudSessions())
    await Promise.resolve()
    await nextTick()

    emit({ type: 'cloud_session_updated', cloudSessionId: 'c1', payload: makeSession('c1', { lastError: 'boom' }) })

    expect(result.sessions.value).toHaveLength(1)
    expect(result.sessions.value[0].lastError).toBe('boom')
    wrapper.unmount()
  })

  it('removes the entry on cloud_session_deleted', async () => {
    const { result, wrapper } = withSetup(() => mod.useCloudSessions())
    await Promise.resolve()
    await nextTick()

    emit({ type: 'cloud_session_deleted', cloudSessionId: 'c1' })

    expect(result.sessions.value).toHaveLength(0)
    wrapper.unmount()
  })

  it('a passive caller (autoStart: false) does not close the stream for the owner', async () => {
    const owner = withSetup(() => mod.useCloudSessions())
    await nextTick()
    withSetup(() => mod.useCloudSessions({ autoStart: false })).wrapper.unmount()
    await nextTick()

    expect(MockEventSource.instances[0].readyState).not.toBe(MockEventSource.CLOSED)
    owner.wrapper.unmount()
  })
})

describe('cloud session actions', () => {
  it('importSession POSTs spawnerId, url and title and returns the view', async () => {
    const fetchMock = vi.fn().mockResolvedValue(jsonResponse(201, makeSession('c9')))
    vi.stubGlobal('fetch', fetchMock)

    const view = await mod.importSession({ spawnerId: 'sp1', url: 'https://claude.ai/code/session_x', title: 'T' })

    expect(fetchMock).toHaveBeenCalledWith('/api/cloud-sessions/import', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ spawnerId: 'sp1', url: 'https://claude.ai/code/session_x', title: 'T' }),
    })
    expect(view.id).toBe('c9')
  })

  it('importSession throws the server error on 409', async () => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(jsonResponse(409, { error: 'already imported' })))

    await expect(mod.importSession({ spawnerId: 'sp1', url: 'u' })).rejects.toThrow('already imported')
  })

  it('sendMessage POSTs the text to the session messages endpoint', async () => {
    const fetchMock = vi.fn().mockResolvedValue(jsonResponse(200, makeSession('c1', { lastMessage: 'hi' })))
    vi.stubGlobal('fetch', fetchMock)

    const view = await mod.sendMessage('c1', 'hi')

    expect(fetchMock).toHaveBeenCalledWith('/api/cloud-sessions/c1/messages', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ text: 'hi' }),
    })
    expect(view.lastMessage).toBe('hi')
  })

  it.each([
    [409, 'account is not signed in'],
    [502, 'claude CLI failed: exit 1'],
  ])('sendMessage throws the server error text on %i', async (status, error) => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(jsonResponse(status, { error })))

    await expect(mod.sendMessage('c1', 'hi')).rejects.toThrow(new Error(error))
  })

  it('falls back to the HTTP status when the error body is not JSON', async () => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue({ ok: false, status: 502, json: () => Promise.reject(new Error('bad json')) }))

    await expect(mod.sendMessage('c1', 'hi')).rejects.toThrow('HTTP 502')
  })

  it('removeSession issues DELETE /api/cloud-sessions/:id', async () => {
    const fetchMock = vi.fn().mockResolvedValue({ ok: true, status: 204 })
    vi.stubGlobal('fetch', fetchMock)

    await mod.removeSession('c1')

    expect(fetchMock).toHaveBeenCalledWith('/api/cloud-sessions/c1', { method: 'DELETE' })
  })
})

describe('useCloudAccounts', () => {
  const accounts = [{
    configDir: '/home/u/.claude',
    spawners: [{ id: 'sp1', name: 'Claude' }],
    auth: { loggedIn: true, authMethod: 'claude.ai', apiProvider: 'firstParty', subscriptionType: 'max' },
  }]

  it('loads /api/cloud-accounts and refresh() calls ?refresh=1', async () => {
    const fetchMock = vi.fn().mockResolvedValue(jsonResponse(200, accounts))
    vi.stubGlobal('fetch', fetchMock)

    const { result } = withSetup(() => mod.useCloudAccounts())
    await vi.waitFor(() => expect(result.accounts.value).toHaveLength(1))
    expect(fetchMock).toHaveBeenCalledWith('/api/cloud-accounts')

    await result.refresh()

    expect(fetchMock).toHaveBeenLastCalledWith('/api/cloud-accounts?refresh=1')
  })

  it('records the error when the request fails', async () => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(jsonResponse(500, {})))

    const { result } = withSetup(() => mod.useCloudAccounts())
    await vi.waitFor(() => expect(result.error.value).toBe('HTTP 500'))
  })
})
