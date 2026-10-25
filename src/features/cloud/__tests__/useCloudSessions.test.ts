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

describe('startSession', () => {
  const input = { spawnerId: 'sp1', folderId: 'f1', description: 'Fix it' }

  it('posts exactly spawnerId, folderId and description and returns the view', async () => {
    const fetchMock = vi.fn().mockResolvedValue(jsonResponse(201, makeSession('c9', { origin: 'started' })))
    vi.stubGlobal('fetch', fetchMock)

    const view = await mod.startSession(input)

    expect(fetchMock).toHaveBeenCalledWith('/api/cloud-sessions', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(input),
    })
    expect(view.id).toBe('c9')
  })

  it('throws StartUnconfirmedError with the url when the server could not record a started session', async () => {
    const url = 'https://claude.ai/code/session_abc'
    const error = `the cloud session started but Kontor could not record it; import it by URL: ${url}`
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(jsonResponse(502, { error, mayHaveStarted: true, url })))

    const thrown = await mod.startSession(input).catch((e: unknown) => e)

    expect(thrown).toBeInstanceOf(mod.StartUnconfirmedError)
    expect(thrown).toMatchObject({ message: error, mayHaveStarted: true, url })
  })

  it('throws StartUnconfirmedError without a url when the start is only unconfirmed', async () => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(jsonResponse(502, { error: 'claude timed out', mayHaveStarted: true })))

    const thrown = await mod.startSession(input).catch((e: unknown) => e)

    expect(thrown).toBeInstanceOf(mod.StartUnconfirmedError)
    expect(thrown).toMatchObject({ message: 'claude timed out', mayHaveStarted: true })
    expect((thrown as { url?: string }).url).toBeUndefined()
  })

  it('throws a plain error for a 502 that did not flag mayHaveStarted', async () => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(jsonResponse(502, { error: 'claude CLI failed: exit 1' })))

    const thrown = await mod.startSession(input).catch((e: unknown) => e)

    expect(thrown).not.toBeInstanceOf(mod.StartUnconfirmedError)
    expect((thrown as Error).message).toBe('claude CLI failed: exit 1')
  })

  it('throws a plain error for a non-502 status even with mayHaveStarted', async () => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(jsonResponse(409, { error: 'folder is not trusted', mayHaveStarted: true })))

    const thrown = await mod.startSession(input).catch((e: unknown) => e)

    expect(thrown).not.toBeInstanceOf(mod.StartUnconfirmedError)
    expect((thrown as Error).message).toBe('folder is not trusted')
  })
})

describe('applying action results to the shared list', () => {
  async function mountLoaded() {
    const mounted = withSetup(() => mod.useCloudSessions())
    await vi.waitFor(() => expect(mounted.result.sessions.value).toHaveLength(1))
    return mounted
  }

  it('importSession prepends the new view, and the later created event adds no duplicate', async () => {
    const { result, wrapper } = await mountLoaded()
    const imported = makeSession('c2')
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(jsonResponse(201, imported)))

    await mod.importSession({ spawnerId: 'sp1', url: imported.url })

    expect(result.sessions.value.map(s => s.id)).toEqual(['c2', 'c1'])
    emit({ type: 'cloud_session_created', cloudSessionId: 'c2', payload: imported })
    expect(result.sessions.value.map(s => s.id)).toEqual(['c2', 'c1'])
    wrapper.unmount()
  })

  it('sendMessage replaces the row in place, and the later updated event is idempotent', async () => {
    const { result, wrapper } = await mountLoaded()
    const sent = makeSession('c1', { lastMessage: 'hi', lastMessageAt: '2026-01-02T00:00:00Z' })
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(jsonResponse(200, sent)))

    await mod.sendMessage('c1', 'hi')

    expect(result.sessions.value).toEqual([sent])
    emit({ type: 'cloud_session_updated', cloudSessionId: 'c1', payload: sent })
    expect(result.sessions.value).toEqual([sent])
    wrapper.unmount()
  })

  it('a failed send leaves the list untouched', async () => {
    const { result, wrapper } = await mountLoaded()
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(jsonResponse(502, { error: 'boom' })))

    await expect(mod.sendMessage('c1', 'hi')).rejects.toThrow('boom')

    expect(result.sessions.value.map(s => s.id)).toEqual(['c1'])
    wrapper.unmount()
  })
})

describe('reconnect', () => {
  it('refetches the list on the first frame of a (re)connected stream', async () => {
    const mounted = withSetup(() => mod.useCloudSessions())
    await vi.waitFor(() => expect(mounted.result.sessions.value).toHaveLength(1))
    const fetchMock = vi.fn().mockResolvedValue(jsonResponse(200, [makeSession('c1'), makeSession('c3')]))
    vi.stubGlobal('fetch', fetchMock)

    emit({ type: 'cloud_session_updated', cloudSessionId: 'unknown', payload: makeSession('unknown') })

    await vi.waitFor(() => expect(mounted.result.sessions.value.map(s => s.id)).toEqual(['c1', 'c3']))
    expect(fetchMock).toHaveBeenCalledWith('/api/cloud-sessions')
    mounted.wrapper.unmount()
  })
})

describe('useCloudAccounts', () => {
  const accounts = [{
    configDir: '/home/u/.claude',
    spawners: [{ id: 'sp1', name: 'Claude' }],
    auth: { loggedIn: true, authMethod: 'claude.ai', apiProvider: 'firstParty', subscriptionType: 'max' },
    ready: true,
    loginHint: 'CLAUDE_CONFIG_DIR=/home/u/.claude claude auth login',
  }]

  it('loads /api/cloud-accounts and refresh() calls ?refresh=1', async () => {
    const fetchMock = vi.fn().mockResolvedValue(jsonResponse(200, accounts))
    vi.stubGlobal('fetch', fetchMock)

    const { result } = withSetup(() => mod.useCloudAccounts())
    await vi.waitFor(() => expect(result.accounts.value).toHaveLength(1))
    expect(result.accounts.value[0].ready).toBe(true)
    expect(result.accounts.value[0].loginHint).toContain('claude auth login')
    expect(fetchMock).toHaveBeenCalledWith('/api/cloud-accounts')

    await result.refresh()

    expect(fetchMock).toHaveBeenLastCalledWith('/api/cloud-accounts?refresh=1')
  })

  it('refetch() calls the plain cached URL', async () => {
    const fetchMock = vi.fn().mockResolvedValue(jsonResponse(200, accounts))
    vi.stubGlobal('fetch', fetchMock)

    const { result } = withSetup(() => mod.useCloudAccounts())
    await vi.waitFor(() => expect(result.accounts.value).toHaveLength(1))
    fetchMock.mockClear()

    await result.refetch()

    expect(fetchMock).toHaveBeenCalledExactlyOnceWith('/api/cloud-accounts')
  })

  it('records the error when the request fails', async () => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(jsonResponse(500, {})))

    const { result } = withSetup(() => mod.useCloudAccounts())
    await vi.waitFor(() => expect(result.error.value).toBe('HTTP 500'))
  })
})
