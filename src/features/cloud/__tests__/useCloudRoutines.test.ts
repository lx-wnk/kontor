import type { CloudSessionView } from '../composables/useCloudSessions'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

let mod: typeof import('../composables/useCloudRoutines')
let sessionsMod: typeof import('../composables/useCloudSessions')

function makeRoutine(id: string, overrides: Record<string, unknown> = {}) {
  return {
    id,
    name: `Routine ${id}`,
    fireUrl: `https://api.anthropic.com/v1/claude_code/routines/trig_${id}/fire`,
    spawnerId: 'sp1',
    hasToken: true,
    createdAt: '2026-01-01T00:00:00Z',
    updatedAt: '2026-01-01T00:00:00Z',
    ...overrides,
  }
}

function makeSession(id: string): CloudSessionView {
  return {
    id,
    sessionId: `session_${id}`,
    url: `https://claude.ai/code/session_${id}`,
    spawnerId: 'sp1',
    title: `Routine ${id}`,
    origin: 'routine',
    lastMessage: '',
    lastMessageAt: null,
    lastError: '',
    createdAt: '2026-01-01T00:00:00Z',
    updatedAt: '2026-01-01T00:00:00Z',
  }
}

function jsonResponse(status: number, body: unknown) {
  return { ok: status >= 200 && status < 300, status, json: () => Promise.resolve(body) }
}

const jsonHeaders = { 'Content-Type': 'application/json' }

beforeEach(async () => {
  vi.stubGlobal('fetch', vi.fn().mockResolvedValue(jsonResponse(200, [makeRoutine('r1')])))
  vi.resetModules()
  mod = await import('../composables/useCloudRoutines')
  sessionsMod = await import('../composables/useCloudSessions')
})

afterEach(() => {
  vi.unstubAllGlobals()
})

describe('useCloudRoutines', () => {
  it('loads /api/cloud-routines', async () => {
    const { routines, refetch } = mod.useCloudRoutines()
    await refetch()

    expect(fetch).toHaveBeenCalledWith('/api/cloud-routines')
    expect(routines.value.map(r => r.id)).toEqual(['r1'])
  })

  it('create POSTs the body and refetches the list', async () => {
    const fetchMock = vi.fn()
      .mockResolvedValueOnce(jsonResponse(201, makeRoutine('r2')))
      .mockResolvedValueOnce(jsonResponse(200, [makeRoutine('r2')]))
    vi.stubGlobal('fetch', fetchMock)
    const input = { name: 'N', fireUrl: 'https://api.anthropic.com/v1/claude_code/routines/trig_x/fire', spawnerId: 'sp1', token: 'sk-ant-oat01-abc' }

    const { routines, create } = mod.useCloudRoutines()
    await create(input)

    expect(fetchMock).toHaveBeenNthCalledWith(1, '/api/cloud-routines', { method: 'POST', headers: jsonHeaders, body: JSON.stringify(input) })
    expect(fetchMock).toHaveBeenNthCalledWith(2, '/api/cloud-routines')
    expect(routines.value.map(r => r.id)).toEqual(['r2'])
  })

  it('update PATCHes without a token key when the token is empty', async () => {
    const fetchMock = vi.fn().mockResolvedValue(jsonResponse(200, makeRoutine('r1')))
    vi.stubGlobal('fetch', fetchMock)

    await mod.useCloudRoutines().update('r1', { name: 'Renamed', token: '' })

    expect(fetchMock).toHaveBeenNthCalledWith(1, '/api/cloud-routines/r1', {
      method: 'PATCH',
      headers: jsonHeaders,
      body: JSON.stringify({ name: 'Renamed' }),
    })
  })

  it('update sends a non-empty token', async () => {
    const fetchMock = vi.fn().mockResolvedValue(jsonResponse(200, makeRoutine('r1')))
    vi.stubGlobal('fetch', fetchMock)

    await mod.useCloudRoutines().update('r1', { token: 'new-token' })

    expect(fetchMock).toHaveBeenNthCalledWith(1, '/api/cloud-routines/r1', {
      method: 'PATCH',
      headers: jsonHeaders,
      body: JSON.stringify({ token: 'new-token' }),
    })
  })

  it('remove issues DELETE and refetches', async () => {
    const fetchMock = vi.fn()
      .mockResolvedValueOnce({ ok: true, status: 204 })
      .mockResolvedValueOnce(jsonResponse(200, []))
    vi.stubGlobal('fetch', fetchMock)

    const { routines, remove } = mod.useCloudRoutines()
    await remove('r1')

    expect(fetchMock).toHaveBeenNthCalledWith(1, '/api/cloud-routines/r1', { method: 'DELETE' })
    expect(fetchMock).toHaveBeenNthCalledWith(2, '/api/cloud-routines')
    expect(routines.value).toEqual([])
  })

  it('throws the server error text on a failed mutation', async () => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(jsonResponse(400, { error: 'name is required' })))

    await expect(mod.useCloudRoutines().create({ name: '', fireUrl: 'u', spawnerId: 's', token: 't' })).rejects.toThrow('name is required')
  })
})

describe('fire', () => {
  it('sends the text and upserts the new session into the shared list', async () => {
    const fetchMock = vi.fn().mockResolvedValue(jsonResponse(201, makeSession('c7')))
    vi.stubGlobal('fetch', fetchMock)

    const view = await mod.useCloudRoutines().fire('r1', 'go')

    expect(fetchMock).toHaveBeenCalledWith('/api/cloud-routines/r1/fire', { method: 'POST', headers: jsonHeaders, body: JSON.stringify({ text: 'go' }) })
    expect(view.id).toBe('c7')
    expect(sessionsMod.useCloudSessions({ autoStart: false }).sessions.value.map(s => s.id)).toEqual(['c7'])
  })

  it('exposes the upstream status and body on a 502 FireError', async () => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(jsonResponse(502, { error: 'routine fire failed with HTTP 429', status: 429, body: '{"detail":"slow down"}' })))

    const err = await mod.useCloudRoutines().fire('r1').catch((e: unknown) => e)

    expect(err).toBeInstanceOf(mod.FireFailedError)
    expect((err as Error).message).toBe('Routine fire failed: HTTP 429')
    expect((err as InstanceType<typeof mod.FireFailedError>).status).toBe(429)
    expect((err as InstanceType<typeof mod.FireFailedError>).body).toBe('{"detail":"slow down"}')
  })

  it('surfaces a 502 without a FireError payload as a plain error', async () => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(jsonResponse(502, { error: 'routine fire returned an invalid session id' })))

    const err = await mod.useCloudRoutines().fire('r1').catch((e: unknown) => e)

    expect(err).not.toBeInstanceOf(mod.FireFailedError)
    expect((err as Error).message).toBe('routine fire returned an invalid session id')
  })

  it('throws FireUnconfirmedError with the server message when the flag says a run may have started', async () => {
    const message = 'the routine may have started a run; check claude.ai before firing again'
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(jsonResponse(502, { error: message, mayHaveStarted: true })))

    const err = await mod.useCloudRoutines().fire('r1').catch((e: unknown) => e)

    expect(err).toBeInstanceOf(mod.FireUnconfirmedError)
    expect(err).not.toBeInstanceOf(mod.FireFailedError)
    expect((err as Error).message).toBe(message)
  })

  it.each([false, 'true', 1, null])('treats mayHaveStarted %j as a plain error', async (flag) => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(jsonResponse(502, { error: 'may have started', mayHaveStarted: flag })))

    const err = await mod.useCloudRoutines().fire('r1').catch((e: unknown) => e)

    expect(err).not.toBeInstanceOf(mod.FireUnconfirmedError)
    expect((err as Error).message).toBe('may have started')
  })

  it('ignores the flag on a status other than 502', async () => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(jsonResponse(500, { error: 'boom', mayHaveStarted: true })))

    const err = await mod.useCloudRoutines().fire('r1').catch((e: unknown) => e)

    expect(err).not.toBeInstanceOf(mod.FireUnconfirmedError)
  })

  it('surfaces the 409 message', async () => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(jsonResponse(409, { error: 'routine is already firing' })))

    await expect(mod.useCloudRoutines().fire('r1')).rejects.toThrow('routine is already firing')
  })
})
